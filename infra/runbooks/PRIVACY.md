# Roost Privacy Infrastructure Runbook

**Last updated**: 2026-02-24
**Owner**: Roost infrastructure team
**Related**: `infra/cdn/workers/stream-proxy/`, `backend/pkg/zerolog/`, `backend/nginx/conf.d/stream.conf`

---

## Overview

Roost's privacy model has two layers:

1. **Origin obfuscation**: Subscribers never discover the Hetzner origin IP, hostname, or
   any infrastructure identifier. All stream traffic flows through Cloudflare Workers.
   Cloudflare Tunnel provides the reverse-proxy connection from Cloudflare to Hetzner.

2. **Zero-logging for streams**: Stream delivery endpoints write no access logs. No IP
   address, subscriber ID, token, or viewing record is stored anywhere in the stream path.

This runbook documents what data is collected, what is not, retention policies, GDPR
deletion procedures, and verification steps.

---

## What Is Collected

### Collected (non-stream paths only)

| Data | Location | Retention | Purpose |
|------|----------|-----------|---------|
| Auth events (login, token issue, revoke) | Postgres `auth_events` table | 90 days | Security audit, fraud detection |
| Subscription events (create, cancel, upgrade) | Postgres `billing_events` table | 7 years | Legal/accounting requirement |
| Stripe customer ID | Postgres `subscribers` table | Duration of account | Billing |
| Email address | Postgres `subscribers` table | Duration of account + 30 days | Account recovery, billing |
| Hashed password (bcrypt) | Postgres `subscribers` table | Duration of account | Authentication |
| Worker safe logs (request_id, status, duration_ms, path_prefix, cf_ray) | Cloudflare Logpush (optional) | 30 days | Performance monitoring |
| Nginx non-stream logs (health endpoint only) | `/var/log/nginx/health.log` | 7 days | Infra health |

### Not Collected (hard technical enforcement)

| Data | Enforcement Mechanism |
|------|-----------------------|
| Subscriber IP address | CF Worker strips CF-Connecting-IP before origin; Nginx has `access_log off` on stream paths |
| What channels/streams a subscriber watched | Stream paths use zerolog middleware; no path-subscriber mapping is written |
| HLS segment request timestamps per subscriber | Same as above |
| Raw tokens in logs | zerolog allowlist drops `token` field; CF Worker never logs token value |
| User-Agent | Stripped by CF Worker before proxying; never logged |
| Viewing session duration | Not tracked at infrastructure level |
| Browser fingerprint | User-Agent, Accept-Language, Accept-Encoding stripped at Worker |
| Subscriber device type | User-Agent stripped |

---

## Log Retention Policy

| Log type | Retention | Storage | Notes |
|----------|-----------|---------|-------|
| **Stream access logs** | **0 days** | **Never written** | Nginx `access_log off`, Worker zero-logging |
| Worker safe logs (if Logpush enabled) | 30 days | Cloudflare R2 or SIEM | Only permitted fields — no PII |
| Auth events | 90 days | Postgres | Purged by `scripts/purge-old-auth-events.sh` |
| Billing events | 7 years | Postgres | Legal requirement |
| Nginx health logs | 7 days | VPS `/var/log/nginx/health.log` | Rotated by logrotate |
| Error logs | 14 days | VPS `/var/log/nginx/error.log` | No stream paths logged |
| Go service logs (non-stream) | 14 days | Docker stdout → journald | Stream paths use zerolog |

---

## GDPR Deletion Procedure

When a subscriber requests data deletion under GDPR Article 17:

### Step 1: Locate subscriber record

```bash
# Connect to Roost Postgres
docker exec -it postgres psql -U roost roost

-- Find subscriber by email (you must have the email from the verified deletion request)
SELECT id, email, stripe_customer_id, created_at FROM subscribers WHERE email = $EMAIL;
```

### Step 2: Cancel Stripe subscription

```bash
# Cancel via Stripe CLI or dashboard
stripe subscriptions cancel $STRIPE_SUBSCRIPTION_ID --at-period-end=false
```

### Step 3: Delete subscriber data from Postgres

```sql
-- Run in a transaction
BEGIN;

-- Store IDs for cascade verification
\set sub_id (SELECT id FROM subscribers WHERE email = $EMAIL)

DELETE FROM auth_events WHERE subscriber_id = :sub_id;
DELETE FROM billing_events WHERE subscriber_id = :sub_id;
DELETE FROM sessions WHERE subscriber_id = :sub_id;
DELETE FROM stream_tokens WHERE subscriber_id = :sub_id;
DELETE FROM subscribers WHERE id = :sub_id;

COMMIT;
```

### Step 4: Purge from Cloudflare KV

Stream proxy session cache uses token-keyed KV entries with 5-minute TTL. They
self-expire. For immediate deletion, use the Cloudflare API:

```bash
# List all KV entries (requires CF API token)
curl -X GET "https://api.cloudflare.com/client/v4/accounts/$CF_ACCOUNT_ID/storage/kv/namespaces/$KV_SESSIONS_ID/keys" \
  -H "Authorization: Bearer $CLOUDFLARE_API_KEY"

# Delete specific key (if token is known from subscriber's own deletion request)
curl -X DELETE "https://api.cloudflare.com/client/v4/accounts/$CF_ACCOUNT_ID/storage/kv/namespaces/$KV_SESSIONS_ID/values/session:$TOKEN" \
  -H "Authorization: Bearer $CLOUDFLARE_API_KEY"
```

In practice, since KV entries have a 5-minute TTL and no subscriber-linked keys exist
(token-keyed only), these expire automatically before any GDPR response deadline.

### Step 5: Confirm deletion

```sql
-- Should return zero rows
SELECT COUNT(*) FROM subscribers WHERE email = $EMAIL;
SELECT COUNT(*) FROM auth_events WHERE subscriber_id = :sub_id;
```

### Step 6: Notify subscriber

Send confirmation email within 72 hours of verified request confirming:
- Account deleted
- Billing data retained for legal compliance (7 years, per Article 6(1)(c))
- No stream viewing records existed (technical attestation)

---

## How to Verify No IP/Subscriber Data in Stream Logs

### Verify Nginx has no stream access logs

```bash
# SSH into the Roost VPS
ssh -i ~/.ssh/flock_deploy root@167.235.195.186

# Verify stream.conf has access_log off
nginx -T | grep -A5 "location /hls"
# Expected: access_log off;

nginx -T | grep -A5 "location /relay"
# Expected: access_log off;

# Confirm no stream logs exist
ls -la /var/log/nginx/
# Should only show: access.log (non-stream), error.log, health.log
# Should NOT show: stream.log, hls.log, relay.log

# Verify access.log contains no /hls/ or /relay/ paths
grep -c "/hls/" /var/log/nginx/access.log 2>/dev/null || echo "0 (not found)"
grep -c "/relay/" /var/log/nginx/access.log 2>/dev/null || echo "0 (not found)"
```

### Verify zerolog middleware drops PII fields

```bash
# Run the zerolog test suite
cd /path/to/roost/backend && go test ./pkg/zerolog/... -v

# Test output should confirm blocked fields (ip, subscriber_id, token) are dropped.
```

### Verify Worker logs contain no PII

Check Cloudflare Workers Tail logs:

```bash
wrangler tail roost-stream-proxy --format=json | jq '{
  has_ip: (has("ip") or has("x_forwarded_for") or has("remote_ip")),
  has_subscriber: (has("subscriber_id") or has("user_id")),
  has_token: (has("token") or has("api_token")),
  safe_fields: keys
}'
```

All `has_*` fields should be `false`. Only safe fields should appear.

---

## Cloudflare Workers Privacy Verification

### Verify origin URL is never in responses

```bash
# Make a stream request and inspect all response headers
curl -sI "https://stream.yourflock.com/hls/test.m3u8?token=TEST_TOKEN" | \
  grep -i "server\|via\|x-powered\|hetzner\|origin\|backend"
# Expected: no output (all such headers are stripped by the Worker)
```

### Verify HLS manifest URLs are rewritten

```bash
# Request a manifest and confirm all URLs point to stream.yourflock.com
curl -s "https://stream.yourflock.com/hls/ch001/stream.m3u8?token=$TOKEN" | \
  grep -v "^#" | head -20
# All segment URLs should start with https://stream.yourflock.com/
# NO URLs should contain "hetzner", "relay", ".de", "49.12.", "167.235."
```

### Verify CORS headers reject non-Owl origins

```bash
curl -sI -H "Origin: https://malicious.example.com" \
  "https://stream.yourflock.com/hls/test.m3u8?token=$TOKEN" | \
  grep "Access-Control"
# Expected: no Access-Control-Allow-Origin header (origin rejected)
```

### Verify rate limiting returns 429 without PII

```bash
# Send 35 rapid manifest requests (limit: 30/min)
for i in $(seq 1 35); do
  curl -s -o /dev/null -w "%{http_code}\n" \
    "https://stream.yourflock.com/hls/test.m3u8?token=$TOKEN"
done
# First 30: 200 or 401
# Last 5: 429
# Verify 429 body contains no subscriber_id, ip, or token:
curl -s "https://stream.yourflock.com/hls/test.m3u8?token=$TOKEN" | jq .
# Expected: {"error":"rate_limit_exceeded","message":"Too many requests..."}
```

---

## Incident Response

If stream log data is discovered that contains PII:

1. **Immediately** disable Logpush in the Cloudflare dashboard (Settings → Logpush).
2. Identify the source: Nginx misconfiguration, Worker bug, or Go service.
3. Deploy a fix within 24 hours.
4. If subscriber IP or viewing data was persisted: treat as a GDPR breach.
   Notify affected subscribers within 72 hours per GDPR Article 33/34.
5. Document in `infra/runbooks/INCIDENT_RESPONSE.md` with timeline.

For full incident response procedures: see `INCIDENT_RESPONSE.md`.
