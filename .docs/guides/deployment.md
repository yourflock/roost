# Roost — Production Deployment Guide

**Server**: roost-prod (CX23, Hetzner fsn1, 167.235.195.186)
**Domain**: roost.yourflock.com
**Stack**: Docker Compose + Cloudflare Tunnel (no inbound port exposure)

---

## Prerequisites

Before the first deploy, ensure these are done:

1. **Cloudflare Tunnel running** on roost-prod
   - Install cloudflared: `curl -fsSL https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb -o cloudflared.deb && dpkg -i cloudflared.deb`
   - Authenticate: `cloudflared tunnel login`
   - Create tunnel: `cloudflared tunnel create roost-prod`
   - Note the Tunnel ID and credentials file path
   - Update `infra/cloudflare/tunnel-config.yml` with your Tunnel ID

2. **DNS records** (Cloudflare dashboard — yourflock.com zone):
   - `roost.yourflock.com` → CNAME to `<tunnel-id>.cfargotunnel.com` (Proxied)
   - `relay.roost.yourflock.com` → CNAME to `<tunnel-id>.cfargotunnel.com` (Proxied)

3. **R2 buckets** created in Cloudflare dashboard:
   - `roost-streams` (live HLS segments)
   - `roost-vod` (VOD content)
   - `roost-backups` (database backups)

4. **Stripe** — create products and price IDs, add webhook endpoint:
   - Webhook URL: `https://roost.yourflock.com/billing/webhook`
   - Events: `customer.subscription.*`, `invoice.*`, `checkout.session.completed`

5. **GitHub Secrets** set in `yourflock/roost`:
   - `DEPLOY_SSH_KEY` — private key for roost-prod (matches `~/.ssh/flock_deploy`)
   - `PRODUCTION_SSH_USER` — `deploy`
   - `VERCEL_TOKEN` — from vault: `VERCEL_TOKEN`
   - `VERCEL_TEAM_ID` — from vault: `VERCEL_TEAM_ID`
   - `STRIPE_PUBLISHABLE_KEY` — Stripe dashboard (pk_live_...)

6. **GitHub Variables** set in `yourflock/roost`:
   - `ROOST_PRODUCTION_HOST` — `167.235.195.186`

---

## First Deploy (Fresh Server)

```bash
# 1. SSH to the server
ssh -i ~/.ssh/flock_deploy deploy@167.235.195.186

# 2. Clone the repo
git clone https://github.com/yourflock/roost.git /opt/roost
cd /opt/roost/backend

# 3. Create .env from template
cp .env.production.example .env
nano .env   # Fill in all CHANGEME values

# 4. Initialize the database
./scripts/init-db.sh \
  --host 127.0.0.1 \
  --port 5433 \
  --db-pass "$(grep POSTGRES_PASSWORD .env | cut -d= -f2)"

# 5. Start the full stack
docker compose -f docker-compose.prod.yml up -d

# 6. Check all services are healthy
docker compose -f docker-compose.prod.yml ps
docker compose -f docker-compose.prod.yml logs --tail=20

# 7. Apply Hasura metadata
cd hasura
hasura metadata apply --endpoint http://localhost:8081 \
  --admin-secret "$(grep HASURA_GRAPHQL_ADMIN_SECRET ../.env | cut -d= -f2)"
cd ..

# 8. Verify health
curl http://localhost/health          # nginx
curl http://localhost:8085/health     # billing
curl http://localhost:8091/owl/manifest.json  # owl_api

# 9. Once Cloudflare Tunnel is active, verify from outside:
curl https://roost.yourflock.com/health
curl https://roost.yourflock.com/owl/manifest.json
```

---

## Rolling Update (Normal Deploy)

Use the GitHub Actions workflow:

1. Tag the release: `git tag v1.2.3 && git push origin v1.2.3`
2. Go to GitHub Actions → "Deploy — Production"
3. Click "Run workflow"
4. Enter the version tag (e.g., `v1.2.3`)
5. Type `DEPLOY` to confirm
6. Monitor the workflow run

The workflow:
1. Validates the tag exists
2. Builds and pushes Docker images to GHCR (parallel, all 6 services)
3. SSH to server: pulls images, runs new migrations, rolling restart
4. Deploys web apps to Vercel
5. Health checks `roost.yourflock.com/health` and `/owl/manifest.json`
6. Auto-rolls back if health checks fail

---

## Manual Rolling Restart (SSH)

If you need to restart without a new code version:

```bash
ssh -i ~/.ssh/flock_deploy deploy@167.235.195.186
cd /opt/roost/backend

# Restart a single service
docker compose -f docker-compose.prod.yml restart relay

# Restart all app services (not infra)
docker compose -f docker-compose.prod.yml restart \
  billing ingest relay catalog epg owl_api nginx

# Full restart (includes postgres — causes ~10s downtime)
docker compose -f docker-compose.prod.yml down
docker compose -f docker-compose.prod.yml up -d
```

---

## Rollback (Manual)

If you need to roll back to a previous version without using the CI/CD rollback:

```bash
ssh -i ~/.ssh/flock_deploy deploy@167.235.195.186
cd /opt/roost

# List available versions
git tag --sort=-version:refname | head -10

# Roll back to a specific version
VERSION=v1.1.0
git checkout $VERSION
cd backend
IMAGE_TAG=$VERSION docker compose -f docker-compose.prod.yml pull
IMAGE_TAG=$VERSION docker compose -f docker-compose.prod.yml up -d \
  billing ingest relay catalog epg owl_api nginx cloudflared

# Verify
curl https://roost.yourflock.com/health
```

---

## Monitoring Checks

After any deploy, verify:

```bash
# All containers running
docker compose -f docker-compose.prod.yml ps

# Service health endpoints
curl http://localhost:8085/health   # billing
curl http://localhost:8094/health   # ingest
curl http://localhost:8090/health   # relay
curl http://localhost:8095/health   # catalog
curl http://localhost:8096/health   # epg
curl http://localhost:8091/health   # owl_api

# Owl addon manifest (public, no auth)
curl http://localhost:8091/owl/manifest.json | jq .

# Cloudflare Tunnel status
docker compose -f docker-compose.prod.yml logs cloudflared | tail -20

# External health (requires Cloudflare Tunnel to be active)
curl https://roost.yourflock.com/health
curl https://roost.yourflock.com/owl/manifest.json | jq .name
```

---

## Disk Space Check

The CX23 has 40 GB SSD. Monitor usage:

```bash
df -h /
docker system df

# If running low on segments disk space:
# Option A: Clean old segments (ingest service manages this automatically)
# Option B: Add a Hetzner Volume before upgrading server
#   hcloud volume create --size 50 --name roost-segments --location fsn1
#   hcloud volume attach <volume-id> --server roost-prod
```

---

## Server Upgrade

When load requires more resources (see GCI infrastructure policy):

```bash
# Using Hetzner API (requires hcloud CLI and HCLOUD_TOKEN set)
export HCLOUD_TOKEN=<your-hetzner-api-token>
hcloud server poweroff roost-prod
hcloud server change-type roost-prod --server-type cpx32   # 4 vCPU, 8 GB
hcloud server poweron roost-prod

# DO NOT use --upgrade-disk (prevents future downgrade)
```

Upgrade path: CX23 (2 vCPU, 4 GB) → CPX32 (4 vCPU, 8 GB) → CPX42 (8 vCPU, 16 GB)

---

## Environment Variables Reference

All production environment variables are documented in:
`backend/.env.production.example`

Never commit `.env` files. Use a local `.env.local` or your preferred secrets manager on dev machines.
