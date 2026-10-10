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
- **LPR camera:** Speco O4BLP2M, one on each entrance lane and one on each exit lane. Only rear plates are captured.
- **NVR:** Speco N64NR, also on our LAN.

### Testbed workbench

The workbench DoorKing setup is on the developer's home network (192.168.1.x), which reaches the testbed's LAN (192.168.42.x) over a VPN. The VPN only connects one way: the testbed (192.168.42.62) can't reach 192.168.1.x.

- **RAM PC:** 192.168.1.41. Runs DoorKing's RAM software and the VPN, whose address is 192.168.70.3. RAM's Live Streaming output is pointed at the testbed.
- **RS-232-to-LAN adapter:** connects RAM to the 1838 controller's serial port. 192.168.1.40 port 1040 (confirmed reachable from the RAM PC, 2026-10-09). The `fsbhoa_ac_doorking` proxy config still records an older 192.168.1.50:10001.
- **Wiegand inputs on the 1838:**
  - An old fob reader. The test fob is printed "603 186 06549", read as facility code 186, card number 06549. It is entered in RAM as device 06549 and is admitted on Relay 2. The panel ignores facility codes.
  - A Raspberry Pi 3B at 192.168.1.210 that simulates a Wiegand card reader. Its GPIO 17 and 27 (BCM numbering) drive the controller's L0 (green) and L1 (white) data lines through transistors. The script is `tools/wiegand_sim.py`; copy it to the Pi to run it. By default it sends facility code 0, card 10018, which is loaded in the panel and admitted on Relay 1 (device 01234 is also loaded); `--facility` and `--card` send any other 26-bit card, and `--keys 1234#` simulates a Wiegand keypad (`--key-bits 4` or `8`).
- **The 1838's own keypad:** gate codes are entered as `#` followed by the code. A resident's real gate code is loaded for testing.

## Design status

The plugin is partly built. Its parts are the WordPress plugin (`fsbhoa_ac_vehicle.php`, settings in `includes/class-fsbhoa-vehicle-settings.php`) and the Go `vehicle_service`. Several pieces are still open:

- **Matching devices to gates and lanes.** `vehicle_service` keeps one in-progress event and doesn't know which gate (north or south) or lane (entrance or exit) an input came from. New settings in `class-fsbhoa-vehicle-settings.php` will map each device's IP address to its gate and lane, and the service must keep a separate event for each lane.
- **Exit events.** Not designed yet. On an exit lane, the LPR camera is the only input (no loop sensor, no DoorKing).
- **Camera input.** For each entry we want three things from the camera: a context photo of the vehicle taken when it leaves the loop, the plate photo, and the plate text from the LPR. The cameras are hard-wired IP cameras. The current FTP drop folder (`WatchDropDir` in `vehicle_service/main.go`) is a rough draft.
- **DoorKing live event feed.** The method isn't chosen yet. Plugins must be self-contained, relying only on core and never on another extension plugin. So the live feed may come straight to this plugin (`/webhook/doorking` in `vehicle_service`) rather than through `fsbhoa_ac_doorking`, which handles DoorKing configuration.

## RAM Live Streaming format (captured 2026-10-09 and 10)

First captured from the workbench RAM. It covers only the record types seen so far: a granted card, and RAM's own connection status. Keypad codes, directory calls and denials are still to be captured. The raw capture is in `/home/pi/ram_stream.log`.

- **Transport:** RAM connects to us over TCP and keeps the connection open for its whole session. It reconnects for a new session. It sends nothing else, and expects no reply. Streaming works whether or not RAM's Live Transaction dialog is open. On the testbed, connections arrive from the VPN address 192.168.70.3, not the RAM PC's own 192.168.1.41.
- **Framing:** each record is 162 bytes: CR LF, 158 characters of fixed-width ASCII padded with spaces, then CR LF. Split on CR LF and skip empty lines.
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
  - **Repeated reads.** The windshield tag reader scans about once a second and reports every read, so a car sitting in its range produces a stream of identical `Cards ... Admit` records (about one a second, each a separate beep at the gate). On the workbench, five fob swipes in six seconds gave six records. Merge identical reads (same gate, same type, same code) into one *presentation* while each arrives less than 5 seconds after the last; keep the first-read time, last-read time and read count in `raw_details`. No two cars share a tag, so a repeat of the same code is always the same car. No two households share a tag, and a gate code repeats only within one household; a code that Access Control finds in more than one household is a data error (see core's `TODO.md`), which the monitor shows as "+N others" rather than hiding.
  - **Two different codes at once.** Only one car fits in the reader's field, but a second readable tag can be in view at the same time (a spare tag in the car, or one carried nearby). A different code always starts its own presentation. If two presentations overlap the same vehicle event, attach both: the first one read is the credential shown, and the other is listed in `raw_details`.
  - Only successful entries are streamed. An unknown card produced no record at all, so failed attempts can't appear in the vehicle log, and a car with no DoorKing record means no *successful* credential.
  - Access Control identifies the person by the device number (a windshield tag) or the gate code (entered at a keypad). The record type says which of the two it is.
  - The name field is short (about 15 characters). It will eventually hold the household name, truncated, filled in from Access Control. It is only for debugging; never use it to identify anyone.
- **Glitches seen on 2026-10-10. The parser must survive them:**
  - **A flood of incomplete records.** After one Relay 1 swipe at 6:22 AM, RAM sent 28,432 copies of a record with only the account, the time and `Relay 1` (no type, device or result), in bursts of 23 about three times a second, for seven minutes. Count a record only if it has a type, a device number and a result, and drop exact repeats that arrive within a second or two. Real swipes of the same card can be seconds apart with identical text.
  - **Late delivery.** The real record for that 6:22 swipe arrived 15 minutes late. Later swipes, on both relays, arrived within seconds. So a vehicle event can't wait for its DoorKing record. It must be sent with the credential pending, and the DoorKing record attached when it arrives. Otherwise a delayed record makes the car look like it got through without a credential.
  - **A frozen time field.** From 6:22 AM on, every record carried `6:22AM` whatever the real time, which suggests the panel's clock or transaction timestamps were stuck. Use arrival time; keep the record's own time only in `raw_details`.
