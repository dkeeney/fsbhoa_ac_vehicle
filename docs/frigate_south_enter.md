# Proposal: South Enter's overview camera in Frigate

Status: **proposal, for the user's approval** (2026-10-22). Nothing on alarm.fsbhoa.com changes until then. Its Frigate belongs to the pool intrusion system (`fsbhoa_intrusion`); the user's rule is that `frigate.yml` is not changed and Frigate is not restarted without the user's explicit OK, and never during the intrusion alert window, 22:30 to 04:40.

## Why

The car count (`vehicle_service/count.go`) only knows that pixels changed in two zones of .141's picture. It can't tell one vehicle from the next, so a truck and its trailer can count as two cars (2026-10-20 12:29, a tree-service truck towing a chipper), a close tailgater can merge with the car ahead, a bicycle is missed, and a white truck body read as the lowered arm. Frigate's object detection tracks each vehicle as one object with an ID through the zones. The user's choice (2026-10-22): accurate camera detection at the gate rather than a second photo beam, which would shine into DoorKing's safety beam receiver.

## The alarm box can take it

Read-only, 2026-10-22 11:35: Frigate 0.17.2, the OpenVINO detector on the Intel GPU at 6.8 ms an inference (about 145 a second), now doing about 43 a second for the five pool cameras (about 30% busy). Frigate uses 2.1 of the i5-13500H's 16 threads (the whole system 14%, load 3.4), 18 GB of 23 free, 326 GB of disk free. .141 at 10 frames a second would add up to about 15 to 20 detections a second while vehicles move (about 40% busy in all). The alarm box reaches .141 through the main router and the south gates' NanoBeam link (3 to 18 ms), not the pool's; its second stream is about 0.5 to 1 Mbit/s, a fraction of the 4K stream the NVR already takes over that link.

## The camera block

The Reolink login (the "All other cameras" line of the passwords file, `admin`) goes in two environment variables of the Frigate container, `FRIGATE_REOLINK_USER` and `FRIGATE_REOLINK_PASSWORD`, not in the file. Coordinates are pixels of the 640 x 360 detect stream, from the zones in the vehicle settings (Car Count Zones).

```yaml
cameras:
  south_enter:
    ffmpeg:
      inputs:
        - path: rtsp://{FRIGATE_REOLINK_USER}:{FRIGATE_REOLINK_PASSWORD}@192.168.107.141:554/h264Preview_01_sub
          roles: [detect]
    detect:
      width: 640
      height: 360
      fps: 10
    record:
      enabled: false        # the NVR records it
    snapshots:
      enabled: false
    objects:
      track: [car, truck, motorcycle, bicycle, person]
    zones:
      approach:             # the lane between the keypad and the lowered arm
        coordinates: 115,360,192,223,269,162,448,162,422,227,256,360
        inertia: 2
      beyond:               # past the arm, where a vehicle has come in
        coordinates: 269,360,448,238,640,209,640,360
        inertia: 2
    review:
      alerts:
        labels: []          # nothing from the gate in the intrusion system's alerts
      detections:
        labels: []
```

To check against the running `frigate.yml` before it goes in: whether the pool cameras use a global `hwaccel_args` (VAAPI, which crashed for two of them early on 2026-10-22). This camera's small stream decodes cheaply on the CPU, so it could leave the GPU decoder alone; the exact override is to be taken from Frigate 0.17's documentation and the existing file.

## The intrusion listener must ignore it

`fsbhoa_intrusion`'s Go listener calls the PBX on Frigate detections. Before the camera goes in, confirm with that project that the listener acts only on its pool cameras (or zones), so a person or car at South Enter can never raise a pool alarm. If it filters by label or zone alone, it needs a camera filter first.

## What the testbed does with it

`vehicle_service` subscribes, read-only, to `frigate/events` on the alarm box's Mosquitto (port 1883) and journals each event for `south_enter` (`frigate_event`: the object's ID, label, the zones it entered, start and end), next to its own counts and the plate reads. For a week nothing else changes; then the counts are compared: per vehicle against the plates, close followers, trucks with trailers, bicycles, night. If Frigate's tracks hold up, a vehicle entering "beyond" after "approach" replaces the pixel count (`car` events), and the arm stays with the bar regions (or a small trained classifier, if they fail at night). It needs a Go MQTT client in `vehicle_service` and an MQTT setting; the testbed never publishes.

A trailer may come out as a second truck, or not at all: the standard model has no trailer class. The week will show which; if it's the first, a model trained on .141's kept passes (full size since 2026-10-22) with a trailer class is the next step.

## Steps, once approved

1. The intrusion session confirms the listener ignores `south_enter`, and changes it if needed.
2. Outside 22:30 to 04:40: add the two environment variables to the Frigate container, add the camera block, restart Frigate, and watch the pool cameras (fps, detector, no ffmpeg crashes) for an hour.
3. Build and install the MQTT journaling in `vehicle_service`.
4. A week later: compare and decide.
