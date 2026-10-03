# Posthaste

<!-- Screenshot placeholder: drop a capture of the main window at
     docs/screenshot.png and uncomment.
![Posthaste main window](docs/screenshot.png) -->

Posthaste is a fast, reliable, offline-first desktop email client for Linux.
It is an **email client, not an email service**: you connect the IMAP/SMTP
accounts you already have and keep full ownership of your mail. There is no
telemetry of any kind — not opt-in, not anonymized, nothing.

> **Status: early development.** The ordered build plan lives in
> [docs/implementation-plan.md](docs/implementation-plan.md). What is described
> below is the destination, not the current feature set.

## Features

- **Multi-account IMAP/SMTP** — plain IMAP/SMTP plus OAuth2 for providers that
  require it (Gmail, Microsoft 365), with guided autodiscovery and a manual
  fallback.
- **Offline-first** — read, search, and compose without a connection; actions
  made offline sit in a durable queue and replay in order on reconnect.
- **Instant full-text search** — SQLite FTS5 indexing over senders, subjects,
  and bodies, kept current at ingest.
- **Conversations** — JWZ-style threading computed once at ingest, not per
  render.
- **Safe HTML rendering** — allowlist sanitization at ingest, rendered in a
  sandboxed iframe with JavaScript disabled and remote content blocked by
  default.
- **Keyboard-driven** — a declarative, configurable keymap plus a fuzzy
  command palette; every action is reachable from both.
- **Quiet desktop integration** — native notifications for meaningful events
  only, optional system tray, no background chatter.

## Requirements

- Linux desktop (the v1 target platform).
- GTK4 + WebKitGTK 6.0 development packages:
  - **Debian/Ubuntu:** `sudo apt install libgtk-4-dev libwebkitgtk-6.0-dev build-essential pkg-config`
  - **Arch:** `sudo pacman -S --needed gtk4 webkitgtk-6.0 base-devel`
  - **Fedora:** `sudo dnf install gtk4-devel webkitgtk6.0-devel gcc gcc-c++ make`
- [Go](https://go.dev) 1.25+
- [Node.js](https://nodejs.org) 20+
- [Task](https://taskfile.dev) — the single entry point for every dev command
- [mise](https://mise.jdx.dev) (optional) — pins the exact toolchains from
  `mise.toml`

## Getting started

```sh
# with mise (recommended — installs the pinned Go, Task, Node, golangci-lint):
mise install
# or install the tools by hand, then:

task deps              # go mod download + npm install
task build             # vite build + go build -> bin/posthaste
task dev               # frontend dev server + live Go run
```

The app shell builds against **GTK4 + WebKitGTK 6.0**, the Wails v3 default
stack, so no build tags are involved. The `wails3` CLI is built from source by
`task tools:wails`.

## Installing locally

Packaging is scoped to a local install for now — distribution channels
(Flatpak, AppImage, AUR) come later. The task runner builds, installs, and can
remove everything:

```sh
task install            # build + install under ~/.local
task uninstall          # remove the binary, icon, and desktop entry
```

`task install` puts the binary at `~/.local/bin/posthaste`, the icon in
`~/.local/share/icons/hicolor/`, and a desktop entry in
`~/.local/share/applications/`. The entry is named `org.wails.posthaste.desktop`
because that must match the GApplication ID Wails creates for the shell —
Wayland compositors use it to associate a window with its icon. Its `Exec` is
written as an absolute path, since `~/.local/bin` is not guaranteed to be on a
graphical session's `PATH`.

To manage the install through pacman instead, build the local package and
install it with `pacman -U`:

```sh
task package            # makepkg -> packaging/posthaste-<ver>-<rel>-<arch>.pkg.tar.zst
sudo pacman -U packaging/posthaste-*.pkg.tar.zst
```

`packaging/PKGBUILD` builds the working tree it lives in, so it always reflects
the checked-out source. After changing the logo, run `task icons` to regenerate
the committed hicolor set from `build/appicon.png`.

## Testing

```sh
task test              # hermetic unit tests + frontend checks (no network, no Docker)
task test:integration  # real Dovecot + Postfix in Docker, driven by go test -tags integration
task test:fuzz         # short native fuzz run over MIME parsing and sanitization
```

The integration harness is the `docker-compose.yml` mail server (Dovecot IMAP
on `127.0.0.1:1143`, Postfix submission on `127.0.0.1:1025`, account
`test@posthaste.local` / `posthaste`). Integration tests skip cleanly when the
container is unreachable, so the fast suite stays hermetic.

## Project layout

```
main.go                Wails app entry: window, services, tray, notifications
internal/              core mail engine (no UI dependencies)
  settings/            global config + XDG paths
  store/               per-account SQLite, migrations, repositories
  mail/                MIME parsing, body extraction, cid resolution
  sanitize/            allowlist HTML sanitizer
  thread/              JWZ-ish threading
  search/              query parser
  auth/                keyring, XOAUTH2, autodiscovery
  attachment/          content-addressed blob store
  imapx/               go-imap/v2 wrapper
  sync/                per-account sync worker, offline queue
  send/                send-state machine + queue worker
  backoff/             shared retry/backoff policy
  logging/             slog setup, rotation, capture-time scrubbing
  app/                 Wails bindings + event bus (only Wails importer)
frontend/              Svelte 5 + TypeScript UI
migrations/            goose SQL migrations, embedded
docker/                integration-test mail server configuration
docs/                  product spec, tech specs, implementation plan
```

## Architecture in five lines

- `internal/` is a framework-agnostic core mail engine: pure Go, unit-testable
  with no display and no network.
- `main.go` + `internal/app` form the Wails v3 app shell and expose the engine
  to the UI as bound services and an event bus; only this layer imports Wails.
- `frontend/` is a Svelte 5 + TypeScript UI rendered by WebKitGTK; it talks to
  bound Go methods and events, never to IMAP/SMTP/SQLite directly.
- Each account gets its own SQLite database and its own sync goroutine;
  cross-account views merge results in Go.
- Credentials live only in the OS keyring; SQLite stores a reference, never a
  secret.

## Privacy

- **No telemetry, ever.** No analytics, no usage counters, no phone-home of
  any form — not even opt-in.
- **Credentials stay in the OS keyring** (GNOME Keyring / KWallet via Secret
  Service).
- **Logs are scrubbed at capture.** Local logging is always on and never
  touches the network; message bodies, subjects, addresses, attachment
  filenames, and tokens are filtered where the value is captured, so there is
  no path for them to reach the log file. Accounts are identified by internal
  ID in logs, never by address.
- **Crash reporting is opt-in and off by default.** When enabled, a local
  report contains only the panic value, stack, OS, architecture, and app
  version — scrubbed at capture, never message content, and never sent
  anywhere automatically.

## License

[MIT](LICENSE) — Copyright (c) 2026 Posthaste contributors.

Design details and rationale live in [`docs/tech-specs.md`](docs/tech-specs.md)
and [`docs/product.md`](docs/product.md).
