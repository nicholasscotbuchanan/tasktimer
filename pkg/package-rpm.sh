#!/usr/bin/env bash
#
# Build the RPM package, for one architecture.
#
# Scratch: build/staging/rpm-<arch>   (rpmbuild tree lives here, never /tmp)
# Input:   build/bin/linux-<goarch>/{task-timer,task-timer-daemon}
#          build/icons/png/icon_<N>.png
# Output:  build/dist/task-timer-<version>-1.<arch>.rpm
#
# Usage: package-rpm.sh [x86_64|aarch64]   (default: aarch64)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PKG_NAME="task-timer"
PKG_ARCH="${1:-aarch64}"
case "$PKG_ARCH" in
  x86_64)  GOARCH="amd64" ;;
  aarch64) GOARCH="arm64" ;;
  *) echo "error: unsupported rpm arch $PKG_ARCH (want x86_64 or aarch64)" >&2; exit 1 ;;
esac

VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo 1.0.0)}"
BUILD_DIR="${BUILD_DIR:-build}"
ALLOW_MISSING_ICONS="${ALLOW_MISSING_ICONS:-0}"

STAGING="${BUILD_DIR}/staging/rpm-${PKG_ARCH}"
RPMBUILD="${STAGING}/rpmbuild"
PAYLOAD="${STAGING}/payload"
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

echo ">> staging rpmbuild tree in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "${RPMBUILD}/BUILD" "${RPMBUILD}/BUILDROOT" "${RPMBUILD}/RPMS" \
         "${RPMBUILD}/SOURCES" "${RPMBUILD}/SPECS" "${RPMBUILD}/SRPMS"
mkdir -p "${PAYLOAD}/usr/local/bin" "${PAYLOAD}/usr/share/applications" \
         "${PAYLOAD}/usr/lib/systemd/user"
mkdir -p "$DIST_DIR"

# --- payload: same content as the deb ---------------------------------------
install -m 0755 "${BIN_SRC}/${PKG_NAME}"        "${PAYLOAD}/usr/local/bin/${PKG_NAME}"
install -m 0755 "${BIN_SRC}/${PKG_NAME}-daemon" "${PAYLOAD}/usr/local/bin/${PKG_NAME}-daemon"
install -m 0644 "pkg/${PKG_NAME}.desktop" \
  "${PAYLOAD}/usr/share/applications/${PKG_NAME}.desktop"

# The sync daemon's user unit, exactly as the deb ships it.
install -m 0644 "pkg/systemd/${PKG_NAME}-daemon.service" \
  "${PAYLOAD}/usr/lib/systemd/user/${PKG_NAME}-daemon.service"

ICON_FILES=""
icons_found=0
for n in $ICON_SIZES; do
  src="${ICON_PNG_DIR}/icon_${n}.png"
  if [ -f "$src" ]; then
    rel="/usr/share/icons/hicolor/${n}x${n}/apps/${PKG_NAME}.png"
    mkdir -p "$(dirname "${PAYLOAD}${rel}")"
    install -m 0644 "$src" "${PAYLOAD}${rel}"
    ICON_FILES="${ICON_FILES}${rel}"$'\n'
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

PAYLOAD_ABS="$(cd "$PAYLOAD" && pwd)"
RPMBUILD_ABS="$(cd "$RPMBUILD" && pwd)"

# --- spec -------------------------------------------------------------------
cat > "${RPMBUILD}/SPECS/${PKG_NAME}.spec" <<EOF
%global _payloaddir ${PAYLOAD_ABS}
%global debug_package %{nil}

Name:      ${PKG_NAME}
Version:   ${VERSION}
Release:   1%{?dist}
Summary:   Task Timer application
License:   MIT
BuildArch: ${PKG_ARCH}

%description
A cross-platform task timer with GUI and synchronization support.
Ships the task-timer GUI and the task-timer-daemon headless sync daemon.

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}
cp -a %{_payloaddir}/. %{buildroot}/

%post
# The sync daemon is a *user* service and is not enabled automatically: it polls
# the user's issue tracker, and switching that on for every account on a machine
# is not a package's decision to make. It ships ready to enable.
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

Put any API token in the daemon's env file (it does not inherit your shell):

  ~/.config/task-timer/credentials.env      e.g.  TASK_TIMER_GATEWAY_TOKEN=...
  chmod 600 ~/.config/task-timer/credentials.env

EOM

%files
/usr/local/bin/${PKG_NAME}
/usr/local/bin/${PKG_NAME}-daemon
/usr/lib/systemd/user/${PKG_NAME}-daemon.service
/usr/share/applications/${PKG_NAME}.desktop
${ICON_FILES}
%changelog
* $(date +"%a %b %d %Y") Task Timer <support@example.com> - ${VERSION}-1
- Initial package
EOF

# Cross-arch packaging on a single-arch host.
#
# The build container is pinned to linux/arm64 (Dockerfile.build), so rpmbuild
# runs on an aarch64 machine. Debian's rpm only lists `noarch` in the aarch64
# buildarch_compat table, so `--target x86_64` fails the build-arch score check
# with "No compatible architectures found for build" - even though nothing is
# compiled here (the Go binary is already cross-compiled and just copied in).
#
# We tell rpm that this host may build the foreign arch by adding a
# buildarch_compat entry via a supplementary rcfile. --rcfile REPLACES the
# default list, so the stock /usr/lib/rpm/rpmrc is included first, then ours.
# Both directions are listed so this works whatever arch the host happens to be.
RPMRC_EXTRA="${STAGING}/rpmrc-cross"
cat > "$RPMRC_EXTRA" <<'RC'
buildarch_compat: aarch64: x86_64
buildarch_compat: x86_64: aarch64
RC

rpmbuild --rcfile "/usr/lib/rpm/rpmrc:$(cd "$(dirname "$RPMRC_EXTRA")" && pwd)/$(basename "$RPMRC_EXTRA")" \
  --define "_topdir ${RPMBUILD_ABS}" --target "${PKG_ARCH}" -bb "${RPMBUILD}/SPECS/${PKG_NAME}.spec"

RPM_FILE="${RPMBUILD}/RPMS/${PKG_ARCH}/${PKG_NAME}-${VERSION}-1.${PKG_ARCH}.rpm"
if [ ! -f "$RPM_FILE" ]; then
  # dist tag may be appended by the build host - fall back to a glob.
  RPM_FILE="$(find "${RPMBUILD}/RPMS" -name "${PKG_NAME}-${VERSION}-*.rpm" | head -n 1)"
fi
if [ ! -f "$RPM_FILE" ]; then
  echo "error: rpmbuild produced no package" >&2
  exit 1
fi

cp "$RPM_FILE" "${DIST_DIR}/"
echo ">> created $(cd "$DIST_DIR" && pwd)/$(basename "$RPM_FILE")"
