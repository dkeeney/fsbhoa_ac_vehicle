# Gate scenarios and lane state machines

Status: **proposed, for review** (2026-10-10). This is how `vehicle_service` turns loop, DoorKing and camera inputs into vehicle events. It's part of "Design: one event per lane" in `CLAUDE.md`.

## Principles

Any driver can stop anywhere, for any length of time: at the reader, on the loop, between the gate and the camera. So:

1. **Order decides, not time.** A lane is a queue. Cars can't pass each other, so they cross the loop and pass the camera in the same order they arrived. Inputs are matched to vehicles by their order on the lane. Time limits exist only as safety nets, to decide that something is stale. They are long, and they never decide *which* car something belongs to.
2. **The loop is the vehicle.** One loop-on to loop-off interval is one vehicle presence. The exceptions (trailers, cars bumper to bumper) are flagged for review, not guessed.
3. **Nothing waits for a stopped car.** An event is sent soon after the car leaves the loop. Whatever arrives later (the plate, a late DoorKing record) is sent as an **amendment** to the same `event_uid`.
4. **Nothing is thrown away.** An input that matches no vehicle becomes its own partial event, with the reason.
5. **Flags, not verdicts.** `is_circumvention` is set only when a vehicle clearly crossed the loop with no credential. Unclear cases get a review flag instead (listed below), so the log doesn't accuse people of tailgating on a guess.

## Entrance lane layout

```
Direction of travel →

 [keypad pedestal]   [windshield reader]   ~1 car length   [ loop ~3 ft ][arm] | gate |  ──► LPR camera's view
   no loop under it    South: ~2 ft past the keypad          Shelly watches it;     (camera on the gate post,
                       North: ~30 ft past the keypad         the arm is over its     pointing away from the gate)
                                                             gate end
                                          overview camera: sees the vehicle as it passes the gate

Each entrance lane has its own DoorKing controller, loop, LPR camera and overview camera.
```

- **Keypad pedestal.** No loop under it. A driver using the keypad, or a visitor making a directory call, waits here, off the loop.
- **Windshield reader.** A little over a car length before the gate. At the north entrance it is about 30 ft past the keypad, and there is room for a car to drive around a car waiting at the keypad.
- **Loop.** About 3 ft long, just outside the gate. The traffic arm is over its gate end, so a car waiting for the arm to rise is standing on the loop.
- **LPR camera.** Mounted on the gate post, pointing away from the gate, so it reads the rear plate after the car has cleared the gate. That's true of all four LPR cameras, entrance and exit. So a plate always arrives **after** that car's loop off, and the LPR camera can't see a car that is still on the loop. The camera reports only plates it actually read: a car it can't read produces nothing, and so does a car that passes during the camera's **recovery period** after a capture. There is no "vehicle seen, no plate" signal.
- **Overview (context) camera.** Every entrance lane has one that sees the vehicle as it passes the gate. The context photo is a snapshot from it, taken by the service. Whether loop on (the car is usually stopped at the arm: sharp, showing the front and the driver) or loop off (the car is passing through the gate, possibly blurred) gives the better picture will be decided from snapshots taken at both during the traffic study.

So every credential is presented **before** the car reaches the loop: a code at the keypad, a tag at the reader. A car on the loop is at the arm.

**How the gate queues cars.** The DoorKing panel knows nothing about the loop: on each authentication it just pulses the gate opener and the arm. The queuing is done by the arm operator, which we can't see. After car A authenticates, it moves up onto the loop and waits for the gate. Once A is on the loop, car B can authenticate; B's authentication is queued until A has cleared the loop, and then B goes through. So cars with tags can pass end to end, as long as A reaches the loop before B authenticates. If B authenticates too soon (before A is on the loop), the arm ignores B's authentication, and B must present again. Consequences:
- The panel logs every authentication, including ones the arm will ignore: six quick fob swipes on the workbench gave six `Admit` records. So an ignored authentication is still in the stream, followed later by the driver presenting again.
- Cars passing end to end still give separate loop intervals: the loop is only about 3 ft long, shorter than the gap between two cars. Scenario 8 (one interval for two cars) needs cars closer than that.

The tag reader re-reads a tag about once a second while the car is in range (seen at the real gates; each read is a separate `Admit` and a beep), so **a car waiting within its range keeps its tag "active"** for as long as it waits. That's what lets a tag be matched to a car however long the driver stops. **Open:** on the workbench, a fob held still on the reader gave only one read; check whether a stopped car at a real gate really keeps producing reads, or only while it moves.

## Inputs

| Input | From | Gives |
|---|---|---|
| Loop on / loop off | Shelly, per entrance lane | presence of a vehicle over the loop |
| Presentation | RAM stream, per gate | a tag (`Cards`) or gate code (`Entry Code`); repeat reads merged (first read, last read, count) |
| Plate | LPR camera through the NVR, per lane | plate text, read time, and two photos taken at the read: the plate crop and a scene photo of the car |
| Context photo | snapshot from the lane's overview camera, taken by the service | the vehicle at the gate (at loop on or loop off; see the layout) |

## Entrance lane scenarios

"Flag" is what the event carries in `raw_details.flags`. `is_circumvention` is listed when it's set.

| # | Scenario | What the inputs show | Handling | Result |
|---|---|---|---|---|
| 1 | Normal entry with a tag | tag reads, loop on, loop off, plate | presentation claimed at loop on; plate assigned after loop off | complete event |
| 2 | Driver stops at the reader before the loop (talking, looking for something) | tag reads continue every second, loop on comes much later | presentation stays active while reads continue, so it's claimed at loop on however long the wait | complete event |
| 3 | Gate code at the keypad | one `Entry Code` read, then loop on when the car reaches the arm | claimed by the next loop on (within the orphan limit) | complete event |
| 4 | Directory call, visitor waits at the keypad for minutes | a long wait off the loop, then a `DK_DIR_CODE` record (format not yet captured), then loop on | the record is claimed by the next loop on, like a gate code | complete event |
| 5 | Car stops on the loop (waiting for the arm, or stopped there) | loop on, long wait, loop off | no limit; after 10 minutes a health warning "loop occupied" is logged, but the event stays open | complete event |
| 6 | Car stops between the arm and the camera | loop off, plate much later; the next car may cross the loop meanwhile | event is sent 5 s after loop off without a plate; the plate is sent as an amendment when it comes | complete after amendment |
| 7 | Tailgate: second car with no credential follows through the open gate | car A: presentation, loop on, loop off; car B: loop on, loop off, no new presentation | one presentation belongs to one vehicle, so B gets none | B: `is_circumvention` |
| 8 | Tailgate bumper to bumper, loop never clears between cars | one loop interval, two plates | the second plate finds no event waiting for a plate; it becomes a second vehicle for that loop interval | B: flag `multi_vehicle_on_loop`, no credential (not `is_circumvention`) |
| 9 | Truck with trailer: loop goes off and on with a short gap | two loop intervals, one presentation, one or two plates | second interval gets no presentation | second: flag `possible_trailer_or_tailgate` when the gap is under 3 s; review the photos |
| 10 | Two tags in view at once (spare tag, tag carried nearby) | two presentations with different codes overlapping the same loop interval | first one read is the credential; the other goes to `raw_details` | complete event, flag `extra_credential` |
| 11 | Credential shown, car turns around | presentation, maybe loop on and off, no plate | event sent without a plate; closes when later cars get plates or after the plate limit | flag `no_plate` |
| 12 | Car turns around without a credential | loop on, loop off, no presentation, no plate | can't tell from a car that drove through unread | flag `no_plate` and `no_credential` (not `is_circumvention`) |
| 13 | Tag read but no car reaches the loop (car in another lane, tag carried on foot) | presentation, no loop on | after the orphan limit, sent as a credential-only event | flag `credential_no_vehicle` |
| 14 | Motorcycle or bicycle that doesn't trip the loop | plate (maybe), no loop | plate with no event waiting becomes a plate-only event | flag `plate_only` |
| 15 | Camera can't read a plate (dirty, missing, odd mount) | event never gets a plate; the next car's plate could be assigned to it | plates go to the oldest event waiting; if more than one event was waiting, the assignment is flagged | flag `plate_order_uncertain`; alignment resets whenever the lane is empty |
| 16 | DoorKing record arrives late (seen once: 15 minutes) | record's own time is well before its arrival | treated as late (below); matched to the most recent event at that gate with no credential, within the record's minute ±1 | amendment, flag `late_credential` |
| 17 | Gate opened by the guard, a remote, or held open (event, emergency) | cars cross the loop with no presentation | **Open:** whether RAM streams manual and hold-open opens. Until then these show as no credential | `is_circumvention` (may be false alarms) |
| 18 | Loop-off signal lost (network, Shelly restart) | a loop on arrives while the lane already has a car on the loop | treated as loop off then loop on; both events flagged | flag `loop_signal_missed` |
| 19 | Loop stuck on (sensor fault) | loop on, never off | health warning after 10 minutes; plates and presentations keep being logged as partial events | health warning |
| 20 | `vehicle_service` restarts mid-event | open events lost; a loop off may arrive for an unknown event | loop off with no open event is logged and ignored; the Shelly's input state is polled on start (and every 30 s) to resync | gap in the log, logged |
| 21 | Panel clock wrong (seen: frozen at 6:22) | every record's own time is far from its arrival | late detection turned off while the offset is steady; health warning "panel clock off" | health warning |
| 22 | Piggyback at the arm: a short car with no credential waits on the loop; the car behind is read (or enters a code); the arm lets the first car in; the second is blocked, backs up and presents again | car A: loop on, no presentation; car B's presentation arrives while A is on the loop; A: loop off, plate A; B: a new presentation of the **same code**, loop on, loop off, plate B | A claims B's presentation (an event on the loop with no credential claims the next one that arrives). B's second presentation is new because the first was claimed and A has left the loop (see the presentation machine). Then the same code has been claimed by two vehicles in a row with different plates | both: flag `credential_used_twice`. If the credential's registered plate is known, the vehicle whose plate doesn't match gets `is_circumvention` |
| 23 | Keypad "steal" (north entrance): car B drives around car A, which is at the keypad, and reaches the loop first | A's `Entry Code`; B: loop on, loop off, plate B; then A either enters the code again (a new presentation of the same code) and reaches the loop, or gives up | B claims A's code. If A presents again: same pattern as 22. If A doesn't, A has nothing more to log | as 22: flag `credential_used_twice` on both, and `is_circumvention` on the plate that doesn't match, when known |
| 24 | Plate doesn't match the credential's registered vehicle (tag moved to another car, borrowed code, or a misread) | complete event, but the plate isn't one registered to that tag's vehicle or that household | checked only when Access Control has plates for that household | flag `plate_mismatch` |
| 25 | Two cars with tags, end to end (allowed by the gate's queue) | A's presentation, A loop on; B's presentation while A is on the loop; A loop off; B loop on, loop off; plates A, B | A already has a credential, so B's presentation waits, unclaimed, for the next loop on, which is B's | two complete events |
| 26 | Second driver authenticates too soon, is ignored by the gate, and presents again | B's presentation arrives before A's loop on; later, B presents the same code again | a read joins its code's unclaimed presentation however long ago it started, so both of B's attempts form one presentation, claimed at B's loop on. A claims the oldest unclaimed presentation, which is A's own | two complete events |
| 27 | Following car passes during the LPR camera's recovery period after a capture (end to end, or tailgating) | car A: plate read; car B: loop off soon after, never a plate; car C: plate | B left the loop within the recovery period after A's plate read, so it is presumed missed: the next plate skips B and goes to C | B: flag `no_plate_lpr_recovery` |

## State machines

### Presentation (one per gate and code)

```
                read of the same code
                     ┌──────┐
                     ▼      │
  first read ──► WAITING ───┘ ── no read for 60 s ──► ORPHAN ──► sent as a credential-only event (13)
                     │
                     │ claimed by a vehicle event
                     ▼
                  CLAIMED ── reads of the same code while its vehicle is on the loop,
                     │       or within 5 s of it leaving: same car, extend
                     │
                     │ its vehicle has been off the loop for 5 s
                     ▼
                  CLOSED ── a later read of the same code starts a new presentation
```

- **Merging.** A read joins its code's WAITING presentation at that gate if there is one, however long ago it started (scenario 26: a driver ignored by the gate who presents again). It extends a CLAIMED presentation only while that vehicle is on the loop or within 5 s of leaving. Otherwise it starts a new presentation. Each presentation keeps its first-read time, last-read time and read count.
- **Claiming.** On loop on, the new event claims the **oldest** WAITING presentation at that gate. An event that is still ON_LOOP with no credential also claims a presentation as soon as one arrives (scenario 22). An event that already has a credential never claims another one by arrival; the next presentation waits for the next car (scenario 25), except a second, different code overlapping the same car's loop interval, which is attached as `extra_credential` (scenario 10).
- **Closing.** Once its vehicle has been off the loop for 5 s, a CLAIMED presentation is closed. A later read of the same code is a new presentation, which keeps the car behind in scenario 22, or a driver presenting again in 23, from being swallowed by a vehicle that has already gone through.
- **Same code, two vehicles.** When a code is claimed by two vehicle events in a row at the same gate within 10 minutes, and their plates differ, both get `credential_used_twice` (scenarios 22, 23).
- **Late records.** A record whose own time is more than 2 minutes before its arrival skips this machine and goes to late matching (scenario 16), unless the panel clock is known to be off (scenario 21).

### Vehicle event (one per loop interval, per entrance lane)

```
  loop on ──► ON_LOOP ── loop off ──► PAST_LOOP ── plate ──► COMPLETE (sent)
                 │                       │
                 │ presentation          │ 5 s, no plate
                 │ (claim if none yet)   ▼
                 │                    SENT_AWAITING_PLATE ── plate ──► COMPLETE (amendment sent)
                 │                       │
                 │                       │ a later event on this lane gets a plate,
                 │                       │ or 10 minutes pass
                 │                       ▼
                 │                    CLOSED_NO_PLATE (scenarios 11, 12, 15)
                 │
                 └── loop on again without a loop off ──► treat as loop off, then a new event (scenario 18)
```

- **Leaving the loop** starts the 5-second wait for the plate. The context photo is taken at loop on or loop off (see the layout).
- **Plate assignment.** A plate read on the lane goes to the **oldest** event in PAST_LOOP or SENT_AWAITING_PLATE that has no plate, skipping any event that left the loop within the LPR camera's recovery period after the previous plate read on that lane. Those are presumed missed: they close with `no_plate_lpr_recovery` (scenario 27). If none is waiting:
  - if an event is ON_LOOP with no plate, the plate is the first of two cars sharing that loop interval (the camera can't see a car on the loop), so it goes to that event, flagged `multi_vehicle_on_loop` (scenario 8);
  - if the latest event already has its plate and its loop interval ended within 3 s, or is still on, the plate is a second vehicle for that interval (scenario 8);
  - otherwise it's a plate-only event (scenario 14).
- **Credential at close.** An event that leaves the loop without a credential is sent with no credential and `is_circumvention`, unless a review flag applies. A late record that later matches it clears `is_circumvention` by amendment.

### Exit lane

```
  plate ──► new exit event, sent at once
  same plate again within 60 s (car stopped in view) ──► merged: read count, last-read time
```

Exit lanes have only the camera, so there is no loop, no credential and no circumvention. Each distinct plate read is one exit.

## Safety-net limits

None of these decides which car an input belongs to. They only decide when something is stale.

| Limit | Value | Used for |
|---|---|---|
| Orphan presentation | 60 s with no read while WAITING | credential with no vehicle (13) |
| Claimed presentation closes | 5 s after its vehicle leaves the loop | a second car using the same code (22, 23) |
| Same code on two vehicles | 10 min | `credential_used_twice` (22, 23) |
| Plate wait before sending | 5 s after loop off | sending the event without its plate (6) |
| Plate wait before closing | 10 min, or a later event gets a plate | closing with no plate (11, 12, 15) |
| Loop occupied warning | 10 min | health warning only (5, 19) |
| Bumper-to-bumper window | 3 s | second plate for one loop interval (8), trailer gap (9) |
| Late record | record time > 2 min before arrival | late matching (16) |
| Exit plate merge | 60 s | same plate re-read on an exit lane |
| LPR recovery period | **to be measured** in the traffic study | presuming a car missed by the camera (27) |
| Shelly state poll | 30 s, and at start | resync after a lost signal or a restart (18, 20) |

These go in the settings page later if real traffic shows they need tuning.

## Open questions

Most of these will be answered by recording real traffic at a gate (see "Build order" in `CLAUDE.md`).


1. Does a stopped car's tag keep producing reads at a real gate? The workbench fob gave only one read while held still.
2. Does RAM stream manual opens, remote opens and hold-open (scenario 17)? If not, do we need a "gate held open" switch on the monitor, so those periods don't flag every car?
3. How long is the LPR camera's recovery period after a capture, and how long does a car take from loop off to its plate read? Both come from the traffic study, and scenario 27 depends on them. The shortest gap between two plate reads on a lane bounds the recovery period. The plate is read roughly 20 ft past the loop (estimate, to be measured), so about 1.5 to 3 seconds at 5 to 10 mph, plus the car's length. (The camera can't report a vehicle it couldn't read.)
4. Is a gate-arm sensor available? Knowing that the arm actually opened would separate "turned around" from "drove through unread" (scenarios 11 and 12).
5. **Registered plates.** Scenarios 22 to 24 depend on knowing which plates belong to a credential. On the testbed, 1,285 of 1,322 tags are linked to a vehicle, but only 3 of 1,292 vehicles have a plate. The plates could be entered by staff, or learned from the vehicle log (a tag seen many times with the same plate).
6. Overview camera snapshots: taken from the camera itself, or through the NVR?
