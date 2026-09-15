# Posthaste — Implementation Plan

*Companion to the Technical Specification (`tech-specs.md`). This is a high-level, ordered task list derived from the §14 roadmap — it does not restate rationale or design, and contains no implementation detail. Each phase has exit criteria; parallel workstreams run alongside all phases.*

**Stack reminder:** Go 1.25+ · Wails v3 · Svelte 5 + TypeScript · per-account SQLite (FTS5) · `go-imap/v2` · `go-mail` · `goose` · OS keyring.

---

## Phase 0 — Foundations

- [x] Resolve repository/module path and choose the license (Wails is MIT; app license is separate).
- [x] Lay out the Go module structure (core engine packages vs. Wails app shell) and the frontend directory.
- [x] Scaffold a Wails v3 app with the Svelte 5 + TypeScript template; pin the exact Wails v3 beta version.
- [x] De-risk the beta framework choice with a throwaway window (multi-window, events, tray, keybinding smoke test).
- [x] Set up the `Taskfile` with dev, build, test, lint, and package targets as the single entry point.
- [x] Configure `golangci-lint` and frontend lint/format tooling; wire both into the task runner.
- [x] Set up `goose` migration scaffolding with embedded SQL and a near-empty initial schema.
- [x] Stand up the Dockerized Dovecot + Postfix harness for integration tests.
- [x] Establish CI: fast unit tests and lint on every push, integration tests on every PR, fuzz jobs nightly.
- [x] Decide the global settings store format (`settings.toml` vs. `global.sqlite`) and the XDG app-directory layout.
- [x] Write developer onboarding docs (setup, system packages, how to run, how to test).

**Exit:** a Wails window builds and runs locally and in CI, with empty migration + test scaffolding in place and all toolchain tasks runnable.

---

## Phase 1 — Single-Account Core Loop (MVP)

- [x] Implement the SQLite layer: per-account database files, WAL + pragmas, connection pool.
- [x] Implement the core schema: `folders`, `messages`, account table (no threading yet).
- [x] Implement manual account entry (host, port, username, password) and persist account metadata.
- [x] Integrate OS keyring credential storage from day one.
- [x] Implement IMAP connect/login and folder listing.
- [x] Implement header + body fetch; store raw MIME on disk and derived `body_text`.
- [x] Implement the sync worker: initial sync, IDLE with polling fallback, reconnect/heartbeat.
- [x] Implement the durable offline action queue (flag, move, delete, archive, read/unread).
- [x] Implement queue replay in order on reconnect and server-wins conflict resolution.
- [x] Implement basic send via SMTP with local state persistence and simple failure handling.
- [x] Build the minimal UI: folder list, message list, reading pane (plain text only).
- [x] Wire the Go core to the frontend via bindings and an event bus; keep SQLite off the main thread.
- [x] Surface per-account connection/auth errors without blocking the UI.
- [x] Support account pause and removal.

**Exit:** a single real account can be added, synced, read, and replied to — reliably, online and offline — and feels fast. No other feature is started before this.

---

## Phase 2 — Multi-Account + Account Setup

- [x] Enforce account isolation: one database file and one sync goroutine per account.
- [x] Build the unified inbox by merging per-account results in the app layer.
- [x] Implement autodiscovery in order: known-provider presets, DNS SRV, MX lookup, Mozilla ISPDB, hostname guesses.
- [x] Build the manual configuration fallback screen, pre-filled from best-guess results.
- [x] Implement the OAuth2 loopback flow; store access + refresh tokens in the keyring.
- [x] Implement silent token refresh for sync workers.
- [x] Implement the in-tree XOAUTH2 SASL client required by Microsoft 365.
- [x] Implement live verification (IMAP login + folder listing) before saving an account.
- [x] Implement content-addressed attachment storage with metadata and reference counting.
- [x] Implement the eager/lazy attachment fetch policy with a configurable threshold.
- [x] Implement attachment garbage collection (periodic sweep of zero-reference blobs).
- [x] Implement debounced draft persistence and Drafts folder sync.
- [x] Harden the compose/send pipeline per the send-state model.

**Exit:** multiple accounts (including Gmail/Outlook via OAuth2) can be added through a guided flow, isolated from each other, with attachments and drafts working offline.

---

## Phase 3 — Search + Threading

- [x] Implement the FTS5 index over sender, recipients, subject, and `body_text`, kept current at ingest.
- [x] Implement the query parser supporting operators (`from:`, `to:`, `subject:`, `has:attachment`, date ranges, `is:`).
- [x] Implement cross-account search fan-out and result merge/ranking in the app layer.
- [x] Implement the threading algorithm (References/In-Reply-To primary, normalized-subject fallback).
- [x] Implement thread reconciliation/merge when missing ancestors arrive.
- [x] Materialize thread membership and denormalized thread metadata at ingest.
- [x] Build the search UI (inline expanding bar, structured filters, no-results state).
- [x] Build the conversation/thread view and thread list sorting.

**Exit:** search feels instant across accounts, and conversations group correctly, including late-arriving ancestors.

---

## Phase 4 — HTML Rendering

- [x] Implement backend HTML sanitization with an allowlist sanitizer at ingest.
- [x] Implement the sandboxed iframe renderer with JS disabled and a restrictive CSP.
- [x] Implement remote-content blocking by default with a per-message opt-in.
- [x] Implement HTML / Plain Text tabs with instant switching.
- [x] Implement inline `cid:` image resolution from the attachment store.
- [x] Implement link interception that opens the OS default browser.
- [x] Add fuzz and property-based tests for MIME parsing and sanitization.
- [ ] Build the curated HTML regression corpus and a manual visual pass workflow.

**Exit:** real-world HTML email renders accurately and safely, with a dependable plain-text fallback and no active-content surface.

---

## Phase 5 — UI Polish + Power-User Features

- [x] Implement the full layout: collapsible docks, resizable panes, tabs, and the compose drawer.
- [x] Implement the context-aware fuzzy command palette.
- [x] Implement the declarative keymap, rebinding UI, and the `?` shortcut reference overlay.
- [x] Integrate native notifications with per-account and global toggles.
- [x] Integrate the system tray with GNOME-tray fallback handling, unread badge, and context menu.
- [x] Implement compose recipient autocomplete from the derived contacts table.
- [x] Implement empty states, motion restraint, and responsive/compact layouts.
- [x] Close out the visual design system gaps (typography scale, iconography, spacing).

**Exit:** the app is fully keyboard-operable, visually settled, and matches the product spec's density/calm goals across window sizes.

---

## Phase 6 — Reliability & Observability

- [x] Unify the backoff/retry configuration across sync, send, and account-failure paths.
- [ ] Complete send retry states and failure UX (Retry, Edit and resend).
- [ ] Implement opt-in crash reporting with strict capture-time scrubbing.
- [x] Implement structured local logging with rotation, size caps, and the same scrubbing rules.
- [x] Implement account-level failure attribution in the UI.
- [ ] Add migration tests over fixture databases representing each prior schema version.

**Exit:** failures are predictable, attributable, and never lose user work; logs and reports never leak message content.

---

## Phase 7 — Local Packaging

Scope for now is a working install on the development machine only. Distribution
channels (Flatpak, AppImage, AUR) and the release gates that go with them are
deferred until after local daily use.

- [ ] Add an `install` task that builds the binary and installs it, the icon, and the desktop entry under `~/.local`.
- [ ] Author a local `PKGBUILD` so the app installs and updates through `makepkg`/pacman on Arch.
- [ ] Finalize the app icon, desktop entry, and metadata for the local install.
- [ ] Verify the installed app launches from the desktop environment (keyring, tray, notifications, and HTML rendering) on the target machine.

**Exit:** the app is installable and launchable locally from the desktop environment with a working icon, tray, notifications, and keyring — no distribution channels required.

---

## Parallel Workstreams

### Visual Design System
- [x] Define color tokens (background/surface/elevated/border, text tiers, single accent, brand-only logo color, destructive-only red).
- [x] Define the typography scale and spacing rhythm.
- [x] Define iconography and motion guidelines.
- [x] Apply the design system before Phase 1 UI work begins, not after.

### Accessibility
- [x] Evaluate WebKitGTK/GTK4 accessibility and focus semantics before committing to Phase 1 UI patterns.
- [x] Define keyboard focus order and screen-reader expectations across panes.
- [x] Treat accessibility as a constraint on every UI phase, not a late add-on.

### Documentation
- [x] Maintain `AGENTS.md` with build/test/lint commands as they stabilize.
- [x] Maintain a user-facing README and a contributor setup guide.
- [ ] Document the local install process once Phase 7 lands.

### Ongoing Discipline
- [x] Keep every dependency pinned; revisit the Wails version at each beta/RC/GA release.
- [x] Keep the dependency table in `tech-specs.md` §13 the single source of truth.
- [x] Keep scrubbing rules enforced at the point of capture in all new code paths.

---

## Phase Dependencies

- Phase 1 depends on Phase 0 only.
- Phase 2 depends on Phase 1 (account + sync foundations).
- Phases 3 and 4 are independent of each other and both depend on Phase 2.
- Phase 5 depends on Phases 1–4 for the views it polishes.
- Phase 6 can begin once Phase 2 lands and runs in parallel with 3–5.
- Phase 7 depends on Phase 5 (UI complete) and Phase 6 (stable behavior).
