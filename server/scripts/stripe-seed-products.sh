#!/usr/bin/env bash
# stripe-seed-products.sh — Create Roost subscription products + prices in Stripe
# and back-fill subscription_plans table with the resulting Stripe IDs.
#
# Usage:
#   ./scripts/stripe-seed-products.sh [--env dev|prod] [--dry-run]
#
# Prerequisites:
#   - STRIPE_SECRET_KEY must be set (or loaded from vault)
#   - DATABASE_URL must be set (or defaults to local dev)
#   - curl and psql must be on PATH
#
# Plans created:
#   basic   — $4.99/mo  $49.99/yr  — up to 2 streams
#   premium — $9.99/mo  $99.99/yr  — up to 5 streams
#   family  — $14.99/mo $149.99/yr — up to 10 streams
#
# The Founder plan is free and has no Stripe product.
# Run once per environment. Safe to re-run: checks existing plans first.
set -euo pipefail

# ── Args ─────────────────────────────────────────────────────────────────────
ENV="dev"
DRY_RUN=false

for arg in "$@"; do
  case $arg in
    --env=*) ENV="${arg#*=}" ;;
    --env) shift; ENV="${1:-dev}" ;;
    --dry-run) DRY_RUN=true ;;
    *) echo "Unknown arg: $arg"; exit 1 ;;
  esac
done

# ── Load vault if key not set ─────────────────────────────────────────────────
if [[ -z "${STRIPE_SECRET_KEY:-}" ]]; then
  if [[ -f ~/.roost-secrets.env ]]; then
    # shellcheck source=/dev/null
    source ~/.roost-secrets.env
    STRIPE_SECRET_KEY="${STRIPE_FLOCK_SECRET_KEY:-}"
  fi
fi

if [[ -z "${STRIPE_SECRET_KEY:-}" ]]; then
  echo "ERROR: STRIPE_SECRET_KEY is not set. Add to .env or vault."
  exit 1
fi

# ── DB connection ─────────────────────────────────────────────────────────────
if [[ -z "${DATABASE_URL:-}" ]]; then
  if [[ "$ENV" == "prod" ]]; then
    echo "ERROR: DATABASE_URL required for prod."
    exit 1
  fi
  DATABASE_URL="postgres://roost:$(grep POSTGRES_PASSWORD backend/.env 2>/dev/null | cut -d= -f2 || echo 'roost')@localhost:5433/roost_dev"
fi

STRIPE_BASE="https://api.stripe.com/v1"

# ── Helpers ───────────────────────────────────────────────────────────────────
stripe_post() {
  local endpoint="$1"; shift
  curl -sS -X POST "${STRIPE_BASE}/${endpoint}" \
    -u "${STRIPE_SECRET_KEY}:" \
    "$@"
}

stripe_get() {
  local endpoint="$1"; shift
  curl -sS -X GET "${STRIPE_BASE}/${endpoint}" \
    -u "${STRIPE_SECRET_KEY}:" \
    "$@"
}

extract_json() {
  local json="$1" key="$2"
  echo "$json" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$key',''))" 2>/dev/null
}

psql_exec() {
  psql "$DATABASE_URL" -t -A -c "$1"
}

echo "==================================================================="
echo "  Roost Stripe Product Seeder"
echo "  ENV: $ENV  DRY_RUN: $DRY_RUN"
echo "  Key: ${STRIPE_SECRET_KEY:0:12}..."
echo "==================================================================="

# ── Plan definitions ──────────────────────────────────────────────────────────
# slug | name | monthly_cents | annual_cents | streams
PLANS=(
  "basic|Roost Basic|499|4999|2"
  "premium|Roost Premium|999|9999|5"
  "family|Roost Family|1499|14999|10"
)

# ── Per-plan setup ────────────────────────────────────────────────────────────
for plan_def in "${PLANS[@]}"; do
  IFS='|' read -r SLUG NAME MONTHLY_CENTS ANNUAL_CENTS _STREAMS <<< "$plan_def"

  echo ""
  echo "--- Plan: $NAME ($SLUG) ---"

  # Check if product already seeded in DB
  EXISTING_PRODUCT=$(psql_exec "SELECT stripe_product_id FROM subscription_plans WHERE slug='$SLUG' AND stripe_product_id IS NOT NULL LIMIT 1" 2>/dev/null || echo "")

  if [[ -n "$EXISTING_PRODUCT" ]]; then
    echo "  Already seeded: stripe_product_id=$EXISTING_PRODUCT  (skipping)"
    continue
  fi

  if [[ "$DRY_RUN" == "true" ]]; then
    echo "  [DRY RUN] Would create Stripe product: $NAME"
    echo "    monthly: ${MONTHLY_CENTS}¢  annual: ${ANNUAL_CENTS}¢"
    continue
  fi

  # Create Stripe product
  echo "  Creating Stripe product..."
  PRODUCT_JSON=$(stripe_post "products" \
    -d "name=${NAME}" \
    -d "metadata[roost_plan]=${SLUG}" \
    -d "metadata[env]=${ENV}")

  PRODUCT_ID=$(extract_json "$PRODUCT_JSON" "id")
  if [[ -z "$PRODUCT_ID" || "$PRODUCT_ID" == "null" ]]; then
    echo "  ERROR creating product: $PRODUCT_JSON"
    exit 1
  fi
  echo "  Product created: $PRODUCT_ID"

  # Monthly price
  echo "  Creating monthly price..."
  MONTHLY_JSON=$(stripe_post "prices" \
    -d "product=${PRODUCT_ID}" \
    -d "currency=usd" \
    -d "unit_amount=${MONTHLY_CENTS}" \
    -d "recurring[interval]=month" \
    -d "metadata[roost_plan]=${SLUG}" \
    -d "metadata[billing_period]=monthly" \
    -d "nickname=${NAME} Monthly")

  MONTHLY_PRICE_ID=$(extract_json "$MONTHLY_JSON" "id")
  if [[ -z "$MONTHLY_PRICE_ID" || "$MONTHLY_PRICE_ID" == "null" ]]; then
    echo "  ERROR creating monthly price: $MONTHLY_JSON"
    exit 1
  fi
  echo "  Monthly price: $MONTHLY_PRICE_ID"

  # Annual price
  echo "  Creating annual price..."
  ANNUAL_JSON=$(stripe_post "prices" \
    -d "product=${PRODUCT_ID}" \
    -d "currency=usd" \
    -d "unit_amount=${ANNUAL_CENTS}" \
    -d "recurring[interval]=year" \
    -d "metadata[roost_plan]=${SLUG}" \
    -d "metadata[billing_period]=annual" \
    -d "nickname=${NAME} Annual")

  ANNUAL_PRICE_ID=$(extract_json "$ANNUAL_JSON" "id")
  if [[ -z "$ANNUAL_PRICE_ID" || "$ANNUAL_PRICE_ID" == "null" ]]; then
    echo "  ERROR creating annual price: $ANNUAL_JSON"
    exit 1
  fi
  echo "  Annual price:   $ANNUAL_PRICE_ID"

  # Update DB
  echo "  Updating subscription_plans in DB..."
  psql_exec "
    UPDATE subscription_plans
    SET stripe_product_id         = '${PRODUCT_ID}',
        stripe_price_id_monthly   = '${MONTHLY_PRICE_ID}',
        stripe_price_id_annual    = '${ANNUAL_PRICE_ID}'
    WHERE slug = '${SLUG}';
  " || { echo "  ERROR: DB update failed for $SLUG"; exit 1; }

  echo "  Done: $NAME -> product=$PRODUCT_ID monthly=$MONTHLY_PRICE_ID annual=$ANNUAL_PRICE_ID"
done

# ── Verify ────────────────────────────────────────────────────────────────────
echo ""
echo "==================================================================="
echo "  Verification — subscription_plans with Stripe IDs:"
echo "==================================================================="
psql_exec "
  SELECT slug, name, monthly_price_cents, annual_price_cents,
         COALESCE(stripe_product_id, 'NOT SET') AS product,
         COALESCE(stripe_price_id_monthly, 'NOT SET') AS monthly_price,
         COALESCE(stripe_price_id_annual,  'NOT SET') AS annual_price
  FROM subscription_plans
  ORDER BY monthly_price_cents;
" 2>/dev/null || echo "  (DB not reachable — run against running instance)"

echo ""
echo "Done. Next: update Roost .env with these price IDs if using checkout links,"
echo "or use the price IDs returned via GET /billing/plans."
