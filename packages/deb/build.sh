#!/bin/sh
# build.sh — Builds the Roost .deb package.
#
# Usage:
#   ./packaging/deb/build.sh [version]
#
# Output:
#   dist/roost_{version}_amd64.deb
#
# Requires: dpkg-deb, go
# Run from the roost/ repo root.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
VERSION="${1:-$(cat "${REPO_ROOT}/VERSION" 2>/dev/null || echo "1.0.0")}"
DIST_DIR="${REPO_ROOT}/dist"
BUILD_DIR="${REPO_ROOT}/.build-temp/deb"
PKG_DIR="${BUILD_DIR}/roost_${VERSION}_amd64"

echo "Building Roost .deb version ${VERSION}..."
mkdir -p "${DIST_DIR}" "${PKG_DIR}"

# Copy the package tree from packaging/deb/.
cp -r "${SCRIPT_DIR}/DEBIAN" "${PKG_DIR}/"
cp -r "${SCRIPT_DIR}/etc" "${PKG_DIR}/"
cp -r "${SCRIPT_DIR}/lib" "${PKG_DIR}/"
cp -r "${SCRIPT_DIR}/usr" "${PKG_DIR}/"

# Update version in control file.
sed -i.bak "s/^Version:.*/Version: ${VERSION}/" "${PKG_DIR}/DEBIAN/control"
rm -f "${PKG_DIR}/DEBIAN/control.bak"

# Compile the real Roost binary.
echo "  Compiling Roost binary (linux/amd64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o "${PKG_DIR}/usr/bin/roost" \
    "${REPO_ROOT}/backend/cmd/api/"

# Set correct permissions on DEBIAN scripts.
chmod 755 "${PKG_DIR}/DEBIAN/preinst" \
          "${PKG_DIR}/DEBIAN/postinst" \
          "${PKG_DIR}/DEBIAN/prerm" \
          "${PKG_DIR}/DEBIAN/postrm"

# Set correct permissions on installed files.
chmod 755 "${PKG_DIR}/usr/bin/roost"
chmod 644 "${PKG_DIR}/etc/roost/roost.env"
chmod 644 "${PKG_DIR}/lib/systemd/system/roost.service"

# Build the .deb package.
DEB_FILE="${DIST_DIR}/roost_${VERSION}_amd64.deb"
echo "  Building ${DEB_FILE}..."
dpkg-deb --build --root-owner-group "${PKG_DIR}" "${DEB_FILE}"

rm -rf "${BUILD_DIR}"

echo ""
echo "Done. Package created:"
ls -lh "${DEB_FILE}"
echo ""
echo "Install with: sudo dpkg -i ${DEB_FILE}"
echo "Or:           sudo apt install ${DEB_FILE}"
