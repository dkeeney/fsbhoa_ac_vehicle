This plugin extends fsbhoa_ac_core.  @/home/pi/fsbhoa_ac_core/other_docs/ARCHITECTURE.md

## Purpose

This plugin connects the hardware that monitors vehicle access to the access control system.

It combines three inputs:
- **Loop detector events.** One component listens for events from the device that monitors the loop detector on the vehicle entrance lane. That device is a **Shelly Pro 1**.
- **License plate data** from our LPR cameras.
- **The authentication feed** from the DoorKing gate controller.

From these it builds a composite event that describes one vehicle entry. That event goes to the vehicle access log and to the real-time display.

- **Vehicle access log.** This plugin runs its own log, the `ac_vehicle_log` table, separate from core's `ac_access_log`. `POST /vehicle-event` writes to it.
- **Real-time display.** Vehicle events appear in the left column of core's live monitor ("Vehicle Gate Traffic"). `assets/js/fsbhoa-vehicle-monitor.js` polls `GET /vehicle-recent` every 3 seconds and fills that column.

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

## Design status

The plugin is partly built. Its parts are the WordPress plugin (`fsbhoa_ac_vehicle.php`, settings in `includes/class-fsbhoa-vehicle-settings.php`) and the Go `vehicle_service`. Several pieces are still open:

- **Matching devices to gates and lanes.** `vehicle_service` keeps one in-progress event and doesn't know which gate (north or south) or lane (entrance or exit) an input came from. New settings in `class-fsbhoa-vehicle-settings.php` will map each device's IP address to its gate and lane, and the service must keep a separate event for each lane.
- **Exit events.** Not designed yet. On an exit lane, the LPR camera is the only input (no loop sensor, no DoorKing).
- **Camera input.** For each entry we want three things from the camera: a context photo of the vehicle taken when it leaves the loop, the plate photo, and the plate text from the LPR. The cameras are hard-wired IP cameras. The current FTP drop folder (`WatchDropDir` in `vehicle_service/main.go`) is a rough draft.
- **Core still contains vehicle code.** This breaks the rule that core never references an extension. The "Vehicle Gate Traffic" column and the `fsbhoa-vehicle-modal` image viewer are hard-coded in core's `includes/monitor/views/view-live-monitor.php`, not added through a hook. Core also creates `ac_vehicle_log`, in `class-fsbhoa-core-repository.php`, `db_schema.sql` and `migration.sql`. Both should move into this plugin, behind a core monitor hook and the plugin's own table setup. See `TODO.md`, "Move vehicle code out of core".
- **DoorKing live event feed.** The method isn't chosen yet. Plugins must be self-contained, relying only on core and never on another extension plugin. So the live feed may come straight to this plugin (`/webhook/doorking` in `vehicle_service`) rather than through `fsbhoa_ac_doorking`, which handles DoorKing configuration.
