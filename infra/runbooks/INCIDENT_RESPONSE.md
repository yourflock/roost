# Roost Incident Response Runbook

**Last updated**: 2026-02-24
**Version**: 1.0
**On-call contact**: admin@yourflock.com

This runbook covers common Roost production incidents. Follow the steps in order.
Always update the incident Slack/Discord channel with your actions.

---

## Incident Severity Levels

| Level | Description | Response Time | Example |
|-------|-------------|---------------|---------|
| SEV-1 | Complete service outage | 15 minutes | All streams down, auth failing |
| SEV-2 | Partial outage | 30 minutes | Single service failing, billing down |
| SEV-3 | Degraded performance | 2 hours | High latency, elevated error rate |
| SEV-4 | Minor issue | Next business day | Non-critical feature broken |

---

## Runbook 1: Stream Outage (SEV-1)

Symptoms: Subscribers cannot start or resume streams. `/owl/stream/:slug` returning errors.

### Step 1: Check Relay Service Health

```bash
export HCLOUD_TOKEN=<your-hetzner-api-token>  # set your credentials
ssh -i ~/.ssh/flock_deploy root@167.235.195.186

# Check relay container
docker ps | grep relay
curl -s http://localhost:8090/health | jq .

# Check relay logs (last 50 lines)
docker logs roost-relay --tail 50
```

### Step 2: Check Cloudflare Tunnel

```bash
# On the roost-prod server
cloudflared tunnel list
cloudflared tunnel info roost-prod

# If tunnel is down, restart cloudflared
systemctl restart cloudflared
# Wait 10-15 seconds, then verify
cloudflared tunnel info roost-prod
```

### Step 3: Check CDN Failover

```bash
# Check current CDN health via admin API
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://roost.yourflock.com/admin/cdn/health | jq .

# Manual CDN failover if Cloudflare is the issue
curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://roost.yourflock.com/admin/cdn/failover | jq .
```

### Step 4: Check Origin Server (Hetzner VPS)

```bash
ssh -i ~/.ssh/flock_deploy root@167.235.195.186
df -h    # check disk space (full disk = common cause)
free -m  # check memory
uptime   # check load
docker ps  # all containers running?
```

### Step 5: Full Service Restart (Last Resort)

```bash
cd /opt/roost
docker compose -f docker-compose.prod.yml restart relay owl_api
# Wait 30 seconds
curl -s http://localhost:8090/health
curl -s http://localhost:8091/health
```

**Recovery time target**: 15 minutes

---

## Runbook 2: Auth Service Down (SEV-1)

Symptoms: Login failing, token validation errors, 401 on all endpoints.

### Step 1: Check Auth Container

```bash
ssh -i ~/.ssh/flock_deploy root@167.235.195.186
docker ps | grep auth
docker logs roost-auth --tail 50
curl -s http://localhost:8082/health
```

### Step 2: Check JWT Secret Configuration

```bash
docker exec roost-auth env | grep AUTH_JWT_SECRET
# If empty: the secret was lost. Rotating it will invalidate all existing tokens.
# Subscribers will need to log in again. Acceptable for auth outage.
docker exec roost-auth sh -c 'echo $AUTH_JWT_SECRET' | wc -c  # should be ≥32 chars
```

### Step 3: Emergency Token Extension

If auth is down and a fix will take >30 minutes, extend existing token TTL:

```bash
# Change JWT_EXPIRY to 24h temporarily (reduces auth pressure during outage)
docker exec -it roost-auth sh
export AUTH_JWT_EXPIRY=86400  # 24 hours in seconds
# Restart the container for the change to take effect
docker restart roost-auth
```

### Step 4: Restart Auth

```bash
docker compose -f docker-compose.prod.yml restart auth
sleep 10
curl -s http://localhost:8082/health
```

---

## Runbook 3: Database Failure (SEV-1)

Symptoms: All services returning 500 errors, "db connection error" in logs.

### Step 1: Check Postgres

```bash
ssh -i ~/.ssh/flock_deploy root@167.235.195.186
docker ps | grep postgres
docker logs roost-postgres --tail 50

# Check postgres is accepting connections
docker exec roost-postgres psql -U roost -d roost_prod -c "SELECT 1;"
```

### Step 2: Check Disk Space (Most Common Cause)

```bash
df -h /var/lib/docker  # Docker data directory
du -sh /var/lib/docker/volumes/roost_postgres_data
```

If disk is full:
- Remove old Docker images: `docker image prune -af`
- Clear logs: `docker system prune`
- If still full: resize Hetzner volume via API (or add a new volume)

### Step 3: Point-in-Time Recovery (from R2 Backup)

Roost performs daily backups to Cloudflare R2 at 03:00 UTC.

```bash
export HCLOUD_TOKEN=<your-hetzner-api-token>  # set your credentials

# List available backups
aws s3 ls s3://roost-backups/postgres/ --endpoint-url https://${CLOUDFLARE_ACCOUNT_ID}.r2.cloudflarestorage.com

# Download latest backup
BACKUP=$(aws s3 ls s3://roost-backups/postgres/ --endpoint-url https://${CLOUDFLARE_ACCOUNT_ID}.r2.cloudflarestorage.com \
  | sort | tail -1 | awk '{print $4}')
aws s3 cp s3://roost-backups/postgres/$BACKUP /tmp/roost-backup.sql.gz \
  --endpoint-url https://${CLOUDFLARE_ACCOUNT_ID}.r2.cloudflarestorage.com

# Restore
gunzip /tmp/roost-backup.sql.gz
docker exec -i roost-postgres psql -U roost roost_prod < /tmp/roost-backup.sql
```

**RPO note**: Up to 24 hours of data loss (last backup). For critical data (billing),
check Stripe dashboard — all payments are logged there independently.

---

## Runbook 4: Billing Webhook Missed (SEV-2)

Symptoms: Subscriber reports paid but account not activated. Stripe shows `checkout.session.completed` fired.

### Step 1: Verify in Stripe Dashboard

1. Go to https://dashboard.stripe.com/webhooks
2. Find the failed webhook event
3. Click "Resend" to replay it

### Step 2: Manual Activation (if resend fails)

```bash
# Get subscriber ID from subscribers table by email
SUBSCRIBER_ID=$(docker exec roost-postgres psql -U roost roost_prod -t -c \
  "SELECT id FROM subscribers WHERE email = 'subscriber@example.com';" | tr -d ' ')

# Manually activate subscription
docker exec roost-postgres psql -U roost roost_prod -c "
  UPDATE subscriptions SET status = 'active' WHERE subscriber_id = '$SUBSCRIBER_ID';
  UPDATE subscribers SET status = 'active' WHERE id = '$SUBSCRIBER_ID';
  UPDATE api_tokens SET revoked_at = NULL WHERE subscriber_id = '$SUBSCRIBER_ID'
    AND revoked_at IS NOT NULL;
"
```

---

## Runbook 5: Security Breach (SEV-1)

Symptoms: Unauthorized access detected, unexpected admin actions in audit log,
credentials exposed.

### Step 1: Revoke All Tokens Immediately

```bash
docker exec roost-postgres psql -U roost roost_prod -c "
  UPDATE api_tokens SET revoked_at = NOW();
  UPDATE refresh_tokens SET revoked_at = NOW();
  UPDATE owl_sessions SET revoked_at = NOW();
"
```

### Step 2: Rotate JWT Secret

```bash
# Generate new secret
NEW_SECRET=$(openssl rand -hex 32)
echo "New JWT secret: $NEW_SECRET"
# Add to your secrets file: AUTH_JWT_SECRET=<new_secret>
# Update docker-compose.prod.yml and restart all services

docker compose -f docker-compose.prod.yml down
# Update AUTH_JWT_SECRET in .env.prod
docker compose -f docker-compose.prod.yml up -d
```

### Step 3: Force Password Reset

```bash
docker exec roost-postgres psql -U roost roost_prod -c "
  UPDATE subscribers SET force_password_reset = true, updated_at = NOW();
"
```

### Step 4: Review Audit Log

```bash
# Check recent admin actions
docker exec roost-postgres psql -U roost roost_prod -c "
  SELECT actor_type, actor_id, action, resource_type, ip_address, created_at
  FROM audit_log
  WHERE created_at > NOW() - INTERVAL '24 hours'
  ORDER BY created_at DESC
  LIMIT 100;
"
```

### Step 5: Notify Affected Subscribers

If subscriber data was accessed, GDPR Article 33 requires notification to the
supervisory authority within 72 hours. Prepare notification:
- Who was affected (subscriber count, data categories)
- When the breach occurred (from audit log)
- What data was accessed
- Mitigation steps taken
