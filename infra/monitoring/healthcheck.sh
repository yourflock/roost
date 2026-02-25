#!/usr/bin/env bash
# healthcheck.sh — Check all Roost services are responding.
# P18-T06: Infrastructure as Code
#
# Checks HTTP /health endpoints for all Go microservices and writes a status
# report. Sends a Telegram alert if any service is unhealthy.
#
# Designed to run via cron every 5 minutes:
#   */5 * * * * /opt/roost/infra/monitoring/healthcheck.sh
#
# Or via systemd timer (see infra/runbooks/ for setup).
#
# Usage:
#   ./healthcheck.sh                   — check all services, alert on failure
#   ./healthcheck.sh --quiet           — only output on failure
#   ./healthcheck.sh --json            — machine-readable JSON output
#   ./healthcheck.sh --once            — single check, exit 1 if any service down
#
# Environment:
#   ROOST_DIR              — application directory (default: /opt/roost)
#   TELEGRAM_BOT_TOKEN     — Telegram bot token for alerts (optional)
#   TELEGRAM_CHAT_ID       — Telegram chat ID for alerts (optional)
#   HEALTH_LOG_FILE        — log file path (default: /var/log/roost-health.log)

set -euo pipefail

ROOST_DIR="${ROOST_DIR:-/opt/roost}"
HEALTH_LOG_FILE="${HEALTH_LOG_FILE:-/var/log/roost-health.log}"
QUIET=false
JSON_OUTPUT=false
ONCE=false

for arg in "$@"; do
    case "$arg" in
        --quiet) QUIET=true  ;;
        --json)  JSON_OUTPUT=true ;;
        --once)  ONCE=true   ;;
    esac
done

# ─────────────────────────────────────────────────────────
# Service definitions: name|port|path
# ─────────────────────────────────────────────────────────

declare -A SERVICE_PORTS=(
    [billing]=8085
    [ingest]=8094
    [relay]=8090
    [catalog]=8095
    [epg]=8096
    [owl_api]=8091
)

# ─────────────────────────────────────────────────────────
# Check a single service
# Returns 0 if healthy, 1 if unhealthy
# ─────────────────────────────────────────────────────────

check_service() {
    local name="$1"
    local port="$2"
    local url="http://localhost:${port}/health"
    local start_time
    start_time=$(date +%s%3N)

    local http_status
    http_status=$(curl --silent --output /dev/null --write-out "%{http_code}" \
        --max-time 5 --connect-timeout 3 "$url" 2>/dev/null || echo "000")

    local end_time
    end_time=$(date +%s%3N)
    local response_ms=$((end_time - start_time))

    if [ "$http_status" = "200" ]; then
        echo "ok|$name|$port|$http_status|${response_ms}ms"
        return 0
    else
        echo "fail|$name|$port|$http_status|${response_ms}ms"
        return 1
    fi
}

# ─────────────────────────────────────────────────────────
# Telegram alert
# ─────────────────────────────────────────────────────────

send_telegram_alert() {
    local message="$1"
    if [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && [ -n "${TELEGRAM_CHAT_ID:-}" ]; then
        curl --silent -X POST \
            "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT_ID}" \
            -d "text=${message}" \
            -d "parse_mode=Markdown" \
            --max-time 10 \
            --output /dev/null 2>/dev/null || true
    fi
}

# ─────────────────────────────────────────────────────────
# Main health check loop
# ─────────────────────────────────────────────────────────

TIMESTAMP=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
FAILED_SERVICES=()
ALL_RESULTS=()

for name in "${!SERVICE_PORTS[@]}"; do
    port="${SERVICE_PORTS[$name]}"
    result=$(check_service "$name" "$port" 2>/dev/null)
    ALL_RESULTS+=("$result")

    status="${result%%|*}"
    if [ "$status" = "fail" ]; then
        FAILED_SERVICES+=("$name")
    fi
done

# ─────────────────────────────────────────────────────────
# Output
# ─────────────────────────────────────────────────────────

if [ "$JSON_OUTPUT" = true ]; then
    # Machine-readable JSON output
    echo "{"
    echo "  \"timestamp\": \"$TIMESTAMP\","
    echo "  \"overall\": \"$([ ${#FAILED_SERVICES[@]} -eq 0 ] && echo ok || echo fail)\","
    echo "  \"services\": ["
    for i in "${!ALL_RESULTS[@]}"; do
        result="${ALL_RESULTS[$i]}"
        IFS='|' read -r status svc_name port http_code resp_time <<< "$result"
        comma=$([ $i -lt $((${#ALL_RESULTS[@]} - 1)) ] && echo "," || echo "")
        echo "    {\"name\": \"$svc_name\", \"status\": \"$status\", \"port\": $port, \"http\": \"$http_code\", \"response\": \"$resp_time\"}$comma"
    done
    echo "  ]"
    echo "}"
else
    # Human-readable output
    if [ "$QUIET" = false ] || [ ${#FAILED_SERVICES[@]} -gt 0 ]; then
        echo "[$TIMESTAMP] Roost Health Check"
        echo "──────────────────────────────────────"
        for result in "${ALL_RESULTS[@]}"; do
            IFS='|' read -r status svc_name port http_code resp_time <<< "$result"
            if [ "$status" = "ok" ]; then
                printf "  [OK]   %-15s port=%s  %s  %s\n" "$svc_name" "$port" "$http_code" "$resp_time"
            else
                printf "  [FAIL] %-15s port=%s  HTTP=%s  %s\n" "$svc_name" "$port" "$http_code" "$resp_time"
            fi
        done
        echo "──────────────────────────────────────"
    fi
fi

# ─────────────────────────────────────────────────────────
# Log and alert
# ─────────────────────────────────────────────────────────

# Write to log file (JSON lines format for easy parsing)
mkdir -p "$(dirname "$HEALTH_LOG_FILE")"
for result in "${ALL_RESULTS[@]}"; do
    IFS='|' read -r status svc_name port http_code resp_time <<< "$result"
    echo "{\"time\":\"$TIMESTAMP\",\"service\":\"$svc_name\",\"status\":\"$status\",\"http\":\"$http_code\",\"response\":\"$resp_time\"}" \
        >> "$HEALTH_LOG_FILE"
done

# Alert on failures
if [ ${#FAILED_SERVICES[@]} -gt 0 ]; then
    ALERT_MSG="*Roost Health Alert* — $(hostname)

*Failed services* ($(date -u '+%H:%M UTC')):
$(for svc in "${FAILED_SERVICES[@]}"; do echo "  - $svc"; done)

Check logs: \`journalctl -u docker -n 50\`
Restart: \`cd /opt/roost/backend && docker compose -f docker-compose.prod.yml restart ${FAILED_SERVICES[*]}\`"

    send_telegram_alert "$ALERT_MSG"

    if [ "$ONCE" = true ]; then
        exit 1
    fi
fi

exit 0
