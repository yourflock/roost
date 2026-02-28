# AntBox Changelog

## v1.0.0 (2026-02-24)

### Initial Stable Release

AntBox v1.0 is the first production-ready release of the AntBox daemon. This release
marks the AntBox daemon API and configuration format as stable.

### Features
- USB DVB/V4L2 tuner discovery and management
- MPEG-TS live stream capture from antenna channels
- gRPC streaming to Owl backend live_tv service
- Health check endpoint with device status reporting
- Configuration validation with clear error messages on invalid config
- Graceful shutdown on SIGINT/SIGTERM
- Log rotation (7-day retention, 100MB max per file)
- Cross-platform packaging: Docker image, .deb, .rpm
- Systemd service unit with automatic restart

### Compatibility
- Linux kernel 4.4+ with DVB subsystem enabled
- Raspberry Pi 4 (Raspberry Pi OS Bullseye+)
- Ubuntu 20.04+, Debian 11+, Fedora 35+, CentOS 9+
- Docker Engine 20.10+

### Configuration
See `configs/antbox.yaml.example` for the full configuration reference.
