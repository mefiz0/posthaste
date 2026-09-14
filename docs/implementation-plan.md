# Posthaste — Implementation Plan

*Companion to the Technical Specification (`tech-specs.md`). This is a high-level, ordered task list derived from the §14 roadmap — it does not restate rationale or design, and contains no implementation detail. Each phase has exit criteria; parallel workstreams run alongside all phases.*

**Stack reminder:** Go 1.25+ · Wails v3 · Svelte 5 + TypeScript · per-account SQLite (FTS5) · `go-imap/v2` · `go-mail` · `goose` · OS keyring.

---

## Phase 0 — Foundations

- [ ] Resolve repository/module path and choose the license (Wails is MIT; app license is separate).
- [ ] Lay out the Go module structure (core engine packages vs. Wails app shell) and the frontend directory.
- [ ] Scaffold a Wails v3 app with the Svelte 5 + TypeScript template; pin the exact Wails v3 beta version.
- [ ] De-risk the beta framework choice with a throwaway window (multi-window, events, tray, keybinding smoke test).
- [ ] Set up the `Taskfile` with dev, build, test, lint, and package targets as the single entry point.
- [ ] Configure `golangci-lint` and frontend lint/format tooling; wire both into the task runner.
- [ ] Set up `goose` migration scaffolding with embedded SQL and a near-empty initial schema.
- [ ] Stand up the Dockerized Dovecot + Postfix harness for integration tests.
- [ ] Establish CI: fast unit tests and lint on every push, integration tests on every PR, fuzz jobs nightly.
- [ ] Decide the global settings store format (`settings.toml` vs. `global.sqlite`) and the XDG app-directory layout.
- [ ] Write developer onboarding docs (setup, system packages, how to run, how to test).

**Exit:** a Wails window builds and runs locally and in CI, with empty migration + test scaffolding in place and all toolchain tasks runnable.

---

## Phase 1 — Single-Account Core Loop (MVP)

- [ ] Implement the SQLite layer: per-account database files, WAL + pragmas, connection pool.
- [ ] Implement the core schema: `folders`, `messages`, account table (no threading yet).
- [ ] Implement manual account entry (host, port, username, password) and persist account metadata.
- [ ] Integrate OS keyring credential storage from day one.
- [ ] Implement IMAP connect/login and folder listing.
- [ ] Implement header + body fetch; store raw MIME on disk and derived `body_text`.
- [ ] Implement the sync worker: initial sync, IDLE with polling fallback, reconnect/heartbeat.
- [ ] Implement the durable offline action queue (flag, move, delete, archive, read/unread).
- [ ] Implement queue replay in order on reconnect and server-wins conflict resolution.
- [ ] Implement basic send via SMTP with local state persistence and simple failure handling.
- [ ] Build the minimal UI: folder list, message list, reading pane (plain text only).
- [ ] Wire the Go core to the frontend via bindings and an event bus; keep SQLite off the main thread.
- [ ] Surface per-account connection/auth errors without blocking the UI.
- [ ] Support account pause and removal.

**Exit:** a single real account can be added, synced, read, and replied to — reliably, online and offline — and feels fast. No other feature is started before this.

---

## Phase 2 — Multi-Account + Account Setup

- [ ] Enforce account isolation: one database file and one sync goroutine per account.
- [ ] Build the unified inbox by merging per-account results in the app layer.
- [ ] Implement autodiscovery in order: known-provider presets, DNS SRV, MX lookup, Mozilla ISPDB, hostname guesses.
- [ ] Build the manual configuration fallback screen, pre-filled from best-guess results.
- [ ] Implement the OAuth2 loopback flow; store access + refresh tokens in the keyring.
- [ ] Implement silent token refresh for sync workers.
- [ ] Implement the in-tree XOAUTH2 SASL client required by Microsoft 365.
- [ ] Implement live verification (IMAP login + folder listing) before saving an account.
- [ ] Implement content-addressed attachment storage with metadata and reference counting.
- [ ] Implement the eager/lazy attachment fetch policy with a configurable threshold.
- [ ] Implement attachment garbage collection (periodic sweep of zero-reference blobs).
- [ ] Implement debounced draft persistence and Drafts folder sync.
- [ ] Harden the compose/send pipeline per the send-state model.

**Exit:** multiple accounts (including Gmail/Outlook via OAuth2) can be added through a guided flow, isolated from each other, with attachments and drafts working offline.

---

## Phase 3 — Search + Threading

- [ ] Implement the FTS5 index over sender, recipients, subject, and `body_text`, kept current at ingest.
- [ ] Implement the query parser supporting operators (`from:`, `to:`, `subject:`, `has:attachment`, date ranges, `is:`).
- [ ] Implement cross-account search fan-out and result merge/ranking in the app layer.
- [ ] Implement the threading algorithm (References/In-Reply-To primary, normalized-subject fallback).
- [ ] Implement thread reconciliation/merge when missing ancestors arrive.
- [ ] Materialize thread membership and denormalized thread metadata at ingest.
- [ ] Build the search UI (inline expanding bar, structured filters, no-results state).
- [ ] Build the conversation/thread view and thread list sorting.

**Exit:** search feels instant across accounts, and conversations group correctly, including late-arriving ancestors.

---

## Phase 4 — HTML Rendering

- [ ] Implement backend HTML sanitization with an allowlist sanitizer at ingest.
- [ ] Implement the sandboxed iframe renderer with JS disabled and a restrictive CSP.
- [ ] Implement remote-content blocking by default with a per-message opt-in.
- [ ] Implement HTML / Plain Text tabs with instant switching.
- [ ] Implement inline `cid:` image resolution from the attachment store.
- [ ] Implement link interception that opens the OS default browser.
- [ ] Add fuzz and property-based tests for MIME parsing and sanitization.
- [ ] Build the curated HTML regression corpus and a manual visual pass workflow.

**Exit:** real-world HTML email renders accurately and safely, with a dependable plain-text fallback and no active-content surface.

---

## Phase 5 — UI Polish + Power-User Features

- [ ] Implement the full layout: collapsible docks, resizable panes, tabs, and the compose drawer.
- [ ] Implement the context-aware fuzzy command palette.
- [ ] Implement the declarative keymap, rebinding UI, and the `?` shortcut reference overlay.
- [ ] Integrate native notifications with per-account and global toggles.
- [ ] Integrate the system tray with GNOME-tray fallback handling, unread badge, and context menu.
- [ ] Implement compose recipient autocomplete from the derived contacts table.
- [ ] Implement empty states, motion restraint, and responsive/compact layouts.
- [ ] Close out the visual design system gaps (typography scale, iconography, spacing).

**Exit:** the app is fully keyboard-operable, visually settled, and matches the product spec's density/calm goals across window sizes.

---

## Phase 6 — Reliability & Observability

- [ ] Unify the backoff/retry configuration across sync, send, and account-failure paths.
- [ ] Complete send retry states and failure UX (Retry, Edit and resend).
- [ ] Implement opt-in crash reporting with strict capture-time scrubbing.
- [ ] Implement structured local logging with rotation, size caps, and the same scrubbing rules.
- [ ] Implement account-level failure attribution in the UI.
- [ ] Add migration tests over fixture databases representing each prior schema version.

**Exit:** failures are predictable, attributable, and never lose user work; logs and reports never leak message content.

---

## Phase 7 — Packaging & Release

- [ ] Build the Flatpak manifest and declare portal permissions (keyring, tray, notifications).
- [ ] Test the keyring, tray, notifications, and message rendering under the Flatpak sandbox.
- [ ] Produce and validate the AppImage build.
- [ ] Author the AUR packages (`posthaste-bin` and source `posthaste`).
- [ ] Finalize app icon, desktop entry, and metadata.
- [ ] Establish the release checklist and versioning/tagging process.
- [ ] Run provider-specific manual test passes as a pre-release gate.
- [ ] Run the HTML regression corpus pass as a pre-release gate.

**Exit:** installable, updatable artifacts on Flathub, AppImage, and AUR, with pre-release gates exercised.

---

## Parallel Workstreams

### Visual Design System
- [ ] Define color tokens (background/surface/elevated/border, text tiers, single accent, brand-only logo color, destructive-only red).
- [ ] Define the typography scale and spacing rhythm.
- [ ] Define iconography and motion guidelines.
- [ ] Apply the design system before Phase 1 UI work begins, not after.

### Accessibility
- [ ] Evaluate WebKitGTK/GTK4 accessibility and focus semantics before committing to Phase 1 UI patterns.
- [ ] Define keyboard focus order and screen-reader expectations across panes.
- [ ] Treat accessibility as a constraint on every UI phase, not a late add-on.

### Documentation
- [ ] Maintain `AGENTS.md` with build/test/lint commands as they stabilize.
- [ ] Maintain a user-facing README and a contributor setup guide.
- [ ] Document the packaging/release process once Phase 7 lands.

### Ongoing Discipline
- [ ] Keep every dependency pinned; revisit the Wails version at each beta/RC/GA release.
- [ ] Keep the dependency table in `tech-specs.md` §13 the single source of truth.
- [ ] Keep scrubbing rules enforced at the point of capture in all new code paths.

---

## Phase Dependencies

- Phase 1 depends on Phase 0 only.
- Phase 2 depends on Phase 1 (account + sync foundations).
- Phases 3 and 4 are independent of each other and both depend on Phase 2.
- Phase 5 depends on Phases 1–4 for the views it polishes.
- Phase 6 can begin once Phase 2 lands and runs in parallel with 3–5.
- Phase 7 depends on Phase 5 (UI complete) and Phase 6 (stable behavior).
