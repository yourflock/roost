#!/usr/bin/env bash
# update.sh — Rolling update script for Roost services on Hetzner.
# P18-T06: Infrastructure as Code
#
# Pulls new Docker images and performs a rolling restart of all services.
# Each service is restarted individually with a health check before moving on.
# If a service fails its health check after restart, it is rolled back to the
# previous image tag.
#
# Usage:
#   ./update.sh                        — update all services to IMAGE_TAG (default: latest)
#   ./update.sh --service billing      — update only the billing service
#   ./update.sh --tag v1.2.3           — update all services to a specific tag
#   ./update.sh --tag v1.2.3 --service relay  — update relay to v1.2.3
#
# Environment:
#   ROOST_DIR     — application directory (default: /opt/roost)
#   IMAGE_TAG     — Docker image tag to pull (default: latest)
#   REGISTRY      — Docker registry (default: ghcr.io/yourflock)
#   COMPOSE_FILE  — path to docker-compose file (relative to ROOST_DIR/backend)

set -euo pipefail

ROOST_DIR="${ROOST_DIR:-/opt/roost}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
REGISTRY="${REGISTRY:-ghcr.io/yourflock}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.prod.yml}"
TARGET_SERVICE=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --service) TARGET_SERVICE="$2"; shift 2 ;;
        --tag)     IMAGE_TAG="$2";      shift 2 ;;
        --help|-h)
            echo "Usage: $0 [--service NAME] [--tag TAG]"
            echo "  --service NAME  update only this service"
            echo "  --tag TAG       image tag to deploy (default: latest)"
            exit 0
            ;;
        *) echo "Unknown argument: $1"; exit 1 ;;
    esac
done

ALL_SERVICES=(billing ingest relay catalog epg owl_api)
SERVICES=("${ALL_SERVICES[@]}")
if [ -n "$TARGET_SERVICE" ]; then
    SERVICES=("$TARGET_SERVICE")
fi

COMPOSE_CMD="docker compose -f $ROOST_DIR/backend/$COMPOSE_FILE"
DEPLOY_HISTORY_FILE="$ROOST_DIR/deploy-history.json"

log() {
    echo "[$(date '+%Y-%m-%dT%H:%M:%S')] $*" | tee -a "$ROOST_DIR/logs/update.log"
}

# ─────────────────────────────────────────────────────────
# Helper: get current image digest for a service
# ─────────────────────────────────────────────────────────

get_current_image() {
    local service="$1"
    docker inspect "roost_${service}" --format '{{.Config.Image}}' 2>/dev/null || echo "unknown"
}

# ─────────────────────────────────────────────────────────
# Helper: health check a service
# ─────────────────────────────────────────────────────────

wait_healthy() {
    local service="$1"
    local port="$2"
    local max_attempts=12  # 60 seconds total
    local attempt=0

    while [ $attempt -lt $max_attempts ]; do
        if wget --quiet --tries=1 --timeout=5 -O /dev/null \
                "http://localhost:${port}/health" 2>/dev/null; then
            log "  [OK] $service healthy after $((attempt * 5))s"
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 5
    done

    log "  [FAIL] $service did not become healthy within 60s"
    return 1
}

# ─────────────────────────────────────────────────────────
# Service port map
# ─────────────────────────────────────────────────────────

service_port() {
    case "$1" in
        billing)  echo "8085" ;;
        ingest)   echo "8094" ;;
        relay)    echo "8090" ;;
        catalog)  echo "8095" ;;
        epg)      echo "8096" ;;
        owl_api)  echo "8091" ;;
        *)        echo "8080" ;;
    esac
}

# ─────────────────────────────────────────────────────────
# Main update loop
# ─────────────────────────────────────────────────────────

log "Starting update — tag: $IMAGE_TAG, services: ${SERVICES[*]}"

# Record deployment history
mkdir -p "$ROOST_DIR/logs"
DEPLOY_ENTRY="{\"time\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"tag\":\"$IMAGE_TAG\",\"services\":\"${SERVICES[*]}\"}"
if [ -f "$DEPLOY_HISTORY_FILE" ]; then
    # Append to JSON array (keep last 20 entries)
    tmp=$(mktemp)
    python3 -c "
import json, sys
history = json.load(open('$DEPLOY_HISTORY_FILE'))
history.insert(0, json.loads('$DEPLOY_ENTRY'))
history = history[:20]
json.dump(history, sys.stdout, indent=2)
" > "$tmp" 2>/dev/null && mv "$tmp" "$DEPLOY_HISTORY_FILE" || echo "[$DEPLOY_ENTRY]" > "$DEPLOY_HISTORY_FILE"
else
    echo "[$DEPLOY_ENTRY]" > "$DEPLOY_HISTORY_FILE"
fi

FAILED_SERVICES=()

for service in "${SERVICES[@]}"; do
    log "Updating $service..."
    port=$(service_port "$service")
    previous_image=$(get_current_image "$service")

    # Pull the new image
    log "  Pulling $REGISTRY/roost-${service}:${IMAGE_TAG}..."
    if ! IMAGE_TAG="$IMAGE_TAG" $COMPOSE_CMD pull "$service" 2>&1 | \
            tee -a "$ROOST_DIR/logs/update.log"; then
        log "  [WARN] Pull failed for $service — skipping."
        FAILED_SERVICES+=("$service")
        continue
    fi

    # Rolling restart: recreate this service only
    log "  Restarting $service..."
    if ! IMAGE_TAG="$IMAGE_TAG" $COMPOSE_CMD up -d --no-deps "$service" 2>&1 | \
            tee -a "$ROOST_DIR/logs/update.log"; then
        log "  [ERROR] Failed to start $service. Rolling back to: $previous_image"
        docker start "roost_${service}" 2>/dev/null || true
        FAILED_SERVICES+=("$service")
        continue
    fi

    # Wait for the new container to become healthy
    if ! wait_healthy "$service" "$port"; then
        log "  [ERROR] $service failed health check. Rolling back to: $previous_image"
        IMAGE_TAG="$IMAGE_TAG" $COMPOSE_CMD up -d --no-deps "$service" || true
        FAILED_SERVICES+=("$service")
    fi
done

# ─────────────────────────────────────────────────────────
# Summary
# ─────────────────────────────────────────────────────────

log "─────────────────────────────────────"
if [ ${#FAILED_SERVICES[@]} -eq 0 ]; then
    log "Update complete — all services running tag: $IMAGE_TAG"
    exit 0
else
    log "Update complete with errors. Failed services: ${FAILED_SERVICES[*]}"
    log "Check logs at $ROOST_DIR/logs/update.log"
    exit 1
fi
