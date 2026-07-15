#!/usr/bin/env bash
#
# Build the RPM for the gateway, for one architecture.
#
# The gateway is a single static Go binary (pure Go, no CGO), so an .rpm for a
# foreign CPU is a plain cross-compile — no emulation, no venv, no compiled
# wheels. That is exactly why the server was moved off Python.
#
# Scratch: build/staging/server-rpm-<arch>   (rpmbuild tree, never /tmp)
# Input:   server/                           (the Go source)
# Output:  build/dist/task-timer-server-<version>-1.<arch>.rpm
#
# Usage: package-server-rpm.sh [x86_64|aarch64]   (default: aarch64)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PKG_NAME="task-timer-server"
PKG_ARCH="${1:-aarch64}"
case "$PKG_ARCH" in
  x86_64)  GOARCH=amd64 ;;
  aarch64) GOARCH=arm64 ;;
  *) echo "error: unsupported rpm arch $PKG_ARCH (want x86_64 or aarch64)" >&2; exit 1 ;;
esac

VERSION="${VERSION:-1.0.0}"
BUILD_DIR="${BUILD_DIR:-build}"

STAGING="${BUILD_DIR}/staging/server-rpm-${PKG_ARCH}"
RPMBUILD="${STAGING}/rpmbuild"
PAYLOAD="${STAGING}/payload"
DIST_DIR="${BUILD_DIR}/dist"

echo ">> staging rpmbuild tree in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "${RPMBUILD}/BUILD" "${RPMBUILD}/BUILDROOT" "${RPMBUILD}/RPMS" \
         "${RPMBUILD}/SOURCES" "${RPMBUILD}/SPECS" "${RPMBUILD}/SRPMS"
mkdir -p "${PAYLOAD}/etc/${PKG_NAME}" "${PAYLOAD}/lib/systemd/system" "${PAYLOAD}/usr/bin"
mkdir -p "$DIST_DIR"

# --- the application (one static binary) -------------------------------------
./pkg/build-server-binary.sh "$GOARCH" "$PAYLOAD"

install -m 0644 "pkg/systemd/${PKG_NAME}.service" \
  "${PAYLOAD}/lib/systemd/system/${PKG_NAME}.service"
install -m 0640 "server/config.example.toml" "${PAYLOAD}/etc/${PKG_NAME}/config.toml"

PAYLOAD_ABS="$(cd "$PAYLOAD" && pwd)"
RPMBUILD_ABS="$(cd "$RPMBUILD" && pwd)"

# --- spec --------------------------------------------------------------------
#
# The one dependency is nothing: a static Go binary needs no runtime libraries,
# not even a C library. AutoReq/AutoProv are off so rpmbuild does not invent
# Requires the binary does not have.
cat > "${RPMBUILD}/SPECS/${PKG_NAME}.spec" <<EOF
%global _payloaddir ${PAYLOAD_ABS}
%global debug_package %{nil}
%global __brp_mangle_shebangs %{nil}
%global __brp_strip %{nil}

Name:      ${PKG_NAME}
Version:   ${VERSION}
Release:   1%{?dist}
Summary:   Task Timer gateway
License:   MIT
BuildArch: ${PKG_ARCH}
AutoReq:   no
AutoProv:  no
Requires(pre): shadow-utils

%description
The server side of Task Timer. Desktop clients push their timed work sessions
here and this service writes them to Jira.

Enterprise credentials never leave this machine: each user connects their own
Atlassian account over OAuth 2.0 (3LO), so work logs are authored in Jira by the
person who did the work rather than by a shared service account.

This package is the SERVER. The desktop application is in the task-timer package
and is installed separately.

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}
cp -a %{_payloaddir}/. %{buildroot}/

%pre
# A system account: no login shell, no real home. It exists to own a database
# file and a listening socket.
getent group ${PKG_NAME} >/dev/null || groupadd -r ${PKG_NAME}
getent passwd ${PKG_NAME} >/dev/null || \\
    useradd -r -g ${PKG_NAME} -d /var/lib/${PKG_NAME} -s /sbin/nologin \\
            -c "Task Timer gateway" ${PKG_NAME}
exit 0

%post
mkdir -p /var/lib/${PKG_NAME}
chown ${PKG_NAME}:${PKG_NAME} /var/lib/${PKG_NAME}
chmod 0750 /var/lib/${PKG_NAME}
chown root:${PKG_NAME} /etc/${PKG_NAME}/config.toml
chmod 0640 /etc/${PKG_NAME}/config.toml

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

# Deliberately not started. It cannot work until an administrator has registered
# an Atlassian app and installed two secrets, and a unit that crash-loops on a
# fresh install teaches everyone to ignore its logs.
cat <<EOM

Task Timer gateway installed. It is NOT running yet - it needs two secrets first.

1. Register an OAuth 2.0 (3LO) app at https://developer.atlassian.com
     Callback URL: <your public_url>/auth/callback
     Scopes:       read:me read:jira-work write:jira-work offline_access
                   (offline_access is REQUIRED - without it every user is
                    asked to reconsent every hour)

2. Put the client id and your public URL in:
     /etc/${PKG_NAME}/config.toml

3. Drop in the two secrets, root-owned, mode 0600:

     printf %s '<atlassian client secret>' \\\\
       > /etc/${PKG_NAME}/atlassian_client_secret

     /usr/bin/${PKG_NAME} gen-key \\\\
       > /etc/${PKG_NAME}/token_encryption_key

     chmod 600 /etc/${PKG_NAME}/atlassian_client_secret \\\\
               /etc/${PKG_NAME}/token_encryption_key

   Back up token_encryption_key and NEVER regenerate it. It decrypts the stored
   Jira refresh tokens; a new key silently invalidates every one of them and
   every user has to reconnect.

4. systemctl enable --now ${PKG_NAME}

EOM

%preun
if [ \$1 -eq 0 ] && [ -d /run/systemd/system ]; then
    systemctl --quiet stop ${PKG_NAME}.service >/dev/null 2>&1 || true
    systemctl --quiet disable ${PKG_NAME}.service >/dev/null 2>&1 || true
fi
exit 0

%postun
if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi
# /var/lib is left alone on erase: it holds every user's Jira OAuth grant, and
# removing it silently signs them all out.
exit 0

%files
%config(noreplace) %attr(0640,root,${PKG_NAME}) /etc/${PKG_NAME}/config.toml
/lib/systemd/system/${PKG_NAME}.service
/usr/bin/${PKG_NAME}

%changelog
* Mon Jan 01 2024 Task Timer <support@example.com> - ${VERSION}-1
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

RPM_FILE="$(find "${RPMBUILD}/RPMS" -name "${PKG_NAME}-${VERSION}-*.rpm" | head -n 1)"
if [ ! -f "$RPM_FILE" ]; then
  echo "error: rpmbuild produced no package" >&2
  exit 1
fi

cp "$RPM_FILE" "${DIST_DIR}/"
echo ">> created $(cd "$DIST_DIR" && pwd)/$(basename "$RPM_FILE")"
