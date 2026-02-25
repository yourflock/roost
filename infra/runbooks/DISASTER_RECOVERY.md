# Roost Disaster Recovery Plan

**Last updated**: 2026-02-24
**Version**: 1.0

---

## Recovery Objectives

| Metric | Target | Notes |
|--------|--------|-------|
| **RTO** (Recovery Time Objective) | 1 hour | Time to full service restore from scratch |
| **RPO** (Recovery Point Objective) | 15 minutes (target) / 24 hours (current) | Current: daily backups. Target: WAL streaming |
| **MTTR** (Mean Time to Recover) | <30 minutes | Average across incidents |

**Current RPO Note**: Backups run daily at 03:00 UTC. A disk failure at 02:59 UTC means
up to 24 hours of data loss. Billing data is independently preserved by Stripe. Upgrade
to WAL streaming (continuous backup) is planned for Phase 17.

---

## Infrastructure Overview

```
roost-prod (Hetzner CX23, fsn1, 167.235.195.186)
├── docker-compose.prod.yml
│   ├── roost-postgres (Postgres 16)
│   ├── roost-relay    (HLS relay, port 8090)
│   ├── roost-owl-api  (Owl addon API, port 8091)
│   ├── roost-billing  (Billing service, port 8085)
│   ├── roost-catalog  (VOD catalog, port 8093)
│   ├── roost-epg      (EPG service, port 8092)
│   ├── roost-ingest   (Stream ingest, port 8094)
│   └── cloudflared    (Cloudflare tunnel)
│
Cloudflare (CDN + Tunnel)
├── roost.yourflock.org → roost-prod tunnel
├── R2: roost-streams (live HLS segments)
├── R2: roost-vod (VOD files)
└── R2: roost-backups (DB backups)
```

---

## Scenario 1: Complete Server Loss (Hetzner VPS destroyed)

**Cause**: Hardware failure, accidental deletion, prolonged outage.
**Target RTO**: 60 minutes.

### Phase 1: Provision New Server (10 min)

```bash
export HCLOUD_TOKEN=<your-hetzner-api-token>  # set your credentials

# Create new CX23 in fsn1
curl -s -X POST https://api.hetzner.cloud/v1/servers \
  -H "Authorization: Bearer $HETZNER_FLOCK_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "roost-prod",
    "server_type": "cx23",
    "location": "fsn1",
    "image": "ubuntu-24.04",
    "ssh_keys": [108068760],
    "user_data": "#cloud-config\npackages:\n  - docker.io\n  - docker-compose-plugin"
  }' | jq .server.public_net.ipv4.ip
```

Update DNS in Cloudflare if using direct IP (not Tunnel):
```bash
# If using Cloudflare Tunnel (recommended), no DNS change needed.
# The tunnel reconnects automatically when cloudflared starts on the new server.
```

### Phase 2: Install Dependencies (5 min)

```bash
ssh -i ~/.ssh/flock_deploy root@NEW_SERVER_IP
apt-get update && apt-get install -y docker.io docker-compose-plugin curl wget
# Install cloudflared
curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb \
  -o cloudflared.deb && dpkg -i cloudflared.deb
```

### Phase 3: Restore Application Code (5 min)

```bash
# Clone the repo (from GitHub)
git clone https://github.com/yourflock/roost /opt/roost
cd /opt/roost

# Copy production .env file (stored encrypted in your local secrets file (e.g. ~/.roost-secrets.env) or 1Password)
# Create .env.prod with all required environment variables
cp .env.prod.template .env.prod
# Edit with production values from vault
```

### Phase 4: Restore Database (20 min)

```bash
export HCLOUD_TOKEN=<your-hetzner-api-token>  # set your credentials

# List backups
aws s3 ls s3://roost-backups/postgres/ \
  --endpoint-url https://${CLOUDFLARE_ACCOUNT_ID}.r2.cloudflarestorage.com | sort | tail -5

# Download latest
BACKUP=roost-2026-02-24-030001.sql.gz
aws s3 cp s3://roost-backups/postgres/$BACKUP /tmp/backup.sql.gz \
  --endpoint-url https://${CLOUDFLARE_ACCOUNT_ID}.r2.cloudflarestorage.com

# Start postgres first
docker compose -f docker-compose.prod.yml up -d roost-postgres
sleep 15

# Restore
gunzip /tmp/backup.sql.gz
docker exec -i roost-postgres psql -U roost roost_prod < /tmp/backup.sql
```

### Phase 5: Start All Services (5 min)

```bash
cd /opt/roost
docker compose -f docker-compose.prod.yml up -d
sleep 30
docker compose -f docker-compose.prod.yml ps

# Run health checks
curl -s http://localhost:8090/health
curl -s http://localhost:8091/health
curl -s http://localhost:8085/health
```

### Phase 6: Reconnect Cloudflare Tunnel (5 min)

```bash
# Authenticate cloudflared (uses stored tunnel credentials)
cloudflared tunnel login  # Follow browser auth flow
cloudflared tunnel run roost-prod &  # Or use systemd service

# Verify tunnel
cloudflared tunnel info roost-prod
```

---

## Scenario 2: Database Corruption

**Cause**: Disk failure, bad migration, runaway query.
**Target RTO**: 45 minutes.

See Runbook 3 (Database Failure) in INCIDENT_RESPONSE.md for step-by-step recovery.

---

## Scenario 3: Cloudflare Outage

**Cause**: Cloudflare global outage (rare — happens ~2x/year globally).
**Target RTO**: 0 minutes (automatic fallback via Hetzner direct IP).

### Setup (one-time, must be done before an outage):

```bash
# Ensure Hetzner IP is reachable directly (firewall allows 443)
# Add backup DNS records:
# roost-backup.yourflock.org → 167.235.195.186 (direct Hetzner IP)
# This is communicated to subscribers via status page during Cloudflare outages.
```

---

## Scenario 4: R2 Storage Failure (Stream Content Unavailable)

**Cause**: Cloudflare R2 outage or accidental bucket deletion.
**Target RTO**: 30 minutes (fallback to origin ingest URLs).

### Emergency Response:

```bash
# Temporarily route streams directly from ingest URLs (bypass R2/CDN)
# This exposes ingest server IPs — acceptable for short-term emergency

# Update relay config to use direct ingest URLs
docker exec roost-relay sh -c 'echo "RELAY_BYPASS_CDN=true" >> /etc/relay.env'
docker restart roost-relay
```

---

## Backup Schedule and Verification

### Current Backup Schedule

| Backup | Frequency | Storage | Retention |
|--------|-----------|---------|-----------|
| Postgres full dump | Daily at 03:00 UTC | R2: roost-backups/postgres/ | 30 days |
| Docker volumes | Weekly on Sunday | R2: roost-backups/volumes/ | 4 weeks |
| Application config | On every deploy | R2: roost-backups/config/ | 90 days |

### Monthly DR Test (Checklist)

- [ ] Download latest backup from R2
- [ ] Restore to a test Docker container
- [ ] Verify row counts match production (within expected delta)
- [ ] Test a billing query: `SELECT count(*) FROM subscriptions WHERE status='active'`
- [ ] Verify API token generation works
- [ ] Document any issues in your runbook notes

---

## Service Rebuild Order

When rebuilding from scratch, start services in this dependency order:

1. `roost-postgres` — all other services depend on DB
2. `roost-auth` — billing and catalog need auth validation
3. `roost-billing` — needs auth + postgres
4. `roost-catalog` — needs postgres
5. `roost-epg` — needs postgres + catalog
6. `roost-ingest` — needs catalog + epg
7. `roost-relay` — needs postgres for session validation
8. `roost-owl-api` — needs all of the above
9. `cloudflared` — start last to avoid routing traffic before services are ready
