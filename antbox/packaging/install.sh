#!/bin/bash
# AntBox Install Script (T-7H.2.005)
# Usage: curl -sSL https://roost.unity.dev/install-antbox.sh | bash

set -euo pipefail

ANTBOX_VERSION="${ANTBOX_VERSION:-1.0.0}"
INSTALL_DIR="/usr/local/bin"
BLUE='\033[0;34m'
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

log() { echo -e "${BLUE}[antbox]${NC} $*"; }
ok()  { echo -e "${GREEN}[antbox]${NC} $*"; }
err() { echo -e "${RED}[antbox]${NC} ERROR: $*" >&2; exit 1; }

# Detect arch
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  armv7*) ARCH="armv7" ;;
  *) err "Unsupported architecture: ${ARCH}" ;;
esac

# Detect package manager
if command -v dpkg >/dev/null 2>&1; then
  PKG_TYPE="deb"
elif command -v rpm >/dev/null 2>&1; then
  PKG_TYPE="rpm"
else
  PKG_TYPE="binary"
fi

log "Installing AntBox v${ANTBOX_VERSION} (${ARCH}, ${PKG_TYPE})..."

BASE_URL="https://github.com/unyeco/owl/releases/download/antbox-v${ANTBOX_VERSION}"

if [ "${PKG_TYPE}" = "deb" ]; then
  PKG="antbox_${ANTBOX_VERSION}_${ARCH}.deb"
  curl -sSL -o "/tmp/${PKG}" "${BASE_URL}/${PKG}"
  dpkg -i "/tmp/${PKG}"
  rm "/tmp/${PKG}"
elif [ "${PKG_TYPE}" = "rpm" ]; then
  PKG="antbox-${ANTBOX_VERSION}.${ARCH}.rpm"
  curl -sSL -o "/tmp/${PKG}" "${BASE_URL}/${PKG}"
  rpm -i "/tmp/${PKG}"
  rm "/tmp/${PKG}"
else
  # Fallback: install binary directly
  curl -sSL -o /tmp/antbox "${BASE_URL}/antbox-${ARCH}"
  chmod +x /tmp/antbox
  mv /tmp/antbox "${INSTALL_DIR}/antbox"
fi

# Create config dir and default env file
mkdir -p /etc/antbox
if [ ! -f /etc/antbox/antbox.env ]; then
  cat > /etc/antbox/antbox.env << ENV
# AntBox Configuration
# Set this to your Owl server's URL (http://your-server-ip:7860)
ANTBOX_SERVER_URL=http://localhost:7860
ANTBOX_LOG_LEVEL=info
ENV
  ok "Created /etc/antbox/antbox.env — edit it with your Owl server URL."
fi

ok "AntBox v${ANTBOX_VERSION} installed."
log "Next steps:"
log "  1. Edit /etc/antbox/antbox.env: set ANTBOX_SERVER_URL"
log "  2. Plug in your USB DVB tuner"
log "  3. systemctl start antbox"
log "  4. systemctl enable antbox  (auto-start on boot)"
