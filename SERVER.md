# Task Timer Server

This is the deep guide to **Task Timer Server** — the backend that stands between
Task Timer clients and Jira. It covers how it is built, how the authentication
flow works, the HTTP contract it exposes, the security model, and how to run and
operate it.

If you just want to stand one up and configure it, the quickstart in
[server/README.md](server/README.md) is shorter and more task-oriented. This
document is the architecture and the *why*. For the client itself, see
[DEVELOPER.md](DEVELOPER.md).

---

## Contents

- [What it is, and what it is not](#what-it-is-and-what-it-is-not)
- [Why a separate program](#why-a-separate-program)
- [Architecture](#architecture)
- [The authentication flow, in full](#the-authentication-flow-in-full)
- [The HTTP API](#the-http-api)
- [Configuration](#configuration)
- [The two secrets](#the-two-secrets)
- [The database](#the-database)
- [Talking to Jira](#talking-to-jira)
- [Security model](#security-model)
- [Running it](#running-it)
- [Operating it](#operating-it)
- [Working on it](#working-on-it)

---

## What it is, and what it is not

Task Timer Server is a small HTTP service. Task Timer clients push their timed
work sessions to it over a bearer key, and it writes them to Jira as work logs —
each user acting through their **own** Atlassian OAuth grant, so the work log is
authored in Jira by the person who did the work, not by a shared robot account.

It **is**: the one place that holds Jira credentials; the one place that decides
which issues a user sees and whether they may close one; a small service backed by
a SQLite database.

It **is not**: a multi-tenant SaaS, a place users log in with a password, or
anything Task Timer depends on to be a useful stopwatch. A team that never sets one
up still gets a fully working local timer.

---

## Why a separate program

The single most important design decision in the whole project: **Task Timer holds
no Jira credentials.** Everything enterprise — the Atlassian OAuth app, the client
secret, the refresh tokens, the JQL that selects issues, the transition that closes
one — lives here, on a machine an administrator controls.

A client that could talk to Jira directly would need either a shared service
account (so every work log is authored by "the robot", and every laptop holds a
credential that can read the whole board) or per-user OAuth secrets scattered
across every client. Task Timer Server replaces both with one bearer key per client
that identifies a user and nothing more. Revoking a laptop is deleting one row.

This is also why Task Timer and Task Timer Server share no Go code. They agree only
on the HTTP contract in [The HTTP API](#the-http-api). You can rewrite either side
without touching the other.

---

## Architecture

A single static Go binary — **pure Go** (`modernc.org/sqlite`, no cgo) — so it
cross-compiles to any OS/CPU with nothing but `GOOS`/`GOARCH`. That is what makes
the `.deb`/`.rpm`/Windows packages cheap to produce. (The one exception is the
macOS `.app`, which uses cgo for its menu-bar item; every other build stays
headless and pure Go.)

```text
server/
  cmd/task-timer-server/
    main.go            entry point + subcommands (gen-key, version, service)
    service_windows.go Windows Service Control Manager integration
    service_other.go   no-op on non-Windows
    tray_darwin.go     macOS menu-bar UI (build tag: tray)
    tray_off.go        no-op tray on every other build
  internal/
    api/               the HTTP surface — routes, auth, handlers
      server.go        routing, the auth flow, tasks + worklogs handlers
      auth.go          bearer-key middleware
      helpers.go       JSON I/O, error mapping, small HTTP utilities
    config/            settings resolution (TOML → env → credentials)
      paths_*.go       per-platform config + credential + database locations
    crypto/            API keys, PKCE verification, AES-256-GCM at rest
    jira/              Atlassian OAuth (oauth.go) + Jira REST v3 (client.go)
    store/             SQLite: users, tokens, keys, pending logins, pushed logs
```

The dependency direction is clean: `main` wires `config`, `crypto`, `store`, and
`api` together; `api` orchestrates `store`, `crypto`, and `jira`; `jira` and
`crypto` depend on nothing internal. Routing uses Go 1.22 method+path patterns
(`GET /api/v1/tasks`, `POST /api/v1/tasks/{issue_key}/complete`) — no web
framework.

---

## The authentication flow, in full

There are **two OAuth legs** that meet in the middle at the server. This is the
part worth understanding before changing anything in `api/server.go` or the
client's `internal/reconcile/providers/gateway/connect.go`.

**Leg 1 — client ⇄ Task Timer Server (native-app OAuth, RFC 8252 + PKCE).**
The client proves possession of a code verifier to the server and walks away with
a long-lived bearer key.

**Leg 2 — Task Timer Server ⇄ Atlassian (OAuth 2.0 3LO).**
Sitting inside leg 1, the server sends the user's browser to Atlassian to consent,
and walks away with a Jira grant it stores encrypted.

The full sequence of a first connection:

```text
 client                   Task Timer Server                  Atlassian
   │  open loopback :N          │                               │
   │  GET /auth/login ─────────▶│                               │
   │   redirect_uri=127.0.0.1:N │  store PendingAuth(state,     │
   │   code_challenge, state    │        code_challenge)        │
   │                            │  302 to Atlassian authorize ─▶│
   │◀───────────────────────────────────────── browser consents │
   │                            │◀── GET /auth/callback?code ────│  (leg 2 code)
   │                            │  exchange code → tokens ──────▶│
   │                            │◀── access + refresh tokens ────│
   │                            │  WhoAmI, FirstJiraSite ───────▶│
   │                            │  check allowed email domain    │
   │                            │  upsert user, encrypt + store  │
   │                            │  tokens, mint one-time code    │
   │◀── 303 to 127.0.0.1:N?code=│  (leg 1 code — NOT the key)   │
   │  POST /api/v1/auth/exchange│                               │
   │   {code, code_verifier} ──▶│  take code, verify PKCE,       │
   │                            │  mint + store hashed API key   │
   │◀── {api_key, user} ────────│                               │
```

Details that matter:

- **The bearer key never travels in a URL.** The `/auth/callback` redirect to the
  client carries a short-lived *code*, not the key. URLs land in browser history,
  proxy logs, and `Referer` headers; the key is handed over only in the TLS body
  of `/api/v1/auth/exchange`.
- **The `code` is one-shot and burned on use.** `exchange` takes-and-deletes the
  code *before* checking the PKCE verifier, so a wrong verifier consumes the code
  rather than leaving it to be brute-forced.
- **PKCE is verified in constant time** (`crypto.PKCEVerify`), and it is the same
  S256 transform on both sides (`base64url(sha256(verifier))`, unpadded).
- **The two login rows are short-lived.** `PendingAuth` lives 10 minutes (long
  enough to read a consent screen); the auth `code` lives 2 minutes (just the hop
  from browser to loopback to exchange).
- **First consent registers the account.** There is no separate sign-up. The
  `allowed_email_domains` list, if set, is the gate: an email outside it is
  refused at callback time.
- **`prompt=consent` is sent to Atlassian on purpose.** Without it, an
  already-consented user comes back with no refresh token and the grant dies in an
  hour. `offline_access` in the scope list is what makes a refresh token exist at
  all.

Once connected, the client sends `Authorization: Bearer tt_…` on every request.
`api/auth.go` hashes the presented key (SHA-256) and looks it up; only the hash is
ever stored.

---

## The HTTP API

The contract the client already speaks. All bodies are JSON. Errors are
`{"detail": "..."}` with a matching status.

| Method + path | Auth | Purpose |
| --- | --- | --- |
| `GET /healthz` | none | Liveness. `{"status":"ok"}`. |
| `GET /auth/login` | none | Start a login. Redirects the browser to Atlassian. Query: `redirect_uri` (must be loopback), `code_challenge`, `state`. |
| `GET /auth/callback` | none | Atlassian's redirect target. Exchanges the Jira code, then bounces a one-time code back to the client's loopback `redirect_uri`. Renders an HTML page on error. |
| `POST /api/v1/auth/exchange` | none | Trade `{code, code_verifier}` for `{api_key, user}`. The only place a key is issued. |
| `GET /api/v1/me` | bearer | The caller's profile and whether Jira is connected. |
| `GET /api/v1/tasks` | bearer | List the user's assigned Jira issues as tasks. Optional `?since=<RFC3339>`. |
| `POST /api/v1/tasks/{issue_key}/complete` | bearer | Transition the issue to done. `403` unless the server allows it. |
| `POST /api/v1/worklogs` | bearer | Push one work log. Idempotent on `idempotency_key`. |

The two write endpoints are the interesting ones:

**`POST /api/v1/worklogs`** takes `{issue_key, started, duration_seconds,
comment, idempotency_key}`. It is idempotent by design because the client retries
on its own schedule and *will* call it twice: a repeat with a seen
`idempotency_key` returns the first push's Jira id and touches Jira not at all
(`"duplicate": true`). Even a race where two retries both pass the initial SELECT
is caught by a unique constraint, and the second is reported as a duplicate rather
than compounded into a third Jira write. `duration_seconds` is rounded up to
Jira's one-minute floor. `started` must be RFC 3339 with an offset.

**`POST /api/v1/tasks/{issue_key}/complete`** is gated twice: the server's
`jira.allow_complete` must be on *and* the client must ask. Writing to a shared
board is opt-in on both sides. When disallowed it is a clear `403`, not a silent
no-op.

---

## Configuration

Settings resolve in ascending order of precedence
([server/internal/config/config.go](server/internal/config/config.go)):

1. the **TOML config file** at the per-platform default path, or wherever
   `TASK_TIMER_SERVER_CONFIG` points;
2. **environment variables** prefixed `TASK_TIMER_SERVER_` (e.g.
   `TASK_TIMER_SERVER_PUBLIC_URL`);
3. the **credential directory**, for the two secrets only.

`config.example.toml` is the annotated starting point. The knobs:

| Setting | TOML | Env suffix | Default | Meaning |
| --- | --- | --- | --- | --- |
| Bind host | `host` | `HOST` | `127.0.0.1` | Interface to listen on. |
| Port | `port` | `PORT` | `8080` | |
| Public URL | `public_url` | `PUBLIC_URL` | `http://127.0.0.1:8080` | The externally reachable base. The Atlassian callback is derived as `<public_url>/auth/callback` and must match the registered URI byte for byte. |
| Database | `database_url` | `DATABASE_URL` | `sqlite:////var/lib/task-timer-server/server.db` | |
| Atlassian client id | `atlassian.client_id` | `ATLASSIAN_CLIENT_ID` | — | Required. |
| Atlassian client secret | `atlassian.client_secret` | `ATLASSIAN_CLIENT_SECRET` | — | **Secret.** Required. |
| Issue query | `jira.jql` | `JIRA_JQL` | `assignee = currentUser() AND statusCategory != Done` | Which issues a user sees. |
| Done transition | `jira.done_transition` | `JIRA_DONE_TRANSITION` | `Done` | The transition used to close an issue. |
| Allow completion | `jira.allow_complete` | `JIRA_ALLOW_COMPLETE` | `false` | Whether clients may close issues at all. |
| Allowed domains | `allowed_email_domains` | `ALLOWED_EMAIL_DOMAINS` | (empty = any) | Registration allow-list, by email domain. |
| Encryption key | — | `TOKEN_ENCRYPTION_KEY` | — | **Secret.** Required. See below. |

Two rules the config layer enforces: a present-but-empty env var is treated as
*unset* (an exported-but-blank variable does not wipe a config value), and the
three required fields (`atlassian_client_id`, `atlassian_client_secret`,
`token_encryption_key`) are checked at **boot** by `RequireOAuth`, not on the
first login — a service that starts happily and then 500s on everyone who connects
looks healthy to every monitor.

Because JQL and the done-transition live here, a client can never send an
arbitrary query. A client that could would be able to read any issue its user can
see — a far wider surface than a timer needs.

---

## The two secrets

The Atlassian **client secret** and the **token-encryption key** have no defaults,
on purpose, and are read from the credential directory in preference to the
environment. `/proc/<pid>/environ` is readable by the same user and an env var
leaks into every child process and crash dump; a file mode-0400 to the service
account does not.

- **Linux:** passed as **systemd credentials** (`LoadCredential=`), which land in a
  tmpfs the unit can read and nothing else can.
- **macOS / Windows:** two files named `atlassian_client_secret` and
  `token_encryption_key` in the config directory.
- Environment variables work everywhere as a fallback.

The **token-encryption key** is AES-256 (base64 of 32 bytes) and encrypts the Jira
refresh tokens at rest with AES-256-GCM
([server/internal/crypto/crypto.go](server/internal/crypto/crypto.go)). Generate
it **once**:

```bash
task-timer-server gen-key      # prints a fresh key; redirect it into the file, chmod 600
```

Never regenerate it. It decrypts every stored refresh token, and a new key
silently invalidates all of them — forcing every user to reconnect. The decrypt
path says exactly this when it fails, rather than surfacing a bare "message
authentication failed" that sends someone hunting through Atlassian's logs for a
fault that is local.

---

## The database

One SQLite file. Six tables and nothing else
([server/internal/store/store.go](server/internal/store/store.go)):

- **users** — one per Atlassian account (keyed by the account id, which never
  changes; email and display name are refreshed on every login).
- **jira_tokens** — the encrypted access + refresh tokens, expiry, cloud id, and
  site URL, one row per user.
- **api_keys** — the hashed bearer keys, with a public prefix and a revocation
  timestamp. Only the SHA-256 hash is stored; the plaintext is shown to the user
  exactly once at issue time.
- **pending_auth** and **auth_codes** — the two short-lived rows of an in-flight
  login (see [the flow](#the-authentication-flow-in-full)).
- **pushed_worklogs** — the idempotency ledger: `(user, idempotency_key)` unique,
  recording which sessions already reached Jira. This is what stops a client retry
  from logging the same session twice.

The store runs with a **single connection** (`SetMaxOpenConns(1)`): this is a
low-traffic process, so serialising database access sidesteps SQLite's
writer-locking entirely, and keeps an in-memory database coherent for the tests.
`sqlite://` (empty) opens a private in-memory database — that is what the test
suite uses. WAL and a busy timeout apply to on-disk databases.

---

## Talking to Jira

Three facts about Atlassian's 3LO drive the shape of
[server/internal/jira](server/internal/jira):

1. **The refresh token rotates.** Every refresh returns a new one and kills the
   old one immediately. Persisting the new value is not an optimisation — skipping
   it locks the user out for good. `storeTokens` is the single choke point every
   refresh routes through, so this can never be forgotten.
2. **`offline_access` is what makes a refresh token appear at all.** Without that
   scope the grant dies in an hour.
3. **The Jira REST API is not at the site's hostname under 3LO.** It is at
   `api.atlassian.com/ex/jira/<cloud_id>`, and the `cloud_id` has to be discovered
   from `/oauth/token/accessible-resources` after the exchange.

Access tokens are refreshed a little early (`RefreshSkew`, 90s) so a token that
expires between the server's check and Jira's receipt of the request is a 401 the
user never has to see. When a refresh fails with `invalid_grant` — the user
revoked us, an admin pulled their access, or the grant aged out — the server maps
it to a `409` telling the user to reconnect, rather than leaking an Atlassian
error code.

Work logs go to Jira in ADF (Atlassian Document Format) with a `started`
timestamp in the one format Jira accepts: exactly three fractional digits and an
offset with no colon (plain RFC 3339 is rejected). Those quirks are handled in
`jira/client.go`; you rarely need to touch them.

---

## Security model

- **The client holds no Jira credential.** One bearer key per client, identifying
  a user. Revoking a laptop is deleting a row in `api_keys`.
- **Jira tokens are encrypted at rest** with AES-256-GCM under a key that lives
  outside the config file and, on Linux, outside the filesystem the service can
  freely read.
- **Bearer keys are stored hashed** (SHA-256 — the input is 256 bits of entropy,
  not a password, so a KDF would buy only latency). A visible `tt_` prefix makes a
  leaked key greppable in logs and catchable by secret scanners.
- **PKCE guards the client leg**; loopback `redirect_uri` is required and checked;
  `state` is verified in constant time.
- **The write surface is bounded by the server, not the client.** JQL and the done
  transition are server config; completion is opt-in on both sides. A compromised
  client can push time and, if allowed, close its own assigned issues — nothing
  wider.
- **Failures are loud where it counts.** Missing OAuth config fails at boot, not
  on first login. A decrypt failure explains that the key was rotated.

---

## Running it

From source, from the `server/` directory:

```bash
cd server
go run ./cmd/task-timer-server            # start it
go run ./cmd/task-timer-server gen-key    # print a fresh AES-256 encryption key
go run ./cmd/task-timer-server version    # print the build version
```

Before it will start you need an Atlassian OAuth 2.0 (3LO) app
(<https://developer.atlassian.com>):

- **Callback:** `<public_url>/auth/callback`
- **Scopes:** `read:me read:jira-work write:jira-work offline_access`

Then supply the three required settings (client id, client secret, encryption
key) by any of the three mechanisms above.

**Packaged**, everything lands under `build/dist/`. Run from the repository root:

| Platform | Command | Produces |
| --- | --- | --- |
| Debian/Ubuntu | `make server-deb` | `.deb` with a systemd unit |
| RHEL/Fedora | `make server-rpm` | `.rpm` with a systemd unit |
| Windows | `make server-exe` | installer that registers the `TaskTimerServer` service |
| macOS | `make mac-server-app` | `TaskTimerServer.app`, a menu-bar LaunchAgent |
| Docker | `make server-docker` | image that installs the `.deb` and runs the binary |
| everything | `make server-package` | deb + rpm + Windows installers in one container run |

The Linux and Windows packages cross-compile inside the build container
(`Dockerfile.build`), which carries `dpkg-deb`, `rpm`, and `makensis`. The macOS
`.app` builds natively on a Mac. On each platform, config, secrets, and the
database land in the per-platform locations tabulated in
[server/README.md](server/README.md#where-things-live-per-platform).

---

## Operating it

- **`GET /healthz`** is your liveness probe. It does not touch Jira or the
  database, so it stays green during an Atlassian outage — which is correct: Task
  Timer Server is up, the upstream is not.
- **Logs name variables, never values.** Startup logs the address it binds; it
  does not print secrets.
- **A user cannot connect** → they have no Jira grant yet. The API answers
  requests needing Jira with a `409` telling them to run "Connect to Jira" in Task
  Timer's Settings.
- **Everyone suddenly has to reconnect** → the token-encryption key changed. This
  is the one unrecoverable operational mistake; the key is write-once. Restore the
  original key if you have it.
- **Work logs push but issues never close** → completion is off. Both
  `jira.allow_complete` on the server and the client's `complete_remote_tasks`
  must be on.
- **`invalid_grant` in the logs for one user** → that user revoked the app or lost
  Jira access; they reconnect and it clears.

---

## Working on it

```bash
cd server && go test ./...     # or, from the root: make server-test
```

The suite runs entirely against an in-memory database and redirects the Atlassian
and Jira endpoints at `httptest` servers (the endpoint URLs in `jira/` are
package `var`s precisely so tests can point them locally). No network and no real
Jira are needed.

Task Timer Server is pure Go with no cgo, so there is nothing to install beyond the
Go toolchain — none of the client's C-compiler and X11/GL prerequisites apply here.
Cross-compiling is just `GOOS`/`GOARCH`.

When changing the HTTP surface, remember the other half of the contract lives in
Task Timer at `internal/reconcile/providers/gateway/` in the root module — the two
evolve together but build separately. See [DEVELOPER.md](DEVELOPER.md) for that
side.
