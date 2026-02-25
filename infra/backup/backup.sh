#!/usr/bin/env bash
# backup.sh — Postgres backup + upload to Cloudflare R2.
# P21.4.001: Postgres Backup with Retention and Verification
#
# Usage:
#   backup.sh              — run a backup now
#   backup.sh verify       — verify the most recent backup is restorable
#   backup.sh restore FILE — restore from a specific backup file
#
# Environment:
#   POSTGRES_URL     — Postgres connection string
#   R2_BUCKET        — Cloudflare R2 bucket name (default: roost-backups)
#   R2_ENDPOINT      — Cloudflare R2 S3 endpoint URL
#   AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY — R2 credentials
#   BACKUP_RETENTION_DAYS — days to keep backups (default: 30)
#
# Dependencies: pg_dump, aws CLI (for S3-compat R2 access), gzip

set -euo pipefail

POSTGRES_URL="${POSTGRES_URL:-postgres://roost:roost@localhost:5432/roost}"
R2_BUCKET="${R2_BUCKET:-roost-backups}"
R2_ENDPOINT="${R2_ENDPOINT:-}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
BACKUP_DIR="/tmp/roost-backups"
TIMESTAMP=$(date -u +"%Y%m%dT%H%M%SZ")
BACKUP_FILE="${BACKUP_DIR}/roost_${TIMESTAMP}.sql.gz"
S3_KEY="postgres/roost_${TIMESTAMP}.sql.gz"

log() { echo "[backup] $(date -u +%H:%M:%S) $*"; }

# ── Backup ────────────────────────────────────────────────────────────────────
run_backup() {
    log "Starting backup → ${BACKUP_FILE}"
    mkdir -p "${BACKUP_DIR}"

    pg_dump "${POSTGRES_URL}" \
        --format=plain \
        --no-password \
        --no-owner \
        --no-privileges \
        --exclude-table=audit_log \
    | gzip -9 > "${BACKUP_FILE}"

    SIZE=$(du -sh "${BACKUP_FILE}" | cut -f1)
    log "Backup complete: ${SIZE}"

    # Upload to R2.
    if [[ -n "${R2_ENDPOINT}" ]]; then
        log "Uploading to R2: s3://${R2_BUCKET}/${S3_KEY}"
        aws s3 cp "${BACKUP_FILE}" "s3://${R2_BUCKET}/${S3_KEY}" \
            --endpoint-url "${R2_ENDPOINT}" \
            --storage-class STANDARD \
            --no-progress
        log "Upload complete"

        # Remove local file after successful upload.
        rm -f "${BACKUP_FILE}"
    else
        log "R2_ENDPOINT not set — backup kept locally at ${BACKUP_FILE}"
    fi

    # Prune old backups from R2.
    prune_old_backups
}

# ── Prune ─────────────────────────────────────────────────────────────────────
prune_old_backups() {
    if [[ -z "${R2_ENDPOINT}" ]]; then return; fi
    log "Pruning backups older than ${RETENTION_DAYS} days"
    CUTOFF=$(date -u -d "${RETENTION_DAYS} days ago" +%Y-%m-%d 2>/dev/null || \
             date -u -v "-${RETENTION_DAYS}d" +%Y-%m-%d)
    aws s3 ls "s3://${R2_BUCKET}/postgres/" \
        --endpoint-url "${R2_ENDPOINT}" | \
    while read -r _ _ _ key; do
        # Extract date from filename: roost_YYYYMMDDTHHMMSSZ.sql.gz
        FILE_DATE=$(echo "${key}" | grep -oE '[0-9]{8}' | head -1 || true)
        if [[ -n "${FILE_DATE}" && "${FILE_DATE}" < "${CUTOFF//-/}" ]]; then
            log "Deleting old backup: ${key}"
            aws s3 rm "s3://${R2_BUCKET}/postgres/${key}" \
                --endpoint-url "${R2_ENDPOINT}" || true
        fi
    done
    log "Prune complete"
}

# ── Verify ────────────────────────────────────────────────────────────────────
verify_backup() {
    log "Verifying most recent backup..."
    if [[ -z "${R2_ENDPOINT}" ]]; then
        log "R2_ENDPOINT not set — cannot verify remote backup"
        exit 1
    fi

    # Find the most recent backup.
    LATEST=$(aws s3 ls "s3://${R2_BUCKET}/postgres/" \
        --endpoint-url "${R2_ENDPOINT}" | sort | tail -1 | awk '{print $4}')
    if [[ -z "${LATEST}" ]]; then
        log "ERROR: No backups found in R2"
        exit 1
    fi
    log "Verifying: ${LATEST}"

    TMP="${BACKUP_DIR}/verify_${TIMESTAMP}.sql.gz"
    mkdir -p "${BACKUP_DIR}"
    aws s3 cp "s3://${R2_BUCKET}/postgres/${LATEST}" "${TMP}" \
        --endpoint-url "${R2_ENDPOINT}"

    # Check gzip integrity.
    if gzip -t "${TMP}"; then
        log "Gzip integrity: PASS"
    else
        log "Gzip integrity: FAIL — backup may be corrupted"
        rm -f "${TMP}"
        exit 1
    fi

    # Check SQL content: must have CREATE TABLE or INSERT.
    if zcat "${TMP}" | grep -q -E "CREATE TABLE|INSERT INTO"; then
        log "SQL content check: PASS"
    else
        log "SQL content check: FAIL — backup may be empty"
        rm -f "${TMP}"
        exit 1
    fi

    rm -f "${TMP}"
    log "Verification PASSED: ${LATEST}"
}

# ── Restore ───────────────────────────────────────────────────────────────────
restore_backup() {
    FILE="${1:-}"
    if [[ -z "${FILE}" ]]; then
        echo "Usage: $0 restore <backup-file>"
        exit 1
    fi
    log "RESTORE: This will overwrite the current database. Press Ctrl+C to cancel."
    sleep 5

    TMP="${BACKUP_DIR}/restore_${TIMESTAMP}.sql.gz"
    mkdir -p "${BACKUP_DIR}"

    # Download from R2 if it looks like an S3 key.
    if [[ "${FILE}" == s3://* ]] || [[ "${FILE}" == postgres/* ]]; then
        aws s3 cp "s3://${R2_BUCKET}/postgres/$(basename "${FILE}")" "${TMP}" \
            --endpoint-url "${R2_ENDPOINT}"
        FILE="${TMP}"
    fi

    log "Restoring from ${FILE}..."
    zcat "${FILE}" | psql "${POSTGRES_URL}"
    log "Restore complete"
    rm -f "${TMP}"
}

# ── Main ──────────────────────────────────────────────────────────────────────
case "${1:-backup}" in
    backup)  run_backup ;;
    verify)  verify_backup ;;
    restore) restore_backup "${2:-}" ;;
    prune)   prune_old_backups ;;
    *)       echo "Usage: $0 {backup|verify|restore FILE|prune}"; exit 1 ;;
esac
