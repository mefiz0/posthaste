# AGENTS.md

Guidance for AI agents and contributors working in this repository.

**Keep this file in sync with the code.** When behaviour, commands, or structure
change, update this file in the same change. The authoritative design is in
[`docs/tech-specs.md`](docs/tech-specs.md) and the product intent in
[`docs/product.md`](docs/product.md); the ordered task list is
[`docs/implementation-plan.md`](docs/implementation-plan.md). When in doubt,
follow the specs. The UI is defined by `mockup/index.html` (gitignored, kept
locally) — match its layout, tokens, and interactions.

## Project Overview

**Posthaste** is a fast, reliable, offline-first desktop email client for Linux.
It is an email client, **not** an email service: users connect their existing
IMAP/SMTP accounts and keep ownership of their mail. There is no telemetry of any
kind (tech-specs §10.1).

It is a **single Go module** (`github.com/mefiz0/posthaste`) with two halves:

- **`internal/`** — the framework-agnostic **core mail engine**. Pure Go,
  no Wails imports, fully unit-testable without a display or network. Owns
  storage, IMAP/SMTP, sync, search, threading, sanitization, credentials.
- **root (`main.go`) + `internal/app`** — the **Wails v3 app shell**. Creates the
  application, window, tray, notifications, and exposes the core engine to the UI
  as bound Go services plus an event bus. Only this layer may import Wails.
- **`frontend/`** — the **Svelte 5 + TypeScript** UI, built by Vite, rendered by
  WebKitGTK through Wails. It never talks to IMAP/SMTP/SQLite directly; it calls
  bound Go methods and subscribes to events. State lives in Svelte stores fed by
  those events.

The dependency choices are consolidated in [`docs/tech-specs.md`](docs/tech-specs.md)
§13 and are the single source of truth. Do not add a dependency that duplicates
one already listed there (e.g. another IMAP client, another SQLite driver).

## Stack

Go 1.25+ (workspace pins 1.27.1) · Wails v3 (`v3.0.0-beta.22`, pinned exactly)
· Svelte 5 + TypeScript + Vite · per-account SQLite (pure-Go `modernc.org/sqlite`
+ FTS5) · `goose` migrations · `go-imap/v2` · `go-mail` · OS keyring
(`zalando/go-keyring`) · `bluemonday` sanitizer.

**Platform scope is Linux desktop, v1.** Testing, packaging, and path handling
target Linux. Do not add cross-platform branches, build tags, or CI matrixes
unless the spec is explicitly extended.

**GTK4/WebKitGTK 6.0 note:** the app shell builds against **GTK4 +
WebKitGTK 6.0**, the pinned Wails v3 default, so plain `go build .` works and
no build tags are involved. The system needs the `gtk4` and `webkitgtk-6.0`
development libraries (see README for per-distro package names). The `wails3`
CLI is built from source by `task tools:wails` — it is intentionally **not** in
`mise.toml`, so the CLI and the shell always link the same GTK stack.

## Repository Layout

```
main.go                     Wails app entry: application, window, services, tray, notifications
                            (the shell lives in shell.go; contextmenu.go suppresses
                            WebKitGTK's built-in context menu so the UI can draw its own)
Taskfile.yml                the single entry point for every dev/build/test/lint action
mise.toml                   pinned toolchain (go, task, node, golangci-lint)
.golangci.yml               Go lint config
build/config.yml            Wails build metadata (product name, identifier, version)
build/appicon.png           app icon source
migrations/*.sql            goose migrations, embedded with go:embed
internal/
  settings/                 global (non-account) config: XDG paths, settings.toml load/save
  backoff/                  shared retry/backoff policy
  store/                    per-account SQLite: open/pragmas, goose migrations, repositories, FTS5
  mail/                     MIME parsing + body_text/body_html extraction
  sanitize/                 allowlist HTML sanitizer (bluemonday), remote-content detection
  thread/                   JWZ-ish threading: reference linking, subject fallback, merge
  search/                   query parser (from:, to:, subject:, has:, is:, date ranges)
  auth/                     keyring, XOAUTH2 SASL, OAuth2 loopback, autodiscovery (presets/SRV/ISPDB)
  attachment/               content-addressed blob store: put/get, sweep
  imapx/                    thin wrapper over go-imap/v2 (connect, list, fetch, flags, idle)
  sync/                     per-account sync worker, IDLE/poll, offline queue, conflict resolution
  send/                     send-state machine + queue worker over go-mail, raw MIME builder
  logging/                  slog setup, rotation, scrubbing handler
  crashreport/              top-level panic capture: scrubbed local crash reports (opt-in)
  app/                      account registry + manager (per-account worker supervision), Wails
                            services (bindings) + event emission; bridges core engine ↔ UI
frontend/
  index.html
  src/
    main.ts                 mounts App
    App.svelte              shell: topbar, sidebar, list, reading pane, overlays
    lib/types.ts            app-side domain types (camelCase mirror of the Go DTOs)
    lib/backend.ts          the Backend interface — the single frontend↔backend contract
    lib/wails.ts            Backend implementation over the generated Wails bindings
    lib/mock.ts             Backend implementation over in-memory sample data (plain-browser dev)
    lib/api.ts              picks wails.ts inside the shell, mock.ts in a plain browser
    lib/stores.svelte.ts    Svelte 5 runes stores + event wiring (accounts, folders, messages,
                            sync, settings, ui)
    lib/keys.ts             declarative keymap + dispatcher
    lib/palette.ts          command registry + fuzzy matcher
    lib/contextmenu.ts      contextual-menu model + right-click target detection
    lib/format.ts           date/bytes/initials helpers
    components/             Sidebar, MessageList, MessageRow, ReadingPane, MessageFrame,
                            ComposeDrawer, CommandPalette, ShortcutsOverlay, ContextMenu,
                            Toast, AccountSetup, Settings
    bindings/               AUTO-GENERATED by `wails3 generate bindings` (gitignored)
docker/
  dovecot/ postfix/         integration-test mail server configuration
docker-compose.yml          brings up the Dovecot + Postfix harness
.github/workflows/ci.yml    unit+lint on push, integration on PR, fuzz nightly
```

`internal/` packages that are leaves (backoff, search, thread, sanitize, mail,
attachment, auth) must not import the app shell or Wails. `internal/store` is the
lowest-level package and imports no other internal package except `settings` for
paths.

## Commands

All commands are Task targets. Run `task --list` for the full set. The important
ones:

- `task tools` — install dev tooling (`wails3` built from source).
- `task deps` — `go mod download` + `npm install` in `frontend/`.
- `task generate` — generate Wails bindings into `frontend/src/bindings`.
- `task build` — build the frontend (`vite build`) then the app binary
  (`go build -o bin/posthaste`). Use this to verify a full build.
- `task dev` — frontend dev server + `go run` against it.
- `task test` — `go test ./...` (core engine; no network, no display) plus
  `npm run check` for the frontend. The root package links GTK4/WebKitGTK 6.0,
  so the test job needs those development packages installed.
- `task test:integration` — brings up the Docker Dovecot/Postfix harness and runs
  tests tagged `integration`.
- `task test:fuzz` — short fuzz run over MIME parsing and sanitization.
- `task lint` — `golangci-lint run` + `npm run check`.
- `task format` — `gofmt`/`golangci-lint fmt` + `npm run format`.
- `task infra:up` / `task infra:down` — start/stop the mail-server harness.
- `task clean` — remove `bin/`, `frontend/dist`, build caches.

`go test ./...` must pass with **no network and no Docker** — that is the fast
suite. Anything requiring the mail container must be behind the `integration`
build tag and skipped automatically when the container is unreachable.

## Core Invariants (do not violate)

These come straight from the specs and are non-negotiable:

- **No telemetry.** Never add analytics, usage counters, or a network call that
  reports app behaviour. Crash reporting is opt-in, off by default, and separate
  from telemetry (§10.1–10.2).
- **Scrub at capture, never redact after.** Message bodies, subjects,
  sender/recipient addresses, attachment filenames, and credentials/tokens must
  never reach a log line or crash report. Filtering happens where the value is
  captured (a slog handler + panic wrapper), so there is no path for a leak.
  When an identifier is needed, log the account's internal ID, never its address.
- **Account isolation.** One SQLite file per account, one sync goroutine per
  account. Never join across accounts in SQL; the unified inbox and cross-account
  search merge per-account results in Go.
- **Credentials live in the OS keyring only.** SQLite stores a keyring reference,
  never the secret. Tokens included.
- **Server wins on state conflict, but never drop queued sends.** Locally queued
  flag/move/delete actions are replayed first; a queued *send* is only affected by
  send failures, never by mailbox state.
- **Untrusted input.** All message content is hostile. Sanitize HTML at ingest
  with the allowlist sanitizer; render it only in a sandboxed iframe with no
  `allow-scripts`/`allow-same-origin`, JS disabled, remote content blocked by
  default, and `cid:` images resolved from the local attachment store.
- **SQLite off the UI thread.** All DB writes happen in background goroutines.
  The UI reads through bound query methods backed by the `database/sql` pool or
  from in-memory state kept current by the event bus.
- **Migrations are additive and transactional**, run per account at startup with
  a pre-migration backup; downgrades are unsupported (§12).

## Conventions — Go

- `gofmt` + `golangci-lint` clean. No `any`; no `@ts-expect-error` equivalent
  (`//nolint` only with a specific reason). `unknown`-style handling in Go means
  narrow interfaces and explicit type assertions with the comma-ok form.
- **Errors are values**: wrap with `fmt.Errorf("store: load folder %d: %w", id, err)`
  using a package prefix. Never `_ = err` silently. Return early on error.
- **`context.Context` is the first parameter** of anything doing I/O, and is
  propagated into `database/sql`, `net`, and IMAP/SMTP calls. Cancellation on
  account pause/removal/app shutdown is expected.
- **Goroutine lifetimes are owned.** Every worker takes a `ctx` and a `done`
  signal; no leaked goroutines. Use `errgroup`/`semaphore` for bounded work
  (§8.4), not ad-hoc `go` in loops.
- **Exported symbols get a doc comment** beginning with the symbol name, one line
  of intent where possible. Comment **why**, not **what**; skip `@param` noise.
- **Comments must stand alone.** Never cite spec sections (no `(tech-specs §3.5)`,
  no `§4.1`), ticket numbers, or phase labels in code comments — a reader of the
  code may not have those documents. Write the reason inline so the comment is
  self-contained. The same applies to TypeScript and Svelte comments.
- Prefer `errors.Is`/`errors.As`; define sentinel errors (`ErrNotFound`,
  `ErrAuthFailed`, `ErrConflict`) in the package that owns them.
- **Interfaces are defined by the consumer**, small and focused (e.g. `sync`'s
  `MailServer`), so tests can substitute a fake without a mock framework.
- **No global mutable state.** Dependencies are passed explicitly. The only
  process-wide singletons are the Wails `application` and the logger, and both are
  injected rather than reached for implicitly.
- Tests use the standard `testing` package and table-driven cases. Fake the
  network boundary, not the logic under test. Injected clocks for anything
  time-based (backoff, drafts debounce) — never `time.Sleep` in unit tests.

## Conventions — TypeScript / Svelte

- Svelte 5 runes (`$state`, `$derived`, `$effect`, `$props`) — not legacy stores
  or `$:` where runes are appropriate. Shared app state lives in `.svelte.ts`
  store modules.
- `strict` TypeScript. `any` is banned; type the Wails binding responses from
  `src/bindings`. Reach across the bridge through `lib/api.ts`, never call
  `bindings/*` directly from a component.
- Components are presentational. Data comes from store modules; side effects
  (calls to Go, keymap registration) live in `lib/` or the store, not in markup.
- Match `mockup/index.html` for layout, spacing, and the token palette. Read the
  design tokens from CSS custom properties on `:root`; do not hardcode colors.
  The single accent is `--accent`; red (`--danger`) is destructive-only.
- Respect `prefers-reduced-motion` and `prefers-reduced-transparency`. The
  reading pane renders message HTML **only** inside the sandboxed iframe
  component, never via `{@html}` in the app DOM.
- Keyboard: register actions in the declarative keymap (`lib/keys.ts`), never as
  ad-hoc `on:keydown` per component. Every action must be reachable from the
  command palette.
- Accessibility is a constraint, not a phase: real `<button>`/`<input>` elements,
  visible focus, `aria-*` on dialogs, focus trapping in overlays, and logical
  focus order across panes.

## Naming

- `camelCase` for Go locals/TS variables; `PascalCase` for Go exported symbols
  and Svelte components; `SCREAMING_SNAKE_CASE` for constants.
- Booleans read as predicates: `isUnread`, `hasAttachment`, `canSend`.
- Domain terms are fixed: **account, folder, message, thread, draft, attachment,
  action queue, send queue**. Don't drift to synonyms (`mailbox`, `email`,
  `conversation` for the data model) in code.
- No abbreviations (`messageID`, not `msgID`; `templateName`, not `tmplName`).
- One name per concept across the whole repo.

## Testing Notes

- **Core engine unit tests** (`internal/*/*_test.go`) are the backbone and run in
  CI on every push. Priority coverage: backoff timing (injected clock),
  search-query parsing, threading (reference chains, subject fallback, merges),
  MIME extraction, sanitizer properties (no script/remote refs in output, never
  logs scrubbed categories), offline-queue replay + conflict drops, send-state
  transitions.
- **Integration tests** (`//go:build integration`) talk to the real Dovecot +
  Postfix container, not a mock. They skip cleanly when `POSTHASTE_TEST_IMAP_ADDR`
  is unset or unreachable, so `task test` stays hermetic. Protocol coverage lives
  in `internal/imapx`, and an end-to-end suite in `internal/app` drives the real
  Manager (initial sync, IDLE wake, threading, flag/move replay) against the
  harness with an injected credential store.
- **Fuzz tests** (`testing.F`) cover MIME parsing and sanitization; run nightly,
  not per-PR.
- Frontend: `vitest` for logic (keymap, palette matching, formats) and
  `svelte-check` for types. UI e2e is not wired up in v1.

## Changelog

This repo keeps a `CHANGELOG.md` in [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
format. Land every user-facing change under a single top `[Unreleased]` section,
grouped under `Added` / `Changed` / `Deprecated` / `Removed` / `Fixed` / `Security`.
Prefix bullets with the scope (`**sync:**`, `**store:**`, `**ui:**`, `**docs:**`).
Don't log internal refactors, formatting, or test-only changes.

## Phase Discipline

The implementation plan (`docs/implementation-plan.md`) is ordered and has
dependencies: Phase 1 depends only on Phase 0; Phase 2 on Phase 1; Phases 3 and 4
are independent of each other and both need Phase 2; Phase 5 needs 1–4. **Do not
start a later phase's feature before the current phase's exit criteria hold.**
Phase 1 is deliberately plain-text-only — HTML rendering is Phase 4 and must not
leak into Phase 1 UI. Keep every dependency pinned; treat the §13 table as the
source of truth.
