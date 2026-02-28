# Roost — Windows Installation

Windows support is planned for a future release. Two delivery paths are in scope:

## Option 1 — Windows Installer (.msi)

A WiX-built `.msi` installer that:
- Installs the Roost binary to `Program Files\Roost\`
- Registers a Windows service (runs at startup, restarts on failure)
- Creates a start menu shortcut to the configuration folder
- Bundles a PostgreSQL installer (or connects to an existing instance)

Download the `.msi` from the [Releases page](https://github.com/unyeco/roost/releases) when available.

## Option 2 — Docker Desktop (available now)

Run Roost on Windows today using Docker Desktop:

1. Install [Docker Desktop for Windows](https://www.docker.com/products/docker-desktop/)
2. Clone the repo or download the compose file:
   ```
   curl -O https://raw.githubusercontent.com/unyeco/roost/main/packages/docker/docker-compose.yml
   ```
3. Copy and edit the example config:
   ```
   curl -O https://raw.githubusercontent.com/unyeco/roost/main/server/.env.example
   copy .env.example .env
   notepad .env
   ```
4. Start Roost:
   ```
   docker compose up -d
   ```

Roost is accessible at `http://localhost:8080`.

## Status

| Package | Status |
| --- | --- |
| `.msi` installer | Planned |
| Windows Service wrapper | Planned |
| Docker Compose | Works now via Docker Desktop |
| Chocolatey/WinGet package | Planned |

## Build Notes (for maintainers)

The Windows installer will be built with [WiX Toolset v4](https://wixtoolset.org/).
Source files will live in this directory:
```
packages/windows/
├── README.md           ← this file
├── roost.wxs           ← WiX installer definition (TODO)
├── installer.ico       ← installer icon (TODO)
└── build.sh            ← build script for CI (TODO)
```
