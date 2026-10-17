# Vehicle TODO

## REST authentication

Every service that calls WordPress should use the one **Access Verification API Key** (core General settings, `fsbhoa_ac_verify_api_key`), sent as `X-API-KEY` (see `fsbhoa_ac_core/other_docs/ARCHITECTURE.md`, "REST authentication").

- [x] **1. Use the Access Verification API Key instead of the Shared Ingest Secret.** `POST /vehicle-event` is called only by the local vehicle service (`vehicle_service/main.go`), which sends `ingest_secret` in `X-FSBHOA-Secret` and `Authorization: Bearer`.
  - `fsbhoa_ac_vehicle_verify_ingest_perms()` (`fsbhoa_ac_vehicle.php`) fails open: an empty secret skips the check, and an empty allowed-IP list skips that too.
  - Check the key with `Fsbhoa_Verification_REST_API::api_key_permission_check` (fails closed). Keep the allowed-IP list as an extra check.
  - Remove the "Shared Ingest Secret" setting (`class-fsbhoa-vehicle-settings.php`). `write_config_from_array()` should copy the core key into `vehicle_service.json`, so it's written automatically when settings are saved (`fsbhoa_update_service_configs`).
  - In `vehicle_service`, send `X-API-KEY` instead of the two current headers. Rebuild and restart the service afterwards.
- [x] **2. Two vehicle routes are open to anyone.** `GET /vehicle-image/{id}` (plate photos) and `GET /vehicle-recent` use `__return_true`. If only logged-in pages use them, require a logged-in user and send `X-WP-Nonce` from the page's JavaScript, like core's monitor routes.

## Move vehicle code out of core

Core must never reference an extension plugin (`fsbhoa_ac_core/other_docs/ARCHITECTURE.md`, "Core vs. extension plugins"). Core still holds vehicle pieces. Move each one into this plugin, and leave core with only a generic hook.

- [x] **3. Inject the vehicle column into the live monitor with a hook.** The "Vehicle Gate Traffic" column (`#vehicle-log-column`) and the `#fsbhoa-vehicle-modal` image viewer are hard-coded in core's `includes/monitor/views/view-live-monitor.php`.
  - In core, replace the vehicle markup with a generic hook in `#activity-log-section`, such as `do_action( 'fsbhoa_monitor_activity_columns' )`, placed before the pedestrian column so extensions can add columns on the left. Add a hook for overlays (such as `fsbhoa_monitor_modals`) at the end of the view, and list both in `ARCHITECTURE.md` under "UI injection".
  - In this plugin, hook both and render the column and modal from a view file here, such as `includes/views/view-monitor-vehicle-column.php`.
  - Tie `fsbhoa_vehicle_enqueue_monitor_assets()` (`fsbhoa_ac_vehicle.php`) to the same hook instead of guessing from the page slug or the shortcode, so the script loads only where the column is rendered.
  - Check the monitor with the vehicle plugin turned off. The pedestrian column should fill the row, and nothing should mention vehicles.
- [x] **4. Create `ac_vehicle_log` in this plugin.** Core creates it in `includes/class-fsbhoa-core-repository.php`, `db_schema.sql` and `migration.sql`.
  - Create the table in this plugin's activation hook, with `CREATE TABLE IF NOT EXISTS` so existing installs keep their data, and keep a schema SQL file in this repo.
  - Remove the table from core's repository and `db_schema.sql`. Leave the past `migration.sql` step alone, since production may already depend on it.
  - The core `CREATE TABLE` is broken right now: there's a missing comma after the `idx_circumvention` key, so it fails with a syntax error. Fix it in the moved copy.
  - Done. `includes/class-fsbhoa-vehicle-db.php` creates the table on activation and, through a schema version (`fsbhoa_vehicle_db_version`), on the first request after an update. It also adds `auth_type` and `idx_auth_lookup` to an older table that lacks them. `db_schema.sql` here is the reference copy.
  - Core's `migration.sql` had the same missing comma in step 1, which would have stopped the production migration there. The table was removed from `migration.sql` too, since this plugin now creates it.
- [x] **5. Check core for any other vehicle-log references.** Search core for `ac_vehicle_log`, `vehicle-event`, `vehicle-recent` and `fsbhoa-vehicle`. Core's own vehicle registry (`ac_vehicles`, `fsbhoa_core_vehicle_saved`) belongs to core and stays.

## Event correlation (`vehicle_service`)

Items 6, 9, 10 and 11 come down to one redesign: keep one in-progress event per lane, and close it when the loop clears. Do them together with the device-to-gate/lane mapping (`CLAUDE.md`, "Design status").

- [ ] **6. Busy traffic is merged into one event, which hides tailgating.** `ensureActiveEvent()` (`main.go`) restarts the correlation timer on every input. Cars less than `correlation_window` seconds apart never close the event, and each new plate or code overwrites the last. A tailgater is merged into the car ahead, which is the case the circumvention flag exists to catch. Overwritten plate images are never deleted from the drop folder.
- [ ] **7. Cardholder lookup never matches.** `/vehicle-recent` joins `ac_credentials` on `auth_type` (such as `DK_WINDSHIELD` or `DK_ENTRY_CODE`), but `flushEvent()` never sends `auth_type`. Every row is stored with an empty type, so no cardholder is ever shown. The DoorKing feed must say whether the input was a windshield tag or a keypad PIN.
- [x] **8. Event times are wrong.**
  - `flushEvent()` doesn't send `Timestamp`, so `fsbhoa_ac_vehicle_ingest_event()` stamps the row when the post arrives, at least `correlation_window` seconds late. Send the time of the first input.
  - The stored time is UTC (`current_time( 'mysql', 1 )`). `fsbhoa-vehicle-monitor.js` shows it as local time, so it's 7–8 hours off.
  - The first-load "last 24 hours" filter in `fsbhoa_ac_vehicle_get_recent()` compares against the database's `NOW()`. Pick one time zone for `event_timestamp` and use it everywhere.
  - Done: `event_timestamp` is local time (site time zone), like `ac_access_log`. The service sends the first input's time, and WordPress falls back to `current_time( 'mysql' )`.
  - **Production deployment:** rows stored before this fix are in UTC. Convert them with `UPDATE ac_vehicle_log SET event_timestamp = created_at WHERE TIMESTAMPDIFF(MINUTE, created_at, event_timestamp) BETWEEN 6*60-5 AND 8*60+5;` (`created_at` holds the local insert time). The testbed has been converted.
- [x] **9. Events are lost when WordPress is unreachable.** `flushEvent()` deletes the images from the drop folder before posting, and a failed post isn't retried. Delete only after a 2xx, and queue or retry failed posts. Done: `vehicle_service/dispatcher.go` queues each event in `/var/lib/fsbhoa/vehicle_queue` (photos are deleted once it's queued), posts oldest first, retries every 30s, and deletes it after a 2xx. 400/409/422 go to `vehicle_queue/failed`. `event_uid` (unique in `ac_vehicle_log`) stops a retried post being stored twice.
- [ ] **10. The context photo is usually missed.** `HandlePlateImage()` looks for the `_src.jpg` scene image only when the plate image arrives. If the scene file lands later, it's never attached and never deleted. Files that don't match the pattern are never cleaned up either. (The camera interface is to be redesigned anyway; see `CLAUDE.md`.)
  - 2026-10-17: the scene photo does arrive about a second after the plate crop. `HandlePlateImage()` is gone; the new reader (`vehicle_service/lpr.go`) waits up to 10 seconds for the pair, and the FTP setup deletes uploads after 7 days. Close this when correlation uses the reader's reads.
- [ ] **11. The loop "off" state is ignored.** `HandleLoopTrigger(false)` does nothing. Leaving the loop is when the context photo should be taken and the entry event closed.
- [ ] **12. Plate confidence.** `lpr_confidence` is never filled in.
  - 2026-10-16: the O4BLP2M reports no confidence (not in its uploads or its plate settings). Unless the NVR has one, the column stays empty.
- [ ] **13. Filename pattern.** `plateRegex` matches `VEHICE_…_plate_([A-Z0-9]+)\.jpg`. "VEHICE" is Speco's own spelling (confirmed from real captures in the testbed drop folder). The pattern drops plates containing a space, a dash or lowercase letters.
  - 2026-10-17: the new reader (`lpr.go`) takes any plate text (`_plate_(.*)\.jpg`) and keeps it as it is. A vehicle without a readable plate comes as `_plate_Unknown000.jpg`; the reader journals it with an empty `plate` and `no_plate`. Still to see: what the camera writes for plates with spaces or dashes.

## Service setup and deployment

- [x] **14. The webhooks can't be reached from the LAN by default.** The service listens on `daemon_host` (default `127.0.0.1`), so the Shelly and DoorKing can't connect. The settings page also uses `daemon_host` as the health-check address. Split it into a listen address and a health-check address. Done: the service listens on all interfaces (webhooks on `daemon_port`, DoorKing RAM stream on 8089); `daemon_host` is now only the address the settings page checks health at, and `/health` answers only this machine.
- [x] **15. The testbed can post to production.**
  - `write_config_from_array()` takes `fsbhoa_ac_wp_host`, which falls back to `access.fsbhoa.com` when unset. `LoadConfig()` then builds `http://<host>/wp-json/...`, so a testbed with the option unset sends its events to production.
  - Even on production, the request arrives from a non-local address and fails the default allowed-IP list (`127.0.0.1, ::1`).
  - Post to the local WordPress (`127.0.0.1` with the right `Host` header), and check `FSBHOA_AC_ENVIRONMENT` (fail closed), as `ARCHITECTURE.md` requires. Done: the service always posts to `127.0.0.1` with this site's own host (from `home_url()`) as the Host header, and sends nothing unless its `environment` is testbed or production. The dispatcher stamps the environment on each post, and `/vehicle-event` refuses it unless it matches this server's `FSBHOA_AC_ENVIRONMENT`.
- [x] **16. Hard-coded drop folder.** `WatchDir` is fixed at `/home/pi/lpr_ftp_drop` (`config.go`). Make it a setting. The production mini-PC may not have a `pi` user. Done 2026-10-17: the "LPR Upload Folder" setting (`lpr_drop_dir`, default `/var/lib/fsbhoa/lpr_drop`), served by vsftpd (`lpr_ftp/setup.sh`; `CLAUDE.md`, "Plate reads: FTP from the cameras").
- [x] **17. Unused settings.** `enable_debug_logging`, `image_retention_days` and `allowed_daemon_ips` are written to `vehicle_service.json`, but the service never reads them. Use them or remove them. Done: retention and the IP list are WordPress-only and no longer written to the JSON; the service uses `enable_debug_logging` for its per-input `[STATE]` lines.
- [x] **18. Health check "uptime".** The `/health` handler returns the current Unix time as `uptime`. Return the seconds since start.
- [x] **19. Repository hygiene.**
  - The compiled `vehicle_service/fsbhoa_vehicle` binary is committed. Remove it from git and add a `.gitignore`.
  - The `fsbhoa_vehicle.service` systemd unit is installed in `/etc/systemd/system` but isn't in the repo. Add it, along with install steps.
  - `rebuild.sh` uses `$(pwd)`, so it works only when run from the repo root. Use the script's own directory.

## Security

- [x] **20. The webhooks have no authentication.** `/webhook/loop` and `/webhook/doorking` accept any request, with any method. Anyone on the LAN can inject events. At a minimum, allow only the configured device IPs (see the device-mapping settings), and require the expected method. Done for addresses: the webhooks accept only the "Device Sources" setting's addresses and this machine, and the RAM listener only "RAM Stream Sources"; refusals are logged and journaled. Methods aren't restricted, since Shelly webhooks send GET. The address lists move into the per-lane gate settings when those are built.
- [x] **21. Database errors are shown to anyone.** On a database error, `fsbhoa_ac_vehicle_get_recent()` returns the error and `last_query` to the caller with HTTP 200, and the route is public (item 2). Log the details, and return a generic 500.
- [x] **22. The monitor builds cards from unescaped text.** `createCard()` in `fsbhoa-vehicle-monitor.js` puts `gate_identifier`, `auth_id`, the plate and `cardholder_name` into `innerHTML` without escaping. `sanitize_text_field` limits the risk, but the values come from unauthenticated webhooks. Escape them, or build the card with `textContent`.
- [x] **23. The config file is readable by everyone.** `write_config_from_array()` writes `vehicle_service.json` with default permissions, and it will hold the API key (item 1). Set the permissions to 0640, with the group the service runs as. Report a failed write to the admin instead of hiding it with `@`.

## Settings page and monitor

- [x] **24. The settings page never shows the service status.** `render_settings_page()` calls `esc_html( $status_msg )` without `echo`, so the status label is always blank.
- [x] **25. The monitor shows a literal `&x2022;`.** In `createCard()`, the bullet entity is missing its `#`: use `&#x2022;`.
- [x] **26. A blank modal when there's no context image.** `createCard()` opens the context image on click whenever there's a plate image, even if `has_context_img` is 0.
- [x] **28. The monitor shows duplicate cards.** `fsbhoa_ac_vehicle_get_recent()` joins `ac_credentials` on type and value, but a value can belong to more than one credential. On the testbed, 261 active `DK_ENTRY_CODE` values (shared household PINs) and 8 `DK_WINDSHIELD` values have more than one row. Each match returns its own row, so one vehicle event shows as several cards with different cardholders. Return one row per event: pick a single credential, or list all the matching names.

## Plugin split

- [x] **29. Hand the vehicle log, its routes and the monitor column to core.** Design: `CLAUDE.md`, "Plugin split: core owns the vehicle log". Core's side is item 5 in `fsbhoa_ac_core/TODO.md`. Do both in one change set and deploy them together.
  - Remove `includes/class-fsbhoa-vehicle-db.php`, `includes/class-fsbhoa-vehicle-retention.php`, `includes/views/view-monitor-vehicle-column.php`, `assets/js/fsbhoa-vehicle-monitor.js` and `db_schema.sql`. Remove the three routes and their callbacks from `fsbhoa_ac_vehicle.php`. Leave the `fsbhoa_vehicle_db_version` option in `wp_options`; it's no longer read.
  - Return true from core's `fsbhoa_monitor_show_vehicle_column` filter.
  - Settings: remove Allowed Daemon IPs and Image Retention. On upgrade, copy `image_retention_days` into core's retention setting once, and unschedule this plugin's purge hook.
  - Change the plugin description to vehicle lane hardware. Keep the folder name.
  - Update "Purpose" in `CLAUDE.md` (the log and display bullets) and mark the split as built.
  - Test: `tools/simulate_traffic.py` events reach the monitor, a reposted `event_uid` is ignored, a wrong-environment event is refused, the photo lightbox opens, and the purge clears old photos.
  - Done 2026-10-13. Removed the DB, retention and monitor-column classes, the monitor script, `db_schema.sql`, the three routes, and the Allowed Daemon IPs and Image Retention settings. `fsbhoa_vehicle_hand_off_to_core()` copied the testbed's retention (90 days) to core and cleared the old purge hook. Tested on the testbed with direct posts: a new event is stored, a repeated `event_uid` is answered as a duplicate, a production event gets 409, a post without the key gets 403, and the monitor routes need a login. Still to check in a browser: the monitor column and lightbox with real `vehicle_service` traffic.

## Not yet built

- [x] **27. Image retention.** The `image_retention_days` setting exists, but nothing purges old images. Each event stores two image blobs in `ac_vehicle_log`. Add a daily WP-Cron job that sets `context_image_data` and `lpr_image_data` to NULL on rows older than the retention period.

## RAM stream

- [x] **30. A `vehicle_service` restart cuts RAM's Live Streaming.** (Superseded the same day by #31: the stream and this hold were removed.) RAM reconnects only when it starts its next job, so each restart (an install, a crash) stopped the stream until `DK_Sync.ahk` restarted RAM. Done 2026-10-13: systemd holds the RAM listener and connections across restarts (`CLAUDE.md`, "RAM connection hold"); `rebuild.sh install` now restarts instead of stopping and starting. Tested with a loopback connection on the testbed.
  - Checked with RAM on 2026-10-13: after a restart of `vehicle_service`, a fob swipe at the workbench DoorKing still reached the live monitor. After a stop and start (as a reboot would), `DK_Sync.ahk` restarted RAM and the stream was back 12 seconds after the service started; a swipe then reached the monitor too.
- [ ] **31. Read RAM's Disk File instead of the stream.** Decided 2026-10-13. RAM writes every live transaction to a file as well as the stream (`CLAUDE.md`, "Disk File destination"). The file never needs reconnecting, kept recording while the stream was down, and can be rotated while RAM runs. It may live on the NAS (the most stable part of the network; everything stops without it anyway). The stream adds only about a second less delay, and a sign that RAM is running, which `DK_Sync.ahk` can check directly on the RAM PC. So the stream goes.
  - **Reading:** `vehicle_service` polls the file twice a second (Linux gets no change notices on a network share) and parses what was added since its saved position: the RAW Text records, each preceded by an empty line, with the same parser as the stream (`ParseRAMRecord`). It saves the position (and the file's identity, such as its size and first record) in its state directory, so a restart or reboot continues where it stopped and loses nothing. A file shorter than the saved position is a new file: read it from the start.
  - **Rotation:** once a day `vehicle_service` finishes reading `stream.txt`, renames it to `stream_YYYY-MM-DD.txt` and reads anything RAM appended between the last read and the rename. RAM starts a new `stream.txt` with its next record. Keep the old files as long as the journal (`journal_days`), then delete them.
  - **Folders and environment:** `Live_Testbed` and `Live_Production` under the DoorKing automation folder, chosen from the server's environment and refused when the environment is missing, as for `To_RAM_*`. The path is a vehicle setting, written to `vehicle_service.json`.
  - **RAM health:** built and tested 2026-10-13 on the workbench RAM PC: with `RamWatch=1` the heartbeat shows on the settings page; after RAM was closed, the script started it again within a minute (14:41:32), RAM wrote its `Network Connection` to the file, and two swipes a few seconds apart were read from the file and made one vehicle event.
  - **Retire the stream:** done 2026-10-13. Removed the RAM listener, `/ram-status`, the systemd hold (`fdstore.go`, the unit's `NotifyAccess` and `FileDescriptorStoreMax`), the DoorKing Stream Port and RAM Stream Sources settings, and the stream text on the settings page. In `fsbhoa_ac_doorking`, `DK_Sync.ahk`'s stream watch became a RAM watch (`RamWatch=1`: start RAM if it isn't running) with the heartbeat `ram_pc_status.txt`, which the settings page shows; DoorKing TODO #4 and #5 fall away. RAM still writes the file with its IP destination off (tested on the testbed).
  - **Built 2026-10-13:** the reader (`vehicle_service/ramfile.go`), the shared record handling (`ramLines` in `ram.go`), the RAM Stream File setting (`ram_file`, with the environment check), and the reader's status on the settings page and in `/health`. The testbed reads the file since 2026-10-13 14:03, with RAM's IP destination off. Setup steps: `CLAUDE.md`, "DoorKing records: from RAM's Disk File". The first daily rotation worked (2026-10-14). Still to do: production's setup at deployment (`CLAUDE.md`, "DoorKing records: from RAM's Disk File").
  - **Records missed after a RAM restart:** swipes in the first seconds after RAM starts reach neither the file nor the stream (they're still in the panel's history, which Upload Trans downloads). The file doesn't fix that; a RAM restart should stay rare.

## Alerts

- [ ] **32. Alert staff when a vehicle may have got in without a credential.** Decided 2026-10-16 (`CLAUDE.md`, "Purpose"): the monitor shows the event and makes a sound, so staff know when to look at the cameras. No loop or arm signal is available, so the first version matches DoorKing credentials with plate reads on the entrance lane:
  - a plate read (or a vehicle read without a plate) with no unused credential shortly before it: possible gate run or tailgate
  - two reads for one credential: possible tailgate
  - a credential and no read: not an alert (turned around, or the plate was missed)
  - Before alerting, record both side by side for a week or two (the journal: `ram_record` and `plate`) to measure the time from credential to plate, how often plates are missed, and whether directory calls appear in RAM. The fireman's key and the emergency strobe sensor open the gate without RAM seeing it, so those will raise false alerts.
  - Run it silently first (flags on the cards); turn the sound on once false alerts are rare. The monitor column, the sound and an acknowledge button are core's.
  - All four LPR cameras upload to the testbed since 2026-10-17.
  - Reads without a plate (`no_plate`) include vehicles that never came through the gate: the north entrance camera's first was a pickup passing on the cross street. Before such reads can raise alerts, they need filtering: from the study (how often, which cameras), then perhaps a smaller detection region or the direction setting ("further") in the camera, which is the user's decision.
