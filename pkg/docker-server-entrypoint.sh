#!/bin/sh
#
# Entrypoint for the Dockerfile.server image: run the gateway that the .deb
# installed, configured from the environment.
#
# The container runs the binary directly, NOT under systemd, so the two secrets
# that are normally systemd credentials come from TASK_TIMER_SERVER_* env vars
# instead. To make `docker run` with no flags actually start and listen - the
# whole point of a throwaway test server - anything required but unset is filled
# with a loud placeholder rather than failing at boot.
set -eu

# Listen on all interfaces so the mapped port is reachable from the host. The
# server defaults to loopback, which inside a container means "nobody".
: "${TASK_TIMER_SERVER_HOST:=0.0.0.0}"
export TASK_TIMER_SERVER_HOST

# Encryption key: generate an EPHEMERAL one if none was supplied. It changes on
# every start, so any Jira grant stored in this container is void after a
# restart - fine for a smoke test, useless for anything you want to keep.
if [ -z "${TASK_TIMER_SERVER_TOKEN_ENCRYPTION_KEY:-}" ]; then
  TASK_TIMER_SERVER_TOKEN_ENCRYPTION_KEY="$(/usr/bin/task-timer-server gen-key)"
  export TASK_TIMER_SERVER_TOKEN_ENCRYPTION_KEY
  echo "WARNING: generated an EPHEMERAL token_encryption_key; it changes on every" >&2
  echo "         restart. Pass -e TASK_TIMER_SERVER_TOKEN_ENCRYPTION_KEY=... to keep grants." >&2
fi

# Atlassian OAuth: the gateway refuses to boot without a client id and secret.
# Fill placeholders so it listens; real logins need real values passed with -e.
if [ -z "${TASK_TIMER_SERVER_ATLASSIAN_CLIENT_ID:-}" ] || \
   [ -z "${TASK_TIMER_SERVER_ATLASSIAN_CLIENT_SECRET:-}" ]; then
  : "${TASK_TIMER_SERVER_ATLASSIAN_CLIENT_ID:=placeholder-client-id}"
  : "${TASK_TIMER_SERVER_ATLASSIAN_CLIENT_SECRET:=placeholder-client-secret}"
  export TASK_TIMER_SERVER_ATLASSIAN_CLIENT_ID TASK_TIMER_SERVER_ATLASSIAN_CLIENT_SECRET
  echo "WARNING: Atlassian client id/secret not fully set; using placeholders. The" >&2
  echo "         server will listen, but OAuth logins fail until you pass real ones." >&2
fi

# The .deb's config lives at /etc/task-timer-server/config.toml (readable by
# root, which is who we run as here); env vars above override what they set.
exec /usr/bin/task-timer-server "$@"
