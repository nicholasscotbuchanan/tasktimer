#!/usr/bin/env bash
#
# Cross-compile the gateway binary for one Linux architecture and stage it into a
# package root, ready to ship.
#
# Shared by package-server-deb.sh and package-server-rpm.sh, because getting the
# build flags wrong in two places independently is how one package ends up with a
# binary the other does not.
#
# THE POINT OF THIS SCRIPT: the server is pure Go (modernc.org/sqlite, no CGO), so
# it cross-compiles to a single static binary for any architecture with nothing
# but GOARCH. That is the whole reason the server was rewritten from Python — a
# venv full of compiled wheels was architecture-bound and could not be built for
# a foreign CPU without emulation; this can.
#
# Usage: build-server-binary.sh <goarch> <package-root>
#   goarch:        amd64 | arm64
#   package-root:  staging dir; the binary lands at <root>/usr/bin/task-timer-server
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

GOARCH_IN="${1:?usage: build-server-binary.sh <goarch> <package-root>}"
ROOT="${2:?usage: build-server-binary.sh <goarch> <package-root>}"

case "$GOARCH_IN" in
  amd64|arm64) : ;;
  *) echo "error: unsupported goarch $GOARCH_IN (want amd64 or arm64)" >&2; exit 1 ;;
esac

VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo 1.0.0)}"
GIT_COMMIT="${GIT_COMMIT:-unknown}"

if [ ! -f "${REPO_ROOT}/server/go.mod" ]; then
  echo "error: server/go.mod not found - run from a checkout with the Go server" >&2
  exit 1
fi

# Make OUT absolute BEFORE the build subshell cd's into server/. The packaging
# scripts pass ROOT as a path relative to REPO_ROOT (e.g. build/staging/...),
# and `go build -o` resolves a relative -o against the subshell's CWD - which is
# server/, not REPO_ROOT. Left relative, the binary lands under server/build/...
# while every other step looks in build/..., so packaging ships an empty usr/bin.
mkdir -p "${ROOT}/usr/bin"
OUT="$(cd "${ROOT}/usr/bin" && pwd)/task-timer-server"

echo ">> building task-timer-server for linux/${GOARCH_IN}"
(
  cd "${REPO_ROOT}/server"
  # CGO_ENABLED=0: a static binary with no libc dependency, so it runs on any
  # glibc or musl host of the right architecture without a Depends: line.
  #
  # -buildvcs=false: the packaging path bind-mounts the repo into a container as
  # a different uid, so git reports "dubious ownership" and Go's default VCS
  # stamping fails the build with "error obtaining VCS status: exit status 128".
  # The version is injected explicitly via -ldflags below, so the incidental VCS
  # metadata buys nothing here; turning it off makes the build independent of the
  # git state of whatever tree it runs against.
  GOOS=linux GOARCH="$GOARCH_IN" CGO_ENABLED=0 \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o "$OUT" ./cmd/task-timer-server
)

# Prove the arch is what we asked for before packaging it under that label.
if command -v file >/dev/null 2>&1; then
  echo ">> $(file "$OUT")"
fi

echo ">> staged $(du -h "$OUT" | cut -f1) binary at ${OUT}"
