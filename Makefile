# ---------------------------------------------------------------------------
# Task Timer build system
#
# RULE: every byte this build produces lands under $(BUILD_DIR). Nothing is
# written to the repo root, to /tmp, or to any other scratch location.
#
#   build/
#     bin/<goos>-<goarch>/   compiled binaries
#     icons/                 generated icons (go run ./tools/icongen)
#     staging/<platform>/    ALL scratch work (macapp, dmg, deb, rpm, nsis)
#     dist/                  final shippable artifacts ONLY
# ---------------------------------------------------------------------------

APP_NAME    := task-timer
# Single source of truth: the top-level VERSION file. Every packaging script
# defaults to reading the same file, so a release is bumped in exactly one place.
VERSION     := $(shell cat VERSION 2>/dev/null || echo 1.0.0)
GIT_COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

BUILD_DIR   := build
BIN_DIR     := $(BUILD_DIR)/bin
ICON_DIR    := $(BUILD_DIR)/icons
STAGING_DIR := $(BUILD_DIR)/staging
DIST_DIR    := $(BUILD_DIR)/dist

# Absolute paths so scripts and container mounts never guess.
ROOT_DIR    := $(abspath .)
ABS_BUILD   := $(ROOT_DIR)/$(BUILD_DIR)

DOCKER      ?= $(shell if command -v podman >/dev/null 2>&1; then echo podman; elif command -v docker >/dev/null 2>&1; then echo docker; else echo podman; fi)
IMAGE_TAG   := task-timer-builder:latest
UNAME       := $(shell uname)
UNAME_M     := $(shell uname -m)

ifeq ($(UNAME_M),arm64)
HOST_ARCH := arm64
else ifeq ($(UNAME_M),aarch64)
HOST_ARCH := arm64
else
HOST_ARCH := amd64
endif

ifeq ($(UNAME),Darwin)
HOST_OS           := darwin
GUI_BIN           := TaskTimer
DAEMON_BIN          := TaskTimer-Daemon
MAC_TARGETS       := mac-app-x86_64 mac-app-aarch64
DMG_TARGETS       := dmg-x86_64 dmg-aarch64
SERVER_MAC_TARGET := mac-server-app
else
HOST_OS           := linux
GUI_BIN           := task-timer
DAEMON_BIN          := task-timer-daemon
MAC_TARGETS       :=
DMG_TARGETS       :=
SERVER_MAC_TARGET :=
endif

HOST_BIN_DIR := $(BIN_DIR)/$(HOST_OS)-$(HOST_ARCH)

# Windows ships one installer per CPU: an arm64 binary cannot run on x64, and an
# x64 binary only runs on ARM64 Windows under emulation. Both are cross-compiled
# in the build container (see Dockerfile.build).
#
# WIN_ARCHES is the Go spelling (GOARCH), used only for the build/bin/windows-<goarch>
# paths the cross-compiler writes. EXE_ARCHES is the uniform public arch label used
# in every artifact name (see the naming standard below); the package scripts map it
# back to GOARCH internally.
WIN_ARCHES := amd64 arm64
EXE_ARCHES := x86_64 aarch64

# ARCH NAMING STANDARD: every artifact name uses x86_64 (Intel/AMD) and aarch64
# (ARM) -- rpm, Windows installers, and the macOS app/dmg. The ONE exception is
# the .deb, whose Architecture field dpkg mandates as amd64/arm64: a deb tagged
# x86_64 is rejected and will not install, so deb keeps its ecosystem's spelling.
# GOARCH (amd64/arm64) still appears in build/bin/<goos>-<goarch> paths only; the
# package scripts map the public label back to GOARCH internally.
#
# The client is CGO (Fyne X11/GL + go-sqlite3). Both Linux arches are produced by
# the build container (see Dockerfile.build).
CLIENT_DEB_ARCHES := amd64 arm64
CLIENT_RPM_ARCHES := x86_64 aarch64

# The gateway is pure Go and cross-compiles to any CPU, so it ships for both.
# deb and rpm spell the architectures differently (amd64/arm64 vs x86_64/aarch64).
SERVER_DEB_ARCHES := amd64 arm64
SERVER_RPM_ARCHES := x86_64 aarch64

LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(GIT_COMMIT)
GOFLAGS_BUILD := -trimpath -ldflags '$(LDFLAGS)'

# The icon generator is being written concurrently. Until tools/icongen has
# sources, `icons` skips with a warning and packaging scripts are allowed to
# proceed without icons. As soon as the generator exists, icons become
# mandatory and a missing .icns/.ico/.png fails the package build.
HAVE_ICONGEN := $(shell ls tools/icongen/*.go >/dev/null 2>&1 && echo 1)
ifeq ($(HAVE_ICONGEN),1)
ALLOW_MISSING_ICONS := 0
else
ALLOW_MISSING_ICONS := 1
endif

# Exported for every script invoked below - single source of truth.
export BUILD_DIR
export VERSION
export GIT_COMMIT
export ALLOW_MISSING_ICONS

.PHONY: all build icons test vet fmt lint docker-build docker-package \
        mac-app-x86_64 mac-app-aarch64 dmg-x86_64 dmg-aarch64 deb rpm exe package release clean help dirs \
        server-deb server-rpm server-exe server-package server-test \
        mac-server-app server-docker

all: build

## dirs: create the build tree
dirs:
	@mkdir -p $(BIN_DIR) $(ICON_DIR) $(STAGING_DIR) $(DIST_DIR)

## build: compile both binaries for the host into build/bin/<goos>-<goarch>
build: dirs
	@echo ">> building host binaries into $(HOST_BIN_DIR)"
	@mkdir -p $(HOST_BIN_DIR)
	go build $(GOFLAGS_BUILD) -o $(HOST_BIN_DIR)/$(GUI_BIN) ./cmd/task-timer
	go build $(GOFLAGS_BUILD) -o $(HOST_BIN_DIR)/$(DAEMON_BIN) ./cmd/task-timer-daemon
	@echo ">> $(HOST_BIN_DIR)/$(GUI_BIN)"
	@echo ">> $(HOST_BIN_DIR)/$(DAEMON_BIN)"

## icons: generate build/icons/{TaskTimer.icns,TaskTimer.ico,png/icon_<N>.png}
icons: dirs
ifeq ($(HAVE_ICONGEN),1)
	@echo ">> generating icons into $(ICON_DIR)"
	go run ./tools/icongen
else
	@echo ">> WARNING: tools/icongen not present yet - skipping icon generation."
	@echo ">>          Packages will be built without icons until it lands."
endif

## test: run the Go test suite
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: gofmt the whole module
fmt:
	gofmt -l -w ./cmd ./internal ./tools

## lint: run golangci-lint if installed
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed - skipping"; \
	fi

## docker-build: cross-compile linux/arm64 + windows/{amd64,arm64} into build/bin
docker-build: dirs
	@if [ "$(DOCKER)" = "podman" ]; then podman system prune -f >/dev/null 2>&1 || true; fi
	$(DOCKER) build --pull -f Dockerfile.build --build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) -t $(IMAGE_TAG) .
	$(DOCKER) run --rm -v $(ABS_BUILD)/bin:/out $(IMAGE_TAG)
	@for a in arm64 amd64; do \
		test -f $(BIN_DIR)/linux-$$a/task-timer || { echo "missing $(BIN_DIR)/linux-$$a/task-timer" >&2; exit 1; }; \
		test -f $(BIN_DIR)/linux-$$a/task-timer-daemon || { echo "missing $(BIN_DIR)/linux-$$a/task-timer-daemon" >&2; exit 1; }; \
	done
	@for a in $(WIN_ARCHES); do \
		test -f $(BIN_DIR)/windows-$$a/task-timer.exe || { echo "missing $(BIN_DIR)/windows-$$a/task-timer.exe" >&2; exit 1; }; \
		test -f $(BIN_DIR)/windows-$$a/task-timer-daemon.exe || { echo "missing $(BIN_DIR)/windows-$$a/task-timer-daemon.exe" >&2; exit 1; }; \
	done

## mac-app-x86_64: assemble build/dist/TaskTimer-x86_64.app, Intel/AMD (macOS host only)
mac-app-x86_64: dirs icons
	@if [ "$(UNAME)" != "Darwin" ]; then echo "mac-app-x86_64 requires a macOS host - skipping"; exit 0; fi
	./scripts/mac-app.sh x86_64

## mac-app-aarch64: assemble build/dist/TaskTimer-aarch64.app, ARM (macOS host only)
mac-app-aarch64: dirs icons
	@if [ "$(UNAME)" != "Darwin" ]; then echo "mac-app-aarch64 requires a macOS host - skipping"; exit 0; fi
	./scripts/mac-app.sh aarch64

## dmg-x86_64: build build/dist/TaskTimer-x86_64.dmg, Intel/AMD (macOS host only)
dmg-x86_64: mac-app-x86_64
	@if [ "$(UNAME)" != "Darwin" ]; then echo "dmg-x86_64 requires a macOS host - skipping"; exit 0; fi
	./pkg/dmg.sh x86_64

## dmg-aarch64: build build/dist/TaskTimer-aarch64.dmg, ARM (macOS host only)
dmg-aarch64: mac-app-aarch64
	@if [ "$(UNAME)" != "Darwin" ]; then echo "dmg-aarch64 requires a macOS host - skipping"; exit 0; fi
	./pkg/dmg.sh aarch64

## deb: build the .deb for every arch into build/dist (runs in the build container)
deb: docker-build icons
	$(DOCKER) run --rm -v $(ROOT_DIR):/src -w /src \
		-e BUILD_DIR=$(BUILD_DIR) -e VERSION=$(VERSION) \
		-e ALLOW_MISSING_ICONS=$(ALLOW_MISSING_ICONS) \
		$(IMAGE_TAG) /bin/sh -c 'set -eu; for a in $(CLIENT_DEB_ARCHES); do ./pkg/package-deb.sh "$$a"; done'

## rpm: build the .rpm for every arch into build/dist (runs in the build container)
rpm: docker-build icons
	$(DOCKER) run --rm -v $(ROOT_DIR):/src -w /src \
		-e BUILD_DIR=$(BUILD_DIR) -e VERSION=$(VERSION) \
		-e ALLOW_MISSING_ICONS=$(ALLOW_MISSING_ICONS) \
		$(IMAGE_TAG) /bin/sh -c 'set -eu; for a in $(CLIENT_RPM_ARCHES); do ./pkg/package-rpm.sh "$$a"; done'

## exe: build the Windows installers (one per arch) into build/dist
exe: docker-build icons
	$(DOCKER) run --rm -v $(ROOT_DIR):/src -w /src \
		-e BUILD_DIR=$(BUILD_DIR) -e VERSION=$(VERSION) \
		-e ALLOW_MISSING_ICONS=$(ALLOW_MISSING_ICONS) \
		$(IMAGE_TAG) /bin/sh -c 'set -eu; for a in $(EXE_ARCHES); do ./pkg/package-exe.sh "$$a"; done'

## docker-package: deb + rpm + exe (all arches) in one container run
docker-package: docker-build icons
	$(DOCKER) run --rm -v $(ROOT_DIR):/src -w /src \
		-e BUILD_DIR=$(BUILD_DIR) -e VERSION=$(VERSION) \
		-e ALLOW_MISSING_ICONS=$(ALLOW_MISSING_ICONS) \
		$(IMAGE_TAG) /bin/sh -c 'set -eu; \
			for a in $(CLIENT_DEB_ARCHES); do ./pkg/package-deb.sh "$$a"; done; \
			for a in $(CLIENT_RPM_ARCHES); do ./pkg/package-rpm.sh "$$a"; done; \
			for a in $(EXE_ARCHES); do ./pkg/package-exe.sh "$$a"; done'

# ---------------------------------------------------------------------------
# The server. A separate package from the client, built from server/ rather than
# from any Go source, and installed on a different machine by a different person.
# ---------------------------------------------------------------------------

## server-test: run the gateway's Go test suite
server-test:
	cd server && go test ./...

## server-deb: build the gateway .deb for every arch into build/dist (build container)
server-deb: dirs
	$(DOCKER) build --pull -f Dockerfile.build --build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) -t $(IMAGE_TAG) .
	$(DOCKER) run --rm -v $(ROOT_DIR):/src -w /src \
		-e BUILD_DIR=$(BUILD_DIR) -e VERSION=$(VERSION) \
		$(IMAGE_TAG) /bin/sh -c 'set -eu; for a in $(SERVER_DEB_ARCHES); do ./pkg/package-server-deb.sh "$$a"; done'

## server-rpm: build the gateway .rpm for every arch into build/dist (build container)
server-rpm: dirs
	$(DOCKER) build --pull -f Dockerfile.build --build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) -t $(IMAGE_TAG) .
	$(DOCKER) run --rm -v $(ROOT_DIR):/src -w /src \
		-e BUILD_DIR=$(BUILD_DIR) -e VERSION=$(VERSION) \
		$(IMAGE_TAG) /bin/sh -c 'set -eu; for a in $(SERVER_RPM_ARCHES); do ./pkg/package-server-rpm.sh "$$a"; done'

## server-exe: build the gateway Windows installers (per arch) into build/dist (build container)
server-exe: dirs
	$(DOCKER) build --pull -f Dockerfile.build --build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) -t $(IMAGE_TAG) .
	$(DOCKER) run --rm -v $(ROOT_DIR):/src -w /src \
		-e BUILD_DIR=$(BUILD_DIR) -e VERSION=$(VERSION) -e ALLOW_MISSING_ICONS=$(ALLOW_MISSING_ICONS) \
		$(IMAGE_TAG) /bin/sh -c 'set -eu; for a in $(EXE_ARCHES); do ./pkg/package-server-exe.sh "$$a"; done'

## mac-server-app: assemble build/dist/TaskTimerServer.app (macOS host only)
mac-server-app: dirs
	@if [ "$(UNAME)" != "Darwin" ]; then echo "mac-server-app requires a macOS host - skipping"; exit 0; fi
	./scripts/mac-server-app.sh

## server-docker: build a container that installs the gateway from its .deb and runs it
server-docker:
	$(DOCKER) build -f Dockerfile.server -t task-timer-server:$(VERSION) --build-arg VERSION=$(VERSION) .
	@echo ">> built image task-timer-server:$(VERSION)"
	@echo ">> run it:  $(DOCKER) run --rm -p 8080:8080 task-timer-server:$(VERSION)"

## server-package: the gateway's deb + rpm + Windows installers, every arch, in one container run
server-package: dirs
	$(DOCKER) build --pull -f Dockerfile.build --build-arg VERSION=$(VERSION) --build-arg GIT_COMMIT=$(GIT_COMMIT) -t $(IMAGE_TAG) .
	$(DOCKER) run --rm -v $(ROOT_DIR):/src -w /src \
		-e BUILD_DIR=$(BUILD_DIR) -e VERSION=$(VERSION) -e ALLOW_MISSING_ICONS=$(ALLOW_MISSING_ICONS) \
		$(IMAGE_TAG) /bin/sh -c 'set -eu; \
			for a in $(SERVER_DEB_ARCHES); do ./pkg/package-server-deb.sh "$$a"; done; \
			for a in $(SERVER_RPM_ARCHES); do ./pkg/package-server-rpm.sh "$$a"; done; \
			for a in $(EXE_ARCHES); do ./pkg/package-server-exe.sh "$$a"; done'

## package: every package this host can produce, client and server
package: docker-package server-package $(DMG_TARGETS) $(SERVER_MAC_TARGET)

## release: test + vet + build everything, then list the artifacts
release: test vet build package
	@echo ""
	@echo "=========================================================="
	@echo " Release artifacts ($(VERSION), $(GIT_COMMIT))"
	@echo " $(ABS_BUILD)/dist"
	@echo "=========================================================="
	@if [ -d "$(DIST_DIR)" ] && [ -n "$$(ls -A $(DIST_DIR) 2>/dev/null)" ]; then \
		for f in $(DIST_DIR)/*; do \
			printf '  %s\n' "$(ROOT_DIR)/$$f"; \
		done; \
	else \
		echo "  (none)"; \
	fi
	@echo ""

## clean: remove build/ plus every legacy dropping from the old build system
clean:
	rm -rf $(BUILD_DIR)
	rm -rf out dist
	rm -f TaskTimer.dmg task-timer-windows-amd64.exe task-timer-linux-arm64
	@# the old make-mac-app.sh left a dangling tasks.db symlink in the repo
	@# root; var/tasks.db is REAL USER DATA and is never touched.
	@if [ -L tasks.db ]; then rm -f tasks.db; fi
	@echo ">> cleaned $(BUILD_DIR)/ and all legacy artifacts (var/tasks.db preserved)"

## help: print this list
help:
	@echo "Task Timer - make targets (everything is written under ./$(BUILD_DIR))"
	@echo ""
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed -e 's/^## //' | awk -F': ' '{printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Layout:"
	@echo "  $(BIN_DIR)/<goos>-<goarch>/   compiled binaries"
	@echo "  $(ICON_DIR)/                  generated icons"
	@echo "  $(STAGING_DIR)/<platform>/    scratch (never /tmp)"
	@echo "  $(DIST_DIR)/                  final artifacts"
