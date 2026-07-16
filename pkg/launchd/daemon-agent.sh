#!/usr/bin/env bash
#
# Install or remove the Task Timer sync daemon as a login agent.
#
# This ships inside the app bundle, at:
#
#   /Applications/TaskTimer.app/Contents/Resources/daemon-agent.sh
#
# Usage:
#   daemon-agent.sh install     start the daemon now and at every login
#   daemon-agent.sh uninstall   stop it and remove the agent
#   daemon-agent.sh status      is it loaded, and is it running?
#
# It resolves the daemon's path from its own location rather than assuming
# /Applications, so a bundle run from ~/Applications or a mounted DMG still
# installs an agent that points at the right binary.
set -euo pipefail

LABEL="com.tasktimer.daemon"
AGENT_DIR="${HOME}/Library/LaunchAgents"
AGENT="${AGENT_DIR}/${LABEL}.plist"

# Contents/Resources/daemon-agent.sh -> Contents/MacOS/TaskTimer-Daemon
RESOURCES="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXEC="$(cd "${RESOURCES}/../MacOS" && pwd)/TaskTimer-Daemon"
TEMPLATE="${RESOURCES}/${LABEL}.plist"

LOG_DIR="${HOME}/Library/Logs"
LOG="${LOG_DIR}/task-timer-daemon.log"

# launchctl's modern verbs need the user's GUI domain.
DOMAIN="gui/$(id -u)"

die() { echo "error: $*" >&2; exit 1; }

install_agent() {
  [ -x "$EXEC" ]     || die "the sync daemon is missing from the bundle: ${EXEC}"
  [ -f "$TEMPLATE" ] || die "the agent template is missing: ${TEMPLATE}"

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
  echo "log:      ${LOG}"
  echo
  echo "The daemon does nothing until a provider is enabled. Open Task Timer's"
  echo "Settings page, or edit config.yaml, then put your API token in:"
  echo "  \$(the data directory)/credentials.env    e.g. TASK_TIMER_GATEWAY_TOKEN=..."
}

uninstall_agent() {
  launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
  rm -f "$AGENT"
  echo "removed ${AGENT}"
}

status_agent() {
  if [ ! -f "$AGENT" ]; then
    echo "not installed"
    return 0
  fi
  echo "agent: ${AGENT}"
  if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then
    echo "state: loaded"
  else
    echo "state: installed but not loaded"
  fi
  echo "log:   ${LOG}"
}

case "${1:-}" in
  install)   install_agent ;;
  uninstall) uninstall_agent ;;
  status)    status_agent ;;
  *)
    echo "usage: $(basename "$0") {install|uninstall|status}" >&2
    exit 2
    ;;
esac
