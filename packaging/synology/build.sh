#!/bin/sh
# build.sh — Builds the Roost Synology SPK package.
#
# Usage:
#   ./packaging/synology/build.sh [version]
#
# Output:
#   dist/roost-{version}-amd64.spk
#   dist/roost-{version}-aarch64.spk
#
# Requires: go (for cross-compilation), tar
# Run from the roost/ repo root.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
VERSION="${1:-$(cat "${REPO_ROOT}/VERSION" 2>/dev/null || echo "1.0.0")}"
DIST_DIR="${REPO_ROOT}/dist"
BUILD_TEMP="${REPO_ROOT}/.build-temp/spk"

echo "Building Roost SPK version ${VERSION}..."

mkdir -p "${DIST_DIR}" "${BUILD_TEMP}"

build_spk() {
    ARCH="$1"         # e.g. "amd64" or "arm64"
    GOARCH="$2"       # e.g. "amd64" or "arm64"
    SPK_NAME="roost-${VERSION}-${ARCH}.spk"
    WORK_DIR="${BUILD_TEMP}/${ARCH}"

    echo "  Building ${ARCH} binary..."
    mkdir -p "${WORK_DIR}/scripts" "${WORK_DIR}/target"

    # Compile the Go binary for this architecture.
    CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o "${WORK_DIR}/target/roost" \
        "${REPO_ROOT}/backend/cmd/api/"

    # Create the payload tarball (package.tgz).
    tar czf "${WORK_DIR}/package.tgz" -C "${WORK_DIR}" target/

    # Copy INFO, scripts, and icons.
    cp "${SCRIPT_DIR}/INFO" "${WORK_DIR}/INFO"
    # Update version in INFO.
    sed -i.bak "s/^version=.*/version=\"${VERSION}-0001\"/" "${WORK_DIR}/INFO"
    rm -f "${WORK_DIR}/INFO.bak"

    cp -r "${SCRIPT_DIR}/scripts/"* "${WORK_DIR}/scripts/"
    cp "${SCRIPT_DIR}/PACKAGE_ICON.PNG" "${WORK_DIR}/PACKAGE_ICON.PNG"
    cp "${SCRIPT_DIR}/PACKAGE_ICON_256.PNG" "${WORK_DIR}/PACKAGE_ICON_256.PNG"
    cp -r "${SCRIPT_DIR}/wizard_ui" "${WORK_DIR}/wizard_ui"

    # Assemble the SPK.
    echo "  Assembling ${SPK_NAME}..."
    tar czf "${DIST_DIR}/${SPK_NAME}" \
        -C "${WORK_DIR}" \
        INFO \
        package.tgz \
        scripts/ \
        wizard_ui/ \
        PACKAGE_ICON.PNG \
        PACKAGE_ICON_256.PNG

    echo "  Created: dist/${SPK_NAME}"
    rm -rf "${WORK_DIR}"
}

build_spk "amd64"   "amd64"
build_spk "aarch64" "arm64"

rm -rf "${BUILD_TEMP}"

echo ""
echo "Done. Packages in dist/:"
ls -lh "${DIST_DIR}"/roost-"${VERSION}"-*.spk 2>/dev/null
