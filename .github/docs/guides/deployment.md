# Roost — Deployment Guide

This guide covers deploying Roost with Docker Compose on a Linux VPS or NAS.

---

## Prerequisites

- Docker Engine 24+ and Docker Compose v2
- A domain name with DNS pointing to your server (for HTTPS)
- 2 GB RAM minimum (4 GB recommended for transcoding)
- 20 GB disk minimum (more for DVR and VOD)

---

## First Deploy

```bash
# 1. Clone the repo
git clone https://github.com/unyeco/roost.git
cd roost

# 2. Configure
cp server/.env.example server/.env
nano server/.env   # set ROOST_SECRET_KEY and POSTGRES_PASSWORD at minimum

# 3. Start
docker compose -f packages/docker/docker-compose.yml up -d

# 4. Verify
curl http://localhost:8080/health
```

Roost is now running on port 8080 (direct) or port 80 (via nginx).

---

## HTTPS Setup

1. Set `ROOST_DOMAIN` and `ACME_EMAIL` in `server/.env`
2. Start the stack: `docker compose -f packages/docker/docker-compose.yml up -d`
3. Issue the first certificate:

   ```bash
   docker compose -f packages/docker/docker-compose.yml run --rm certbot certonly \
     --webroot -w /var/www/certbot \
     -d yourdomain.com \
     --email you@yourdomain.com \
     --agree-tos --no-eff-email
   ```

4. Uncomment the SSL server block in `packages/docker/nginx.conf`
5. Uncomment the `certbot` service in `packages/docker/docker-compose.yml`
6. Restart nginx: `docker compose -f packages/docker/docker-compose.yml restart nginx`

---

## Rolling Update

```bash
./packages/update.sh
```

Or manually:

```bash
docker compose -f packages/docker/docker-compose.yml pull
docker compose -f packages/docker/docker-compose.yml up -d
```

The update script checks for a new version, pulls images, restarts services, and
verifies health before finishing. Pass `--yes` to skip the confirmation prompt.

---

## Rollback

To revert to a previous version:

```bash
# Pull a specific version
IMAGE_TAG=v1.1.0 docker compose -f packages/docker/docker-compose.yml pull
IMAGE_TAG=v1.1.0 docker compose -f packages/docker/docker-compose.yml up -d

# Verify
curl http://localhost:8080/health
```

---

## Manual Restart

```bash
# Restart a single service
docker compose -f packages/docker/docker-compose.yml restart relay

# Restart all app services (not infra)
docker compose -f packages/docker/docker-compose.yml restart \
  roost ingest relay catalog epg owl_api nginx

# Full restart (includes postgres — causes ~5s downtime)
docker compose -f packages/docker/docker-compose.yml down
docker compose -f packages/docker/docker-compose.yml up -d
```

---

## Health Checks

After any deploy:

```bash
curl http://localhost:8080/health     # main API
curl http://localhost:8094/health     # ingest
curl http://localhost:8090/health     # relay
curl http://localhost:8095/health     # catalog
curl http://localhost:8096/health     # epg
curl http://localhost:8091/health     # owl_api

# Owl addon manifest (no auth required)
curl http://localhost:8091/owl/manifest.json | jq .name
```

---

## Disk Space

Monitor usage and manage storage:

```bash
df -h /
docker system df

# Clean unused images after updates
docker image prune -f
```

---

## Logs

```bash
# Follow all services
docker compose -f packages/docker/docker-compose.yml logs -f

# Single service
docker compose -f packages/docker/docker-compose.yml logs -f owl_api

# System service (DEB/RPM installs)
journalctl -u roost -f
```

---

## Environment Variables Reference

All configuration is via environment variables. See `server/.env.example` for the
full list with descriptions and defaults.
