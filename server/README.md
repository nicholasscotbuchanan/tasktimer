# Task Timer Server (the gateway)

The **server** side of Task Timer. Desktop clients push their timed work sessions
here over a bearer token, and this service writes them to Jira — each user acting
through their **own** Atlassian OAuth 2.0 (3LO) grant, so work logs are authored
in Jira by the person who did the work, not by a shared robot account.

This is a separate program from the desktop app, installed on a different machine
by a different person. The desktop client holds no Jira credentials; everything
enterprise lives here. See the [top-level README](../README.md) for the client.

It is a **single static Go binary** — pure Go (`modernc.org/sqlite`, no CGO) — so
it cross-compiles to any OS/CPU with nothing but `GOOS`/`GOARCH`, which is what
makes the packages below cheap to produce.

---

## Run it from source

```bash
cd server
go run ./cmd/task-timer-server            # start the gateway
go run ./cmd/task-timer-server gen-key    # print a fresh AES-256 encryption key
go run ./cmd/task-timer-server version    # print the build version
```

## Configure it

Settings resolve from, in ascending order of precedence:

1. the **config file** (TOML) at the per-platform default path, or wherever
   `TASK_TIMER_SERVER_CONFIG` points;
2. **environment variables** prefixed `TASK_TIMER_SERVER_` (e.g.
   `TASK_TIMER_SERVER_PUBLIC_URL`, `TASK_TIMER_SERVER_ATLASSIAN_CLIENT_ID`);
3. the **credential directory**, for the two secrets that must never sit in a
   shared config file.

The two secrets — the Atlassian **client secret** and the **token-encryption
key** — have no defaults on purpose. Generate the encryption key **once** and
never regenerate it: it decrypts the stored Jira refresh tokens, and a new key
silently invalidates every one of them, forcing every user to reconnect.

`config.example.toml` is the annotated starting point.

### Where things live, per platform

| | Config file | Secrets (credential dir) | Database |
| --- | --- | --- | --- |
| **Linux** | `/etc/task-timer-server/config.toml` | systemd credentials (`LoadCredential=`) | `/var/lib/task-timer-server/server.db` |
| **macOS** | `~/Library/Application Support/TaskTimerServer/config.toml` | same directory (`atlassian_client_secret`, `token_encryption_key` files) | set `database_url` in config |
| **Windows** | `%ProgramData%\TaskTimerServer\config.toml` | same directory (two secret files) | set `database_url` in config |

The two secret files, when read from the credential directory, are named
`atlassian_client_secret` and `token_encryption_key`. On Linux they are passed as
systemd credentials instead; env vars work everywhere as a fallback.

### Atlassian app

Register an OAuth 2.0 (3LO) app at <https://developer.atlassian.com>:

- **Callback:** `<public_url>/auth/callback`
- **Scopes:** `read:me read:jira-work write:jira-work offline_access`
  (`offline_access` is required — without it there is no refresh token and every
  user is asked to reconsent every hour).

---

## Packages

Everything lands under `build/dist/`. Run these from the repository root.

| Platform | Command | Produces |
| --- | --- | --- |
| **Debian/Ubuntu** | `make server-deb` | `task-timer-server_<v>_{amd64,arm64}.deb` — systemd unit |
| **RHEL/Fedora** | `make server-rpm` | `task-timer-server-<v>.{x86_64,aarch64}.rpm` — systemd unit |
| **Windows** | `make server-exe` | `task-timer-server-installer-{amd64,arm64}.exe` — registers a Windows service |
| **macOS** | `make mac-server-app` | `TaskTimerServer.app` — headless LaunchAgent |
| **Docker** | `make server-docker` | an image that installs the `.deb` and runs the server |
| everything | `make server-package` | deb + rpm + Windows installers in one container run |

The `.deb`/`.rpm`/Windows installers cross-compile inside the build container
(`Dockerfile.build`), which carries the packaging toolchains (`dpkg-deb`, `rpm`,
`makensis`). The macOS `.app` builds natively on a Mac host.

### macOS `.app`

`TaskTimerServer.app` runs the gateway with a **menu bar (status bar) item** — an
`LSUIElement` agent, so it lives in the menu bar with no Dock icon and no window.
The menu shows the address to configure it at (the machine's LAN IP and the
configured port, e.g. `http://192.168.1.42:8080`), with **Open in Browser**,
**Copy Address**, and **Quit**. That is the one build of the server that uses CGO
(the menu bar needs AppKit, via `fyne.io/systray` behind the `tray` build tag);
every other target stays headless and pure Go.

```bash
open TaskTimerServer.app                                   # seeds a starter config, shows the menu bar item
TaskTimerServer.app/Contents/Resources/server-agent.sh install   # run at every login (LaunchAgent)
TaskTimerServer.app/Contents/Resources/server-agent.sh status
TaskTimerServer.app/Contents/Resources/server-agent.sh uninstall
```

Config and secrets live in `~/Library/Application Support/TaskTimerServer/`.

### Windows service

The installer drops `task-timer-server.exe` in `Program Files`, writes a starter
config to `%ProgramData%\TaskTimerServer\`, and registers the **`TaskTimerServer`**
Windows service. It does not start it — put `config.toml` and the two secret
files in `%ProgramData%\TaskTimerServer\` first, then `sc start TaskTimerServer`.
The binary manages its own service:

```text
task-timer-server service install | start | stop | uninstall
```

### Docker (install and start from the `.deb`)

```bash
make server-docker
docker run --rm -p 8080:8080 task-timer-server:1.0.0
```

The image builds the `.deb`, installs it into a clean Debian image (so the real
package `postinst`, service user, and config all land), then runs the binary
directly — a container has no systemd. With no `-e` flags it starts with
placeholder Atlassian credentials and an **ephemeral** encryption key so it boots
and listens for smoke-testing; pass real `TASK_TIMER_SERVER_*` values for a
working OAuth flow. See [`pkg/docker-server-entrypoint.sh`](../pkg/docker-server-entrypoint.sh).

---

## Test

```bash
cd server && go test ./...     # or: make server-test
```
