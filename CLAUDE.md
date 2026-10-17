This plugin extends fsbhoa_ac_core.  @/home/pi/fsbhoa_ac_core/other_docs/ARCHITECTURE.md

## Purpose

This plugin connects the hardware that monitors vehicle access to the access control system.

It combines three inputs:
- **Loop detector events.** One component listens for events from the device that monitors the loop detector on the vehicle entrance lane. That device is a **Shelly Pro 1**.
- **License plate data** from our LPR cameras.
- **The authentication feed** from the DoorKing gate controller.

From these it builds a composite event that describes one vehicle entry. That event goes to the vehicle access log and to the real-time display.

- **Delivery.** `vehicle_service` posts each finished event to `POST /vehicle-event` on this machine through an on-disk queue (`/var/lib/fsbhoa/vehicle_queue`), so events survive WordPress being down. Events carry the environment and a unique `event_uid`; WordPress refuses the other environment's events and ignores repeats.
- **Vehicle access log.** Core owns the log (`ac_vehicle_log`), `POST /vehicle-event`, `GET /vehicle-recent` and `GET /vehicle-image/{id}` (`fsbhoa_ac_core/includes/monitor/class-fsbhoa-vehicle-log-rest-api.php`, whose docblock is the payload contract). Schema changes go in core's three schema files.
- **Real-time display.** Vehicle events appear in the left column of core's live monitor ("Vehicle Gate Traffic"). Core draws the column, the photo lightbox and the script that polls `/vehicle-recent`. This plugin turns them on by returning true from `fsbhoa_monitor_show_vehicle_column`.
- **Photo retention** is core's: the "Vehicle Photo Retention (days)" field on the Live Monitor settings page (default 14), and a daily purge at 03:05.

Plates read on the exit lanes are also logged, as vehicle exits from the community.

**The goal** (2026-10-16): tell the office staff, on the live monitor and with a sound, when a vehicle may have got in without a credential (ran the gate, tailgated, or slipped in some other way), so they know when to look at the cameras. The system can't prove a violation; it points staff at the moment and the photos. With no loop or arm signal available ("Hardware" below), the first version compares DoorKing credentials with plate reads on the entrance lane: each credential admits one vehicle, and each plate read (or vehicle read without a plate) is a vehicle that got in.

## Plugin split: core owns the vehicle log

Status: **built 2026-10-13** (item 29 in `TODO.md`, item 5 in `fsbhoa_ac_core/TODO.md`).

Vehicles get the same shape as pedestrians. Core owns the logs, the ingest routes and the live monitor columns. Extensions only talk to hardware and post finished events to core.

| | Pedestrian | Vehicle |
|---|---|---|
| Log table | core: `ac_access_log` | core: `ac_vehicle_log` |
| Ingest route | core: `/monitor/log-event` | core: `/vehicle-event` |
| Live monitor column | core | core |
| Hardware that posts events | uhppote, kiosk, light, doorking | this plugin |

**Moved to core (generic):**
- `ac_vehicle_log`: core's `create_tables()`, `db_schema.sql` and a `migration.sql` step. The schema is version 2 of this plugin's former `includes/class-fsbhoa-vehicle-db.php` (with `auth_type`, `event_uid` and their indexes).
- `POST /vehicle-event`: the API key check, the environment check and the `event_uid` duplicate check.
- `GET /vehicle-recent`, including its cardholder lookup in `ac_credentials`, which is core's data.
- `GET /vehicle-image/{id}`.
- The monitor column, the photo lightbox and `assets/js/fsbhoa-vehicle-monitor.js` (now in core's `assets/js`).
- Photo retention: the daily purge and the Image Retention (days) setting.

**Stayed here (vehicle lane hardware):**
- All of `vehicle_service`: the Shelly loop webhook, the Speco plate drop folder, the RAM Disk File reader, the journal, per-lane correlation and the on-disk queue to `/vehicle-event`.
- Settings: the lane tables, the device port, the RAM Stream File, correlation window, DoorKing-only events and debug logging. Also the `vehicle_service.json` writer and the system service registration.
- The RAM Disk File reader can't move to `fsbhoa_ac_doorking`, because this plugin would then depend on another extension (rule 3).

**The contract** between the two is the `/vehicle-event` payload, documented in the docblock of core's `class-fsbhoa-vehicle-log-rest-api.php`. The route names didn't change, so `vehicle_service` didn't either.

**Decisions made with the split:**
- **The folder stays `fsbhoa_ac_vehicle`**, because WordPress tracks active plugins by folder. Only the plugin's description changes, to vehicle lane hardware.
- **When core shows the vehicle column:** only when a filter, `fsbhoa_monitor_show_vehicle_column` (default false), returns true. This plugin returns true. With no vehicle hardware plugin, the pedestrian column fills the row as it does today.
- **The allowed-IP list is dropped.** Core's `/vehicle-event` checks only the Access Verification API Key, like `/monitor/log-event`. Core can't read this plugin's settings, and the key is the guard on every other service route. The Allowed Daemon IPs setting goes away.
- **Image retention:** core has its own setting (`fsbhoa_ac_vehicle_photo_retention_days`) and cron hook (`fsbhoa_vehicle_log_purge_photos`). `fsbhoa_vehicle_hand_off_to_core()` runs once per site: it copies this plugin's old `image_retention_days` into core's setting, if core's isn't set yet, and unschedules the old `fsbhoa_vehicle_purge_images` hook. It records `fsbhoa_vehicle_core_handoff_done`. An extension may call core.
- **Deploy both plugins together.** Both registering the same routes would conflict.

**Alternatives considered:**
- **A message queue (MQTT)** between hardware adapters and processors (2026-10-12). It's a cleaner architecture, but it changes nearly every service in a system that already works. Set aside.
- **A generic correlator in core** with a local input API for loop, plate and credential inputs. It only pays off if the lane hardware is ever replaced, because the correlation follows this hardware's timing (`docs/GATE_SCENARIOS.md`). This split can grow into it later without changing core's side.

## Deployment

The plugin and `vehicle_service` run on the access control system, the same machine as WordPress and all the other plugins. There are two:
- **Production:** access.fsbhoa.com, a Linux mini-PC.
- **Testbed:** testbed.fsbhoa.com, a Raspberry Pi 5 running Raspberry Pi OS.

One `vehicle_service` on each machine handles all of that system's gates. The devices at the gates reach it over the LAN.

The hardware components live at the vehicle gates:
- **Production:** one set at the north entrance and one at the south entrance.
- **Testbed:** a gate controller and loop sensor on the workbench, but no cameras. The cameras are too expensive to duplicate, so the testbed taps the real production lane cameras.

### DoorKing records: from RAM's Disk File

DoorKing records come only from RAM's Live Streaming Disk File (since 2026-10-13; "Disk File destination" below; TODO #31). The stream was retired ("RAM stream (retired)" below). To set up a server:
1. On the RAM PC, create the environment's folder under `Y:\Automation\DoorKing\`: `Live_Testbed` or `Live_Production`.
2. In RAM's Live Streaming settings, for each account, add a Disk File destination at `Y:\Automation\DoorKing\Live_<Environment>\stream.txt`, Raw Text, and turn the IP Address destination off.
3. In the RAM PC's `dk_sync.ini`, set `RamWatch=1` (and delete any old `StreamStatusUrl` line), then reload `DK_Sync.ahk`. Its log should show "RAM watch on". Every 60 seconds the script starts RAM if it isn't running, and writes the heartbeat `ram_pc_status.txt` in the `Live_<Environment>` folder, which the settings page shows. The file name is a contract with `fsbhoa_ac_doorking`'s script (`FSBHOA_Vehicle_Settings::RAM_PC_STATUS_FILE`). The script also reads `stream.txt` itself, during a configuration push, to see RAM's "Download Data" or "Database is Up To Date" record for the send (`fsbhoa_ac_doorking`'s CLAUDE.md, "Configuration sync"). It only reads, and it handles the daily rotation (a file shorter than where it was reading is read from the start), so keep the file's name, folder and Raw Text format.
4. In the vehicle settings, set **RAM Stream File** to the suggested path (`/mnt/shared/Automation/DoorKing/Live_<Environment>/stream.txt`) and restart `vehicle_service`. On its first start it skips the records already in the file.

### Tapping production cameras from the testbed

This is allowed as long as it can't affect production (see "Environment separation" in `ARCHITECTURE.md`). The testbed may only read from a production camera, meaning its snapshots, plate images and plate text. It must never change a camera's settings, its FTP or event targets, or anything else production relies on. Any code that writes to a camera must check `FSBHOA_AC_ENVIRONMENT` and fail closed.

## Hardware

- **Gate controller:** DoorKing 1838-010 Rev AB, 3000-resident capacity. A Wiegand keypad and a windshield RFID sensor are attached to it.
- **Loop sensor:** planned: a Shelly Pro 1 on a dry contact from the arm's loop detector. Not installed, and not possible for now (traffic arm, below).
- **Traffic arm:** DoorKing 1601. Its box has outputs for arm up, loop active and up pulse, but we have no key: it's maintained under contract by Open & Shut, and wiring to it would go through them. So no arm or loop signal is available.
- **Loops:** two, both the arm's: one under the arm and one about 6 ft inside the gate. No loop arms the keypad.
- **What opens the gate:**
  - the keypad: `#` and a 4-digit gate code (RAM: `Entry Code`)
  - a windshield tag (RAM: `Cards`)
  - a directory call: a visitor finds a resident, who presses 9 to open. Whether RAM records it is still to be seen.
  - a guard button (there's no guard), a fireman's key switch and an emergency vehicle strobe sensor. These don't go through the DoorKing controller, so RAM never sees them: a vehicle let in by them looks like one with no credential.
- **LPR camera:** Speco O4BLP2M, one on each entrance lane and one on each exit lane. Each is mounted on the gate post, pointing away from the gate, so it reads the rear plate about 20 ft past the gate, after the car has cleared it. A plate read on an entrance lane therefore means a vehicle got in.
- **Overview camera:** each entrance lane also has an overview (context) camera that sees the vehicle as it passes the gate, and sees the arm.
- Each entrance lane has its own complete set: DoorKing controller, loop, LPR camera and overview camera.
- **Plate reads:** the LPR camera itself captures, for each read, the plate crop and a scene photo of the car (the `..._plate_<PLATE>.jpg` and `..._src.jpg` pairs) and uploads them by FTP ("Plate reads: FTP from the cameras" below). The overview camera is a separate camera the service can take its own snapshot from.
- **NVR:** Speco N64NR (192.168.107.74), also on our LAN. It also receives the plate reads (a scrolling list in its web app, and a search); its API hasn't been looked at.

### Testbed workbench

The workbench DoorKing setup is on the developer's home network (192.168.1.x), which reaches the testbed's LAN (192.168.42.x) over a VPN. The VPN only connects one way: the testbed (192.168.42.62) can't reach 192.168.1.x.

- **RAM PC:** 192.168.1.41. Runs DoorKing's RAM software and the VPN, whose address is 192.168.70.3. RAM's Live Streaming output is pointed at the testbed.
- **RS-232-to-LAN adapter:** connects RAM to the 1838 controller's serial port. 192.168.1.40 port 1040 (confirmed reachable from the RAM PC, 2026-10-09).
- **Wiegand inputs on the 1838:**
  - An old fob reader. The test fob is printed "603 186 06549", read as facility code 186, card number 06549. It is entered in RAM as device 06549 and is admitted on Relay 2. The panel ignores facility codes.
  - A Raspberry Pi 3B at 192.168.1.210 that simulates a Wiegand card reader. Its GPIO 17 and 27 (BCM numbering) drive the controller's L0 (green) and L1 (white) data lines through transistors. The script is `tools/wiegand_sim.py`; copy it to the Pi to run it. By default it sends facility code 0, card 10018, which is loaded in the panel and admitted on Relay 1 (device 01234 is also loaded); `--facility` and `--card` send any other 26-bit card, and `--keys 1234#` simulates a Wiegand keypad (`--key-bits 4` or `8`).
- **The 1838's own keypad:** gate codes are entered as `#` followed by the code. A resident's real gate code is loaded for testing.

## Design: one event per lane

Status: **proposed, for review** (2026-10-10). The WordPress side (`fsbhoa_ac_vehicle.php`, settings in `includes/class-fsbhoa-vehicle-settings.php`) and the delivery path (on-disk queue, `POST /vehicle-event`) are built. `vehicle_service` still keeps a single in-progress event for the whole system; this design replaces that. Items marked **Open** need a decision or a look at the hardware before they are built.

### Lanes and devices (settings)

The settings page has two tables (built in step 1):
- **Entrance Lanes** (config key `gates`), **one row per entrance lane**. Each entrance lane has its own DoorKing controller, loop sensor, LPR camera and overview camera, so each row has:
  - **Name**, used as `gate_identifier` in the log and on the monitor. The testbed's say which way they go, since the monitor card shows only the name (and an EXIT tag when the name doesn't say exit): `North Enter`, `South Enter`, `Workbench`; exit lanes `North Exit`, `South Exit`.
  - **RAM account**, as it appears in the stream (`WORKBENCH`; matched case-insensitively). This is how a DoorKing record is matched to its lane.
  - The IP addresses of its **loop sensor** (Shelly), **LPR camera**, **overview camera** (which sees the vehicle as it passes the gate, and the arm) and **keypad camera** (which sees the vehicle at the keypad and tag reader; added 2026-10-17). At the south entrance the overview camera is 192.168.107.131 and the keypad camera 192.168.107.134 (both Speco O2VLB5).
- **Exit Lanes** (config key `exit_lanes`), one row per exit lane: **name**, **LPR camera** and an optional **overview camera**. Exit lanes have no DoorKing and no loop sensor. They aren't tied to an entrance: a car may enter at any entrance and leave at any exit. An exit lane's name may repeat an entrance's (`South`).

Names are unique within a table, RAM accounts are unique, and a device address may be used only once across both tables. The device addresses are the webhook allowlist, and an address tells which lane a call is from.

Plus, for the whole system:
- **RAM Stream File:** where this server reads RAM's Disk File (`ram_file`; "DoorKing records: from RAM's Disk File" above).
- **Port:** the device port (HTTP, 8088: webhooks, `/health`).

Later, the camera entries will also need whatever the service uses to reach each camera (login, API details).

Production today:
- **North:** one entrance lane, one exit lane.
- **South:** one entrance lane and one exit lane in use. A second entrance lane exists but is unused; if it's opened, it gets its own DoorKing controller, loop sensor and LPR camera, and is added as another entrance lane.
- The South entrance's cameras: LPR camera 192.168.107.138, overview camera 192.168.107.131. The testbed's Workbench lane uses them, read-only (see "Tapping production cameras from the testbed").
- The LPR cameras, with the MAC folder each uploads into ("Plate reads: FTP from the cameras"):

  | Lane | Address | MAC folder |
  |---|---|---|
  | South entrance | 192.168.107.138 | `5c-f2-07-5f-fd-97` |
  | North entrance | 192.168.107.140 | `5c-f2-07-5f-fd-9d` |
  | South exit | 192.168.107.137 | `5c-f2-07-5f-fd-be` |
  | North exit | 192.168.107.139 | `5c-f2-07-5f-fd-88` |

### Listeners

- **DoorKing Disk File reader** (`vehicle_service/ramfile.go`, built 2026-10-13): `vehicle_service` reads DoorKing records from RAM's Disk File, set by the RAM Stream File setting (`ram_file`); without it, no DoorKing records are read. It polls the file twice a second (no change notices on the NAS's CIFS mount) and parses what was added since its saved position (`ram_file_state.json` in the journal folder: the file's inode, the byte offset to the end of the last complete line, and the day the file was first seen). A different inode, or a file shorter than the offset, is a new file, read from the start. On the first start with no saved position it skips what is already in the file, since the stream was the source until then. Once a day it reads the file to the end, renames it to `stream_YYYY-MM-DD.txt`, waits 2 seconds and reads anything RAM appended in between; RAM starts a new file with its next record. Renamed files older than `journal_days` are deleted. The first rotation ran as expected after midnight 2026-10-14 (`stream_2026-10-13.txt` and a new `stream.txt`). The file is read only when the environment is valid and the folder name contains it (`Live_Testbed`, `Live_Production`); the settings page refuses other paths too. Records are handled by `ramLines` in `ram.go` ( XML assembly, flood collapsing, the journal with `"source": "file"`, lane tags, DoorKing-only events). Arrival time is when a line is read, so after an outage a backlog arrives at once; the record's own date and time (whole minutes) tell when it happened, and repeat reads of one tag in a backlog collapse into one DoorKing-only event. `/health` reports the reader's state (`ram_file`), shown on the settings page with the RAM PC's heartbeat. Tested 2026-10-13 on the testbed with the IP destination off: two swipes were read from the file within a second, journaled and stored in the vehicle log. Outage test: with `vehicle_service` stopped for a minute, three swipes of one tag went into the file; on start the reader continued from its saved position and read all three at once. Because they were identical and arrived together, the flood filter journaled one `ram_record` and a `ram_repeat` with count 2, and one vehicle event was stored. At a gate, repeat reads of one tag within a minute are almost always one car, so that is usually right; if not, a backlog could be judged by the record's own minute instead of its arrival.
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

Exit lanes are their own table (above), not part of an entrance. Every plate read on an exit lane becomes an event with `direction` = `exit`, under the exit lane's name, holding the plate text, the plate photo and a scene photo, sent straight away. It's stored in `ac_vehicle_log` (core's `direction` column, since 2026-10-17) and shown in the same monitor column with an "EXIT" label. Built for now by the plate read events (step 1).

### Cameras

Each entrance lane has two cameras, and each entry gets three photos:
- **Plate text, plate crop and scene photo** from the LPR camera, captured together at the plate read. These come through the NVR (the `..._plate_<PLATE>.jpg` and `..._src.jpg` pairs). Stored as `lpr_image_data` and, for the scene, a second image column (needs DB_VERSION 3).
- **Overview snapshot** from the lane's overview camera, taken by the service at loop on or loop off. Stored as `context_image_data`.

Plate text must keep its spaces, dashes and case (TODO item 13). `lpr_confidence` stays empty: the camera reports no confidence (item 12). **Open:** whether the overview snapshot comes from the camera or through the NVR.

### Plate reads: FTP from the cameras

Built 2026-10-17. The cameras upload each read by FTP to the server; `vehicle_service` journals it.
- **FTP server:** vsftpd on port 2121, set up by `lpr_ftp/setup.sh` (`sudo LPR_FTP_PASSWORD=... ./lpr_ftp/setup.sh <camera IPs>`). The one account, `specolpr`, can only upload, only into its home, the drop folder `/var/lib/fsbhoa/lpr_drop` (group `www-data`), and only from the camera addresses (TCP wrappers). Uploads older than 7 days are deleted (`systemd-tmpfiles`); a scene photo is about 1 MB.
- **Camera settings** (Network > TCP/IP > FTP): server, port 2121, user `specolpr`, path `/`. The camera cuts a longer password to 15 characters without saying so. It uploads to `<path>/<its MAC>/VEHICE/<date>/<hour>/`, and needs to set its folders' permissions (with vsftpd's `chmod_enable=NO` its uploads fail). The FTP target allows one server only. On the testbed the password is in `~/.config/fsbhoa/lpr_ftp` (root, 0600).
- **What arrives:** the plate crop (about 4 KB) and the scene photo (about 1 MB), each uploaded as `.tmp` and renamed, the crop about a second before the photo, 1 to 7 seconds after the capture; and the hour's summary `VEHICE_<date>-<hour>.txt`, one line per read with the camera's name, MAC and address. "Capture Plate Absence Vehicle" is on, so a vehicle without a readable plate arrives too, as plate `Unknown000` (`Unknown` in the summary); its "plate crop" is a crop of the whole vehicle, and the scene photo's overlay has the camera's guess at its color, year and type. Such a read isn't always a vehicle at the gate: the first one (north entrance, 2026-10-17 08:09) was a pickup driving along the cross street beyond the stop sign, inside the camera's detection area.
- **Reader** (`vehicle_service/lpr.go`): watches the folder set by the "LPR Upload Folder" setting (`lpr_drop_dir`), pairs each crop with its scene photo by the capture time in the names (waiting up to 10 seconds for the other), and journals one `plate` entry per read: the plate, the capture time, both files and their arrival times, `missing` (`plate` or `scene`), `no_plate` (with the camera's text in `camera_plate`, and `plate` empty) for a vehicle without a readable plate, and the camera and its lane (by its address from the summary). Other files are journaled as `lpr_file`. Files already there when it starts are not read. Plates don't feed the old correlator, and the files are left in place.
- **Testbed and production:** all four LPR cameras (both entrances and both exits) upload to the testbed since 2026-10-17 (`setup.sh` was given all four addresses) (the user's decision; nothing in production used its FTP output). Production's `vehicle_service` will need the uploads instead, or a second source such as the NVR.

**The cameras' API** (looked at read-only, 2026-10-16): TVT firmware under Speco's name (firmware 5.1.1.0, API 1.7): XML over HTTP POST to `/<Command>` with Basic authentication, no HTTPS. Read-only commands used: `GetDeviceInfo`, `GetVehicleConfig`, `GetAlarmStatus`. **Never send `GetAlarmServerConfig`**: it blocked the testbed's address (the camera's ports 80 and 554) for a few minutes, twice, the second time with a valid login. Also found:
- `GetAlarmStatus` gives only a true/false `vehicleAlarm`, held for 20 seconds after a read, with no plate, so polling can't replace the uploads.
- Saving to the SD card is off, so the camera's image search (`GetImagesPage`) has nothing.
- Plate detection settings: `capturePlateAbsenceVehicle` on, repeat suppression (`dedupMode`, 5 s) off, direction "no limit" (can be "approach" or "further"), no confidence value.
- The cameras have an `automation` user for this (Normal user); give it only Remote preview and Remote view logs, and a strong password.
- Other events the firmware offers (line crossing, pass-line counting) don't appear in the O4BLP2M's menus.

### Event payload changes

Added to what `vehicle_service` sends: `auth_type` (TODO item 7), `direction`, and in `raw_details`: the lane, loop-on and loop-off times, every presentation (first and last read, read count, relay, the record's own time and name), and the plate's arrival time.

### Build order

0. **Record real traffic.** *Built 2026-10-10:* the RAM listener (`vehicle_service/ram.go`; replaced on 2026-10-13 by the Disk File reader, so the `ram_connect`, `ram_resume`, `ram_disconnect` and `ram_refused` entries below no longer occur), the journal (`journal.go`), loop off, and the "RAM Stream Sources" and "Device Sources" settings (Device Sources was replaced by the lane tables in step 1). The journal is `/var/lib/fsbhoa/vehicle_journal/journal-YYYY-MM-DD.jsonl`, kept 30 days; entry kinds are `ram_record`, `ram_repeat` (a collapsed flood), `ram_connect`, `ram_resume` (a connection held across a restart, below), `ram_disconnect`, `ram_refused`, `loop`, `loop_invalid`, `plate` and `webhook_refused`. DoorKing records are journaled only; they don't feed the old single-event correlator. Not built yet: overview snapshots (needs the cameras' snapshot URL and login). Before writing the correlation, `vehicle_service` records every raw input (DoorKing records, loop on and off, plates) with its arrival time to a journal, one JSON line per input, without correlating anything. Run it against a real gate to check the scenario table, and keep the recordings: the correlation code is then tested by replaying them, and again whenever the rules change. This needs:
   - **The loop signal at a real gate.** A new cable from the loop detector to a Shelly. Take it from a spare output on the arm's loop detector (many detectors have a second relay) as a dry contact. Never connect to the loop wire itself: that would change what the arm's detector sees and could affect the gate. **Not available for now** (2026-10-16): the arm's box is maintained under contract by another company, and we have no key ("Hardware"). Until then the study records DoorKing records and plate reads only.
   - **Plate reads.** *Built 2026-10-17:* "Plate reads: FTP from the cameras" above; the south entrance camera uploads to the testbed.
   - **The live stream from the production RAM.** The office RAM doesn't stream today, so pointing its Live Streaming output at the testbed for the study affects no one (the user's decision, 2026-10-10). RAM probably streams to only one address, so it moves to the production server when production's own `vehicle_service` needs it.
   - **Overview camera snapshots** at both loop on and loop off, to choose which moment gives the better context photo.
1. Settings: the lane tables and RAM sources, written to `vehicle_service.json`. *Built 2026-10-11:* the Entrance Lanes and Exit Lanes tables on the settings page ("Lanes and devices" above; a blank row adds a lane, a cleared name removes one), the `gates` and `exit_lanes` lists and `ram_port` in `vehicle_service.json`, and `vehicle_service/gates.go`. The lanes' devices replace the "Device Sources" list as the webhook allowlist (an older config's `device_sources` is still honored). The journal tags `ram_record` entries with their `gate` (by RAM account) and `loop` entries with their `gate`, `direction` and `device` (by address). Applied without a restart. Correlation doesn't use the gates yet.
   - **DoorKing-only events (stopgap, built 2026-10-11):** the "DoorKing-Only Events" setting (config key `doorking_events`, off by default, applied without a restart) sends each admitted `Cards` or `Entry Code` record to the vehicle log on its own, with no loop or plate (`vehicle_service/doorking_events.go`). The gate is the lane whose RAM account sent it (or the account name if no lane has it), and `raw_details.source` is `doorking_only`. Repeat reads of the same code at the same lane less than 10 seconds apart are one event. It shows live DoorKing traffic on the monitor until correlation (step 3) replaces it; turn it off then, or each entry will be logged twice.
   - **Plate read events (stopgap, built 2026-10-17):** the "Plate Read Events" setting (config key `plate_events`, off by default, applied without a restart) sends each LPR camera read, entrance and exit, to the vehicle log on its own, with no credential (`vehicle_service/plate_events.go`). The gate is the camera's lane (or the camera's name if it is in no lane), `direction` is the lane's (core's `direction` column), the time is the camera's capture time, `event_uid` is `lpr_<camera MAC>_<capture time>`, and `raw_details.source` is `plate_only`. The plate crop goes in `lpr_image_data` as it is; the scene photo goes in `context_image_data` shrunk to a third each way (864 x 506, about 55 KB, instead of about 1 MB). A vehicle without a readable plate has an empty plate (the monitor shows "NO PLATE"). The monitor marks exits "EXIT". Like DoorKing-only events, turn it off when correlation replaces it.
2. DoorKing listener, parser and presentations, with unit tests built from the captured records in `/home/pi/ram_stream.log`, including the flood.
3. Per-lane correlation following `docs/GATE_SCENARIOS.md`, with the existing loop webhook (adding loop off). Unit tests for each scenario in its table; then test with the Shelly, the fob and the Pi.
4. Amendments: `POST /vehicle-event` updates, for late plates and late DoorKing records.
5. Cameras and exit lanes, once those are decided.

## RAM stream (retired 2026-10-13)

Until 2026-10-13, RAM's Live Streaming IP Address destination connected to a TCP listener in `vehicle_service` (port 8089, with a "RAM Stream Sources" allowlist and the "DoorKing Stream Port" setting). RAM never reconnects a dropped stream by itself ("RAM Live Streaming format" below), so a stack of workarounds grew around it: `GET /ram-status` listing the connected RAM accounts; a stream watch in `DK_Sync.ahk` that restarted RAM when that list was empty; and a hold in systemd's file descriptor store (`fdstore.go`, `NotifyAccess` and `FileDescriptorStoreMax` in the unit) that kept RAM's connection open across a `vehicle_service` restart. All of it was removed when the Disk File proved able to replace the stream: it never needs reconnecting, it keeps recording while `vehicle_service` is down, and it can be rotated while RAM runs. The tests that led there are in "RAM Live Streaming format" below; the code is in git history (fsbhoa_ac_vehicle commits up to 2026-10-13).

## RAM Live Streaming format (captured 2026-10-09 and 10)

First captured from the workbench RAM. It covers only the record types seen so far: a granted card, and RAM's own connection status. Keypad codes, directory calls and denials are still to be captured. The raw capture is in `/home/pi/ram_stream.log`.

- **Transport (IP Address destination, no longer used):** RAM connects to us over TCP and keeps the connection open for its whole session. It reconnects for a new session. It sends nothing else, and expects no reply. Streaming works whether or not RAM's Live Transaction dialog is open, and with RAM minimized.
- **RAM doesn't reconnect on its own.** If the connection drops (a `vehicle_service` restart, a reboot, a network break), RAM opens a new one only when it starts its next job (a download, an upload, Send Time). A live transaction in between doesn't make it reconnect, and is never streamed (it is still in RAM's batch history). It also didn't retry after its first connection was refused. So a dropped connection stays down until someone prompts RAM, and an open RAM connection must be watched. This is why the Disk File replaced the stream. Accordingly, `vehicle_service` watches its config file and applies the address lists when the settings are saved, without restarting (`reload.go`; settings read only at start are logged as needing a restart), and the vehicle settings page shows each open RAM connection (address, accounts, last record) or an error when none is open.
- **Making RAM reconnect, retest 2026-10-13 (workbench):** RAM checks its stream connection only when it starts a job. Then it closes the dead connection and opens a new one, early in the job, before the panel answers. Each round below dropped the connection (stop and start of `vehicle_service`, with `DK_Sync.ahk` exited), checked that RAM stayed disconnected for 30 seconds, then ran a job on the WORKBENCH account (select WORKBENCH, then the account's line on the right, then the Action menu):
  - **Send Time,** twice: reconnected both times, 4 seconds after the click. Records: `Network Connection`, then `RS232 Database is Up To Date`.
  - **Upload Trans** (receiving the panel's transactions), twice: reconnected both times, 64 seconds after the click, because RAM prepares for about a minute first. Records: `Network Connection`, then `RS232 Upload Trans`. Too slow, and it downloads the panel's transactions, so it isn't the trigger to use.
  - **Config Ports → Tuning**, opened and closed several times with nothing changed: no reconnect in 10 minutes, and RAM sent nothing at all to the stream port (it didn't even close the dead connection).
  - **Session Re-Try Limit** (Config Ports → Tuning) raised from 3 to 30, the maximum: saving it reconnected the stream at once, with the same records as Send Time, so saving starts a panel session like a job does. With the new value and nobody touching RAM, a dropped stream stayed down for 10 minutes. So the limit doesn't make RAM retry the stream. The Tuning settings (Live Tran Handshake Timer, Live Tran Polling Interval, Sending Keep-alive Interval, Session Re-Try Limit) look like settings for RAM's session with the panel. RAM sends nothing on an idle stream connection (the only packets are this server's TCP keep-alives, every 15 seconds), so none of them is a stream keep-alive.
  - So Send Time was the preferred trigger, with a RAM restart as the fallback; with the Disk File as the source, reconnecting is no longer needed. Not yet tested: Send Time when the panel can't be reached (production reaches it by cellular, through DoorKing's cloud). Send Time also sets the panel's clock from the RAM PC, so that PC's clock must be right.
- **Making RAM reconnect, first tests** (workbench 2026-10-11): **starting RAM** opens the stream at once ("Network Connection" right after launch), and that is what `DK_Sync.ahk` relies on ("RAM reconnect" above). Other triggers looked unreliable then, but the 2026-10-13 retest above contradicts that for jobs. **Changing** an account's Live Streaming settings (format XML, then back to RAW Text) reconnected three times in the morning, with RAM closing any open connection and opening a new one at once; clicking OK with nothing changed did nothing. From early afternoon the same change, by hand or by script, made RAM send not a single packet (checked with tcpdump), even after RAM was restarted. An Upload Trans reconnected in the morning but not in the afternoon. What decides it is unknown. RAM sends all accounts over one connection: a status record for a second account arrived on the connection WORKBENCH had opened. No record was lost across a format switch (a swipe made while in XML arrived in XML).
- **Disk File destination** (tested on the workbench 2026-10-13). Live Streaming has three destinations, Com Port, Disk File and IP Address, and any or all can be on at once, each with the RAW Text or XML format (manual, 5.4.1 "Output Destinations"). With the IP destination left on, a Disk File was added at `Y:\Automation\DoorKing\Live_Testbed\stream.txt`, which the Pi reads at `/mnt/shared/Automation/DoorKing/Live_Testbed/stream.txt`:
  - **Same records as the stream,** in the same RAW Text layout, each one preceded by an empty line (CR LF): 162 bytes per record, the record line padded with spaces. Status records (`Network Connection`) are written too.
  - **Fast:** 21 swipes reached the file and the stream in the same order, each within about a second of the stream (the Pi polled the file every half second; Linux gets no change notices on a network share). The Pi can read the file while RAM writes it.
  - **The file keeps recording while the stream is down:** with the stream dropped, 10 swipes reached the file and none reached the stream.
  - **A RAM restart appends** to the file, starting with a `Network Connection` record; it doesn't start the file over.
  - **Swipes right after a RAM restart are lost from both.** Two swipes made just after RAM started reached neither the file nor the stream, although the stream had reconnected; swipes a minute later arrived. RAM seems to pass on live transactions only once its link to the panel is up (the manual's setup sequence waits for "Connection" in the Live Transactions window). Such swipes should still be in the panel's history, which Upload Trans downloads. So a RAM restart costs a short gap in the live feed, another reason to prefer Send Time for reconnecting (DoorKing TODO #5).
  - **It can be rotated while RAM runs.** Renaming `stream.txt` from the Pi worked, and RAM wrote the next record to a new `stream.txt`; the renamed file didn't change. So RAM opens the file for each record and doesn't hold it open.
  - **The NAS being unavailable wasn't tested,** on purpose: production and the office staff use the NAS. Decided 2026-10-13 that the file may live on the NAS: it's the most stable part of the network, and when it's down everything else is down too.
  - The workbench panel's clock was about a minute slow: a swipe at 13:30:30 was stamped 1:29PM. Send Time sets it from the RAM PC.
- **A configuration push doesn't disturb the stream** (five pushes by `DK_Sync.ahk` on the workbench, 2026-10-11): the connection stayed open, and RAM's status records for the push ("Network Connection", then "Download Data" or "Database is Up To Date") arrived over it.
- **Streaming is set per RAM account.** Each account (one per panel) has its own Live Streaming target. All accounts can use the same address and port; the account name in each record identifies the panel.
- **Two formats: RAW Text and XML.** Use RAW Text. The XML format carries the same fields under names (`Account`, `Timestamp`, `Event`, `Number`, `Name`, `Access`, `Relay`, plus `APB`, `Description` and `TrackerBoard`, all empty so far), one tag per line between `<Transaction>` and `</Transaction>`. It isn't valid XML (RAM doesn't escape `&` in names), and its timestamp still has whole minutes and a UTC offset that ignores daylight saving time (`-08:00` in October). `vehicle_service` accepts both, so a format switch can't lose a record; each journal entry records its `format`.
- **Office RAM PC:** 192.168.42.182 (production LAN). It first tried to stream to the testbed on 2026-10-10. Remote Desktop to it isn't available (port 3389 doesn't answer). On the testbed, connections arrive from the VPN address 192.168.70.3, not the RAM PC's own 192.168.1.41.
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

  RAM's status records (`Network`, `RS232`) put a message at offset 65 instead, such as `Connection`, `Download Data`, `Upload Trans` (a batch download of the panel's transactions), `Database is Up To Date` or `Failed To Connect`.
- **What this means for the design:**
  - The account name identifies the gate, so production's RAM account names are what map events to the north and south gates.
  - The time has whole minutes only. Correlation must use the time the record arrives, not the time in it.
  - A card record shows the device number but not the facility code.
  - The record type tells a tag (`Cards`) from a gate code (`Entry Code`). That holds for the 1838's own keypad. A Wiegand keypad that buffers the digits and sends them as one 26-bit frame is logged as `Cards`, indistinguishable from a tag; the panel ignored key-by-key Wiegand bursts (4 or 8 bits per key) entirely.
  - How the relay is chosen: for a device, by the reader input it was read on (the same device 06549 fires Relay 2 from the fob reader and Relay 1 from the Pi); for a gate code, by the code ranges in RAM's System Information; for a directory call, by the tone the resident presses (9 for Relay 1, 5 for Relay 2). So the relay isn't a reliable sign of which input was used. Keep it in `raw_details` only.
  - **Repeated reads.** The windshield tag reader scans about once a second and reports every read, so a car sitting in its range produces a stream of identical `Cards ... Admit` records (about one a second, each a separate beep at the gate). On the workbench, five fob swipes in six seconds gave six records. Merge identical reads (same gate, same type, same code) into one *presentation* (rules in `docs/GATE_SCENARIOS.md`); keep the first-read time, last-read time and read count in `raw_details`. No two cars share a tag, so a repeat of the same code is always the same car. No two households share a tag, and a gate code repeats only within one household; a code that Access Control finds in more than one household is a data error (see core's `TODO.md`), which the monitor shows as "+N others" rather than hiding.
  - **Two different codes at once.** Only one car fits in the reader's field, but a second readable tag can be in view at the same time (a spare tag in the car, or one carried nearby). A different code always starts its own presentation. If two presentations overlap the same vehicle event, attach both: the first one read is the credential shown, and the other is listed in `raw_details`.
  - **Live reporting runs in parallel with batch downloads** (tested on the workbench 2026-10-10): swipes streamed live were still in RAM's next batch download ("Upload Trans"). So turning on live reporting at a production panel doesn't change the transaction history the office staff use. The office RAM doesn't show live transactions today; live reporting is turned on at each panel, and whether DoorKing's cloud passes live transactions through is still to be seen.
  - Only successful entries are streamed. An unknown card produced no record at all, so failed attempts can't appear in the vehicle log, and a car with no DoorKing record means no *successful* credential.
  - Access Control identifies the person by the device number (a windshield tag) or the gate code (entered at a keypad). The record type says which of the two it is.
  - The name field is short (about 15 characters). It will eventually hold the household name, truncated, filled in from Access Control. It is only for debugging; never use it to identify anyone.
- **Glitches seen on 2026-10-10. The parser must survive them:**
  - **A flood of incomplete records.** After one Relay 1 swipe at 6:22 AM, RAM sent 28,432 copies of a record with only the account, the time and `Relay 1` (no type, device or result), in bursts of 23 about three times a second, for seven minutes. Count a record only if it has a type, a device number and a result, and drop exact repeats that arrive within a second or two. Real swipes of the same card can be seconds apart with identical text.
  - **Late delivery.** The real record for that 6:22 swipe arrived 15 minutes late. Later swipes, on both relays, arrived within seconds. So a vehicle event can't wait for its DoorKing record. It must be sent with the credential pending, and the DoorKing record attached when it arrives. Otherwise a delayed record makes the car look like it got through without a credential.
  - **A frozen time field.** From 6:22 AM on, every record carried `6:22AM` whatever the real time, which suggests the panel's clock or transaction timestamps were stuck. Use arrival time; keep the record's own time only in `raw_details`.
