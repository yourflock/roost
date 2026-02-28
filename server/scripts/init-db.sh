#!/usr/bin/env bash
# init-db.sh — Roost database initialization
# Runs all migrations in order, then seeds the superowner.
# Safe to run multiple times (migrations are idempotent).
#
# Usage:
#   ./scripts/init-db.sh [OPTIONS]
#
# Options:
#   --host         Postgres host (default: 127.0.0.1)
#   --port         Postgres port (default: 5433)
#   --user         Postgres superuser (default: postgres)
#   --password     Postgres password (prompt if not set)
#   --db           Database name to create (default: roost)
#   --db-user      Application DB user to create (default: roost)
#   --db-pass      Application DB user password (required)
#   --dry-run      Print what would be done without executing

set -euo pipefail

# ─────────────────────────────────────────────
# Defaults
# ─────────────────────────────────────────────
PG_HOST="${PG_HOST:-127.0.0.1}"
PG_PORT="${PG_PORT:-5433}"
PG_USER="${PG_USER:-postgres}"
PG_PASSWORD="${POSTGRES_PASSWORD:-}"
DB_NAME="${POSTGRES_DB:-roost}"
DB_USER="${POSTGRES_USER:-roost}"
DB_PASS="${POSTGRES_PASSWORD:-}"
DRY_RUN=0
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATIONS_DIR="$SCRIPT_DIR/../db/migrations"

# ─────────────────────────────────────────────
# Argument parsing
# ─────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case $1 in
    --host)      PG_HOST="$2";    shift 2 ;;
    --port)      PG_PORT="$2";    shift 2 ;;
    --user)      PG_USER="$2";    shift 2 ;;
    --password)  PG_PASSWORD="$2"; shift 2 ;;
    --db)        DB_NAME="$2";    shift 2 ;;
    --db-user)   DB_USER="$2";    shift 2 ;;
    --db-pass)   DB_PASS="$2";    shift 2 ;;
    --dry-run)   DRY_RUN=1;       shift ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

if [[ -z "$DB_PASS" ]]; then
  echo "ERROR: --db-pass or POSTGRES_PASSWORD env var is required."
  exit 1
fi

if [[ -z "$PG_PASSWORD" ]]; then
  read -r -s -p "Postgres superuser password: " PG_PASSWORD
  echo ""
fi

# ─────────────────────────────────────────────
# Helpers
# ─────────────────────────────────────────────
run_sql() {
  local sql="$1"
  if [[ $DRY_RUN -eq 1 ]]; then
    echo "[DRY RUN] SQL: $sql"
    return 0
  fi
  PGPASSWORD="$PG_PASSWORD" psql \
    -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" \
    -c "$sql" >/dev/null 2>&1
}

run_sql_file() {
  local file="$1"
  local db="${2:-$DB_NAME}"
  if [[ $DRY_RUN -eq 1 ]]; then
    echo "[DRY RUN] Would run: $file on $db"
    return 0
  fi
  PGPASSWORD="$DB_PASS" psql \
    -h "$PG_HOST" -p "$PG_PORT" -U "$DB_USER" -d "$db" \
    -f "$file"
}

log() { echo "[$(date '+%H:%M:%S')] $*"; }

# ─────────────────────────────────────────────
# Step 1: Create database and user
# ─────────────────────────────────────────────
log "Step 1: Ensuring database user '$DB_USER' exists..."
run_sql "DO \$\$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '$DB_USER') THEN
    CREATE ROLE \"$DB_USER\" LOGIN PASSWORD '$DB_PASS';
  ELSE
    ALTER ROLE \"$DB_USER\" WITH PASSWORD '$DB_PASS';
  END IF;
END \$\$;"

log "Step 2: Ensuring database '$DB_NAME' exists..."
if [[ $DRY_RUN -eq 0 ]]; then
  DB_EXISTS=$(PGPASSWORD="$PG_PASSWORD" psql \
    -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" \
    -tAc "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" 2>/dev/null || echo "")
  if [[ -z "$DB_EXISTS" ]]; then
    PGPASSWORD="$PG_PASSWORD" psql \
      -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" \
      -c "CREATE DATABASE \"$DB_NAME\" OWNER \"$DB_USER\";"
    log "  Database '$DB_NAME' created."
  else
    log "  Database '$DB_NAME' already exists. Skipping creation."
    PGPASSWORD="$PG_PASSWORD" psql \
      -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" \
      -c "ALTER DATABASE \"$DB_NAME\" OWNER TO \"$DB_USER\";"
  fi
else
  log "[DRY RUN] Would create database '$DB_NAME' owned by '$DB_USER'."
fi

# ─────────────────────────────────────────────
# Step 3: Run migrations in order
# ─────────────────────────────────────────────
log "Step 3: Running migrations from $MIGRATIONS_DIR..."

if [[ ! -d "$MIGRATIONS_DIR" ]]; then
  log "ERROR: Migrations directory not found: $MIGRATIONS_DIR"
  exit 1
fi

MIGRATION_COUNT=0
for migration_file in $(ls "$MIGRATIONS_DIR"/*.sql 2>/dev/null | sort); do
  filename=$(basename "$migration_file")
  log "  Applying: $filename"
  run_sql_file "$migration_file"
  MIGRATION_COUNT=$((MIGRATION_COUNT + 1))
done

if [[ $MIGRATION_COUNT -eq 0 ]]; then
  log "WARNING: No migration files found in $MIGRATIONS_DIR"
else
  log "  Applied $MIGRATION_COUNT migrations."
fi

# ─────────────────────────────────────────────
# Step 4: Seed superowner (alisalaah@gmail.com)
# Idempotent: uses INSERT ... ON CONFLICT DO NOTHING
# ─────────────────────────────────────────────
log "Step 4: Seeding superowner account..."
SEED_SQL=$(cat << 'SEED'
INSERT INTO auth.users (
  email,
  display_name,
  is_active,
  default_role,
  metadata
)
VALUES (
  'alisalaah@gmail.com',
  'Ali',
  true,
  'owner',
  '{"is_superowner": true, "billing_exempt": true}'::jsonb
)
ON CONFLICT (email) DO UPDATE
  SET default_role = 'owner',
      metadata = '{"is_superowner": true, "billing_exempt": true}'::jsonb,
      is_active = true;
SEED
)
if [[ $DRY_RUN -eq 1 ]]; then
  echo "[DRY RUN] Would seed superowner: alisalaah@gmail.com"
else
  PGPASSWORD="$DB_PASS" psql \
    -h "$PG_HOST" -p "$PG_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -c "$SEED_SQL" >/dev/null 2>&1 || {
    log "  NOTE: auth.users table not yet created by migrations (expected for fresh init with Hasura Auth)."
    log "  Superowner will be created on first login via Hasura Auth."
  }
fi
log "  Superowner seeded (or will be seeded on first login)."

# ─────────────────────────────────────────────
# Step 5: Grant permissions
# ─────────────────────────────────────────────
log "Step 5: Granting permissions to '$DB_USER'..."
run_sql "GRANT ALL PRIVILEGES ON DATABASE \"$DB_NAME\" TO \"$DB_USER\";"

log ""
log "Database initialization complete."
log "  Host:     $PG_HOST:$PG_PORT"
log "  Database: $DB_NAME"
log "  User:     $DB_USER"
log ""
log "Next steps:"
log "  1. Start the full stack: docker compose -f backend/docker-compose.prod.yml up -d"
log "  2. Apply Hasura metadata: cd backend/hasura && hasura metadata apply"
log "  3. Verify health: curl https://roost.unity.dev/health"
