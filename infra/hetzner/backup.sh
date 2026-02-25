#!/usr/bin/env bash
# backup.sh — PostgreSQL backup to Cloudflare R2 for Roost.
# P18-T06: Infrastructure as Code
#
# Performs a pg_dump of the Roost database and uploads to R2 via rclone.
# Designed to run via cron. Keeps 30 days of daily backups + 12 months of monthly.
#
# Cron example (daily at 02:00 UTC, as deploy user):
#   0 2 * * * /opt/roost/infra/hetzner/backup.sh >> /opt/roost/logs/backup.log 2>&1
#
# Prerequisites:
#   - PostgreSQL client (pg_dump) installed
#   - rclone configured with R2 remote named "r2" (see infra/runbooks/ for setup)
#   - .env loaded (or POSTGRES_* and R2_BUCKET_BACKUPS env vars set)
#
# Usage:
#   ./backup.sh                — standard backup with env from /opt/roost/.env
#   ./backup.sh --verify       — backup and verify restore (slow, for weekly checks)
#   ./backup.sh --list         — list existing backups in R2

set -euo pipefail

ROOST_DIR="${ROOST_DIR:-/opt/roost}"
LOG_FILE="$ROOST_DIR/logs/backup.log"
VERIFY=false
LIST_ONLY=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --verify)    VERIFY=true;    shift ;;
        --list)      LIST_ONLY=true; shift ;;
        --help|-h)
            echo "Usage: $0 [--verify] [--list]"
            echo "  --verify  restore backup to a temp DB and verify row counts"
            echo "  --list    list existing backups in R2"
            exit 0
            ;;
        *) echo "Unknown argument: $1"; exit 1 ;;
    esac
done

log() {
    echo "[$(date '+%Y-%m-%dT%H:%M:%S')] $*" | tee -a "$LOG_FILE"
}

# Load environment if not already set
if [ -f "$ROOST_DIR/.env" ]; then
    set -a
    # shellcheck disable=SC1091
    . "$ROOST_DIR/.env"
    set +a
fi

# Required env vars
: "${POSTGRES_HOST:=localhost}"
: "${POSTGRES_PORT:=5433}"
: "${POSTGRES_USER:=roost}"
: "${POSTGRES_PASSWORD:?ERROR: POSTGRES_PASSWORD not set}"
: "${POSTGRES_DB:=roost}"
: "${R2_BUCKET_BACKUPS:=roost-backups}"

# ─────────────────────────────────────────────────────────
# List backups
# ─────────────────────────────────────────────────────────

if [ "$LIST_ONLY" = true ]; then
    log "Listing backups in r2:$R2_BUCKET_BACKUPS/postgres/"
    rclone ls "r2:$R2_BUCKET_BACKUPS/postgres/" 2>/dev/null | sort -k2
    exit 0
fi

# ─────────────────────────────────────────────────────────
# Create backup
# ─────────────────────────────────────────────────────────

TIMESTAMP=$(date -u '+%Y%m%d_%H%M%S')
DAY_OF_MONTH=$(date -u '+%d')
BACKUP_DIR=$(mktemp -d)
BACKUP_FILE="$BACKUP_DIR/roost_${TIMESTAMP}.sql.gz"

log "Starting backup of $POSTGRES_DB @ $POSTGRES_HOST:$POSTGRES_PORT"

# pg_dump with compression
PGPASSWORD="$POSTGRES_PASSWORD" pg_dump \
    -h "$POSTGRES_HOST" \
    -p "$POSTGRES_PORT" \
    -U "$POSTGRES_USER" \
    -d "$POSTGRES_DB" \
    --no-password \
    --format=plain \
    --no-privileges \
    --no-owner \
    | gzip -9 > "$BACKUP_FILE"

BACKUP_SIZE=$(du -sh "$BACKUP_FILE" | cut -f1)
log "Backup created: $(basename "$BACKUP_FILE") ($BACKUP_SIZE)"

# ─────────────────────────────────────────────────────────
# Upload to R2
# ─────────────────────────────────────────────────────────

# Daily backup path
DAILY_PATH="r2:$R2_BUCKET_BACKUPS/postgres/daily/$(date -u '+%Y/%m')/$(basename "$BACKUP_FILE")"
log "Uploading to $DAILY_PATH..."
rclone copy "$BACKUP_FILE" "$(dirname "$DAILY_PATH")" --progress 2>/dev/null
log "Upload complete."

# Monthly backup (on the 1st of each month)
if [ "$DAY_OF_MONTH" = "01" ]; then
    MONTHLY_PATH="r2:$R2_BUCKET_BACKUPS/postgres/monthly/$(date -u '+%Y')/$(basename "$BACKUP_FILE")"
    log "Monthly backup — uploading to $MONTHLY_PATH..."
    rclone copy "$BACKUP_FILE" "$(dirname "$MONTHLY_PATH")" --progress 2>/dev/null
    log "Monthly backup uploaded."
fi

# ─────────────────────────────────────────────────────────
# Verify backup (optional)
# ─────────────────────────────────────────────────────────

if [ "$VERIFY" = true ]; then
    log "Verifying backup integrity (restore to temp DB)..."
    VERIFY_DB="roost_backup_verify_$$"

    # Create temp database
    PGPASSWORD="$POSTGRES_PASSWORD" psql \
        -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" \
        -c "CREATE DATABASE $VERIFY_DB;" postgres

    # Restore
    gunzip -c "$BACKUP_FILE" | PGPASSWORD="$POSTGRES_PASSWORD" psql \
        -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" \
        -d "$VERIFY_DB" --quiet

    # Check row counts for critical tables
    for table in subscribers subscriptions channels plans; do
        COUNT=$(PGPASSWORD="$POSTGRES_PASSWORD" psql \
            -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" \
            -d "$VERIFY_DB" -t -c "SELECT COUNT(*) FROM $table;" 2>/dev/null | tr -d ' ')
        log "  $table: $COUNT rows"
    done

    # Drop temp database
    PGPASSWORD="$POSTGRES_PASSWORD" psql \
        -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" \
        -c "DROP DATABASE $VERIFY_DB;" postgres

    log "Backup verification complete."
fi

# ─────────────────────────────────────────────────────────
# Cleanup local temp file
# ─────────────────────────────────────────────────────────

rm -rf "$BACKUP_DIR"

# ─────────────────────────────────────────────────────────
# Prune old daily backups (keep 30 days)
# ─────────────────────────────────────────────────────────

log "Pruning daily backups older than 30 days..."
rclone delete "r2:$R2_BUCKET_BACKUPS/postgres/daily/" \
    --min-age 30d \
    --progress 2>/dev/null || true

# Prune monthly backups (keep 12 months)
log "Pruning monthly backups older than 365 days..."
rclone delete "r2:$R2_BUCKET_BACKUPS/postgres/monthly/" \
    --min-age 365d \
    --progress 2>/dev/null || true

log "Backup job complete. Size: $BACKUP_SIZE"
