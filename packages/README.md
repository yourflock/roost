# Roost — Installation Guide

Roost is a self-hosted media backend for Owl. It handles Live TV, DVR, EPG, VOD, music, podcasts, and games.

This guide covers every supported installation method. Pick the one that matches your setup.

---

## Table of Contents

- [Docker Compose (recommended)](#docker-compose-recommended)
- [macOS (Homebrew)](#macos-homebrew)
- [Windows](#windows)
- [Synology DSM 7+](#synology-dsm-7)
- [QNAP QTS / QuTS](#qnap-qts--quts)
- [Unraid Community Applications](#unraid-community-applications)
- [Debian / Ubuntu (DEB)](#debian--ubuntu-deb)
- [Rocky Linux / RHEL / Fedora (RPM)](#rocky-linux--rhel--fedora-rpm)
- [Manual source build](#manual-source-build)
- [Updating Roost](#updating-roost)
- [Configuration reference](#configuration-reference)

---

## Docker Compose (recommended)

The simplest way to run Roost on any Linux machine with Docker installed.

### Requirements

- Docker Engine 24+ and Docker Compose v2
- 1 GB RAM minimum (2 GB recommended)
- 10 GB disk space (more for DVR recordings)

### One-liner quick start

```bash
git clone https://github.com/unyeco/roost.git
cd roost
cp server/.env.example server/.env
nano server/.env   # set ROOST_SECRET_KEY and POSTGRES_PASSWORD at minimum
docker compose -f packages/docker/docker-compose.yml up -d
```

Roost is now running. Open `http://localhost` (via Nginx) or `http://localhost:8080` (direct).

Verify: `curl http://localhost:8080/health` should return `{"status":"ok"}`.

### What gets started

| Service | Purpose |
| --- | --- |
| roost | Roost backend API |
| postgres | Database (not exposed externally) |
| redis | Rate limiting and caching (not exposed externally) |
| minio | Object storage for DVR and HLS segments |
| nginx | Reverse proxy, HTTP→HTTPS redirect, TLS termination |

### Enable HTTPS

1. Set `ROOST_DOMAIN` and `ACME_EMAIL` in `.env`
2. Start the stack: `docker compose -f docker-compose.roost.yml up -d`
3. Issue the first certificate:
   ```bash
   docker compose -f docker-compose.roost.yml run --rm certbot certonly \
     --webroot -w /var/www/certbot \
     -d yourdomain.com \
     --email you@yourdomain.com \
     --agree-tos --no-eff-email
   ```
4. Uncomment the SSL server block in `packages/docker/nginx.conf`
5. Uncomment the `certbot` service in `packages/docker/docker-compose.yml`
6. Restart: `docker compose -f packages/docker/docker-compose.yml up -d`

### Updating

```bash
./packages/update.sh
```

Or manually:

```bash
docker compose -f packages/docker/docker-compose.yml pull
docker compose -f packages/docker/docker-compose.yml up -d
```

---

## macOS (Homebrew)

Install via the Roost Homebrew tap:

```bash
brew install unyeco/tap/roost
brew services start roost
```

Roost requires PostgreSQL 16:

```bash
brew install postgresql@16
brew services start postgresql@16
```

See [packages/macos/roost.rb](macos/roost.rb) for formula details and configuration instructions.

---

## Windows

Windows installer (`.msi`) is planned. In the meantime, use Docker Desktop — see [packages/windows/README.md](windows/README.md).

---

## Synology DSM 7+

Install Roost from the Synology Package Center using a custom SPK package.

### Requirements

- DSM 7.0 or later
- x86_64 or arm64 NAS

### Steps

1. Download the latest SPK from the [Releases page](https://github.com/unyeco/roost/releases).
   Choose the correct architecture: `roost-{version}-amd64.spk` or `roost-{version}-aarch64.spk`.

2. In DSM, open **Package Center** → **Manual Install**

3. Upload the `.spk` file

4. Follow the install wizard:
   - Select your media library path (e.g. `/volume1/Media`)
   - Set an HTTP port (default: 7979)
   - Choose operating mode (default: private)
   - Set a secret key (at least 32 characters)

5. Click **Apply**. Roost starts automatically.

Access Roost at `http://NAS-IP:7979`.

### Configuration

Edit `/etc/roost/roost.env` to change settings, then restart from Package Center.

### Logs

View from **Package Center** → Roost → **Log**, or SSH to the NAS:

```bash
cat /var/log/roost/roost.log
```

---

## QNAP QTS / QuTS

Install Roost as a QPKG on QTS 5.x or QuTS Hero h5.x.

### Requirements

- QTS 5.0+ or QuTS Hero h5.0+
- x86_64 or arm64 NAS

### Steps

1. Download the latest QPKG from the [Releases page](https://github.com/unyeco/roost/releases).
   Choose: `Roost_{version}_x86_64.qpkg` or `Roost_{version}_aarch64.qpkg`.

2. In App Center, click **Install Manually** (gear icon → Manual Installation)

3. Upload the `.qpkg` file and follow the prompts

4. After installation, Roost starts on port 7979.

### Configuration

Edit `/etc/config/roost.conf` via SSH, then restart:

```bash
/etc/init.d/Roost.sh restart
```

### Logs

```bash
cat /var/log/roost.log
```

---

## Unraid Community Applications

Install Roost through the Unraid Community Applications plugin.

### Requirements

- Unraid 6.12+
- Community Applications plugin installed

### Steps

1. In Unraid, click **Apps** in the top menu

2. Search for **Roost**

3. Click **Install**

4. Fill in required settings:
   - **Secret Key**: generate with `openssl rand -hex 32`
   - **App Data / Config**: path for persistent data (e.g. `/mnt/user/appdata/roost`)
   - **Postgres Password**: any strong password

5. Optionally set your media library path and DVR recordings path

6. Click **Apply**

Access Roost at `http://UNRAID-IP:7979`.

See [packages/unraid/README.unraid.md](unraid/README.unraid.md) for detailed Unraid instructions.

---

## Debian / Ubuntu (DEB)

Install Roost as a managed system service on Debian 12+ or Ubuntu 22.04+.

### Steps

1. Download the `.deb` package from the [Releases page](https://github.com/unyeco/roost/releases):

   ```bash
   wget https://github.com/unyeco/roost/releases/download/v1.0.0/roost_1.0.0_amd64.deb
   ```

2. Install:

   ```bash
   sudo apt install ./roost_1.0.0_amd64.deb
   ```

3. Configure:

   ```bash
   sudo nano /etc/roost/roost.env
   # Set ROOST_SECRET_KEY and POSTGRES_PASSWORD at minimum
   ```

4. Start:

   ```bash
   sudo systemctl enable --now roost
   ```

5. Verify:

   ```bash
   curl http://localhost:8080/health
   systemctl status roost
   ```

### Logs

```bash
journalctl -u roost -f
```

### Updating

```bash
wget https://github.com/unyeco/roost/releases/download/v{new-version}/roost_{new-version}_amd64.deb
sudo dpkg -i roost_{new-version}_amd64.deb
```

The service restarts automatically after upgrade.

---

## Rocky Linux / RHEL / Fedora (RPM)

Install Roost as a managed system service on Rocky Linux 9+, RHEL 9+, or Fedora 39+.

### Steps

1. Download the `.rpm` package from the [Releases page](https://github.com/unyeco/roost/releases):

   ```bash
   wget https://github.com/unyeco/roost/releases/download/v1.0.0/roost-1.0.0-1.x86_64.rpm
   ```

2. Install:

   ```bash
   sudo dnf install ./roost-1.0.0-1.x86_64.rpm
   ```

3. Configure:

   ```bash
   sudo nano /etc/roost/roost.env
   # Set ROOST_SECRET_KEY and POSTGRES_PASSWORD at minimum
   ```

4. Start:

   ```bash
   sudo systemctl enable --now roost
   ```

5. Verify:

   ```bash
   curl http://localhost:8080/health
   systemctl status roost
   ```

### Logs

```bash
journalctl -u roost -f
```

---

## Manual Source Build

Build Roost from source on any system with Go installed.

### Requirements

- Go 1.22+
- PostgreSQL 16+
- Redis 7+ (optional — disables rate limiting if absent)

### Steps

```bash
# Clone
git clone https://github.com/unyeco/roost.git
cd roost

# Build
cd server
go build -o bin/roost ./cmd/api/

# Configure
cp .env.example .env
nano .env  # set ROOST_SECRET_KEY, POSTGRES_PASSWORD, etc.

# Run
./bin/roost
```

Roost listens on port 8080 by default. Set `PORT` in `.env` to change it.

---

## Updating Roost

### Docker Compose

Use the included update script for a zero-downtime update:

```bash
./packages/update.sh
```

Check for updates without installing:

```bash
./packages/update.sh --check
```

Automated (cron) updates:

```bash
./packages/update.sh --yes
```

### Synology / QNAP

Roost checks for updates in the background. When an update is available, a notification
appears in the Package Center / App Center. Click **Update** to install.

### DEB / RPM

Roost is added to the package manager repository. Updates are installed with:

```bash
# Debian / Ubuntu
sudo apt update && sudo apt upgrade roost

# Rocky / RHEL / Fedora
sudo dnf upgrade roost
```

---

## Configuration Reference

All configuration is via environment variables. See [`server/.env.example`](../server/.env.example) for
the full list with comments.

### Required for all installs

| Variable | Description |
| --- | --- |
| `ROOST_SECRET_KEY` | JWT signing secret. Generate: `openssl rand -hex 32` |
| `POSTGRES_PASSWORD` | PostgreSQL password |

### Optional but recommended

| Variable | Default | Description |
| --- | --- | --- |
| `ROOST_MODE` | `private` | `private` (self-hosted) or `public` (managed) |
| `ROOST_DOMAIN` | `localhost` | Public hostname for CORS and cookies |
| `PORT` | `8080` | HTTP listen port inside the container |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis URL. Rate limiting disabled if not set |

### Public mode only (ROOST_MODE=public)

| Variable | Description |
| --- | --- |
| `STRIPE_SECRET_KEY` | Stripe secret key |
| `STRIPE_WEBHOOK_SECRET` | Stripe webhook signing secret |
| `CDN_RELAY_URL` | CDN relay endpoint for obfuscated stream delivery |
| `CDN_HMAC_SECRET` | HMAC secret for signing CDN stream URLs |

Roost fails fast at startup if any public-mode variable is missing when `ROOST_MODE=public`.
