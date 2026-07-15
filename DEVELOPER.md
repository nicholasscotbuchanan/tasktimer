# Developer Guide

This is the guide for working *on* Task Timer, rather than using it. If you only
want to build and run the app, the [README](README.md) is the place to start;
come here when you want to understand how the pieces fit, add a sync provider, or
change the interface with confidence.

**Task Timer Server** — the backend that talks to Jira — is a separate program
with its own architecture. Its internals are documented in [SERVER.md](SERVER.md);
this guide covers Task Timer, the desktop client, and its background daemon.

---

## Contents

- [The shape of the project](#the-shape-of-the-project)
- [The two client binaries](#the-two-client-binaries)
- [Package layout](#package-layout)
- [How the data flows](#how-the-data-flows)
- [The database](#the-database)
- [The provider plugin system](#the-provider-plugin-system)
- [The user interface](#the-user-interface)
- [Secrets and where they live](#secrets-and-where-they-live)
- [Testing](#testing)
- [Build, format, lint](#build-format-lint)
- [Conventions worth knowing](#conventions-worth-knowing)
- [Adding a sync provider, end to end](#adding-a-sync-provider-end-to-end)

---

## The shape of the project

There are **two independent Go modules** in this repository, built and shipped
separately:

| Module | Path | What it is |
| --- | --- | --- |
| `task-timer-app` | repository root | The desktop client and the sync daemon. Requires cgo (SQLite + Fyne). |
| `task-timer-server` | `server/` | Task Timer Server. Pure Go, no cgo, cross-compiles anywhere. |

They are deliberately decoupled. The client and the server share **no Go code** —
they agree only on an HTTP contract (the JSON Task Timer sends and Task Timer
Server accepts). You can work on one without building the other, and the two
are installed on different machines by different people.

This guide is about the root module. The `server/` module has its own
[SERVER.md](SERVER.md).

---

## The two client binaries

The root module builds two programs that share one SQLite database:

| Program | Entry point | Role |
| --- | --- | --- |
| `task-timer` | [cmd/task-timer/main.go](cmd/task-timer/main.go) | The GUI: timer, task table, reports, settings. |
| `task-timer-sync` | [cmd/task-timer-sync/main.go](cmd/task-timer-sync/main.go) | A headless daemon that pulls remote tasks and pushes logged time. |

Both `main` packages are **wiring only** — they open the store, blank-import the
compiled-in providers, and hand off. All behaviour lives in `internal/`. The two
`main.go` files are the *only* place in the client that names a concrete provider
(see [The provider plugin system](#the-provider-plugin-system)).

The GUI writes sessions to the database and never touches the network on its own.
Nothing is pulled or pushed until `task-timer-sync` runs. That separation is the
whole reason there are two binaries: the timer stays useful and entirely local
even when the daemon is not installed or the backend is down.

---

## Package layout

```text
cmd/
  task-timer/          GUI entry point (wiring)
  task-timer-sync/     daemon entry point (wiring)

internal/
  task/                domain model + SQLite store + reporting aggregation
    task.go            Task and Remote types, the domain vocabulary
    store.go           schema, migrations, every query
    stats.go           pure aggregation for the Reports page
  sync/                the sync engine and the provider contract
    engine.go          the pull → push → complete cycle
    provider.go        the Provider interface and the registry
    config.go          sync.json load/save (atomic writes)
    env.go             sync.env load/save (the token file)
    connect.go         the interactive sign-in dispatch
    providers/
      gateway/         talks to Task Timer Server (bearer token + PKCE)
      jsonfile/        reads/writes a directory of JSON files
  ui/                  the entire Fyne interface
    app.go             the App: state, the timer loop, page routing
    dashboard.go       Overview page (the live timer)
    tasks.go           full session history
    reports.go         charts and breakdowns
    settings.go        working day + provider forms (built from the registry)
    about.go           build info and data locations
    components.go      shared widgets (cards, buttons, meters, insets)
    table.go           the session table
    theme.go           colours, fonts, the Fyne theme
    connect.go         the sign-in dialog
    format.go icons.go  formatting helpers and vector icons
  assets/              embedded icon and fonts (go:embed)

tools/icongen/         generates .icns/.ico/.png from icon.svg
pkg/ scripts/          packaging (dmg, deb, rpm, Windows installer, launchd)
```

The one architectural rule that matters: **`internal/ui` must never import a
provider package.** It is enforced by a test (see [Testing](#testing)). Everything
else follows from it.

---

## How the data flows

A single work session travels a well-defined path. Understanding it explains most
of the code.

**1. The user times something (GUI).**
`App.Start(name)` records a start time and kicks off the display ticker
([internal/ui/app.go](internal/ui/app.go)). `App.Stop` writes a row to the `tasks`
table with status `Work Logged`. If the task name matches a remote task that was
pulled earlier, the row also carries that task's `foreign_key` and `foreign_url`,
which is what later lets the daemon push time against the right upstream issue.

**2. The daemon pulls remote tasks (sync).**
`Engine.pull` asks each enabled provider for tasks changed since the last cursor
and upserts them into `remote_tasks` ([internal/sync/engine.go](internal/sync/engine.go)).
The GUI reads that table to populate the task picker, so assigned issues show up
as timer targets.

**3. The daemon pushes logged time (sync).**
`Engine.push` finds sessions in `Work Logged` state that have a `foreign_key`,
calls `Provider.Push`, and stamps the returned work-log id onto the row. That
stamp is what makes pushing idempotent: a session with a signature is never pushed
again, even across daemon restarts.

**4. The daemon reports completion (sync).**
If the user marked a task done, `Engine.complete` calls `Provider.Complete` and
records that the provider was told.

The cycle is **pull → push → complete**, per provider, on a timer. A provider that
fails is logged and skipped; it never stops the others or kills the daemon,
because a backend outage should not take down a local stopwatch.

```text
  ┌────────────┐   writes sessions    ┌─────────────┐
  │  task-timer │ ───────────────────▶ │  tasks.db    │
  │    (GUI)    │ ◀─────────────────── │  (SQLite)    │
  └────────────┘   reads sessions +    └─────────────┘
                   remote tasks              ▲  │
                                             │  │ pull → push → complete
                                    reads/   │  ▼
                                   writes ┌──────────────────┐
                                          │ task-timer-sync   │
                                          │    (daemon)       │
                                          └──────────────────┘
                                                   │  Provider.Pull/Push/Complete
                                                   ▼
                                          ┌──────────────────┐
                                          │ gateway / jsonfile │
                                          └──────────────────┘
```

---

## The database

One SQLite file, opened by both binaries at once, defined in one place:
[internal/task/store.go](internal/task/store.go).

- **`tasks`** — one row per timed session. The interesting columns are
  `task_status` (the lifecycle: `Work Logged` → `Syncing` → `Synchronized
  Progress`/`Complete`), `foreign_key`/`foreign_url` (the upstream issue), and
  `timer_sync_signature` (the provider's work-log id — the idempotency anchor).
- **`remote_tasks`** — tasks pulled from providers, keyed by `(provider, key)`.
  These are timer *targets*, not sessions.
- **`sync_state`** — one `last_pull` cursor per provider.

Concurrency is real, not theoretical: the GUI and the daemon write the same file
simultaneously. The store opens with `WAL` journalling and a 5-second busy timeout
so a concurrent write waits rather than failing. Schema lives in the `schema`
constant; column additions go through `migrate()`, and indexes are created
*after* migration (an index over a column that a not-yet-migrated database lacks
would fail `Open` before the migration could add it — a real upgrade bug the
comments call out).

The store is the only package that writes SQL. `stats.go` beside it is pure
aggregation over already-loaded rows, kept separate so the Reports page is unit
testable without a database.

---

## The provider plugin system

This is the design centrepiece, and worth reading
[internal/sync/provider.go](internal/sync/provider.go) in full.

A sync backend is anything that satisfies `sync.Provider`:

```go
type Provider interface {
    Name() string
    Pull(ctx, since) ([]task.Remote, error)
    Push(ctx, WorkLog) (string, error)
    Complete(ctx, key) error
}
```

`Pull` and `Push` are independent capabilities. A read-only backend returns
`sync.ErrUnsupported` from `Push` and `Complete`, and the engine skips that half
of the cycle rather than treating it as an error.

Providers **register themselves** from `init()` and are pulled into a binary by a
blank import. The registry (`Register`, `Descriptors`, `Describe`) is what lets
the rest of the system stay ignorant of any specific backend:

- The daemon builds only the providers the config enables.
- `sync.json`'s starter file is generated from the registry, so a new provider
  appears in it automatically.
- **The Settings page renders a form for a provider it has never heard of**, by
  walking `Descriptors()` and reading each provider's declared `Fields`.

That last point is the reason for the `Field` type. Without it, every new backend
would mean editing the settings screen — and a plugin system whose host must be
modified for each plugin is not a plugin system. `TestAppDoesNotDependOnAnyProvider`
in [internal/sync/architecture_test.go](internal/sync/architecture_test.go) fails
the build if `internal/ui`, the engine, or the store ever grows an import of a
provider package.

The two shipped providers:

- **`gateway`** ([internal/sync/providers/gateway](internal/sync/providers/gateway)) —
  the real one. Holds a bearer token, syncs through Task Timer Server, and
  implements the interactive `Connect` flow (loopback + PKCE, see
  [Secrets](#secrets-and-where-they-live)).
- **`jsonfile`** ([internal/sync/providers/jsonfile](internal/sync/providers/jsonfile)) —
  reads task definitions from `<dir>/tasks/*.json` and writes work logs to
  `<dir>/worklogs/*.json`. No network, no credentials — it is how you exercise the
  whole sync path in a test or a demo.

[Adding a provider](#adding-a-sync-provider-end-to-end) walks through writing one.

---

## The user interface

The GUI is [Fyne](https://fyne.io/) v2. The whole interface lives in
`internal/ui`, organised as one `App` plus five pages.

- **`App`** ([internal/ui/app.go](internal/ui/app.go)) owns the state: the running
  timer, the cached sessions and remote tasks, the window, and the page router.
  It runs the display ticker in a goroutine and marshals every UI mutation back
  onto Fyne's main thread. Pages ask the `App` to act (`App.Start`, `App.Stop`,
  `App.Synchronize`); the `App` calls back into pages via small hooks
  (`timerStarted`, `refresh`).
- Each **page** is a struct with a `content fyne.CanvasObject` and a `refresh()`
  method. The router shows a page's content and calls `refresh()` on arrival, so a
  page never queries the store while it is still being constructed.
- **`components.go`** is the shared vocabulary: `card`, `iconButton`, `meter`,
  `inset`/`insetXY`, `muted`, `sized`. Pages are assembled from these, which is
  why they read as declarative layout rather than pixel-pushing.
- **`theme.go`** defines the palette (`colText`, `colPrimary`, `colAccent`,
  `colDanger`, …) and wires the embedded Inter font into a Fyne theme.

A recurring Fyne gotcha the code guards against everywhere: `widget.Select`'s
`SetSelected` fires `OnChanged`. So pages set the initial selection *before*
attaching the callback — otherwise a refresh runs against a page whose table does
not exist yet, and dereferences nil. If you add a `Select`, follow the pattern.

---

## Secrets and where they live

The client holds exactly one secret: the **bearer token** for Task Timer Server.
It never holds a Jira credential — that lives on the server.

Two files, both in the [data directory](README.md#where-your-data-lives):

- **`sync.json`** — the provider config. No secret belongs here, because it is the
  file people paste into support tickets. Written atomically (temp file + rename,
  mode 0600) so a crash mid-save cannot leave a half-written config the daemon
  refuses to parse ([internal/sync/config.go](internal/sync/config.go)).
- **`sync.env`** — `KEY=VALUE` lines, mode 0600, where the bearer token actually
  lives under the variable named by `api_token_env`
  ([internal/sync/env.go](internal/sync/env.go)). A daemon under launchd/systemd
  inherits none of your shell's exports, so the token must be on disk somewhere
  the daemon reads at startup — this is that somewhere. The loader logs variable
  *names* only, never values, and warns if the file is world-readable.

The sign-in flow (`gateway.Connect`,
[internal/sync/providers/gateway/connect.go](internal/sync/providers/gateway/connect.go))
is OAuth for a native app (RFC 8252): open a listener on `127.0.0.1:0`, send the
browser to Task Timer Server, receive a one-time code on the loopback port, and trade it
for the token over TLS proving possession of a PKCE verifier. The token
deliberately never rides in a redirect URL — URLs end up in history and proxy
logs. `state` is checked in constant time before the code is touched.

The rule the whole scheme rests on: a `Field` a provider does not *declare* is
never rendered by the Settings form and is **preserved untouched** when the form
saves. That is how the `gateway` provider keeps an optional inline `api_token`
editable by hand while keeping it off the screen.

---

## Testing

```bash
make test          # go test ./...
cd server && go test ./...   # or: make server-test
```

Beyond ordinary unit tests, two are worth knowing about:

**The render test** ([internal/ui/render_test.go](internal/ui/render_test.go))
builds the entire interface on a headless Fyne canvas and lays out all five pages.
It catches two classes of bug a compiler cannot: a page that panics while
constructing itself, and a layout that overflows the window (in Fyne, that
produces a window the user physically cannot shrink). It also doubles as the
screenshot tool:

```bash
TASK_TIMER_SHOTS=/tmp/shots go test ./internal/ui -run TestRenderPages
open /tmp/shots     # xdg-open on Linux
```

The screenshots in the README were produced exactly this way. It runs headless,
so it works over SSH and in CI where no display exists.

**The architecture test**
([internal/sync/architecture_test.go](internal/sync/architecture_test.go)) walks
the import graph and fails if the app, the engine, or the store depends on any
provider package. It is the mechanical guarantee behind the plugin system: the
first time someone shortcuts the registry and imports `gateway` directly from the
UI, this test goes red.

`stats.go` is deliberately database-free so the reporting logic is tested as pure
functions ([internal/task/stats_test.go](internal/task/stats_test.go)).

---

## Build, format, lint

Everything the build produces lands under `build/` — nothing is written to the
repo root or `/tmp`. `make help` lists every target; the ones you'll use daily:

```bash
make build    # compile both binaries into build/bin/<goos>-<goarch>/
make test     # go test ./...
make vet      # go vet ./...
make fmt      # gofmt -w over cmd/ internal/ tools/
make lint     # golangci-lint if installed (skipped with a message otherwise)
make icons    # regenerate icons from internal/assets/icon.svg
```

`golangci-lint` is configured by [.golangci.yml](.golangci.yml) but not bundled:

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

**Dependencies are vendored.** Both modules commit the full source of their
dependencies under `vendor/` (and `server/vendor/`), so `go build`/`go test` run
against the vendored tree with no module download — builds work with the network
off. After changing `go.mod`, re-run `go mod vendor` (and `cd server && go mod
vendor`) and commit the result so the vendored tree stays in step.

cgo is **mandatory** for the client — `go-sqlite3` compiles SQLite and Fyne binds
OpenGL/X11 — so `CGO_ENABLED=0` will not build it. That is the one prerequisite
vendoring cannot remove: a native GUI is compiled C, not vendorable Go source. The
C compiler and, on Linux, the X11/GL dev headers are in the
[README](README.md#prerequisites). The server module is pure Go and has none of
these constraints.

---

## Conventions worth knowing

- **`main` is wiring.** Behaviour goes in `internal/`. The two client `main.go`
  files exist to open the store and blank-import providers.
- **Only `main` names a provider.** If you find yourself importing `gateway` from
  anywhere in `internal/ui`, stop — you are about to break the plugin system, and
  the architecture test will tell you so.
- **Secrets go to `sync.env`, config goes to `sync.json`.** Nothing that would
  hurt in a support ticket belongs in the config file.
- **Config and token writes are atomic** (temp file + rename, mode 0600). Follow
  that pattern for anything else that must survive a crash mid-write.
- **The store owns all SQL.** Aggregation and formatting are pure functions
  elsewhere, so they can be tested without a database and reused by any page.
- **Set a `Select`'s value before attaching `OnChanged`.** `SetSelected` fires the
  callback; a refresh before the page is fully built is a nil dereference.
- **Build metadata is injected, not hard-coded.** The Makefile stamps
  `main.version`/`main.commit` via `-ldflags`, and the UI reads them back for the
  About page.

---

## Adding a sync provider, end to end

Say you want a `linear` provider for Linear issues.

**1. Implement `sync.Provider`** in a new package under
`internal/sync/providers/linear/`. Implement `Pull` and/or `Push`; return
`sync.ErrUnsupported` from whichever half your backend cannot do.

**2. Register it from `init()`, declaring your settings as `Fields`:**

```go
func init() {
    tsync.Register(tsync.Registration{
        Name:    "linear",
        Title:   "Linear",
        Summary: "Pull assigned Linear issues and push time back.",
        New:     New, // func(json.RawMessage) (tsync.Provider, error)
        Fields: []tsync.Field{
            {
                Key:     "api_key_env",
                Label:   "API key variable",
                Hint:    "The name of an environment variable holding the key.",
                Kind:    tsync.KindText,
                Default: "LINEAR_API_KEY",
            },
            {
                Key:     "push_time",
                Label:   "Time tracking",
                Kind:    tsync.KindBool,
                Default: true,
            },
        },
    })
}
```

**3. Add one blank import** to both
[cmd/task-timer-sync/main.go](cmd/task-timer-sync/main.go) and
[cmd/task-timer/main.go](cmd/task-timer/main.go):

```go
_ "task-timer-app/internal/sync/providers/linear"
```

That is the whole job. Because you declared `Fields`, you now get for free:

- the provider listed by `task-timer-sync -providers`,
- an entry in the starter `sync.json` with your defaults filled in,
- **a Settings form** — a card, an enable toggle, and the right control per field —
  with no change to `internal/ui`.

**Secrets:** declare the *name of an environment variable*, never the secret
itself. An undeclared field is never rendered and is preserved when the form
saves, which is exactly how the `gateway` provider supports an inline `api_token`
for hand-editing while keeping it off the screen.

If your backend needs interactive sign-in (like the `gateway` provider's OAuth
flow), set `URLField`, `Connect`, and `HasToken` on the `Registration` — the app
then offers a **Log in** button and pre-fills the URL, again without importing
your package. The `gateway` provider is the worked example to copy.
