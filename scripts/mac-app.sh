#!/usr/bin/env bash
#
# Assemble TaskTimer.app for one architecture.
#
# Scratch:  build/staging/macapp-<goarch>
# Binaries: build/bin/darwin-<goarch>/{TaskTimer,TaskTimer-Sync}
# Output:   build/dist/TaskTimer-<version>-x86_64.app    (Intel/AMD)
#           build/dist/TaskTimer-<version>-aarch64.app   (ARM)
#
# Usage: mac-app.sh [x86_64|aarch64]   (default: host arch)
#
# The public arch label is the uniform x86_64/aarch64; GOARCH (amd64/arm64) is
# the Go spelling, used only for the bin/ path the compiler writes to.
#
# amd64 is cross-built on Apple Silicon with the stock toolchain: the Go tool
# drives clang with -arch x86_64 automatically, so no extra flags are needed.
#
# Nothing is ever written to the repo root or to /tmp.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

APP_NAME="TaskTimer"
BUNDLE_ID="com.tasktimer.app"
MIN_MACOS="11.0"

VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo 1.0.0)}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILD_DIR="${BUILD_DIR:-build}"
ALLOW_MISSING_ICONS="${ALLOW_MISSING_ICONS:-0}"

DIST_DIR="${BUILD_DIR}/dist"
ICON_DIR="${BUILD_DIR}/icons"
ICNS="${ICON_DIR}/${APP_NAME}.icns"

if [ "$(uname)" != "Darwin" ]; then
  echo "error: mac-app.sh must run on macOS" >&2
  exit 1
fi

# Arch to build: explicit arg wins, else the host arch.
ARCH_ARG="${1:-}"
if [ -z "$ARCH_ARG" ]; then
  case "$(uname -m)" in
    arm64|aarch64) ARCH_ARG="aarch64" ;;
    x86_64|amd64)  ARCH_ARG="x86_64" ;;
    *) echo "error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
  esac
fi
case "$ARCH_ARG" in
  x86_64)  GOARCH="amd64"; APP_OUT="${APP_NAME}-${VERSION}-x86_64" ;;
  aarch64) GOARCH="arm64"; APP_OUT="${APP_NAME}-${VERSION}-aarch64" ;;
  *) echo "error: unsupported architecture $ARCH_ARG (want x86_64 or aarch64)" >&2; exit 1 ;;
esac

BIN_DIR="${BUILD_DIR}/bin/darwin-${GOARCH}"
STAGING="${BUILD_DIR}/staging/macapp-${GOARCH}"

# --- compile both binaries directly into build/bin (never the repo root) ----
echo ">> building binaries into ${BIN_DIR}"
mkdir -p "$BIN_DIR"
# CGO_ENABLED=1 is explicit, not the default: cross-building (arm64 host ->
# amd64 target, or vice versa) turns cgo OFF unless forced, and Fyne's OpenGL
# bindings are cgo-gated, so a cgo-off build excludes every go-gl file. The Go
# tool drives clang with the right -arch from GOARCH on its own.
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${GIT_COMMIT}"
GOOS=darwin GOARCH="$GOARCH" CGO_ENABLED=1 go build -trimpath -ldflags "$LDFLAGS" \
  -o "${BIN_DIR}/${APP_NAME}" ./cmd/task-timer
GOOS=darwin GOARCH="$GOARCH" CGO_ENABLED=1 go build -trimpath -ldflags "$LDFLAGS" \
  -o "${BIN_DIR}/${APP_NAME}-Sync" ./cmd/task-timer-daemon

# --- icons ------------------------------------------------------------------
if [ ! -f "$ICNS" ]; then
  if [ "$ALLOW_MISSING_ICONS" = "1" ]; then
    echo ">> WARNING: ${ICNS} not found - bundling without an icon."
  else
    echo "error: missing icon ${ICNS}" >&2
    echo "       run 'make icons' (go run ./tools/icongen) first." >&2
    exit 1
  fi
fi

# --- assemble the bundle in staging ----------------------------------------
BUNDLE="${STAGING}/${APP_OUT}.app"
CONTENTS="${BUNDLE}/Contents"
MACOS_DIR="${CONTENTS}/MacOS"
RESOURCES_DIR="${CONTENTS}/Resources"

echo ">> staging bundle in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR"

cp "${BIN_DIR}/${APP_NAME}" "${MACOS_DIR}/${APP_NAME}"
cp "${BIN_DIR}/${APP_NAME}-Sync" "${MACOS_DIR}/${APP_NAME}-Sync"
chmod 755 "${MACOS_DIR}/${APP_NAME}" "${MACOS_DIR}/${APP_NAME}-Sync"

ICON_PLIST_ENTRY=""
if [ -f "$ICNS" ]; then
  cp "$ICNS" "${RESOURCES_DIR}/${APP_NAME}.icns"
  ICON_PLIST_ENTRY="    <key>CFBundleIconFile</key>
    <string>${APP_NAME}.icns</string>"
fi

# --- sync daemon login agent ------------------------------------------------
# The bundle carries the daemon binary, so it must also carry the means to run
# it. Shipping TaskTimer-Daemon with no way to start it is how the backend ends up
# installed but dead. daemon-agent.sh resolves the daemon's path from its own
# location and installs the LaunchAgent; the template alongside it is not
# loadable on its own.
cp "pkg/launchd/com.tasktimer.daemon.plist" "${RESOURCES_DIR}/com.tasktimer.daemon.plist"
cp "pkg/launchd/daemon-agent.sh"            "${RESOURCES_DIR}/daemon-agent.sh"
chmod 644 "${RESOURCES_DIR}/com.tasktimer.daemon.plist"
chmod 755 "${RESOURCES_DIR}/daemon-agent.sh"

# No 'var' symlink: the app resolves its data directory at runtime from
# TASK_TIMER_DATA_DIR or the OS user-config dir, so the bundle stays read-only.
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
    <string>Task Timer</string>
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
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>LSMinimumSystemVersion</key>
    <string>${MIN_MACOS}</string>
</dict>
</plist>
EOF

# --- publish the finished bundle -------------------------------------------
mkdir -p "$DIST_DIR"
rm -rf "${DIST_DIR}/${APP_OUT}.app"
cp -R "$BUNDLE" "${DIST_DIR}/${APP_OUT}.app"

echo ">> created $(cd "$DIST_DIR" && pwd)/${APP_OUT}.app"
