# Vehicle TODO

## REST authentication

Every service that calls WordPress should use the one **Access Verification API Key** (core General settings, `fsbhoa_ac_verify_api_key`), sent as `X-API-KEY` (see `fsbhoa_ac_core/other_docs/ARCHITECTURE.md`, "REST authentication").

- [ ] **1. Use the Access Verification API Key instead of the Shared Ingest Secret.** `POST /vehicle-event` is called only by the local vehicle service (`vehicle_service/main.go`), which sends `ingest_secret` in `X-FSBHOA-Secret` and `Authorization: Bearer`.
  - `fsbhoa_ac_vehicle_verify_ingest_perms()` (`fsbhoa_ac_vehicle.php`) fails open: an empty secret skips the check, and an empty allowed-IP list skips that too.
  - Check the key with `Fsbhoa_Verification_REST_API::api_key_permission_check` (fails closed). Keep the allowed-IP list as an extra check.
  - Remove the "Shared Ingest Secret" setting (`class-fsbhoa-vehicle-settings.php`). `write_config_from_array()` should copy the core key into `vehicle_service.json`, so it's written automatically when settings are saved (`fsbhoa_update_service_configs`).
  - In `vehicle_service`, send `X-API-KEY` instead of the two current headers. Rebuild and restart the service afterwards.
- [ ] **2. Two vehicle routes are open to anyone.** `GET /vehicle-image/{id}` (plate photos) and `GET /vehicle-recent` use `__return_true`. If only logged-in pages use them, require a logged-in user and send `X-WP-Nonce` from the page's JavaScript, like core's monitor routes.
