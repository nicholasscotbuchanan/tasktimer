#!/usr/bin/env bash
#
# Install or remove the Task Timer gateway as a per-user LaunchAgent.
#
# This ships inside the app bundle, at:
#
#   TaskTimerServer.app/Contents/Resources/server-agent.sh
#
# Usage:
#   server-agent.sh install     start the gateway now and at every login
#   server-agent.sh uninstall   stop it and remove the agent
#   server-agent.sh status      is it loaded, and is it running?
#
# It resolves the gateway's path from its own location rather than assuming
# /Applications, so a bundle run from ~/Applications or a mounted DMG still
# installs an agent that points at the right binary.
set -euo pipefail

LABEL="com.tasktimer.server"
AGENT_DIR="${HOME}/Library/LaunchAgents"
AGENT="${AGENT_DIR}/${LABEL}.plist"

# Contents/Resources/server-agent.sh -> Contents/MacOS/task-timer-server
RESOURCES="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXEC="$(cd "${RESOURCES}/../MacOS" && pwd)/task-timer-server"
TEMPLATE="${RESOURCES}/${LABEL}.plist"
CONFIG_EXAMPLE="${RESOURCES}/config.example.toml"

# Where the gateway reads its config and secrets on macOS (see paths_darwin.go).
SUPPORT_DIR="${HOME}/Library/Application Support/TaskTimerServer"
CONFIG="${SUPPORT_DIR}/config.toml"

LOG_DIR="${HOME}/Library/Logs"
LOG="${LOG_DIR}/task-timer-server.log"

# launchctl's modern verbs need the user's GUI domain.
DOMAIN="gui/$(id -u)"

die() { echo "error: $*" >&2; exit 1; }

seed_config() {
  mkdir -p "$SUPPORT_DIR"
  chmod 700 "$SUPPORT_DIR"
  if [ ! -f "$CONFIG" ]; then
    [ -f "$CONFIG_EXAMPLE" ] || die "the config template is missing: ${CONFIG_EXAMPLE}"
    cp "$CONFIG_EXAMPLE" "$CONFIG"
    chmod 600 "$CONFIG"
    echo "seeded ${CONFIG}"
  fi
}

install_agent() {
  [ -x "$EXEC" ]     || die "the gateway is missing from the bundle: ${EXEC}"
  [ -f "$TEMPLATE" ] || die "the agent template is missing: ${TEMPLATE}"

  seed_config
  mkdir -p "$AGENT_DIR" "$LOG_DIR"

  # Replace the placeholders. '|' as the delimiter because the paths contain '/'.
  sed -e "s|__EXEC__|${EXEC}|g" \
      -e "s|__LOG__|${LOG}|g" \
      "$TEMPLATE" > "$AGENT"
  chmod 644 "$AGENT"

  plutil -lint "$AGENT" >/dev/null || die "the generated agent is not a valid plist"

  # Replace any previous copy rather than erroring on a re-install.
  launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
  launchctl bootstrap "$DOMAIN" "$AGENT"
  launchctl enable "$DOMAIN/$LABEL"

  echo "installed ${AGENT}"
  echo "          -> ${EXEC}"
  echo "config:   ${CONFIG}"
  echo "log:      ${LOG}"
  echo
  echo "The gateway will not start until it is configured. Edit the config above"
  echo "(client_id, public_url), then drop in the two secrets beside it, 0600:"
  echo
  echo "    printf %s '<atlassian client secret>' \\"
  echo "      > '${SUPPORT_DIR}/atlassian_client_secret'"
  echo "    '${EXEC}' gen-key \\"
  echo "      > '${SUPPORT_DIR}/token_encryption_key'"
  echo "    chmod 600 '${SUPPORT_DIR}/atlassian_client_secret' \\"
  echo "              '${SUPPORT_DIR}/token_encryption_key'"
  echo
  echo "Back up token_encryption_key and never regenerate it: it decrypts the"
  echo "stored Jira refresh tokens, and a new key invalidates every one of them."
  echo
  echo "Then reload it:  '${BASH_SOURCE[0]}' install"
}

uninstall_agent() {
  launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
  rm -f "$AGENT"
  echo "removed ${AGENT}"
  echo "note: ${SUPPORT_DIR} was left in place. It holds the users' Jira"
  echo "      connections and the encryption key. Remove it by hand if you mean to."
}

status_agent() {
  if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then
    echo "loaded:  yes ($LABEL)"
    launchctl print "$DOMAIN/$LABEL" 2>/dev/null | grep -E '^\s*(state|pid) =' || true
  else
    echo "loaded:  no"
  fi
  echo "config:  ${CONFIG}$( [ -f "$CONFIG" ] && echo '' || echo '  (missing)')"
  echo "log:     ${LOG}"
}

case "${1:-}" in
  install)   install_agent ;;
  uninstall) uninstall_agent ;;
  status)    status_agent ;;
  *) echo "usage: $(basename "$0") {install|uninstall|status}" >&2; exit 2 ;;
esac
