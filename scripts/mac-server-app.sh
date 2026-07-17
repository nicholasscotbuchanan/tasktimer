#!/usr/bin/env bash
#
# Assemble TaskTimerServer.app - the gateway (server) as a macOS .app bundle.
#
# The gateway is a headless daemon, but this bundle gives it a menu bar
# (status bar) item showing the address to configure it at - so it is an
# LSUIElement agent: a menu bar presence with no Dock icon and no window. The
# bundle also carries a LaunchAgent and an installer (server-agent.sh) so it can
# be made to run at login.
#
# This is the ONE build of the server that uses CGO: the menu bar needs AppKit,
# reached through fyne.io/systray behind the `tray` build tag. Every other target
# (.deb, .rpm, Docker, Windows) excludes it and stays headless and pure Go.
#
# Scratch:  build/staging/mac-server-app
# Binary:   build/bin/darwin-<arch>/task-timer-server   (CGO, -tags tray)
# Output:   build/dist/TaskTimerServer-<version>.app
#
# Nothing is ever written to the repo root or to /tmp.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

APP_NAME="TaskTimerServer"
BIN_NAME="task-timer-server"
BUNDLE_ID="com.tasktimer.server"
MIN_MACOS="11.0"

VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo 1.0.0)}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILD_DIR="${BUILD_DIR:-build}"

STAGING="${BUILD_DIR}/staging/mac-server-app"
DIST_DIR="${BUILD_DIR}/dist"
ICON_DIR="${BUILD_DIR}/icons"
ICNS="${ICON_DIR}/TaskTimer.icns"

if [ "$(uname)" != "Darwin" ]; then
  echo "error: mac-server-app.sh must run on macOS" >&2
  exit 1
fi

if [ ! -f "${REPO_ROOT}/server/go.mod" ]; then
  echo "error: server/go.mod not found - run from a checkout with the Go server" >&2
  exit 1
fi

case "$(uname -m)" in
  arm64|aarch64) GOARCH="arm64" ;;
  x86_64|amd64)  GOARCH="amd64" ;;
  *) echo "error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

BIN_DIR="${BUILD_DIR}/bin/darwin-${GOARCH}"

# --- compile the gateway into build/bin (never the repo root) ---------------
# CGO_ENABLED=1 with -tags tray: this build, and only this build, links the menu
# bar UI (AppKit via fyne.io/systray). It targets the host arch because CGO does
# not cross-compile without a cross toolchain, which is fine - a .app is only ever
# built on and for a Mac. Make OUT absolute before the subshell cd's into server/,
# so `go build -o` does not resolve it against the wrong directory.
echo ">> building ${BIN_NAME} for darwin/${GOARCH} (CGO, -tags tray) into ${BIN_DIR}"
mkdir -p "$BIN_DIR"
OUT="$(cd "$BIN_DIR" && pwd)/${BIN_NAME}"
(
  cd "${REPO_ROOT}/server"
  GOOS=darwin GOARCH="$GOARCH" CGO_ENABLED=1 \
    go build -trimpath -buildvcs=false -tags tray \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o "$OUT" ./cmd/task-timer-server
)

# --- assemble the bundle in staging -----------------------------------------
BUNDLE="${STAGING}/${APP_NAME}.app"
CONTENTS="${BUNDLE}/Contents"
MACOS_DIR="${CONTENTS}/MacOS"
RESOURCES_DIR="${CONTENTS}/Resources"

echo ">> staging bundle in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR"

# The real binary, plus a launcher wrapper as CFBundleExecutable. Double-clicking
# a headless server that needs config would otherwise just exit; the wrapper
# seeds a starter config first so there is something to edit, then execs it.
cp "$OUT" "${MACOS_DIR}/${BIN_NAME}"
chmod 755 "${MACOS_DIR}/${BIN_NAME}"

cat > "${MACOS_DIR}/${APP_NAME}" <<'LAUNCHER'
#!/bin/bash
# CFBundleExecutable for TaskTimerServer.app. Seeds a starter config on first
# launch, then runs the gateway. The server resolves config and secrets from
# ~/Library/Application Support/TaskTimerServer on its own (see paths_darwin.go);
# this only makes the first double-click do something coherent.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"      # Contents/MacOS
RES="$(cd "${HERE}/../Resources" && pwd)"
SUPPORT="${HOME}/Library/Application Support/TaskTimerServer"
mkdir -p "$SUPPORT"; chmod 700 "$SUPPORT"
if [ ! -f "${SUPPORT}/config.toml" ] && [ -f "${RES}/config.example.toml" ]; then
  cp "${RES}/config.example.toml" "${SUPPORT}/config.toml"
  chmod 600 "${SUPPORT}/config.toml"
fi
exec "${HERE}/task-timer-server"
LAUNCHER
chmod 755 "${MACOS_DIR}/${APP_NAME}"

# Config template + the LaunchAgent and its installer.
install -m 0644 "server/config.example.toml"            "${RESOURCES_DIR}/config.example.toml"
install -m 0644 "pkg/launchd/com.tasktimer.server.plist" "${RESOURCES_DIR}/com.tasktimer.server.plist"
install -m 0755 "pkg/launchd/server-agent.sh"           "${RESOURCES_DIR}/server-agent.sh"

ICON_PLIST_ENTRY=""
if [ -f "$ICNS" ]; then
  cp "$ICNS" "${RESOURCES_DIR}/${APP_NAME}.icns"
  ICON_PLIST_ENTRY="    <key>CFBundleIconFile</key>
    <string>${APP_NAME}.icns</string>"
fi

cat > "${CONTENTS}/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
${ICON_PLIST_ENTRY}
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_ID}</string>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>Task Timer Server</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleSupportedPlatforms</key>
    <array>
        <string>MacOSX</string>
    </array>
    <key>LSMinimumSystemVersion</key>
    <string>${MIN_MACOS}</string>
    <!-- Agent app: a menu bar (status bar) item, no Dock icon, no window.
         LSUIElement (not LSBackgroundOnly) is required - a background-only app
         is forbidden from putting anything in the menu bar. -->
    <key>LSUIElement</key>
    <true/>
</dict>
</plist>
EOF

# --- publish the finished bundle -------------------------------------------
# The dist artifact carries the version (uniform with every other package); the
# bundle inside stays TaskTimerServer.app, so the installed app keeps that name.
APP_OUT="${APP_NAME}-${VERSION}"
mkdir -p "$DIST_DIR"
rm -rf "${DIST_DIR}/${APP_OUT}.app"
cp -R "$BUNDLE" "${DIST_DIR}/${APP_OUT}.app"

echo ">> created $(cd "$DIST_DIR" && pwd)/${APP_OUT}.app"
