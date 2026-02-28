# antbox

Edge capture and encoding subsystem for OTA ingest.

## Responsibilities

- tuner and channel control on edge hardware
- live encoding/transcoding pipeline initiation
- reliable stream upload to antserver
- local buffering and recovery behavior
- health telemetry and heartbeat reporting

## Hardware Direction (Reference)

- Intel NUC-class edge node with Quick Sync support
- HDHomeRun tuner integration
- directional antenna setup for OTA channels

## Folder Map

```text
antbox/
├── daemon/
├── drivers/
│   ├── hdhomerun/
│   └── atsc/
├── pipelines/
│   ├── live/
│   ├── recording/
│   └── postprocess/
├── configs/
├── systemd/
├── scripts/
├── docs/
```

## Documentation

- `docs/hardware/reference-design.md`
- `docs/pipelines/live-flow.md`
- `docs/ops/operations.md`
- `docs/security/device-trust.md`
