This plugin extends fsbhoa_ac_core.  @/home/pi/fsbhoa_ac_core/other_docs/ARCHITECTURE.md

## Purpose

This plugin connects the hardware that monitors vehicle access to the access control system.

It combines three inputs:
- **Loop detector events.** One component listens for events from the device that monitors the loop detector on the vehicle entrance lane. That device is a **Shelly Pro 1**.
- **License plate data** from our LPR cameras.
- **The authentication feed** from the DoorKing gate controller.

From these it builds a composite event that describes one vehicle entry. That event goes to the vehicle access log and to the real-time display.

- **Delivery.** `vehicle_service` posts each finished event to `POST /vehicle-event` on this machine through an on-disk queue (`/var/lib/fsbhoa/vehicle_queue`), so events survive WordPress being down. Events carry the environment and a unique `event_uid`; WordPress refuses the other environment's events and ignores repeats.
- **Vehicle access log.** This plugin runs its own log, the `ac_vehicle_log` table, separate from core's `ac_access_log`. `POST /vehicle-event` writes to it. The plugin creates and upgrades the table itself (`includes/class-fsbhoa-vehicle-db.php`); bump `DB_VERSION` when the schema changes.
- **Real-time display.** Vehicle events appear in the left column of core's live monitor ("Vehicle Gate Traffic"). This plugin adds the column and the photo lightbox through core's `fsbhoa_monitor_activity_columns` and `fsbhoa_monitor_modals` hooks (`includes/views/view-monitor-vehicle-column.php`). `assets/js/fsbhoa-vehicle-monitor.js` polls `GET /vehicle-recent` every 3 seconds and fills the column.

Plates read on the exit lanes are also logged, as vehicle exits from the community.

## Deployment

The plugin and `vehicle_service` run on the access control system, the same machine as WordPress and all the other plugins. There are two:
- **Production:** access.fsbhoa.com, a Linux mini-PC.
- **Testbed:** testbed.fsbhoa.com, a Raspberry Pi 5 running Raspberry Pi OS.

One `vehicle_service` on each machine handles all of that system's gates. The devices at the gates reach it over the LAN.

The hardware components live at the vehicle gates:
- **Production:** one set at the north entrance and one at the south entrance.
- **Testbed:** a gate controller and loop sensor on the workbench, but no cameras. The cameras are too expensive to duplicate, so the testbed taps the real production lane cameras.

### Tapping production cameras from the testbed

This is allowed as long as it can't affect production (see "Environment separation" in `ARCHITECTURE.md`). The testbed may only read from a production camera, meaning its snapshots, plate images and plate text. It must never change a camera's settings, its FTP or event targets, or anything else production relies on. Any code that writes to a camera must check `FSBHOA_AC_ENVIRONMENT` and fail closed.

## Hardware

- **Gate controller:** DoorKing 1838-010 Rev AB, 3000-resident capacity. A Wiegand keypad and a windshield RFID sensor are attached to it.
- **Loop sensor:** Shelly Pro 1, monitoring the loop detector on the entrance lane.
- **LPR camera:** Speco O4BLP2M, one on each entrance lane and one on each exit lane. Each is mounted on the gate post, pointing away from the gate, so it reads the rear plate after the car has cleared the gate.
- **Overview camera:** each entrance lane also has an overview (context) camera that sees the vehicle as it passes the gate.
- Each entrance lane has its own complete set: DoorKing controller, loop, LPR camera and overview camera.
- **What the NVR provides:** for each plate read, the NVR captures the plate crop and a scene photo of the car at the moment of the read (the `..._plate_<PLATE>.jpg` and `..._src.jpg` pairs). The overview camera is a separate camera the service can take its own snapshot from.
- **NVR:** Speco N64NR, also on our LAN.

### Testbed workbench

The workbench DoorKing setup is on the developer's home network (192.168.1.x), which reaches the testbed's LAN (192.168.42.x) over a VPN. The VPN only connects one way: the testbed (192.168.42.62) can't reach 192.168.1.x.

- **RAM PC:** 192.168.1.41. Runs DoorKing's RAM software and the VPN, whose address is 192.168.70.3. RAM's Live Streaming output is pointed at the testbed.
- **RS-232-to-LAN adapter:** connects RAM to the 1838 controller's serial port. 192.168.1.40 port 1040 (confirmed reachable from the RAM PC, 2026-10-09). The `fsbhoa_ac_doorking` proxy config still records an older 192.168.1.50:10001.
- **Wiegand inputs on the 1838:**
  - An old fob reader. The test fob is printed "603 186 06549", read as facility code 186, card number 06549. It is entered in RAM as device 06549 and is admitted on Relay 2. The panel ignores facility codes.
  - A Raspberry Pi 3B at 192.168.1.210 that simulates a Wiegand card reader. Its GPIO 17 and 27 (BCM numbering) drive the controller's L0 (green) and L1 (white) data lines through transistors. The script is `tools/wiegand_sim.py`; copy it to the Pi to run it. By default it sends facility code 0, card 10018, which is loaded in the panel and admitted on Relay 1 (device 01234 is also loaded); `--facility` and `--card` send any other 26-bit card, and `--keys 1234#` simulates a Wiegand keypad (`--key-bits 4` or `8`).
- **The 1838's own keypad:** gate codes are entered as `#` followed by the code. A resident's real gate code is loaded for testing.

## Design: one event per lane

Status: **proposed, for review** (2026-10-10). The WordPress side (`fsbhoa_ac_vehicle.php`, settings in `includes/class-fsbhoa-vehicle-settings.php`) and the delivery path (on-disk queue, `POST /vehicle-event`) are built. `vehicle_service` still keeps a single in-progress event for the whole system; this design replaces that. Items marked **Open** need a decision or a look at the hardware before they are built.

### Gates, lanes and devices (settings)

The settings page gets a **Gates** table, with **one entry per entrance lane**. Each entrance lane has its own DoorKing controller, loop, LPR camera and overview camera, so each entry has:
- **Name**, used as `gate_identifier` in the log and on the monitor: `North`, `South`, `Workbench`.
- **RAM account name**, exactly as it appears in the stream (`WORKBENCH`). This is how a DoorKing record is matched to a gate.
- **Entrance lane:** the Shelly's IP address and the LPR camera's IP address.
- **Exit lane** (optional): the LPR camera's IP address. Exit lanes have no loop sensor and no DoorKing.
- **Overview camera:** the IP address of the lane's overview (context) camera, which sees the vehicle as it passes the gate.

Plus, for the whole system:
- **RAM stream sources:** the IP addresses allowed to connect to the DoorKing listener (192.168.70.3 on the testbed; the office RAM PC in production).

The settings are written to `vehicle_service.json` with the rest of the config, as a `gates` list. Later, the camera entries will also need whatever the service uses to reach each camera (login, API details).

Production today:
- **North:** one entrance lane, one exit lane.
- **South:** one entrance lane and one exit lane in use. A second entrance lane exists but is unused; if it's opened, it gets its own DoorKing controller, loop sensor and LPR camera, and is added as another gate entry (with no exit lane).

### Listeners

- **DoorKing listener:** TCP, `0.0.0.0:8089`, in `vehicle_service` itself. RAM's Live Streaming output points here directly, not through `fsbhoa_ac_doorking`, because plugins rely only on core. Connections from any address not in the RAM sources list are closed at once and logged. RAM reconnects for every job (a push, Send Time), so any number of reconnects is normal.
- **Webhooks (Shelly, and cameras if they push):** HTTP, `0.0.0.0:8088`. A request is accepted only from an IP address configured for that device, and the address alone identifies the gate and lane (no `device` parameter needed). This covers TODO items 14 and 20. **Open:** whether the Shelly can also send a fixed token in the URL, as a second check.
- **Health:** `/health` answers only to 127.0.0.1, for the settings page. It reports, per gate, when the last RAM record, loop change and plate arrived, so a silent device shows up.

### DoorKing records

Parsing follows "RAM Live Streaming format" below:
1. Split on CR LF. Read the fixed columns. Skip status records (`Network`, `RS232`), but note their time for the health report.
2. Keep only `Cards` and `Entry Code` records that have a code and the result `Admit`. Anything else that isn't a status record is logged in full, so new record types (such as a directory call) show up.
3. Map the account name to a gate. An unknown account is logged and dropped.
4. **Merge repeats into presentations:** a read joins the waiting (not yet claimed) presentation of the same code at that gate, or the one claimed by a car still on the loop. A presentation records the first-read time, the last-read time and the read count. The exact rules are in `docs/GATE_SCENARIOS.md`.
5. Use the **arrival time**, never the record's own time; keep that in `raw_details`.

Record types map to `auth_type`: `Cards` → `DK_WINDSHIELD`, `Entry Code` → `DK_ENTRY_CODE`. The code is kept exactly as sent (`06549`, `6020`), which matches how `ac_credentials` stores them. Directory calls (`DK_DIR_CODE`) wait until the record format has been captured.

### Correlation

The full rules are in `docs/GATE_SCENARIOS.md`: every situation we expect at a gate (a driver stopping anywhere, tailgating, trailers, turning around, missed plates, late records, lost signals), and the state machines for presentations and vehicle events. In short:
- **Order decides, not time.** A lane is a queue, so loop intervals and plates are matched by their order. Time limits are only safety nets for deciding that something is stale.
- **The loop is the vehicle**, and **one presentation belongs to one vehicle**, which is what makes tailgating visible (TODO item 6). A waiting car keeps its tag active (the reader re-reads it every second), so a credential is matched however long the driver stops.
- **Send early, amend later.** An event is sent 5 seconds after the car leaves the loop. A plate or DoorKing record that comes later is sent as an amendment to the same `event_uid`. This needs `POST /vehicle-event` to accept updates to an existing event; today a repeat is ignored.
- **Unclear cases get review flags** in `raw_details` instead of `is_circumvention`.

### Exit lanes

**Open:** how exits look. Proposed: every plate read on an exit lane becomes an event with `direction` = `exit`, holding the plate text, the plate photo and a scene photo, sent straight away. It's stored in `ac_vehicle_log` and shown in the same monitor column with an "Exit" label. Needs a `direction` column (DB_VERSION 3).

### Cameras

Each entrance lane has two cameras, and each entry gets three photos:
- **Plate text, plate crop and scene photo** from the LPR camera, captured together at the plate read. These come through the NVR (the `..._plate_<PLATE>.jpg` and `..._src.jpg` pairs). Stored as `lpr_image_data` and, for the scene, a second image column (needs DB_VERSION 3).
- **Overview snapshot** from the lane's overview camera, taken by the service at loop on or loop off. Stored as `context_image_data`.

**Open:** how the service gets plate reads: FTP push (from the NVR or the camera) or the Speco HTTP API. FTP from the LPR camera is known to work (the files in `/home/pi/lpr_ftp_drop` came that way); a first try at HTTP failed, but the cause wasn't looked into. If nothing in production uses a camera's FTP output, pointing it at the testbed for the traffic study affects no one (the user's decision), like the RAM stream. Also open: and whether the overview snapshot comes from the camera or through the NVR. On the testbed, production cameras and the NVR may only be *read*, and pointing their FTP output at the testbed would change their settings. So the testbed needs a way to pull over HTTP, and that is the likelier choice for both machines. Look at the O4BLP2M's and N64NR's APIs (read-only) before deciding. Plate text must keep its spaces, dashes and case (TODO item 13), and `lpr_confidence` should be filled if the camera reports it (item 12). The drop folder path stops being hard-coded either way (item 16).

### Event payload changes

Added to what `vehicle_service` sends: `auth_type` (TODO item 7), `direction`, and in `raw_details`: the lane, loop-on and loop-off times, every presentation (first and last read, read count, relay, the record's own time and name), and the plate's arrival time.

### Build order

0. **Record real traffic.** *Built 2026-10-10:* the RAM listener (`vehicle_service/ram.go`), the journal (`journal.go`), loop off, and the "RAM Stream Sources" and "Device Sources" settings (stand-ins for the Gates table). The journal is `/var/lib/fsbhoa/vehicle_journal/journal-YYYY-MM-DD.jsonl`, kept 30 days; entry kinds are `ram_record`, `ram_repeat` (a collapsed flood), `ram_connect`, `ram_disconnect`, `ram_refused`, `loop`, `loop_invalid`, `plate` and `webhook_refused`. DoorKing records are journaled only; they don't feed the old single-event correlator. Not built yet: overview snapshots (needs the cameras' snapshot URL and login). Before writing the correlation, `vehicle_service` records every raw input (DoorKing records, loop on and off, plates) with its arrival time to a journal, one JSON line per input, without correlating anything. Run it against a real gate to check the scenario table, and keep the recordings: the correlation code is then tested by replaying them, and again whenever the rules change. This needs:
   - **The loop signal at a real gate.** A new cable from the loop detector to a Shelly. Take it from a spare output on the arm's loop detector (many detectors have a second relay) as a dry contact. Never connect to the loop wire itself: that would change what the arm's detector sees and could affect the gate.
   - **The live stream from the production RAM.** The office RAM doesn't stream today, so pointing its Live Streaming output at the testbed for the study affects no one (the user's decision, 2026-10-10). RAM probably streams to only one address, so it moves to the production server when production's own `vehicle_service` needs it.
   - **Overview camera snapshots** at both loop on and loop off, to choose which moment gives the better context photo.
1. Settings: the Gates table and RAM sources, written to `vehicle_service.json`.
2. DoorKing listener, parser and presentations, with unit tests built from the captured records in `/home/pi/ram_stream.log`, including the flood.
3. Per-lane correlation following `docs/GATE_SCENARIOS.md`, with the existing loop webhook (adding loop off). Unit tests for each scenario in its table; then test with the Shelly, the fob and the Pi.
4. Amendments: `POST /vehicle-event` updates, for late plates and late DoorKing records.
5. Cameras and exit lanes, once those are decided.

## RAM Live Streaming format (captured 2026-10-09 and 10)

First captured from the workbench RAM. It covers only the record types seen so far: a granted card, and RAM's own connection status. Keypad codes, directory calls and denials are still to be captured. The raw capture is in `/home/pi/ram_stream.log`.

- **Transport:** RAM connects to us over TCP and keeps the connection open for its whole session. It reconnects for a new session. It sends nothing else, and expects no reply. Streaming works whether or not RAM's Live Transaction dialog is open. On the testbed, connections arrive from the VPN address 192.168.70.3, not the RAM PC's own 192.168.1.41.
- **Framing:** each record is CR LF, fixed-width ASCII padded with spaces (usually 158 characters; a long status message made one 162), then CR LF. Split on CR LF and skip empty lines.
- **Columns** (0-based offsets into the 158 characters):

  | Offset | Field | Examples |
  |---|---|---|
  | 0–22 | RAM account name | `WORKBENCH`, `SOUTH GATES NEW` |
  | 23–34 | Date, MM/DD/YY | `10/09/26` |
  | 35–43 | Time, h:mmAM/PM | `4:22PM` |
  | 44–56 | Record type | `Cards` (a tag or card), `Entry Code` (a keypad gate code), `Network`, `RS232` |
  | 57–64 | Device number or gate code | `06549`, `01234` (devices, zero-padded to 5 digits); `6020` (an entry code, as typed) |
  | 65–82 | Name on the device, from RAM | `& SHUT, OPEN` |
  | 83–90 | Result | `Admit` |
  | 91– | Relay | `Relay 2` |

  RAM's status records (`Network`, `RS232`) put a message at offset 65 instead, such as `Connection`, `Download Data` or `Failed To Connect`.
- **What this means for the design:**
  - The account name identifies the gate, so production's RAM account names are what map events to the north and south gates.
  - The time has whole minutes only. Correlation must use the time the record arrives, not the time in it.
  - A card record shows the device number but not the facility code.
  - The record type tells a tag (`Cards`) from a gate code (`Entry Code`). That holds for the 1838's own keypad. A Wiegand keypad that buffers the digits and sends them as one 26-bit frame is logged as `Cards`, indistinguishable from a tag; the panel ignored key-by-key Wiegand bursts (4 or 8 bits per key) entirely.
  - How the relay is chosen: for a device, by the reader input it was read on (the same device 06549 fires Relay 2 from the fob reader and Relay 1 from the Pi); for a gate code, by the code ranges in RAM's System Information; for a directory call, by the tone the resident presses (9 for Relay 1, 5 for Relay 2). So the relay isn't a reliable sign of which input was used. Keep it in `raw_details` only.
  - **Repeated reads.** The windshield tag reader scans about once a second and reports every read, so a car sitting in its range produces a stream of identical `Cards ... Admit` records (about one a second, each a separate beep at the gate). On the workbench, five fob swipes in six seconds gave six records. Merge identical reads (same gate, same type, same code) into one *presentation* (rules in `docs/GATE_SCENARIOS.md`); keep the first-read time, last-read time and read count in `raw_details`. No two cars share a tag, so a repeat of the same code is always the same car. No two households share a tag, and a gate code repeats only within one household; a code that Access Control finds in more than one household is a data error (see core's `TODO.md`), which the monitor shows as "+N others" rather than hiding.
  - **Two different codes at once.** Only one car fits in the reader's field, but a second readable tag can be in view at the same time (a spare tag in the car, or one carried nearby). A different code always starts its own presentation. If two presentations overlap the same vehicle event, attach both: the first one read is the credential shown, and the other is listed in `raw_details`.
  - Only successful entries are streamed. An unknown card produced no record at all, so failed attempts can't appear in the vehicle log, and a car with no DoorKing record means no *successful* credential.
  - Access Control identifies the person by the device number (a windshield tag) or the gate code (entered at a keypad). The record type says which of the two it is.
  - The name field is short (about 15 characters). It will eventually hold the household name, truncated, filled in from Access Control. It is only for debugging; never use it to identify anyone.
- **Glitches seen on 2026-10-10. The parser must survive them:**
  - **A flood of incomplete records.** After one Relay 1 swipe at 6:22 AM, RAM sent 28,432 copies of a record with only the account, the time and `Relay 1` (no type, device or result), in bursts of 23 about three times a second, for seven minutes. Count a record only if it has a type, a device number and a result, and drop exact repeats that arrive within a second or two. Real swipes of the same card can be seconds apart with identical text.
  - **Late delivery.** The real record for that 6:22 swipe arrived 15 minutes late. Later swipes, on both relays, arrived within seconds. So a vehicle event can't wait for its DoorKing record. It must be sent with the credential pending, and the DoorKing record attached when it arrives. Otherwise a delayed record makes the car look like it got through without a credential.
  - **A frozen time field.** From 6:22 AM on, every record carried `6:22AM` whatever the real time, which suggests the panel's clock or transaction timestamps were stuck. Use arrival time; keep the record's own time only in `raw_details`.
