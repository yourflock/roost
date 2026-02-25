#!/usr/bin/env bash
# setup.sh — Create all Flock TV R2 buckets via Cloudflare API.
# Phase FLOCKTV: sets up the five R2 buckets required before any Flock TV service
# or CF Worker can be deployed.
#
# Usage:
#   source ~/.roost-secrets.env  # loads CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_API_KEY
#   ./infra/r2/setup.sh
#
# Idempotent: re-running is safe — already-existing buckets produce a success message.
set -euo pipefail

source ~/.roost-secrets.env 2>/dev/null || true

ACCOUNT_ID="${CLOUDFLARE_ACCOUNT_ID:-}"
API_KEY="${CLOUDFLARE_API_KEY:-}"

if [[ -z "$ACCOUNT_ID" || -z "$API_KEY" ]]; then
  echo "ERROR: CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_KEY are required."
  echo "Run: source ~/.roost-secrets.env"
  exit 1
fi

# All Flock TV R2 buckets.
# Bucket naming: lowercase, hyphens only (CF R2 requirement).
BUCKETS=(
  "flock-content"        # shared media pool — movies, shows, music, games, podcasts
  "flock-family-private" # per-family isolated data — photos, diary, social (NEVER shared)
  "flock-live"           # always-on IPTV DVR segments (7-day rolling window)
  "flock-media"          # general Flock media (avatars, cover art, etc.)
  "flock-backups"        # DB backups from all family containers
)

echo "Creating R2 buckets in account ${ACCOUNT_ID}..."
echo ""

SUCCESS_COUNT=0
SKIP_COUNT=0
FAIL_COUNT=0

for BUCKET in "${BUCKETS[@]}"; do
  printf "  %-30s " "$BUCKET"

  RESPONSE=$(curl -s -X POST \
    "https://api.cloudflare.com/client/v4/accounts/${ACCOUNT_ID}/r2/buckets" \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "{\"name\": \"${BUCKET}\"}")

  # Parse success field from CF response JSON.
  API_SUCCESS=$(python3 -c \
    "import sys,json; d=json.load(sys.stdin); print(str(d.get('success',False)).lower())" \
    <<< "$RESPONSE" 2>/dev/null || echo "false")

  if [[ "$API_SUCCESS" == "true" ]]; then
    echo "created"
    (( SUCCESS_COUNT++ )) || true
  else
    # Error code 10006 = bucket already exists — that is fine.
    ERROR_CODES=$(python3 -c \
      "import sys,json; d=json.load(sys.stdin); print([str(e.get('code','')) for e in d.get('errors',[])])" \
      <<< "$RESPONSE" 2>/dev/null || echo "[]")

    if echo "$ERROR_CODES" | grep -q "10006"; then
      echo "already exists (ok)"
      (( SKIP_COUNT++ )) || true
    else
      echo "FAILED"
      echo "    Response: $RESPONSE"
      (( FAIL_COUNT++ )) || true
    fi
  fi
done

echo ""
echo "Done: ${SUCCESS_COUNT} created, ${SKIP_COUNT} already existed, ${FAIL_COUNT} failed."

if [[ "$FAIL_COUNT" -gt 0 ]]; then
  echo "Some buckets failed to create. Check the API response above."
  exit 1
fi

echo ""
echo "Next steps:"
echo "  1. Deploy CF Workers: cd infra/workers/flock-tv && wrangler deploy"
echo "  2. Set Worker secret: wrangler secret put FLOCK_JWT_SECRET"
echo "  3. Verify: wrangler r2 bucket list"
