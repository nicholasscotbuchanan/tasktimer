#!/usr/bin/env bash
#
# Build a Windows installer with NSIS, for one architecture.
#
# Usage: package-exe.sh [amd64|arm64]     (default: amd64)
#
# Scratch: build/staging/nsis/<arch>   (never /tmp)
# Input:   build/bin/windows-<arch>/{task-timer.exe,task-timer-daemon.exe}
#          build/icons/TaskTimer.ico
# Output:  build/dist/task-timer-installer-<arch>.exe
#
# The installer stub NSIS emits is a 32-bit x86 binary whatever ARCH says, and
# Windows on ARM runs it emulated. ARCH describes the *payload*, so it is passed
# to the .nsi as -DARCH, which refuses to unpack onto a CPU that cannot run it.
#
# All paths are handed to makensis via -D defines; the .nsi never hardcodes
# a relative path of its own.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

ARCH="${1:-amd64}"
case "$ARCH" in
  amd64|arm64) ;;
  *) echo "error: unsupported arch '${ARCH}' (want amd64 or arm64)" >&2; exit 1 ;;
esac

PKG_NAME="task-timer"
INSTALLER_NAME="task-timer-installer-${ARCH}.exe"

VERSION="${VERSION:-1.0.0}"
BUILD_DIR="${BUILD_DIR:-build}"
ALLOW_MISSING_ICONS="${ALLOW_MISSING_ICONS:-0}"

STAGING="${BUILD_DIR}/staging/nsis/${ARCH}"
DIST_DIR="${BUILD_DIR}/dist"
BIN_SRC="${BUILD_DIR}/bin/windows-${ARCH}"
ICO="${BUILD_DIR}/icons/TaskTimer.ico"

for b in "${PKG_NAME}.exe" "${PKG_NAME}-sync.exe"; do
  if [ ! -f "${BIN_SRC}/${b}" ]; then
    echo "error: missing binary ${BIN_SRC}/${b} - run 'make docker-build' first" >&2
    exit 1
  fi
done

echo ">> staging nsis payload (${ARCH}) in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "$STAGING" "$DIST_DIR"

install -m 0755 "${BIN_SRC}/${PKG_NAME}.exe"      "${STAGING}/${PKG_NAME}.exe"
install -m 0755 "${BIN_SRC}/${PKG_NAME}-sync.exe" "${STAGING}/${PKG_NAME}-sync.exe"

NSIS_DEFS=()
if [ -f "$ICO" ]; then
  install -m 0644 "$ICO" "${STAGING}/TaskTimer.ico"
  NSIS_DEFS+=("-DICONFILE=$(cd "$STAGING" && pwd)/TaskTimer.ico")
else
  if [ "$ALLOW_MISSING_ICONS" = "1" ]; then
    echo ">> WARNING: ${ICO} not found - installer will use the default NSIS icon."
  else
    echo "error: missing icon ${ICO}" >&2
    echo "       run 'make icons' (go run ./tools/icongen) first." >&2
    exit 1
  fi
fi

STAGING_ABS="$(cd "$STAGING" && pwd)"
DIST_ABS="$(cd "$DIST_DIR" && pwd)"
OUT="${DIST_ABS}/${INSTALLER_NAME}"
rm -f "$OUT"

makensis \
  "-DOUTFILE=${OUT}" \
  "-DSTAGING=${STAGING_ABS}" \
  "-DVERSION=${VERSION}" \
  "-DARCH=${ARCH}" \
  ${NSIS_DEFS[@]+"${NSIS_DEFS[@]}"} \
  pkg/task-timer.nsi

echo ">> created ${OUT}"
