# Vehicle TODO

## REST authentication

Every service that calls WordPress should use the one **Access Verification API Key** (core General settings, `fsbhoa_ac_verify_api_key`), sent as `X-API-KEY` (see `fsbhoa_ac_core/other_docs/ARCHITECTURE.md`, "REST authentication").

- [ ] **1. Use the Access Verification API Key instead of the Shared Ingest Secret.** `POST /vehicle-event` is called only by the local vehicle service (`vehicle_service/main.go`), which sends `ingest_secret` in `X-FSBHOA-Secret` and `Authorization: Bearer`.
  - `fsbhoa_ac_vehicle_verify_ingest_perms()` (`fsbhoa_ac_vehicle.php`) fails open: an empty secret skips the check, and an empty allowed-IP list skips that too.
  - Check the key with `Fsbhoa_Verification_REST_API::api_key_permission_check` (fails closed). Keep the allowed-IP list as an extra check.
  - Remove the "Shared Ingest Secret" setting (`class-fsbhoa-vehicle-settings.php`). `write_config_from_array()` should copy the core key into `vehicle_service.json`, so it's written automatically when settings are saved (`fsbhoa_update_service_configs`).
  - In `vehicle_service`, send `X-API-KEY` instead of the two current headers. Rebuild and restart the service afterwards.
- [ ] **2. Two vehicle routes are open to anyone.** `GET /vehicle-image/{id}` (plate photos) and `GET /vehicle-recent` use `__return_true`. If only logged-in pages use them, require a logged-in user and send `X-WP-Nonce` from the page's JavaScript, like core's monitor routes.

## Move vehicle code out of core

Core must never reference an extension plugin (`fsbhoa_ac_core/other_docs/ARCHITECTURE.md`, "Core vs. extension plugins"). Core still holds vehicle pieces. Move each one into this plugin, and leave core with only a generic hook.

- [ ] **3. Inject the vehicle column into the live monitor with a hook.** The "Vehicle Gate Traffic" column (`#vehicle-log-column`) and the `#fsbhoa-vehicle-modal` image viewer are hard-coded in core's `includes/monitor/views/view-live-monitor.php`.
  - In core, replace the vehicle markup with a generic hook in `#activity-log-section`, such as `do_action( 'fsbhoa_monitor_activity_columns' )`, placed before the pedestrian column so extensions can add columns on the left. Add a hook for overlays (such as `fsbhoa_monitor_modals`) at the end of the view, and list both in `ARCHITECTURE.md` under "UI injection".
  - In this plugin, hook both and render the column and modal from a view file here, such as `includes/views/view-monitor-vehicle-column.php`.
  - Tie `fsbhoa_vehicle_enqueue_monitor_assets()` (`fsbhoa_ac_vehicle.php`) to the same hook instead of guessing from the page slug or the shortcode, so the script loads only where the column is rendered.
  - Check the monitor with the vehicle plugin turned off. The pedestrian column should fill the row, and nothing should mention vehicles.
- [ ] **4. Create `ac_vehicle_log` in this plugin.** Core creates it in `includes/class-fsbhoa-core-repository.php`, `db_schema.sql` and `migration.sql`.
  - Create the table in this plugin's activation hook, with `CREATE TABLE IF NOT EXISTS` so existing installs keep their data, and keep a schema SQL file in this repo.
  - Remove the table from core's repository and `db_schema.sql`. Leave the past `migration.sql` step alone, since production may already depend on it.
  - The core `CREATE TABLE` is broken right now: there's a missing comma after the `idx_circumvention` key, so it fails with a syntax error. Fix it in the moved copy.
- [ ] **5. Check core for any other vehicle-log references.** Search core for `ac_vehicle_log`, `vehicle-event`, `vehicle-recent` and `fsbhoa-vehicle`. Core's own vehicle registry (`ac_vehicles`, `fsbhoa_core_vehicle_saved`) belongs to core and stays.

## Event correlation (`vehicle_service`)

Items 6, 9, 10 and 11 come down to one redesign: keep one in-progress event per lane, and close it when the loop clears. Do them together with the device-to-gate/lane mapping (`CLAUDE.md`, "Design status").

- [ ] **6. Busy traffic is merged into one event, which hides tailgating.** `ensureActiveEvent()` (`main.go`) restarts the correlation timer on every input. Cars less than `correlation_window` seconds apart never close the event, and each new plate or code overwrites the last. A tailgater is merged into the car ahead, which is the case the circumvention flag exists to catch. Overwritten plate images are never deleted from the drop folder.
- [ ] **7. Cardholder lookup never matches.** `/vehicle-recent` joins `ac_credentials` on `auth_type` (such as `DK_WINDSHIELD` or `DK_ENTRY_CODE`), but `flushEvent()` never sends `auth_type`. Every row is stored with an empty type, so no cardholder is ever shown. The DoorKing feed must say whether the input was a windshield tag or a keypad PIN.
- [ ] **8. Event times are wrong.**
  - `flushEvent()` doesn't send `Timestamp`, so `fsbhoa_ac_vehicle_ingest_event()` stamps the row when the post arrives, at least `correlation_window` seconds late. Send the time of the first input.
  - The stored time is UTC (`current_time( 'mysql', 1 )`). `fsbhoa-vehicle-monitor.js` shows it as local time, so it's 7–8 hours off.
  - The first-load "last 24 hours" filter in `fsbhoa_ac_vehicle_get_recent()` compares against the database's `NOW()`. Pick one time zone for `event_timestamp` and use it everywhere.
- [ ] **9. Events are lost when WordPress is unreachable.** `flushEvent()` deletes the images from the drop folder before posting, and a failed post isn't retried. Delete only after a 2xx, and queue or retry failed posts.
- [ ] **10. The context photo is usually missed.** `HandlePlateImage()` looks for the `_src.jpg` scene image only when the plate image arrives. If the scene file lands later, it's never attached and never deleted. Files that don't match the pattern are never cleaned up either. (The camera interface is to be redesigned anyway; see `CLAUDE.md`.)
- [ ] **11. The loop "off" state is ignored.** `HandleLoopTrigger(false)` does nothing. Leaving the loop is when the context photo should be taken and the entry event closed.
- [ ] **12. Plate confidence.** `lpr_confidence` is never filled in.
- [ ] **13. Filename pattern.** `plateRegex` matches `VEHICE_…_plate_([A-Z0-9]+)\.jpg`. "VEHICE" is Speco's own spelling (confirmed from real captures in the testbed drop folder). The pattern drops plates containing a space, a dash or lowercase letters.

## Service setup and deployment

- [ ] **14. The webhooks can't be reached from the LAN by default.** The service listens on `daemon_host` (default `127.0.0.1`), so the Shelly and DoorKing can't connect. The settings page also uses `daemon_host` as the health-check address. Split it into a listen address and a health-check address.
- [ ] **15. The testbed can post to production.**
  - `write_config_from_array()` takes `fsbhoa_ac_wp_host`, which falls back to `access.fsbhoa.com` when unset. `LoadConfig()` then builds `http://<host>/wp-json/...`, so a testbed with the option unset sends its events to production.
  - Even on production, the request arrives from a non-local address and fails the default allowed-IP list (`127.0.0.1, ::1`).
  - Post to the local WordPress (`127.0.0.1` with the right `Host` header), and check `FSBHOA_AC_ENVIRONMENT` (fail closed), as `ARCHITECTURE.md` requires.
- [ ] **16. Hard-coded drop folder.** `WatchDir` is fixed at `/home/pi/lpr_ftp_drop` (`config.go`). Make it a setting. The production mini-PC may not have a `pi` user.
- [ ] **17. Unused settings.** `enable_debug_logging`, `image_retention_days` and `allowed_daemon_ips` are written to `vehicle_service.json`, but the service never reads them. Use them or remove them.
- [ ] **18. Health check "uptime".** The `/health` handler returns the current Unix time as `uptime`. Return the seconds since start.
- [ ] **19. Repository hygiene.**
  - The compiled `vehicle_service/fsbhoa_vehicle` binary is committed. Remove it from git and add a `.gitignore`.
  - The `fsbhoa_vehicle.service` systemd unit is installed in `/etc/systemd/system` but isn't in the repo. Add it, along with install steps.
  - `rebuild.sh` uses `$(pwd)`, so it works only when run from the repo root. Use the script's own directory.

## Security

- [ ] **20. The webhooks have no authentication.** `/webhook/loop` and `/webhook/doorking` accept any request, with any method. Anyone on the LAN can inject events. At a minimum, allow only the configured device IPs (see the device-mapping settings), and require the expected method.
- [ ] **21. Database errors are shown to anyone.** On a database error, `fsbhoa_ac_vehicle_get_recent()` returns the error and `last_query` to the caller with HTTP 200, and the route is public (item 2). Log the details, and return a generic 500.
- [ ] **22. The monitor builds cards from unescaped text.** `createCard()` in `fsbhoa-vehicle-monitor.js` puts `gate_identifier`, `auth_id`, the plate and `cardholder_name` into `innerHTML` without escaping. `sanitize_text_field` limits the risk, but the values come from unauthenticated webhooks. Escape them, or build the card with `textContent`.
- [ ] **23. The config file is readable by everyone.** `write_config_from_array()` writes `vehicle_service.json` with default permissions, and it will hold the API key (item 1). Set the permissions to 0640, with the group the service runs as. Report a failed write to the admin instead of hiding it with `@`.

## Settings page and monitor

- [x] **24. The settings page never shows the service status.** `render_settings_page()` calls `esc_html( $status_msg )` without `echo`, so the status label is always blank.
- [x] **25. The monitor shows a literal `&x2022;`.** In `createCard()`, the bullet entity is missing its `#`: use `&#x2022;`.
- [ ] **26. A blank modal when there's no context image.** `createCard()` opens the context image on click whenever there's a plate image, even if `has_context_img` is 0.
- [ ] **28. The monitor shows duplicate cards.** `fsbhoa_ac_vehicle_get_recent()` joins `ac_credentials` on type and value, but a value can belong to more than one credential. On the testbed, 261 active `DK_ENTRY_CODE` values (shared household PINs) and 8 `DK_WINDSHIELD` values have more than one row. Each match returns its own row, so one vehicle event shows as several cards with different cardholders. Return one row per event: pick a single credential, or list all the matching names.

## Not yet built

- [ ] **27. Image retention.** The `image_retention_days` setting exists, but nothing purges old images. Each event stores two image blobs in `ac_vehicle_log`. Add a daily WP-Cron job that sets `context_image_data` and `lpr_image_data` to NULL on rows older than the retention period.
