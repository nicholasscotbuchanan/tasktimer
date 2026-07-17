#!/usr/bin/env bash
#
# Build the Debian package, for one architecture.
#
# Scratch: build/staging/deb-<arch>   (never /tmp)
# Input:   build/bin/linux-<arch>/{task-timer,task-timer-daemon}
#          build/icons/png/icon_<N>.png
# Output:  build/dist/task-timer_<version>_<arch>.deb
#
# Usage: package-deb.sh [amd64|arm64]   (default: arm64)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PKG_NAME="task-timer"
PKG_ARCH="${1:-arm64}"
case "$PKG_ARCH" in
  amd64|arm64) : ;;
  *) echo "error: unsupported deb arch $PKG_ARCH (want amd64 or arm64)" >&2; exit 1 ;;
esac
GOARCH="$PKG_ARCH"

VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo 1.0.0)}"
BUILD_DIR="${BUILD_DIR:-build}"
ALLOW_MISSING_ICONS="${ALLOW_MISSING_ICONS:-0}"

STAGING="${BUILD_DIR}/staging/deb-${PKG_ARCH}"
ROOT="${STAGING}/root"
DIST_DIR="${BUILD_DIR}/dist"
BIN_SRC="${BUILD_DIR}/bin/linux-${GOARCH}"
ICON_PNG_DIR="${BUILD_DIR}/icons/png"

ICON_SIZES="16 32 48 64 128 256 512 1024"

for b in "${PKG_NAME}" "${PKG_NAME}-daemon"; do
  if [ ! -f "${BIN_SRC}/${b}" ]; then
    echo "error: missing binary ${BIN_SRC}/${b} - run 'make docker-build' first" >&2
    exit 1
  fi
done

echo ">> staging deb root in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "${ROOT}/DEBIAN" \
         "${ROOT}/usr/local/bin" \
         "${ROOT}/usr/share/applications" \
         "${ROOT}/usr/lib/systemd/user"
mkdir -p "$DIST_DIR"

# --- binaries (both of them) ------------------------------------------------
install -m 0755 "${BIN_SRC}/${PKG_NAME}"          "${ROOT}/usr/local/bin/${PKG_NAME}"
install -m 0755 "${BIN_SRC}/${PKG_NAME}-daemon"   "${ROOT}/usr/local/bin/${PKG_NAME}-daemon"

# --- desktop entry ----------------------------------------------------------
install -m 0644 "pkg/${PKG_NAME}.desktop" \
  "${ROOT}/usr/share/applications/${PKG_NAME}.desktop"

# --- sync daemon service ----------------------------------------------------
# A user unit, not a system one: the daemon reads a per-user database and a
# per-user token. Shipping the binary without this is how the backend ends up
# installed but never running.
install -m 0644 "pkg/systemd/${PKG_NAME}-daemon.service" \
  "${ROOT}/usr/lib/systemd/user/${PKG_NAME}-daemon.service"

# --- maintainer scripts -----------------------------------------------------
# The service is not enabled for the user automatically. It is a network-polling
# daemon that talks to their issue tracker, and turning that on for every account
# on a machine without being asked is not a package's decision to make. So the
# unit ships ready to go and the user opts in with one command, which postinst
# prints rather than leaving them to find in a README.
cat > "${ROOT}/DEBIAN/postinst" <<'POSTINST'
#!/bin/sh
set -e

if [ "$1" = "configure" ]; then
    # Pick up the newly installed user unit without a re-login.
    if [ -d /run/systemd/system ]; then
        systemctl --global daemon-reload >/dev/null 2>&1 || true
    fi

    cat <<'EOM'

Task Timer is installed.

  task-timer        the desktop app
  task-timer-daemon   the sync daemon (optional; does nothing until you enable a
                    provider in the app's Settings page, or in config.yaml)

To run the sync daemon in the background, as your own user:

  systemctl --user enable --now task-timer-daemon.service
  systemctl --user status task-timer-daemon.service

Put any API token in the daemon's env file (it does not inherit your shell):

  ~/.config/task-timer/credentials.env      e.g.  TASK_TIMER_GATEWAY_TOKEN=...
  chmod 600 ~/.config/task-timer/credentials.env

EOM
fi

exit 0
POSTINST
chmod 0755 "${ROOT}/DEBIAN/postinst"

cat > "${ROOT}/DEBIAN/prerm" <<'PRERM'
#!/bin/sh
set -e

# Best effort: the unit is a *user* unit, and dpkg runs as root, so the running
# user instances cannot all be reached from here. Stopping the invoking user's
# copy is the most that can honestly be done; a user who enabled it for another
# account disables it there themselves.
if [ "$1" = "remove" ] && [ -n "${SUDO_USER:-}" ] && [ -d /run/systemd/system ]; then
    runuser -u "$SUDO_USER" -- systemctl --user disable --now task-timer-daemon.service >/dev/null 2>&1 || true
fi

exit 0
PRERM
chmod 0755 "${ROOT}/DEBIAN/prerm"

# --- hicolor icons ----------------------------------------------------------
icons_found=0
for n in $ICON_SIZES; do
  src="${ICON_PNG_DIR}/icon_${n}.png"
  if [ -f "$src" ]; then
    dst="${ROOT}/usr/share/icons/hicolor/${n}x${n}/apps/${PKG_NAME}.png"
    mkdir -p "$(dirname "$dst")"
    install -m 0644 "$src" "$dst"
    icons_found=$((icons_found + 1))
  fi
done

if [ "$icons_found" -eq 0 ]; then
  if [ "$ALLOW_MISSING_ICONS" = "1" ]; then
    echo ">> WARNING: no icons in ${ICON_PNG_DIR} - packaging without icons."
  else
    echo "error: no icons found in ${ICON_PNG_DIR}" >&2
    echo "       run 'make icons' (go run ./tools/icongen) first." >&2
    exit 1
  fi
else
  echo ">> installed ${icons_found} hicolor icon size(s)"
fi

# --- control ----------------------------------------------------------------
sed -e "s/^Version:.*/Version: ${VERSION}/" \
    -e "s/^Architecture:.*/Architecture: ${PKG_ARCH}/" \
    pkg/control > "${ROOT}/DEBIAN/control"

OUT="${DIST_DIR}/${PKG_NAME}_${VERSION}_${PKG_ARCH}.deb"
rm -f "$OUT"
dpkg-deb --root-owner-group --build "$ROOT" "$OUT"

echo ">> created $(cd "$DIST_DIR" && pwd)/$(basename "$OUT")"
