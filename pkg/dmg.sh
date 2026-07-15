#!/usr/bin/env bash
#
# Build the macOS disk image.
#
# Scratch: build/staging/dmg   (never /tmp)
# Input:   build/dist/TaskTimer.app (falls back to build/staging/macapp)
# Output:  build/dist/TaskTimer.dmg
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

APP_NAME="TaskTimer"
BUILD_DIR="${BUILD_DIR:-build}"
VERSION="${VERSION:-1.0.0}"

STAGING="${BUILD_DIR}/staging/dmg"
DIST_DIR="${BUILD_DIR}/dist"
DMG_PATH="${DIST_DIR}/${APP_NAME}.dmg"

if [ "$(uname)" != "Darwin" ]; then
  echo "error: dmg.sh requires macOS (hdiutil)" >&2
  exit 1
fi

APP_BUNDLE="${DIST_DIR}/${APP_NAME}.app"
if [ ! -d "$APP_BUNDLE" ]; then
  APP_BUNDLE="${BUILD_DIR}/staging/macapp/${APP_NAME}.app"
fi
if [ ! -d "$APP_BUNDLE" ]; then
  echo "error: no ${APP_NAME}.app found - run 'make mac-app' first" >&2
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

echo ">> created $(cd "$DIST_DIR" && pwd)/${APP_NAME}.dmg"
