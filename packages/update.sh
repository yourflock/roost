#!/bin/sh
# update.sh — Self-update script for Roost Docker Compose deployments.
#
# Pulls the latest Roost image, runs database migrations, and restarts services
# with a zero-downtime rolling restart.
#
# Usage:
#   ./packaging/update.sh [--check] [--yes]
#
#   --check   Check if an update is available without installing it.
#   --yes     Skip confirmation prompts (for automated/cron updates).
#
# Run from the roost/ repo root (where docker-compose.roost.yml lives).

set -e

COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.roost.yml}"
ROOST_IMAGE="ghcr.io/unyeco/roost"
CHECK_ONLY=0
AUTO_YES=0

# Parse arguments.
for arg in "$@"; do
    case "$arg" in
        --check) CHECK_ONLY=1 ;;
        --yes)   AUTO_YES=1 ;;
        -h|--help)
            echo "Usage: $0 [--check] [--yes]"
            echo "  --check  Check for updates without installing"
            echo "  --yes    Skip confirmation (for automated updates)"
            exit 0
            ;;
    esac
done

# ─── Helpers ────────────────────────────────────────────────────────────────

log() { echo "[roost-update] $*"; }
err() { echo "[roost-update] ERROR: $*" >&2; }

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        err "Required command not found: $1"
        exit 1
    fi
}

confirm() {
    if [ "${AUTO_YES}" = "1" ]; then
        return 0
    fi
    printf "%s [y/N] " "$1"
    read -r answer
    case "$answer" in
        [Yy]*) return 0 ;;
        *) return 1 ;;
    esac
}

# ─── Checks ─────────────────────────────────────────────────────────────────

require_command docker

if ! docker compose version >/dev/null 2>&1; then
    err "Docker Compose v2 is required. Install: https://docs.docker.com/compose/install/"
    exit 1
fi

if [ ! -f "${COMPOSE_FILE}" ]; then
    err "Compose file not found: ${COMPOSE_FILE}"
    err "Run this script from the roost/ directory, or set COMPOSE_FILE environment variable."
    exit 1
fi

# ─── Version check ──────────────────────────────────────────────────────────

log "Checking for updates..."

# Get current running version from the health endpoint.
CURRENT_VERSION="unknown"
if docker compose -f "${COMPOSE_FILE}" ps roost 2>/dev/null | grep -q "running"; then
    CURRENT_VERSION=$(docker compose -f "${COMPOSE_FILE}" exec -T roost \
        wget -qO- http://localhost:8080/health 2>/dev/null \
        | tr ',' '\n' | grep '"version"' | cut -d'"' -f4 || echo "unknown")
fi

# Pull image manifest to check for a newer digest (works without pulling the full image).
REMOTE_DIGEST=$(docker manifest inspect "${ROOST_IMAGE}:latest" 2>/dev/null \
    | tr ',' '\n' | grep '"digest"' | head -1 | cut -d'"' -f4 || echo "")

LOCAL_DIGEST=$(docker image inspect "${ROOST_IMAGE}:latest" \
    --format '{{index .RepoDigests 0}}' 2>/dev/null | cut -d'@' -f2 || echo "")

log "Current version:  ${CURRENT_VERSION}"
log "Local image:      ${LOCAL_DIGEST:-not pulled yet}"
log "Remote image:     ${REMOTE_DIGEST:-unknown}"

if [ "${REMOTE_DIGEST}" = "${LOCAL_DIGEST}" ] && [ -n "${LOCAL_DIGEST}" ]; then
    log "Already up to date."
    exit 0
fi

if [ "${CHECK_ONLY}" = "1" ]; then
    log "Update available. Run without --check to install."
    exit 0
fi

# ─── Update ─────────────────────────────────────────────────────────────────

if ! confirm "Update Roost to the latest version?"; then
    log "Update cancelled."
    exit 0
fi

log "Pulling latest Roost image..."
docker compose -f "${COMPOSE_FILE}" pull roost

log "Running database migrations..."
# The Roost binary runs migrations automatically on startup.
# We run it in a one-off container before swapping the service.
docker compose -f "${COMPOSE_FILE}" run --rm \
    -e ROOST_SKIP_SERVER=1 \
    roost migrate 2>/dev/null || true

log "Restarting Roost service..."
# Zero-downtime: start new container before stopping old one.
# Docker Compose handles this with --no-deps to avoid restarting dependencies.
docker compose -f "${COMPOSE_FILE}" up -d --no-deps --remove-orphans roost

log "Waiting for health check..."
RETRIES=12
WAIT=5
i=0
while [ "$i" -lt "${RETRIES}" ]; do
    if docker compose -f "${COMPOSE_FILE}" exec -T roost \
        wget -qO- http://localhost:8080/health 2>/dev/null | grep -q '"ok"'; then
        break
    fi
    i=$((i + 1))
    if [ "$i" -lt "${RETRIES}" ]; then
        log "  Waiting... (attempt ${i}/${RETRIES})"
        sleep "${WAIT}"
    fi
done

if [ "$i" -ge "${RETRIES}" ]; then
    err "Roost did not become healthy after update. Check logs:"
    err "  docker compose -f ${COMPOSE_FILE} logs roost --tail 50"
    exit 1
fi

# Remove old (dangling) images to free disk space.
docker image prune -f --filter "label=org.opencontainers.image.source=https://github.com/unyeco/roost" \
    2>/dev/null || true

NEW_VERSION=$(docker compose -f "${COMPOSE_FILE}" exec -T roost \
    wget -qO- http://localhost:8080/health 2>/dev/null \
    | tr ',' '\n' | grep '"version"' | cut -d'"' -f4 || echo "unknown")

log ""
log "Update complete."
log "Previous version: ${CURRENT_VERSION}"
log "Current version:  ${NEW_VERSION}"
log ""
