#!/bin/bash
# Build AntBox DEB and RPM packages (T-7H.2.003, T-7H.2.004)
# Requires: nfpm (install: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest)

set -e

VERSION="${VERSION:-1.0.0}"
ARCH="${ARCH:-amd64}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_DIR="${SCRIPT_DIR}/.."
OUTPUT="${SCRIPT_DIR}/dist"

mkdir -p "${OUTPUT}"
mkdir -p "${BUILD_DIR}/bin"

echo "[build] Building antbox binary for ${ARCH}..."
cd "${BUILD_DIR}"
GOARCH="${ARCH}" CGO_ENABLED=0 GOOS=linux go build \
  -ldflags="-s -w -X main.Version=${VERSION}" \
  -o "bin/antbox-${ARCH}" \
  .

echo "[build] Building DEB and RPM packages..."

cat > /tmp/antbox-nfpm.yml << NFPM
name: "antbox"
arch: "${ARCH}"
platform: "linux"
version: "${VERSION}"
section: "default"
priority: "extra"
maintainer: "Owl Contributors <hello@yourflock.org>"
description: "AntBox - Owl USB TV Tuner Daemon. Turns any Linux machine with a USB DVB tuner into a live TV source for Owl."
homepage: "https://github.com/yourflock/owl"
license: "MIT"

contents:
  - src: "${BUILD_DIR}/bin/antbox-${ARCH}"
    dst: "/usr/local/bin/antbox"
    file_info:
      mode: 0755
  - src: "${SCRIPT_DIR}/antbox.service"
    dst: "/etc/systemd/system/antbox.service"
  - src: "${SCRIPT_DIR}/99-dvb-usb.rules"
    dst: "/etc/udev/rules.d/99-dvb-usb.rules"

scripts:
  postinstall: |
    systemctl daemon-reload
    systemctl enable antbox.service
    echo "AntBox installed. Set ANTBOX_SERVER_URL in /etc/antbox/antbox.env then: systemctl start antbox"

overrides:
  deb:
    depends:
      - ffmpeg
  rpm:
    depends:
      - ffmpeg
NFPM

nfpm package --config /tmp/antbox-nfpm.yml --packager deb --target "${OUTPUT}/antbox_${VERSION}_${ARCH}.deb"
nfpm package --config /tmp/antbox-nfpm.yml --packager rpm --target "${OUTPUT}/antbox-${VERSION}.${ARCH}.rpm"

echo "[build] Done:"
ls -la "${OUTPUT}/"
