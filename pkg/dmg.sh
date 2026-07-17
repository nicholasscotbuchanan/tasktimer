#!/usr/bin/env bash
#
# Build the macOS disk image for one architecture.
#
# Scratch: build/staging/dmg-<goarch>   (never /tmp)
# Input:   build/dist/<app>.app (falls back to build/staging/macapp-<goarch>)
# Output:  build/dist/TaskTimer-<version>-x86_64.dmg    (Intel/AMD)
#          build/dist/TaskTimer-<version>-aarch64.dmg   (ARM)
#
# Usage: dmg.sh [x86_64|aarch64]   (default: aarch64)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

APP_NAME="TaskTimer"
BUILD_DIR="${BUILD_DIR:-build}"
VERSION="${VERSION:-1.0.0}"

case "${1:-aarch64}" in
  x86_64)  GOARCH="amd64"; APP_OUT="${APP_NAME}-${VERSION}-x86_64" ;;
  aarch64) GOARCH="arm64"; APP_OUT="${APP_NAME}-${VERSION}-aarch64" ;;
  *) echo "error: unsupported architecture ${1} (want x86_64 or aarch64)" >&2; exit 1 ;;
esac

STAGING="${BUILD_DIR}/staging/dmg-${GOARCH}"
DIST_DIR="${BUILD_DIR}/dist"
DMG_PATH="${DIST_DIR}/${APP_OUT}.dmg"

if [ "$(uname)" != "Darwin" ]; then
  echo "error: dmg.sh requires macOS (hdiutil)" >&2
  exit 1
fi

APP_BUNDLE="${DIST_DIR}/${APP_OUT}.app"
if [ ! -d "$APP_BUNDLE" ]; then
  APP_BUNDLE="${BUILD_DIR}/staging/macapp-${GOARCH}/${APP_OUT}.app"
fi
if [ ! -d "$APP_BUNDLE" ]; then
  echo "error: no ${APP_OUT}.app found - run 'make mac-app' first" >&2
  exit 1
fi

echo ">> staging dmg contents in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "$STAGING" "$DIST_DIR"
rm -f "$DMG_PATH"

cp -R "$APP_BUNDLE" "${STAGING}/${APP_NAME}.app"
ln -s /Applications "${STAGING}/Applications"

hdiutil create \
  -volname "${APP_NAME} ${VERSION}" \
  -srcfolder "$STAGING" \
  -ov \
  -format UDZO \
  "$DMG_PATH"

echo ">> created $(cd "$DIST_DIR" && pwd)/${APP_OUT}.dmg"
