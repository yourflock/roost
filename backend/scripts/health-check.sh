#!/usr/bin/env bash
# health-check.sh — Check health of all Roost services
# Run from the server: ./scripts/health-check.sh
# Or remotely: ssh deploy@167.235.195.186 /opt/roost/backend/scripts/health-check.sh

set -euo pipefail

PASS=0
FAIL=0

check() {
  local name="$1"
  local url="$2"
  local expected="${3:-200}"
  
  HTTP_STATUS=$(curl --silent --output /dev/null --write-out "%{http_code}" \
    --max-time 5 "$url" 2>/dev/null || echo "000")
  
  if [ "$HTTP_STATUS" = "$expected" ]; then
    echo "  PASS  $name ($HTTP_STATUS)"
    PASS=$((PASS + 1))
  else
    echo "  FAIL  $name (expected $expected, got $HTTP_STATUS)"
    FAIL=$((FAIL + 1))
  fi
}

echo "=== Roost Health Check ($(date '+%Y-%m-%d %H:%M:%S UTC')) ==="
echo ""
echo "Internal service endpoints:"
check "postgres (pg_isready)"  "" || true
# Check postgres via docker
if docker compose -f /opt/roost/backend/docker-compose.prod.yml exec -T postgres \
    pg_isready -U roost 2>/dev/null; then
  echo "  PASS  postgres (pg_isready)"
  PASS=$((PASS + 1))
else
  echo "  FAIL  postgres (pg_isready failed)"
  FAIL=$((FAIL + 1))
fi

check "nginx"     "http://localhost/health"
check "billing"   "http://localhost:8085/health"
check "ingest"    "http://localhost:8094/health"
check "relay"     "http://localhost:8090/health"
check "catalog"   "http://localhost:8095/health"
check "epg"       "http://localhost:8096/health"
check "owl_api"   "http://localhost:8091/health"
check "hasura"    "http://localhost:8081/healthz"
check "auth"      "http://localhost:4000/healthz"

echo ""
echo "External endpoints (via Cloudflare Tunnel):"
check "roost.yourflock.org/health"          "https://roost.yourflock.org/health"
check "roost.yourflock.org/owl/manifest"    "https://roost.yourflock.org/owl/manifest.json"

echo ""
echo "Results: $PASS passed, $FAIL failed"
echo ""
if [ $FAIL -gt 0 ]; then
  echo "Recent logs for failing services:"
  docker compose -f /opt/roost/backend/docker-compose.prod.yml logs --tail=10 2>/dev/null || true
  exit 1
fi
