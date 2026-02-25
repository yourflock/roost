#!/bin/sh
# build.sh — Builds the Roost RPM package.
#
# Usage:
#   ./packaging/rpm/build.sh [version]
#
# Output:
#   dist/roost-{version}-1.x86_64.rpm
#
# Requires: rpmbuild (rpm-build), go
# Install rpm-build: dnf install rpm-build  or  yum install rpm-build
# Run from the roost/ repo root.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
VERSION="${1:-$(cat "${REPO_ROOT}/VERSION" 2>/dev/null || echo "1.0.0")}"
DIST_DIR="${REPO_ROOT}/dist"
BUILD_TEMP="${REPO_ROOT}/.build-temp/rpm"
RPMBUILD_DIR="${HOME}/rpmbuild"

echo "Building Roost RPM version ${VERSION}..."
mkdir -p "${DIST_DIR}" "${BUILD_TEMP}" "${RPMBUILD_DIR}"/{SPECS,SOURCES,BUILD,RPMS,SRPMS}

# Compile the Roost binary.
echo "  Compiling Roost binary (linux/amd64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o "${BUILD_TEMP}/roost" \
    "${REPO_ROOT}/backend/cmd/api/"

# Create the source tarball that rpmbuild expects.
TARBALL_DIR="${BUILD_TEMP}/roost-${VERSION}"
mkdir -p "${TARBALL_DIR}/packaging/rpm/SOURCES"
cp "${BUILD_TEMP}/roost" "${TARBALL_DIR}/"
cp "${REPO_ROOT}/README.md" "${TARBALL_DIR}/" 2>/dev/null || touch "${TARBALL_DIR}/README.md"
cp "${SCRIPT_DIR}/SOURCES/roost.service" "${TARBALL_DIR}/packaging/rpm/SOURCES/"
cp "${SCRIPT_DIR}/SOURCES/roost.env" "${TARBALL_DIR}/packaging/rpm/SOURCES/"

# Create the source tarball.
TARBALL="${RPMBUILD_DIR}/SOURCES/roost-${VERSION}-linux-amd64.tar.gz"
tar czf "${TARBALL}" -C "${BUILD_TEMP}" "roost-${VERSION}/"
echo "  Source tarball: ${TARBALL}"

# Copy and update the spec file.
SPEC_FILE="${RPMBUILD_DIR}/SPECS/roost.spec"
sed "s/^Version:.*/Version:        ${VERSION}/" "${SCRIPT_DIR}/roost.spec" > "${SPEC_FILE}"

# Check if rpmbuild is available.
if command -v rpmbuild >/dev/null 2>&1; then
    echo "  Running rpmbuild..."
    rpmbuild -bb "${SPEC_FILE}" \
        --define "_topdir ${RPMBUILD_DIR}" \
        --define "version ${VERSION}"

    # Copy the built RPM to dist/.
    RPM_FILE=$(find "${RPMBUILD_DIR}/RPMS" -name "roost-${VERSION}-*.x86_64.rpm" | head -1)
    if [ -n "${RPM_FILE}" ]; then
        cp "${RPM_FILE}" "${DIST_DIR}/"
        echo ""
        echo "Done. RPM created:"
        ls -lh "${DIST_DIR}/roost-${VERSION}-"*.rpm 2>/dev/null
        echo ""
        echo "Install with: sudo rpm -i roost-${VERSION}-1.x86_64.rpm"
        echo "Or:           sudo dnf install roost-${VERSION}-1.x86_64.rpm"
    else
        echo "  WARNING: rpmbuild ran but no RPM file found."
    fi
else
    echo ""
    echo "  rpmbuild not found. Install with: dnf install rpm-build"
    echo "  Spec file written to: ${SPEC_FILE}"
    echo "  Source tarball: ${TARBALL}"
    echo "  To build on a RHEL/Rocky/Fedora system:"
    echo "    dnf install rpm-build"
    echo "    rpmbuild -bb ${SPEC_FILE}"
fi

rm -rf "${BUILD_TEMP}"
