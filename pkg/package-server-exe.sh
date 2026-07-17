#!/usr/bin/env bash
#
# Build the Windows installer for the gateway, for one architecture.
#
# This is the SERVER. It installs a single .exe and registers it as a Windows
# service (via `task-timer-server service install`), the Windows analogue of the
# systemd unit the .deb ships. The desktop client's installer is a separate thing
# built by package-exe.sh.
#
# The gateway is pure Go, so the payload .exe is a plain cross-compile - no CGO,
# no mingw. Only makensis is needed to wrap it, which the build container has.
#
# Scratch: build/staging/nsis-server/<arch>   (never /tmp)
# Input:   server/                            (the Go source)
# Output:  build/dist/task-timer-server-installer-<version>-<arch>.exe
#
# Usage: package-server-exe.sh [x86_64|aarch64]   (default: x86_64)
#
# The public arch label is the uniform x86_64/aarch64; GOARCH (amd64/arm64) is
# the Go spelling handed to build-server-exe.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

ARCH="${1:-x86_64}"
case "$ARCH" in
  x86_64)  GOARCH="amd64" ;;
  aarch64) GOARCH="arm64" ;;
  *) echo "error: unsupported arch '${ARCH}' (want x86_64 or aarch64)" >&2; exit 1 ;;
esac

VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo 1.0.0)}"
BUILD_DIR="${BUILD_DIR:-build}"
ALLOW_MISSING_ICONS="${ALLOW_MISSING_ICONS:-0}"

INSTALLER_NAME="task-timer-server-installer-${VERSION}-${ARCH}.exe"

STAGING="${BUILD_DIR}/staging/nsis-server/${ARCH}"
DIST_DIR="${BUILD_DIR}/dist"
ICO="${BUILD_DIR}/icons/TaskTimer.ico"

if ! command -v makensis >/dev/null 2>&1; then
  echo "error: makensis not found - the Windows installer is built in the" >&2
  echo "       Dockerfile.build container (make server-exe), which ships NSIS." >&2
  exit 1
fi

echo ">> staging server nsis payload (${ARCH}) in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "$STAGING" "$DIST_DIR"

# --- the application (one static .exe) + the config template ----------------
./pkg/build-server-exe.sh "$GOARCH" "$STAGING"
install -m 0644 "server/config.example.toml" "${STAGING}/config.example.toml"

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
  pkg/task-timer-server.nsi

echo ">> created ${OUT}"
