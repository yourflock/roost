#!/bin/sh
# build.sh — Builds the Roost QNAP QPKG package.
#
# Usage:
#   ./packaging/qnap/build.sh [version]
#
# Output:
#   dist/Roost_{version}_x86_64.qpkg
#   dist/Roost_{version}_aarch64.qpkg
#
# Run from the roost/ repo root.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
VERSION="${1:-$(cat "${REPO_ROOT}/VERSION" 2>/dev/null || echo "1.0.0")}"
DIST_DIR="${REPO_ROOT}/dist"
BUILD_TEMP="${REPO_ROOT}/.build-temp/qpkg"

echo "Building Roost QPKG version ${VERSION}..."
mkdir -p "${DIST_DIR}" "${BUILD_TEMP}"

build_qpkg() {
    ARCH="$1"
    GOARCH="$2"
    QPKG_FILE="Roost_${VERSION}_${ARCH}.qpkg"
    WORK_DIR="${BUILD_TEMP}/${ARCH}"

    echo "  Building ${ARCH} binary..."
    mkdir -p "${WORK_DIR}"

    # Compile the Roost binary for this architecture.
    CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o "${WORK_DIR}/roost" \
        "${REPO_ROOT}/backend/cmd/api/"

    # Copy QPKG manifest and scripts.
    cp "${SCRIPT_DIR}/qpkg.cfg" "${WORK_DIR}/qpkg.cfg"
    cp "${SCRIPT_DIR}/qpkg.sh" "${WORK_DIR}/qpkg.sh"
    cp -r "${SCRIPT_DIR}/shared/"* "${WORK_DIR}/" 2>/dev/null || true

    # Update version in qpkg.cfg.
    sed -i.bak "s/^QPKG_VER=.*/QPKG_VER=\"${VERSION}\"/" "${WORK_DIR}/qpkg.cfg"
    rm -f "${WORK_DIR}/qpkg.cfg.bak"

    # Use qbuild (QNAP Development Kit) if available, otherwise create a stub archive.
    if command -v qbuild >/dev/null 2>&1; then
        echo "  Using QDK qbuild..."
        cp -r "${WORK_DIR}" "${BUILD_TEMP}/qdk_src"
        cd "${BUILD_TEMP}/qdk_src"
        qbuild --build-arch "${ARCH}"
        cp *.qpkg "${DIST_DIR}/${QPKG_FILE}" 2>/dev/null || true
        cd "${REPO_ROOT}"
    else
        echo "  QDK not found — creating stub archive (install QDK for a proper .qpkg)."
        tar czf "${DIST_DIR}/${QPKG_FILE}" -C "${WORK_DIR}" .
    fi

    echo "  Created: dist/${QPKG_FILE}"
    rm -rf "${WORK_DIR}"
}

build_qpkg "x86_64"  "amd64"
build_qpkg "aarch64" "arm64"

rm -rf "${BUILD_TEMP}"

echo ""
echo "Done. Packages in dist/:"
ls -lh "${DIST_DIR}"/Roost_"${VERSION}"_*.qpkg 2>/dev/null
