#!/usr/bin/env bash
#
# Cross-compile the gateway .exe for one Windows architecture into build/bin.
#
# Shared by package-server-exe.sh (and callable on its own), for the same reason
# build-server-binary.sh exists: the build flags live in exactly one place.
#
# The server is pure Go (modernc.org/sqlite, no CGO), and the Windows service
# support uses golang.org/x/sys/windows/svc - also pure Go. So this cross-compiles
# to a single static .exe with nothing but GOARCH; no mingw, no CGO toolchain.
#
# Usage: build-server-exe.sh <goarch> <out-dir>
#   goarch:   amd64 | arm64
#   out-dir:  the .exe lands at <out-dir>/task-timer-server.exe
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

GOARCH_IN="${1:?usage: build-server-exe.sh <goarch> <out-dir>}"
OUT_DIR="${2:?usage: build-server-exe.sh <goarch> <out-dir>}"

case "$GOARCH_IN" in
  amd64|arm64) : ;;
  *) echo "error: unsupported goarch $GOARCH_IN (want amd64 or arm64)" >&2; exit 1 ;;
esac

VERSION="${VERSION:-1.0.0}"

if [ ! -f "${REPO_ROOT}/server/go.mod" ]; then
  echo "error: server/go.mod not found - run from a checkout with the Go server" >&2
  exit 1
fi

# Absolute OUT before the subshell cd's into server/, so `go build -o` does not
# resolve a relative path against server/ and drop the binary in the wrong tree.
mkdir -p "$OUT_DIR"
OUT="$(cd "$OUT_DIR" && pwd)/task-timer-server.exe"

echo ">> building task-timer-server.exe for windows/${GOARCH_IN}"
(
  cd "${REPO_ROOT}/server"
  # -buildvcs=false: the packaging path bind-mounts the repo into a container as
  # a different uid, so git reports "dubious ownership" and Go's VCS stamping
  # fails the build. The version is injected via -ldflags, so nothing is lost.
  GOOS=windows GOARCH="$GOARCH_IN" CGO_ENABLED=0 \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o "$OUT" ./cmd/task-timer-server
)

if command -v file >/dev/null 2>&1; then
  echo ">> $(file "$OUT")"
fi
echo ">> staged $(du -h "$OUT" | cut -f1) binary at ${OUT}"
