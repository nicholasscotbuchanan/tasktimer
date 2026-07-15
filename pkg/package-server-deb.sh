#!/usr/bin/env bash
#
# Build the Debian package for the gateway, for one architecture.
#
# This is the SERVER. It is a separate package from task-timer for a reason: the
# two are installed on different machines by different people. A desktop gets the
# client; one server, somewhere, gets this.
#
# The gateway is a single static Go binary (pure Go, no CGO), so a .deb for a
# foreign CPU is a plain cross-compile — no emulation, no venv, no compiled
# wheels. That is exactly why the server was moved off Python.
#
# Scratch: build/staging/server-deb-<arch>   (never /tmp)
# Input:   server/                           (the Go source)
# Output:  build/dist/task-timer-server_<version>_<arch>.deb
#
# Usage: package-server-deb.sh [amd64|arm64]   (default: arm64)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PKG_NAME="task-timer-server"
PKG_ARCH="${1:-arm64}"
case "$PKG_ARCH" in
  amd64|arm64) : ;;
  *) echo "error: unsupported deb arch $PKG_ARCH (want amd64 or arm64)" >&2; exit 1 ;;
esac

VERSION="${VERSION:-1.0.0}"
BUILD_DIR="${BUILD_DIR:-build}"

STAGING="${BUILD_DIR}/staging/server-deb-${PKG_ARCH}"
ROOT="${STAGING}/root"
DIST_DIR="${BUILD_DIR}/dist"

echo ">> staging ${PKG_NAME} ${PKG_ARCH} deb root in ${STAGING}"
rm -rf "$STAGING"
mkdir -p "${ROOT}/DEBIAN" \
         "${ROOT}/etc/${PKG_NAME}" \
         "${ROOT}/lib/systemd/system" \
         "${ROOT}/usr/bin"
mkdir -p "$DIST_DIR"

# --- the application (one static binary) -------------------------------------
# The deb arch names (amd64/arm64) are exactly Go's GOARCH names, so no mapping.
./pkg/build-server-binary.sh "$PKG_ARCH" "$ROOT"

# --- systemd system unit -----------------------------------------------------
install -m 0644 "pkg/systemd/${PKG_NAME}.service" \
  "${ROOT}/lib/systemd/system/${PKG_NAME}.service"

# --- config ------------------------------------------------------------------
# 0640 root:task-timer-server. It holds no secret (those are systemd credentials)
# but it does hold the Jira query and the site URL, and there is no reason for
# every account on the box to read it.
install -m 0640 "server/config.example.toml" "${ROOT}/etc/${PKG_NAME}/config.toml"

# --- maintainer scripts ------------------------------------------------------
# The service is NOT started on install. It cannot work until an administrator
# has registered an Atlassian app and dropped in two secrets, and a unit that
# crash-loops on a fresh install teaches everyone to ignore its logs.
cat > "${ROOT}/DEBIAN/postinst" <<POSTINST
#!/bin/sh
set -e

PKG_NAME="${PKG_NAME}"

if [ "\$1" = "configure" ]; then
    # A system account: no login shell, no home directory to speak of, no
    # password. It exists to own a database file and a listening socket.
    if ! getent passwd "\$PKG_NAME" >/dev/null; then
        adduser --system --group --no-create-home \\
                --home /var/lib/"\$PKG_NAME" \\
                --shell /usr/sbin/nologin \\
                "\$PKG_NAME" >/dev/null
    fi

    mkdir -p /var/lib/"\$PKG_NAME"
    chown "\$PKG_NAME":"\$PKG_NAME" /var/lib/"\$PKG_NAME"
    chmod 0750 /var/lib/"\$PKG_NAME"

    chown root:"\$PKG_NAME" /etc/"\$PKG_NAME"/config.toml
    chmod 0640 /etc/"\$PKG_NAME"/config.toml

    if [ -d /run/systemd/system ]; then
        systemctl daemon-reload >/dev/null 2>&1 || true
    fi

    cat <<EOM

Task Timer gateway installed. It is NOT running yet - it needs two secrets first.

1. Register an OAuth 2.0 (3LO) app at https://developer.atlassian.com
     Callback URL: <your public_url>/auth/callback
     Scopes:       read:me read:jira-work write:jira-work offline_access
                   (offline_access is REQUIRED - without it every user is
                    asked to reconsent every hour)

2. Put the client id and your public URL in:
     /etc/$PKG_NAME/config.toml

3. Drop in the two secrets, root-owned, mode 0600:

     printf %s '<atlassian client secret>' \\
       > /etc/$PKG_NAME/atlassian_client_secret

     /usr/bin/$PKG_NAME gen-key \\
       > /etc/$PKG_NAME/token_encryption_key

     chmod 600 /etc/$PKG_NAME/atlassian_client_secret \\
               /etc/$PKG_NAME/token_encryption_key

   Back up token_encryption_key and NEVER regenerate it. It decrypts the stored
   Jira refresh tokens; a new key silently invalidates every one of them and
   every user has to reconnect.

4. systemctl enable --now $PKG_NAME

EOM
fi

exit 0
POSTINST
chmod 0755 "${ROOT}/DEBIAN/postinst"

cat > "${ROOT}/DEBIAN/prerm" <<PRERM
#!/bin/sh
set -e

if [ "\$1" = "remove" ] && [ -d /run/systemd/system ]; then
    systemctl --quiet stop ${PKG_NAME}.service   >/dev/null 2>&1 || true
    systemctl --quiet disable ${PKG_NAME}.service >/dev/null 2>&1 || true
fi

exit 0
PRERM
chmod 0755 "${ROOT}/DEBIAN/prerm"

cat > "${ROOT}/DEBIAN/postrm" <<POSTRM
#!/bin/sh
set -e

# On purge, the config goes but /var/lib does NOT. It holds the users' Jira OAuth
# grants; deleting it silently signs everyone out, and 'purge' is a word people
# type while trying to fix something else.
if [ "\$1" = "purge" ]; then
    rm -rf /etc/${PKG_NAME}
    echo "note: /var/lib/${PKG_NAME} was left in place. It holds every user's Jira"
    echo "      connection. Remove it by hand if you really mean to."
fi

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

exit 0
POSTRM
chmod 0755 "${ROOT}/DEBIAN/postrm"

# The config is a conffile: dpkg must not silently overwrite an edited one on
# upgrade. Without this, an upgrade quietly reverts the operator's client_id.
echo "/etc/${PKG_NAME}/config.toml" > "${ROOT}/DEBIAN/conffiles"

# --- control -----------------------------------------------------------------
sed -e "s/^Version:.*/Version: ${VERSION}/" \
    -e "s/^Architecture:.*/Architecture: ${PKG_ARCH}/" \
    pkg/control-server > "${ROOT}/DEBIAN/control"

OUT="${DIST_DIR}/${PKG_NAME}_${VERSION}_${PKG_ARCH}.deb"
rm -f "$OUT"
dpkg-deb --root-owner-group --build "$ROOT" "$OUT"

echo ">> created $(cd "$DIST_DIR" && pwd)/$(basename "$OUT")"
