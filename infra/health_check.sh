#!/bin/bash
# health_check.sh — Roost service health monitor.
# P16-T06: Incident Response & Disaster Recovery
#
# Checks all Roost services and sends an alert if any is down.
# Run manually or schedule via cron: */5 * * * * /opt/roost/infra/health_check.sh
#
# Alert method: writes to /var/log/roost-health.log and sends a Telegram message
# if TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are set in the environment.
#
# Usage:
#   ./health_check.sh              — check all services
#   ./health_check.sh --quiet      — only output on failure
#   ./health_check.sh --json       — machine-readable JSON output

set -euo pipefail

QUIET=false
JSON_OUTPUT=false
for arg in "$@"; do
  case "$arg" in
    --quiet) QUIET=true ;;
    --json)  JSON_OUTPUT=true ;;
  esac
done

# Service definitions: name:host:port
SERVICES=(
  "relay:localhost:8090"
  "owl_api:localhost:8091"
  "epg:localhost:8092"
  "catalog:localhost:8093"
  "billing:localhost:8085"
  "ingest:localhost:8094"
  "vod:localhost:8096"
  "catchup:localhost:8097"
  "recommendations:localhost:8098"
  "dvr:localhost:8099"
  "grid_compositor:localhost:8100"
  "sports:localhost:8095"
)

LOG_FILE="${LOG_FILE:-/var/log/roost-health.log}"
TIMESTAMP=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
FAILED=()
RESULTS=()

for svc in "${SERVICES[@]}"; do
  name="${svc%%:*}"
  rest="${svc#*:}"
  host="${rest%%:*}"
  port="${rest##*:}"

  # Attempt health check with 3-second timeout.
  HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    --max-time 3 \
    "http://${host}:${port}/health" 2>/dev/null || echo "000")

  if [ "$HTTP_CODE" = "200" ]; then
    STATUS="ok"
    $QUIET || echo "[${TIMESTAMP}] $name: OK (HTTP $HTTP_CODE)"
    RESULTS+=("{\"service\":\"$name\",\"status\":\"ok\",\"http_code\":$HTTP_CODE}")
  else
    STATUS="down"
    echo "[${TIMESTAMP}] ALERT: $name is DOWN (HTTP $HTTP_CODE)" | tee -a "$LOG_FILE"
    FAILED+=("$name")
    RESULTS+=("{\"service\":\"$name\",\"status\":\"down\",\"http_code\":$HTTP_CODE}")
  fi
done

# JSON output mode.
if $JSON_OUTPUT; then
  echo "{"
  echo "  \"timestamp\": \"$TIMESTAMP\","
  echo "  \"services\": ["
  echo "    $(IFS=','; echo "${RESULTS[*]}")"
  echo "  ],"
  echo "  \"failed_count\": ${#FAILED[@]}"
  echo "}"
fi

# Send alert if any services are down.
if [ ${#FAILED[@]} -gt 0 ]; then
  MESSAGE="🚨 Roost Health Alert ($TIMESTAMP)%0A%0ADown services: $(IFS=', '; echo "${FAILED[*]}")%0A%0ACheck: ssh root@$(hostname -I | awk '{print $1}')"

  # Telegram alert (if configured).
  if [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && [ -n "${TELEGRAM_CHAT_ID:-}" ]; then
    curl -s -X POST \
      "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
      -d "chat_id=${TELEGRAM_CHAT_ID}" \
      -d "text=${MESSAGE}" \
      -d "parse_mode=HTML" \
      > /dev/null 2>&1 || true
  fi

  # Log the failure summary.
  echo "[${TIMESTAMP}] SUMMARY: ${#FAILED[@]} service(s) down: $(IFS=', '; echo "${FAILED[*]}")" >> "$LOG_FILE"

  # Exit non-zero so cron/monitoring systems detect the failure.
  exit 1
fi

$QUIET || echo "[${TIMESTAMP}] All services healthy."
exit 0
