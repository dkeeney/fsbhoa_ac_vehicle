# Proposal: South Enter's overview camera in Frigate

Status: **proposal, for the user's approval** (2026-10-22). Nothing on alarm.fsbhoa.com changes until then. Its Frigate belongs to the pool intrusion system (`fsbhoa_intrusion`); the user's rule is that `frigate.yml` is not changed and Frigate is not restarted without the user's explicit OK, and never during the intrusion alert window, 22:30 to 04:40.

## Why

The car count (`vehicle_service/count.go`) only knows that pixels changed in two zones of .141's picture. It can't tell one vehicle from the next, so a truck and its trailer can count as two cars (2026-10-20 12:29, a tree-service truck towing a chipper), a close tailgater can merge with the car ahead, a bicycle is missed, and a white truck body read as the lowered arm. Frigate's object detection tracks each vehicle as one object with an ID through the zones. The user's choice (2026-10-22): accurate camera detection at the gate rather than a second photo beam, which would shine into DoorKing's safety beam receiver.

## The alarm box can take it

Read-only, 2026-10-22 11:35: Frigate 0.17.2, the OpenVINO detector on the Intel GPU at 6.8 ms an inference (about 145 a second), now doing about 43 a second for the five pool cameras (about 30% busy). Frigate uses 2.1 of the i5-13500H's 16 threads (the whole system 14%, load 3.4), 18 GB of 23 free, 326 GB of disk free. .141 at 10 frames a second would add up to about 15 to 20 detections a second while vehicles move (about 40% busy in all). The alarm box reaches .141 through the main router and the south gates' own NanoBeam link (3 to 18 ms; the lodge has three links, to the pool, the north gates and the south gates, the user, 2026-10-22), not the pool's; its second stream is about 0.5 to 1 Mbit/s, a fraction of the 4K stream the NVR already takes over that link.

## A Frigate of its own for the gates

The user's preference (2026-10-22): keep the gates apart from the pool alarm. A second Frigate container on the same machine does that, at the cost of about 2 to 3 GB of memory (18 GB free) and one more container to keep updated. The existing one (read 2026-10-22) runs in the Docker network `fsbhoa_intrusion_frigate_net` with its config in `~/fsbhoa_intrusion/`, its API on 127.0.0.1:5000, its web page on 8971, go2rtc on 8554 and 8555, `/dev/dri` for the GPU, and Mosquitto on 1883 in the same network. The gates' instance:

- its own folder, `~/fsbhoa_gates/` (a `docker-compose.yml`, `config/config.yml`, no recordings), and its own Docker network;
- its own Mosquitto, published on 1884, with the gates' Frigate pointed at it by container name on their own network and `topic_prefix: frigate_gates` as a second safeguard. This matters: the intrusion listener doesn't filter by camera, so any `person` event on the pool's broker (1883, `frigate/events`) counts toward the pool alarm and can place a PBX call (the intrusion session, 2026-10-22). The gates' Frigate must never publish to 1883;
- its API on 127.0.0.1:5002 (Apache holds 5001, the user's HTTPS route to the pool Frigate) and its web page on 8972; go2rtc's 8554 and 8555 not published (the pool Frigate holds them). In use on alarm (2026-10-22): Apache 80, 443, 5001, 8090; Docker 1883, 8555, 8971, 127.0.0.1:5000, 127.0.0.1:8554; the listener 127.0.0.1:8091;
- decoding on the CPU (640 x 360 at 10 fps is cheap), off the GPU's video decoder;
- `/dev/dri` shared: each instance loads its own copy of the detection model on the GPU (OpenVINO). At today's load (about 43 detections a second for the pool, up to about 20 for the gate, against about 145) that fits.
- Restarts and upgrades of either leave the other alone. They still share the machine, the GPU and its driver: a GPU hang or a reboot stops both. If the gates grow (North Enter's cameras), a small PC of their own removes that.

The pool's GPU decoding crashes of 2026-10-22 morning ("Failed to sync surface") came from corrupt input on Pool_SW over the pool's NanoBeam link, not from GPU load (the intrusion session), and are resolved (the user). The pool Frigate keeps the GPU about 19% busy.

## The camera block

The Reolink login (the "All other cameras" line of the passwords file, `admin`) goes in two environment variables of the Frigate container, `FRIGATE_REOLINK_USER` and `FRIGATE_REOLINK_PASSWORD`, not in the file. Coordinates are pixels of the 640 x 360 detect stream, from the zones in the vehicle settings (Car Count Zones).

```yaml
mqtt:
  host: gates-mosquitto     # the gates' own broker, by container name on their network; never the pool's (1883)
  port: 1883                # inside the gates' network; published on the host as 1884
  topic_prefix: frigate_gates
  client_id: frigate_gates

detectors:                  # as the pool's (OpenVINO on the GPU); copy its detector and model sections
  ov:
    type: openvino
    device: GPU

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

No `hwaccel_args`: in its own instance the camera decodes on the CPU, which is cheap for 640 x 360 at 10 frames a second and leaves the GPU's video decoder to the pool. The detector's `model` section (the OpenVINO model's path, size and input format) is copied from the pool's `frigate.yml` when the file is written.

## The intrusion listener

`fsbhoa_intrusion`'s Go listener calls the PBX on Frigate detections. With the gates on their own Frigate and Mosquitto it never receives a gate event. (Had the camera gone into the pool's Frigate, the listener would have needed to act only on its pool cameras.)

## What the testbed does with it

`vehicle_service` subscribes, read-only, to `frigate_gates/events` on the gates' Mosquitto (port 1884) and journals each event for `south_enter` (`frigate_event`: the object's ID, label, the zones it entered, start and end), next to its own counts and the plate reads. For a week nothing else changes; then the counts are compared: per vehicle against the plates, close followers, trucks with trailers, bicycles, night. If Frigate's tracks hold up, a vehicle entering "beyond" after "approach" replaces the pixel count (`car` events), and the arm stays with the bar regions (or a small trained classifier, if they fail at night). It needs a Go MQTT client in `vehicle_service` and an MQTT setting; the testbed never publishes.

A trailer may come out as a second truck, or not at all: the standard model has no trailer class. The week will show which; if it's the first, a model trained on .141's kept passes (full size since 2026-10-22) with a trailer class is the next step.

## Steps, once approved

1. Write `~/fsbhoa_gates/` on alarm (compose file, config, the Reolink login in an `.env` file readable only by `fsbhoa`), reviewed with the intrusion session.
2. Outside 22:30 to 04:40: start the gates' containers (the pool's Frigate isn't touched or restarted) and watch the pool cameras (fps, detector speed, no ffmpeg crashes) for an hour.
3. Build and install the MQTT journaling in `vehicle_service`.
4. A week later: compare and decide.
