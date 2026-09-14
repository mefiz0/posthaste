# Posthaste — Technical Specification

*Companion to the Product Specification. This document covers architecture and implementation decisions; it does not restate product rationale except where a technical choice is directly driven by a product principle.*

**Status:** 14 sections drafted (v1 scope). Open items and future-phase notes are marked inline where relevant.

**Naming note:** the project is named **Posthaste** — a historical mail term (an instruction written on urgent letters to carry them as fast as possible), chosen to reflect the product spec's "Fast" principle (§3). Availability confirmed on crates.io.

---

## 1. Architecture Overview

- **Language:** Rust
- **UI framework:** GPUI (Zed's UI library)
- **Async runtime:** tokio — de facto standard in the Rust ecosystem, mature IMAP/SMTP crate support (`async-imap`, `lettre`), and GPUI is runtime-agnostic so there's no conflict (§8.1)

### 1.1 High-Level Components

```
                         ┌───────────────────────┐
                         │      GPUI UI Layer     │
                         │  (docks, panes, tabs,   │
                         │   command palette)      │
                         └───────────┬────────────┘
                                     │ async channels (§8.3)
                                     ▼
                         ┌───────────────────────┐
                         │   Core Mail Engine      │
                         │ (account/message state, │
                         │  action queue, search)  │
                         └───┬───────┬────────┬────┘
                             │       │        │
                 ┌───────────┘       │        └───────────┐
                 ▼                   ▼                    ▼
        ┌────────────────┐  ┌───────────────┐   ┌──────────────────┐
        │ Sync Workers     │  │ Send Queue     │   │ Storage Layer     │
        │ (per-account,    │  │ Worker         │   │ (per-account       │
        │  IMAP IDLE/poll, │  │ (SMTP, retry/  │   │  SQLite + FTS5,    │
        │  §3)             │  │  backoff, §5)  │   │  shared attachment │
        └────────┬─────────┘  └──────┬────────┘   │  filesystem, §2)   │
                  │                   │            └──────────────────┘
                  ▼                   ▼
             IMAP servers        SMTP servers
```

- The **UI layer** never talks to IMAP/SMTP or SQLite directly — it interacts only with the core mail engine via async channels, keeping rendering decoupled from I/O (§8.3).
- The **core mail engine** owns in-memory state (recently viewed messages, pending action queue) and mediates between the UI, sync workers, send queue, and storage.
- **Sync workers** and the **send queue worker** are independent tokio tasks (§8.2), each with bounded concurrency (§8.4).
- **Storage** is split between per-account SQLite databases (message/account state + FTS5 search index) and a single shared, content-addressed attachment filesystem store (§2).

### 1.2 Scope for v1

- Protocols: plain IMAP/SMTP only (§6.1) — no provider-specific APIs.
- Platform: **Linux desktop, by product design** (product spec §4) — not a technical limitation. Several core dependencies (GPUI, `wry`, `keyring`) happen to already be cross-platform (macOS, Windows), but v1 implementation, testing, and packaging (§9) target Linux only. Cross-platform support is not currently in scope and would require its own packaging track (§9) and platform-specific path handling (§2.2, §7.5) rather than being a side effect of the libraries chosen.
- Deferred: attachment content search (§4.2) is designed for but not built in v1.

---

## 2. Storage Layer

### 2.1 Database

SQLite is the local mail store, chosen for:

- Mature Rust support (`rusqlite` / `sqlx`)
- Transactional writes — critical for the product's "reliable" principle, since sync writes (new messages, flag changes, moves) must be atomic so a crash mid-sync can't corrupt local state
- **FTS5** for full-text search over sender, recipients, subject, and full message body, directly serving the product spec's search goals without a separate search engine (full field list in §4.1)
- Precedent: similar Rust-based IMAP clients (e.g., Delta Chat) use SQLite for the same purpose

**WAL mode** is enabled by default to improve concurrent read/write behavior, since sync workers and the UI will both touch the database frequently.

**Schema scope: per-account SQLite files.** Each account gets its own database file rather than sharing one DB with an `account_id` column.

Rationale:
- Structural account isolation — matches the product spec's requirement that account separation stay "obvious enough to prevent accidental actions from the wrong account" (§6). Physical separation prevents cross-account leakage at the storage layer, not just via query discipline.
- Clean account removal: deleting an account is deleting a file, with no risk of orphaned rows in shared tables.
- Simpler per-account backup/export.

Trade-off: unified inbox and cross-account search (§6, §14 of the product spec) require querying multiple SQLite files and merging results in the app layer — via SQLite's `ATTACH DATABASE` for ad hoc joins, or fanning out queries per-DB and merging in Rust. This is a well-understood pattern and a lower cost than the alternative of shared-file account bleed.

Attachments remain in a single shared filesystem store (§2.2) regardless of per-account DB split, since content-addressing already allows safe sharing of identical attachment blobs across accounts.

### 2.2 Attachment Storage

Attachments are stored on the **filesystem**, not as SQLite blobs, with SQLite holding only metadata and a path reference.

**Rationale:**
- Attachments are opaque, immutable blobs — no partial reads or in-place updates needed, which is exactly the case where a filesystem is as good as or better than a database
- Content-addressed storage gives automatic deduplication: the same attachment forwarded/CC'd multiple times, or shared across accounts, is stored once
- Keeps the SQLite file small, which matters for `VACUUM`, backup/restore, and FTS5 performance
- Enables zero-copy reads and simple OS-level integration (e.g., "reveal in file manager", opening in default apps via file path)

**Layout:**
```
<app-data-dir>/attachments/<first-2-hash-chars>/<full-hash>
```
Resolved via the `dirs` crate (or equivalent) rather than a hardcoded path — on Linux this resolves to the XDG-conventional `~/.local/share/posthaste`, but using a resolution crate instead of a literal path string costs nothing today and avoids baking a Linux-only assumption into the storage layer, consistent with the platform note in §1.2. Sharding by hash prefix avoids one directory with millions of entries.

**Metadata table:**
```
attachments (
  id,
  message_id,
  filename,
  mime_type,
  size_bytes,
  content_hash,
  storage_path,
  last_accessed_at,
  fetch_state   -- e.g. 'not_fetched' | 'fetched'
)
```

**Garbage collection:** Since content-addressed files may be referenced by more than one message, deleting a message decrements a reference count rather than deleting the file directly. A periodic sweep removes files with zero references. This prevents both orphaned files and accidental deletion of a blob still in use elsewhere.

### 2.3 Attachment Fetch Policy (Eager vs. Lazy)

To balance the product requirement that offline users can "review attachments that are available locally" (product spec §8) against the practical concern that eagerly fetching every attachment can balloon local storage, the client uses a **hybrid policy**:

- **Small attachments are fetched eagerly** during normal sync — default threshold **2 MB**. This covers the common case (images, PDFs, short documents) that users expect to already be available when a message is opened, including offline.
- **Large attachments are fetched lazily** — metadata (filename, size, MIME type) is synced immediately via IMAP `BODYSTRUCTURE`, but the attachment body is only downloaded on explicit user action (tap/click to open or save). Once downloaded, it's cached locally using the same content-addressed store.
- **Threshold is user-configurable**, globally and per-account, fitting the product spec's account-level synchronization preferences (§15). Users on metered or slow connections can lower the threshold or disable eager fetch entirely; users on fast unmetered connections can raise it.
- **Cache eviction:** lazily-fetched attachments use `last_accessed_at` for optional LRU eviction after a configurable age/size cap, so the local store doesn't grow unbounded. Eagerly-fetched small attachments are not evicted by default, since they're assumed to be part of the "always available offline" baseline.

**Protocol note:** IMAP supports fetching message structure (`BODYSTRUCTURE`) independently of attachment bytes, so the sync engine can populate the attachments table with full metadata before deciding whether to fetch the body — this maps cleanly onto the eager/lazy split without extra protocol complexity.

### 2.4 Message & Folder Schema

Core tables in each per-account SQLite database:

```
folders (
  id,
  name,
  imap_path,          -- server-side folder path
  type                 -- e.g. 'inbox' | 'sent' | 'drafts' | 'archive' | 'trash' | 'user'
)

messages (
  id,
  folder_id,
  uid,                 -- IMAP UID, used for sync identity (§3)
  message_id_header,   -- RFC 5322 Message-ID, used for threading
  in_reply_to,         -- for thread/conversation grouping
  from_address,
  to_addresses,
  cc_addresses,
  subject,
  date,
  flags,               -- read/unread, starred, etc.
  raw_mime_path,       -- path to the raw MIME source on disk (see storage note below)
  body_text,           -- normalized plain-text extraction, used for display fallback and FTS5 indexing (§4.1)
  body_html            -- sanitized HTML body, used for rendering in the reading pane
)
```

**Raw message storage:** the original raw MIME source is stored on the filesystem (alongside attachments, §2.2, using the same content-addressed layout) rather than in a SQLite column — full messages can be large, and this keeps the SQLite file itself small and keeps `VACUUM`/backup fast, consistent with the reasoning in §2.2. `body_text` and `body_html` in the `messages` table are derived, normalized copies: `body_text` feeds FTS5 indexing (§4.1) and is used as a plain-text fallback in the reading pane; `body_html` is a sanitized version used for HTML rendering. The raw MIME file remains the source of truth and can be re-parsed to regenerate either derived field if needed.

`messages.folder_id` and `flags` are also what the sync engine's conflict resolution (§3.2) and offline action queue (§3.3) read and write against — the queue records intended changes to these fields before they're confirmed against the server.

---

## 3. Sync Engine

### 3.1 Fetching New Mail: IMAP IDLE with Polling Fallback

- Where the server supports it, the client uses **IMAP IDLE** as the primary mechanism for detecting new mail and remote state changes. This gives near-instant notification of changes, directly serving the product spec's "detect new messages promptly" (§7) and "quiet" (minimal unnecessary background activity/battery use) principles.
- Per RFC 2177 guidance, IDLE connections are **re-issued approximately every 29 minutes** to avoid server-side timeouts, and reconnected automatically on silent drops (detected via a heartbeat/no-op check).
- For accounts/providers where IDLE is unsupported or unreliable, the client falls back to **periodic polling**, default interval **5–10 minutes**, configurable per account (ties into product spec §15's synchronization preferences).
- Each account runs its own sync worker/task independently — one account's IDLE connection dropping or a slow provider doesn't block or delay sync for other accounts.

### 3.2 Conflict Resolution: Server Wins

Because IMAP has no native multi-way merge semantics, the sync engine adopts a simple, predictable default: **server state wins** on conflict. This applies specifically to **state conflicts** — flags (read/unread, starred), folder membership, and existence (deleted/moved) — not to actions still queued locally.

Behavior:
- **Locally queued actions are always attempted first** against the server on reconnect (e.g., a flag change made offline is sent once connectivity returns).
- If a queued action **can still apply cleanly** (e.g., the message still exists in the expected location), it's applied normally — this is not a "conflict," just a deferred write.
- If a queued action **can no longer apply cleanly** because remote state has diverged (e.g., the message was deleted or moved by another client while this client was offline), the queued action is **dropped**, remote state is treated as authoritative, and the user is informed via a lightweight, non-intrusive notice (not a blocking dialog) — consistent with the "quiet" principle and §9's guidance that notifications should be meaningful, not routine noise.
- This keeps the mental model simple for users: "if you're not sure what happened while you were offline, trust what the server says now" — while still making a good-faith attempt to preserve genuinely offline-made changes when they don't actually conflict with anything.

Note: this conflict policy is distinct from the **compose/send pipeline** (§5), where locally composed/queued outgoing messages are never discarded due to sync conflicts — a queued send is only affected by actual send failures, not by mailbox state changes.

### 3.3 Offline Queueing

- All user actions performed offline (flag, move, delete, archive, mark read/unread) are recorded as an **ordered, durable local action queue** in SQLite, not just applied optimistically to the in-memory UI state.
- On reconnect, queued actions are replayed **in order**, per account, against the server.
- Actions apply optimistically to the local UI/cache immediately (so the app feels instant per the "fast" principle), then get reconciled against server truth per §3.2 once the queue is flushed.

### 3.4 Retry and Backoff

- Failed sync operations (network errors, transient server errors) use **exponential backoff with jitter**, capped at a reasonable maximum interval (default: 5 minutes), to avoid hammering flaky connections or providers during outages.
- Persistent failures (e.g., repeated auth failures) stop retrying automatically and surface as an **account-level problem** (§6.4) rather than silently retrying forever in the background — consistent with the product spec's requirement that account failures be "clearly attributed to the affected account."
- This backoff policy (max interval, jitter, attempt cap before surfacing as a persistent failure) is defined as a **single shared config/constant**, not reimplemented separately per subsystem — it's reused as-is by the send pipeline's retry policy (§5.2) and account-level failure handling (§6.4), so the three don't drift out of sync with each other over time.

### 3.5 Threading / Conversation Grouping

Threading determines which messages belong to the same conversation, feeding the "Conversation/thread views" requirement in product spec §5 (Read) and the "Conversation" search criterion in §14.

**Algorithm:** based on the standard approach used by most mail clients (Thunderbird, Gmail) — commonly known as JWZ threading — rather than inventing something novel:

1. **Primary signal — `References`/`In-Reply-To` headers.** Every message's `References` header contains the full chain of ancestor `Message-ID`s in the conversation (more reliable than `In-Reply-To`, which typically only names the direct parent). The sync engine parses this into an ordered ancestor list per message.
2. **Linking:** as each message is ingested, its `Message-ID` and reference chain are used to attach it to existing messages sharing any ancestor. If earlier messages in the chain haven't been synced yet (e.g., they're in a folder not yet fetched, or predate the account being added), the message still threads correctly once those ancestors do arrive — see reconciliation, below.
3. **Fallback — subject grouping.** Some clients/mailing lists don't set `References` correctly. For messages with no usable reference chain, group by **normalized subject** (stripping `Re:`/`Fwd:`/`Re[2]:` prefixes, case-insensitive) within a reasonable date window, as a best-effort fallback — consistent with how JWZ handles "subject gathering."
4. **Cross-folder, not cross-account:** threading spans all folders within a single account's database (a reply in Sent threads correctly with the original in Inbox, or a message that's been archived) — trivial since both live in the same per-account SQLite file (§2.1). Threading does **not** span accounts, consistent with account isolation (§6.3) — a unified inbox view (§7.1) shows each account's threads separately rather than merging them.

**Reconciliation:** if a missing ancestor message arrives later (e.g., after a fuller sync, or a delayed message), previously-separate threads may need to be **merged** into one. This is handled as an explicit merge operation on `thread_id` (see schema below) — when a newly-ingested message's reference chain matches an existing `thread_id`, all messages under the new message's own previously-assigned thread (if any, e.g. from subject-fallback grouping) are re-pointed to the existing one.

**Materialization:** thread membership is **computed once at ingest time and stored**, not recomputed on every list-view render — this keeps message-list and conversation-view queries fast (serving the "fast" principle, product spec §3), at the cost of needing the reconciliation step above to keep stored state correct as new mail arrives.

**Schema additions to §2.4:**
```
messages (
  ...(existing fields)...
  references_header,   -- ordered list of ancestor Message-IDs, parsed from the References header
  thread_id             -- FK to threads.id; assigned/updated by the algorithm above
)

threads (
  id,
  subject_normalized,   -- for fallback grouping and display
  latest_date,           -- denormalized, for fast sort in thread-list views
  message_count          -- denormalized, for UI display without a COUNT query
)
```

`threads.latest_date` and `message_count` are updated incrementally as messages are linked/merged into a thread, rather than recomputed from scratch, to keep ingest cheap even for long-running threads.

---

## 4. Search

### 4.1 Indexing Strategy

Search is backed by **SQLite FTS5**, kept in sync with the `messages` table (§2.4) via triggers or explicit writes at ingest time (new mail, edited drafts) rather than a separate rebuild pass — so the index stays current without a noticeable delay, serving the "search should feel instant" goal in product spec §14.

**v1 scope — indexed fields:**
- Sender (name + address)
- Recipients (To/Cc)
- Subject
- **Full message body** (`body_text` from §2.4; HTML bodies are stripped to text before indexing, `body_html` is not indexed directly)
- Folder/label (for filtering, not full-text matched)

Each per-account SQLite database (§2.1) has its own FTS5 index. Cross-account search fans out the same query across each account's DB and merges/ranks results in the app layer — consistent with the per-account storage decision.

### 4.2 Attachment Content Search (Future Phase)

Not in v1, but the schema and fetch policy in §2.3 are designed to not block this later:

- Once an attachment is fetched (eagerly or lazily — §2.3), a background extraction pass can pull text from common formats (PDF, DOCX, plain text; scanned/image-based PDFs would need OCR, which is a heavier dependency and likely a separate opt-in stage).
- Extracted text would be indexed in a **separate FTS5 table** keyed by `content_hash` (not `message_id`), so a deduplicated attachment is only extracted and indexed once, even if it appears in multiple messages — reusing the same content-addressing benefit described in §2.2.
- Because this only operates on locally-fetched attachments, it naturally respects the user's eager/lazy fetch threshold and any connectivity/storage constraints — a lazily-fetched large attachment simply isn't searchable until it's been opened at least once, which is a reasonable, unsurprising limitation to communicate to users.
- This phase is deferred to avoid coupling v1's search feature (already a heavily-relied-on capability per §14 of the product spec) to a more complex, dependency-heavy extraction pipeline.

### 4.3 Query Interface

- Supports simple free-text queries by default (matching product spec's requirement that search be approachable without needing advanced syntax knowledge).
- Structured filters (sender, date range, folder, has-attachment) are exposed as UI affordances layered on top of the FTS5 query, rather than requiring users to learn search operators — though a lightweight operator syntax (e.g., `from:`, `has:attachment`) can be supported for power users without being required.
- Results are ranked using FTS5's built-in BM25 relevance scoring, with recency as a secondary factor, since "the contract email from Sarah last year" (per the product spec's framing example) implies both relevance and rough recency matter.

---

## 5. Compose / Send Pipeline

### 5.1 Send States

Every outgoing message moves through an explicit state, persisted in SQLite (so it survives app restarts, matching §11's requirement that drafts/sends never silently get lost):

`draft` → `queued` → `sending` → `sent` | `failed`

These map directly onto the four states the product spec calls for in §11 (successfully sent, waiting to send, failed to send, retrying).

### 5.2 Retry Policy

- On send failure, the client **automatically retries with exponential backoff**, using the same shared backoff config as sync and account-failure handling (§3.4) — capped at 5 minutes between attempts by default — since most failures are transient (brief network drop, server hiccup) and resolve on their own.
- Automatic retry continues for up to **~24 hours or 10 attempts, whichever comes first** — a send-specific ceiling on top of the shared backoff config, since an unsent message warrants a longer overall retry window than a background sync reconnect. During this window the message shows as `queued`/`retrying` in the UI — a passive status, not an interruptive prompt, consistent with the "quiet" principle.
- **Permanent failures** (e.g., SMTP 5xx rejection for an invalid recipient, authentication failure) are detected and fail immediately rather than exhausting the full retry window — there's no reason to wait 24 hours to tell a user their recipient address was rejected.
- After the retry window is exhausted (or a permanent failure is detected), the message moves to an explicit **`failed`** state:
  - A real, meaningful notification is surfaced (this qualifies under §9's "meaningful events," unlike routine sync activity).
  - The user gets manual actions: **Retry** or **Edit and resend**.
  - The message is never silently discarded — it remains visible and actionable until the user resolves it, protecting the user's composed work per §11.

### 5.3 Drafts

- Drafts are persisted to SQLite on every meaningful edit (debounced, not on every keystroke, to avoid excessive writes) rather than only on explicit "Save Draft" — this protects against data loss from a crash or accidental close, per §11's directive to treat drafts as valuable work.
- Drafts are account-scoped (living in that account's SQLite file, §2.1) and appear in a Drafts folder consistent with the account's actual IMAP Drafts folder where applicable — optionally synced to the server's Drafts folder so drafts are visible from other clients too, though local draft persistence does not depend on that sync succeeding.

### 5.4 Relationship to Sync Conflicts

The send pipeline is **independent of the sync conflict policy in §3.2** — a queued outgoing message is never dropped or overridden due to mailbox state changes (e.g., the draft it was composed from being modified/deleted elsewhere). Only actual send failures affect a queued message's fate.

---

## 6. Account Management

### 6.1 Protocol Scope (v1)

Plain **IMAP/SMTP only** for v1 — no provider-specific data APIs (Gmail API, Microsoft Graph, etc.). This keeps the sync engine protocol-uniform and avoids forking sync/label logic per provider before the core client is solid. Revisit if a strong need emerges (e.g., provider-specific push notifications, label semantics that don't map cleanly onto IMAP folders).

**OAuth2 authentication is in scope for v1**, despite the above — this is a different axis than the data-protocol decision. Several major providers (Gmail, Microsoft 365/Outlook) have deprecated plain password authentication for IMAP entirely and require the standard `XOAUTH2`/`OAUTHBEARER` SASL mechanism to authenticate, even though the underlying mail protocol remains ordinary IMAP/SMTP. Without OAuth2 support, the client simply cannot connect to two of the most common providers users will have, so this isn't optional scope — see §6.6.

### 6.2 Credential Storage

Credentials (passwords, or OAuth tokens for providers that require them — see §6.1, §6.6) are stored in the **OS-native credential store**, not in SQLite or any custom encrypted file, via the `keyring` crate — which is itself cross-platform (Secret Service/`libsecret` on Linux, Keychain on macOS, Credential Manager on Windows), though v1 implementation and testing target the Linux backend only (§1.2). On Linux specifically, this means GNOME Keyring or KWallet depending on desktop environment.

Rationale:
- Avoids the client having to implement and audit its own encryption-at-rest for secrets — the OS keyring is a well-tested, purpose-built component already present on the target platform.
- Matches user expectations on Linux desktops, where credential prompts and keyring unlock are already familiar patterns.
- Keeps SQLite files (which may be backed up, synced, or inspected by the user) free of sensitive credential material — only non-sensitive account metadata (email address, server settings, sync preferences) lives in SQLite.

SQLite account table holds only: account id, email address, IMAP/SMTP server settings, sync preferences (§2.3 threshold, poll interval), and a reference key used to look up the credential in the OS keyring — never the credential itself.

### 6.3 Account Isolation

Each account maps to its own SQLite database file (§2.1) and its own independent sync worker (§3.1), so one account's failure, slow provider, or credential issue cannot block or degrade another account's sync.

### 6.4 Account-Level Failure Attribution

Per product spec §15 ("account failures should be clearly attributed to the affected account"), authentication failures, repeated connection failures, or credential issues surface as a problem scoped to that specific account in the UI — not as a generic app-wide error — and stop retrying automatically using the **shared backoff/attempt-cap config defined in §3.4**, requiring explicit user action (e.g., re-entering credentials) to resume.

### 6.5 Account Preferences

Stored per account in SQLite: signature, notification settings, sync/poll interval, attachment eager-fetch threshold (§2.3), default send-from status (for unified compose), and pause/active state.

### 6.6 Account Setup Flow

Serves product spec §15's requirement that adding an account be "simple and guided" — the user should not need to know their provider's IMAP hostname or port.

**Flow:**
1. **User enters email address only.**
2. **Autodiscovery is attempted**, in order:
   - **Known-provider presets** — hardcoded IMAP/SMTP settings (and auth requirements) for the handful of providers covering the large majority of users: Gmail, Outlook/Microsoft 365, Yahoo, iCloud. Matched by domain (including custom domains routed through Google Workspace/Microsoft 365, detected via MX record lookup where the address's domain isn't directly one of the above).
   - **DNS SRV records** (`_imap._tcp`, `_submission._tcp`, per RFC 6186) — standard mechanism for domains that publish autoconfiguration this way.
   - **Mozilla ISPDB** (Thunderbird's public autoconfig database, keyed by domain) — a well-established, free, third-party source of provider configs that avoids reinventing this database from scratch.
   - **Common hostname patterns** (`imap.<domain>`, `mail.<domain>`) as a last-resort guess, verified by an actual connection attempt before being trusted.
3. **Auth method is determined by the matched provider/preset:** known OAuth-requiring providers (Gmail, Microsoft 365 — §6.1) trigger the OAuth flow (step 4a); everything else falls through to password entry (step 4b).
4. **Credential collection:**
   - **(4a) OAuth2 flow:** opens the provider's consent screen in the user's default browser (not an embedded webview — avoids the client ever handling the user's actual provider password, and avoids adding another use of an embedded browser beyond §7.6's already-scoped one). Uses a local loopback HTTP listener for the redirect/callback, standard for native-app OAuth flows. The resulting access + refresh tokens are stored in the OS keyring (§6.2); the refresh token is used to silently re-authenticate sync workers as access tokens expire, without repeated user prompts.
   - **(4b) Password/app-password flow:** standard IMAP/SMTP password entry, stored via the OS keyring (§6.2). For providers that require an app-specific password (distinct from OAuth) when 2FA is enabled, the setup UI surfaces a short explanatory note with a link to that provider's app-password documentation, rather than leaving the user to guess why their regular password fails.
5. **Manual fallback:** if autodiscovery fails entirely, the user is dropped into a manual configuration screen (host, port, encryption type, username) — pre-filled with whatever best-guess values autodiscovery produced, so the user is correcting fields rather than starting blank.
6. **Verification before saving:** the client performs a live test connection (IMAP login + folder listing) before the account is considered added — a broken account should never be silently saved and only surfaced as an error later during background sync.

Autodiscovery results (matched preset or resolved settings) are cached only for the duration of setup — they are not treated as permanent truth, since a provider's configuration can change; the account's stored settings (§6.2's account table) are what sync actually uses going forward.

---

## 7. UI Architecture

### 7.1 Layout Model

Following Zed's own panel/dock model (since GPUI is built around it natively):

- **Left dock:** account list, folders/labels, unified inbox entry — collapsible, matching Zed's project-panel convention. Supports multiple accounts stacked or grouped, with clear visual account separation (per product spec §6).
- **Center pane:** message list + reading pane, either side-by-side (wide window) or stacked (narrow window), similar to how Zed adapts editor layout to available space.
- **Tabs:** open messages (and search/settings views) are tabs, not modal windows — consistent with Zed's tab model. Composing opens in a **right-side compose drawer** that overlays the center pane (with minimize and fullscreen modes); drafts behave like unsaved buffers inside it (dirty-state indicator, protected against accidental close per §11's draft-protection requirement), and the inbox stays visible while writing. (Updated v1.1 per mockup review; supersedes the earlier compose-as-tab wording.)
- **Right dock (optional/collapsible):** contextual info — e.g., full thread view, attachment previews. Closed by default to preserve the "visually calm," density-without-clutter goal from product spec §12; opens automatically when viewing a message that has a multi-message thread or attachments worth previewing, and can otherwise be toggled manually (consistent with the command palette/keymap approach in §7.3).
- **Command palette:** a Zed-style fuzzy command palette (`Cmd/Ctrl+Shift+P` convention) exposes actions like "Archive," "Search," "Switch account," "Compose new" — reinforcing the keyboard-first principle (§13) without requiring users to memorize every individual shortcut.

### 7.2 Component Structure

- Built as composable GPUI views/elements, mirroring Zed's own internal separation of panels, panes, and tabs as independent, reusable components rather than one monolithic window view.
- Each dock/pane manages its own scroll and focus state independently, so keyboard navigation (§13) can move focus between panes (e.g., folder list → message list → reading pane) predictably, matching patterns users may already know from Zed's file-tree-to-editor focus flow.

### 7.3 Keyboard Shortcut System

- A centralized, declarative keymap (GPUI supports keybinding definition similar to Zed's `keymap.json` approach) rather than shortcuts hardcoded per-component — this directly enables §13's requirement that shortcuts be **discoverable and configurable**.
- Default keymap covers the actions listed in product spec §13 (navigate, open, archive, delete, mark read/unread, search, reply, forward, compose, move, switch accounts, navigate conversations).
- Users can view and rebind shortcuts through a settings UI, consistent with Zed's own keymap customization model.

### 7.4 Notifications

- Native OS desktop notifications (not custom in-app popups) for the meaningful events defined in product spec §9 — new mail, important mail, send failures, account problems.
- Per-account notification toggles are stored in account preferences (§6.5); the global notification toggle lives in the global app settings introduced in §7.5, alongside other app-wide (non-account-scoped) preferences.
- No notification is generated for routine sync activity — only for the explicitly meaningful events listed above, per the "quiet" principle.

### 7.5 System Tray / Minimize Behavior

- The app can minimize to the system tray instead of closing or occupying the taskbar — an **opt-in setting**, off by default, stored as a **global app preference** (not per-account, since it's a window-management behavior, not an account behavior — see note on global settings below).
- Implemented via the `StatusNotifierItem`/`KStatusNotifierItem` D-Bus protocol (the modern Linux tray standard, supported natively by KDE, XFCE, and most non-GNOME desktop environments), most likely through the `ksni` or `tray-icon` Rust crate rather than a hand-rolled D-Bus integration.
- **GNOME caveat:** GNOME has no native tray support without a user-installed extension (e.g., AppIndicator/KStatusNotifierItem extension). The setting should still be exposed on GNOME, but if tray integration is unavailable at runtime, the app should **detect this and fall back gracefully** — e.g., minimizing to taskbar instead of silently doing nothing — rather than leaving the user with an app that seems to have vanished. This matters directly for the "reliable"/"quiet" principles: a feature that silently fails to do what the user asked breaks trust.
- When minimized to tray, the tray icon should reflect basic state (e.g., unread count badge or icon variant) so background presence still communicates something meaningful, rather than being purely decorative — consistent with §9's guidance that background activity should be unobtrusive but new-mail state should still be visible.
- Tray icon exposes a minimal context menu: show window, compose new, quit. Left-click (or the platform-conventional action) restores the main window.
- Closing the main window when this setting is enabled minimizes to tray rather than quitting; an explicit **Quit** action (menu, tray context menu, or keymap) is required to actually exit — this distinction should be clearly discoverable, since silently redefining what the close button does is exactly the kind of ambiguity the product spec's "quiet"/"user-controlled" principles warn against.

**Global app settings note:** this and the global notification toggle (§7.4) are app-wide rather than per-account (§6.5's Account Preferences table is account-scoped). Global settings like these live in a small separate config store (e.g., `settings.toml` or a dedicated `global.sqlite`) under the same `dirs`-resolved app config directory as §2.2 (XDG-conventional `~/.config/posthaste` on Linux) rather than inside any per-account database, since they need to exist and be readable before any account is even configured.

### 7.6 Message Rendering & Sanitization

The reading pane renders HTML message bodies (`body_html`, §2.4) via a **sandboxed embedded webview**, using the `wry` crate — itself cross-platform (WebKitGTK on Linux, WKWebView on macOS, WebView2 on Windows), though v1 targets the Linux/WebKitGTK path only (§1.2). On Linux, WebKitGTK is a system library already present on most desktops (used by GNOME Web, Evolution, and many other GTK apps), not a bundled browser engine like Electron/Chromium. This gives accurate rendering of real-world HTML email (nested tables, inline styles, Outlook-specific markup), which a constrained custom renderer would handle imperfectly for a large fraction of everyday email.

**Sandboxing policy (non-negotiable defaults, not user-configurable toggles):**
- **JavaScript disabled entirely** in the message webview — HTML email has no legitimate need for script execution, and disabling it removes an entire class of attack surface.
- **Remote content (images, fonts, etc.) blocked by default** — remote images are a common tracking-pixel vector; blocking them by default protects user privacy (product spec §16) without waiting on any explicit setting. A per-message "load remote content" affordance lets the user opt in when they trust the sender.
- **Navigation restricted** — clicking a link opens the OS default browser, not an in-place navigation within the reading pane's webview. The webview renders one message body; it is not a general-purpose browser surface, consistent with product spec §17 explicitly ruling that out.

**View switching — HTML / Plain Text tabs:**
- The reading pane shows the message with two tabs: **HTML** (rendered via the sandboxed webview, described above) and **Plain Text** (rendered natively via `body_text`, §2.4 — fast, no webview involved at all).
- **HTML is the default active tab** when a message has an HTML body; Plain Text is available as a one-click switch for any message, and is the only option for messages that are plain-text-only to begin with.
- Because Plain Text uses the already-stored `body_text` extraction and involves no webview, switching to it is instant and has zero rendering/security surface — useful as a fallback for suspicious or slow-rendering messages, and consistent with keeping the default experience fast (product spec §3) even though the default *view* uses a heavier rendering path.
- Only one webview instance is active per open message tab (§7.1); switching away from a message tab or to Plain Text can tear down its webview rather than keeping it resident, bounding total resource use in line with §8.4's concurrency/resource limits.

---

## 8. Concurrency Model

### 8.1 Runtime

**tokio** is the async runtime for all I/O-bound work — IMAP/SMTP connections, sync workers, send retries. GPUI itself is UI-framework-level and runtime-agnostic, so it coexists with tokio without conflict; GPUI's own executor handles UI-thread scheduling while tokio handles background async work.

### 8.2 Task Structure

- **One sync worker task per account** (§3.1, §6.3), each owning its own IMAP connection/IDLE loop, independent of other accounts — a slow or failing account cannot stall others.
- **A separate send-queue worker** processes the outgoing message queue (§5), independent of sync workers, so a stalled sync doesn't block sends and vice versa.
- **Attachment fetch tasks** (§2.3) run as short-lived, on-demand tokio tasks triggered by explicit user action (lazy fetch) or inline during sync (eager fetch under threshold), bounded by a concurrency limit to avoid saturating the connection or disk I/O.

### 8.3 UI Thread / Background Worker Communication

- Background tokio tasks never touch GPUI state directly. State changes (new mail arrived, send succeeded/failed, sync status changed) are communicated to the UI layer via channels (e.g., `tokio::sync::mpsc` or GPUI's own async context bridging, where available), and the UI layer re-renders reactively off that state — keeping a clean boundary between async I/O and UI rendering, and avoiding the class of bugs where background work races with UI state mutation.
- All SQLite writes happen off the UI thread; the UI reads either from an in-memory cache kept current by the sync layer, or via async queries dispatched to a dedicated SQLite worker/connection pool — never blocking the UI thread on disk I/O, which is central to the "fast" principle (§3 of the product spec: navigation should feel instantaneous).

### 8.4 Backpressure and Resource Bounds

- Sync, send-retry, and attachment-fetch tasks are all subject to bounded concurrency (e.g., via `tokio::sync::Semaphore`) so that, for example, adding many accounts at once or opening many large attachments doesn't spawn unbounded parallel network/disk work and degrade the "fast"/"quiet" experience for whatever the user is actively doing in the foreground.

---

## 9. Packaging & Distribution

**Scope note:** this section is Linux-only, consistent with the v1 platform scope (§1.2). It is not written to generalize to other platforms — macOS distribution would need `.dmg`/notarization, Windows would need `.msi`/code-signing, and neither is planned or estimated here.

### 9.1 Distribution Formats

**Primary: Flatpak (Flathub).** Recommended as the flagship distribution channel:
- Discoverability via Flathub, which is now the de facto app-store experience across most major Linux desktops.
- Automatic updates handled by the packaging system itself rather than a custom in-app updater — see §9.3.
- Sandboxing aligns with the product spec's privacy principle (§16) and pairs naturally with the security posture already established for the HTML webview (§7.6) — the app is sandboxed at the OS level, not just within its own webview.

**Secondary: AppImage.** Offered as a no-install, portable fallback for users who prefer to avoid the Flatpak runtime or want a single-file binary. No sandboxing by default, simpler build (one artifact, no manifest/portal permissions to maintain), but update mechanism (§9.3) and system integrations (§9.2) need separate handling from the Flatpak build.

**Native `.deb`/`.rpm`:** lower priority, likely community-maintained rather than a primary release artifact — the packaging/dependency burden of tracking multiple distro-specific WebKitGTK versions (relevant to §7.6) across native package formats is significant, and Flatpak/AppImage already cover the practical distribution need.

**AUR (Arch User Repository):** worth an official presence given the audience overlap — Arch and Arch-derived distros (Manjaro, EndeavourOS) skew heavily toward the technical, Linux-first users the product spec targets (§4). Two package variants are typical and worth offering:
- **`posthaste-bin`** — fetches the prebuilt release binary (fastest to install, no local compilation), likely the better default for most users.
- **`posthaste`** — builds from source via a `PKGBUILD`, for users who prefer building locally or want to track a specific commit.

Unlike Flatpak, AUR packages run **unsandboxed** as normal native processes — no `xdg-desktop-portal` concerns apply (§9.2), so keyring/tray/webview integration behaves the same as running the raw binary directly. Maintenance is lighter than full `.deb`/`.rpm` packaging (a `PKGBUILD` is a small, declarative build script, not a multi-distro pipeline), making it more feasible to maintain officially rather than leaving entirely to the community, though it can also function as a community-maintained package if that's preferred.

### 9.2 System Integration Under Sandboxing

Three previously-specified subsystems depend on D-Bus and need explicit handling under Flatpak's sandbox, via `xdg-desktop-portal`:

- **OS keyring (§6.2):** Secret Service access works within Flatpak via the portal (`org.freedesktop.portal.Secret` or proxied D-Bus access to `org.freedesktop.secrets`), but requires the correct permission declared in the Flatpak manifest — this needs explicit testing against GNOME Keyring and KWallet, not assumed to work identically to the unsandboxed case.
- **System tray (§7.5):** `StatusNotifierItem`/D-Bus tray access also needs to be proxied through the sandbox; this is a well-trodden path (many Flatpak apps ship tray icons) but still needs the corresponding manifest permission and testing on both KDE and GNOME+extension setups.
- **Sandboxed webview (§7.6):** WebKitGTK itself runs fine inside Flatpak (GNOME Web ships this way), but combining an *already-sandboxed webview* with an *already-sandboxed Flatpak process* means two layers of sandboxing to reason about — worth explicit testing to confirm the webview's own JS-disabled/no-remote-content policy isn't relying on anything the outer Flatpak sandbox restricts differently.

The AppImage and AUR builds have none of these portal concerns (both run as normal unsandboxed processes), which somewhat simplifies those build targets at the cost of the sandboxing benefit described in §9.1.

### 9.3 Update Mechanism

**No custom in-app auto-updater.** Updates are handled entirely by the packaging format:
- Flatpak: `flatpak update`, or automatic background updates if the user's desktop environment has that enabled (e.g., GNOME Software).
- AppImage: `AppImageUpdate`/zsync-based delta updates, run by the user or a desktop-integration tool, rather than the app itself checking for and downloading updates.

This is a deliberate choice, not an omission: building a custom updater means running another background service/network check, which cuts against the "quiet" principle (§9 of the product spec explicitly warns against the app doing unprompted background activity) and the "user-controlled" principle — letting the OS/packaging system own updates keeps that responsibility where the user already expects it to live.

---

## 10. Telemetry, Crash Reporting & Logging

### 10.1 Telemetry Policy: None

The client collects **no usage telemetry or analytics of any kind** — not opt-in, not anonymized, not aggregated. No event tracking, no feature-usage counters, no "phone home" of any form.

This is a deliberate positioning choice, not just a privacy default: product spec §16 frames the core relationship as **User ↔ Email Provider**, explicitly rejecting a **User ↔ Email Client Company ↔ Email Provider** model, and §18's differentiator explicitly rules out "unnecessary services." Even *opt-in* analytics infrastructure is a service the app runs on its own initiative that most competitors treat as a given — not including it at all is part of what makes this product distinct, not an oversight to revisit later.

### 10.2 Crash Reporting: Opt-In, Separate from Telemetry

Crash reporting is treated as **categorically distinct from analytics/telemetry** — it exists to serve the "reliable" principle (product spec §3), not to gather usage data — and is handled accordingly:

- **Off by default.** Nothing is ever sent automatically. The user must explicitly enable crash reporting in settings (stored as a global app setting, §7.5) before any crash data leaves the device.
- **When enabled**, a crash report is sent automatically on the next crash without a per-crash prompt (since the user already consented when enabling it) — avoiding a "send this crash report?" dialog every time, which would be both interruptive and, ironically, itself a small telemetry-like decision point repeated constantly.
- **Strict scrubbing, non-negotiable regardless of settings:** a crash report contains only the Rust panic message/location, OS and architecture, app version, and a sanitized stack trace. It **never** includes message bodies, subjects, sender/recipient addresses, attachment filenames, or credentials/tokens — these are stripped at the point of capture, not redacted after the fact, so there's no path for them to leak into a report even accidentally.
- The settings UI describes what a crash report actually contains before the user enables it, so consent is informed rather than a blind toggle.

### 10.3 Local Logging

Local, on-device logging is **always on** (independent of the crash-reporting setting above) and serves the user's own troubleshooting — e.g., manually attaching a log excerpt when filing a bug report — with **zero network involvement** by default.

- Structured logs (e.g., via the `tracing` crate) are written locally, rotated and size-capped so they don't grow unbounded.
- **The same scrubbing rules as §10.2 apply to local logs**, not just remote crash reports: message bodies, subjects, attachment filenames, and credentials/tokens are never written to logs in plaintext. Where an identifier is genuinely needed for debugging (e.g., correlating a sync failure to a specific account), the account's internal ID is logged rather than its email address.
- Log verbosity is user-configurable (e.g., a "verbose logging" toggle for temporarily capturing more detail when actively troubleshooting an issue with support), defaulting to a minimal level in normal operation.

---

## 11. Testing Strategy

### 11.1 Unit Tests

Pure-logic components get straightforward unit tests, independent of any network or UI:
- **Threading algorithm** (§3.5) — reference-chain linking, subject-fallback grouping, and thread-merge/reconciliation, tested against constructed message sets including deliberately malformed/missing `References` chains.
- **Sync conflict resolution** (§3.2) — the "server wins with queued-action replay" logic, tested against constructed local-queue/remote-state scenarios (clean replay, dropped action due to divergence, etc.).
- **Backoff/retry policy** (§3.4, reused by §5.2/§6.4) — timing and cap behavior tested deterministically (mocked clock, not real sleeps).
- **Search query parsing** (§4.3) — operator syntax (`from:`, `has:attachment`) parsed correctly, and gracefully falls back to plain free-text when no operators are present.
- **MIME/HTML parsing and scrubbing** (§2.4, §10.2/§10.3's scrubbing rules) — verifying scrubbing rules actually strip what they claim to, since a scrubbing bug is a privacy bug, not just a correctness bug.

### 11.2 Integration Tests: Real IMAP/SMTP, Not Mocks

**Mocking IMAP is deliberately avoided as the primary integration-testing strategy.** A large fraction of real-world IMAP bugs come from quirky, non-compliant, or edge-case server behavior (partial `FETCH` responses, `UIDVALIDITY` changes, non-standard folder separators) that a hand-rolled mock server won't faithfully reproduce — testing against a mock mainly proves the code handles the mock, not real mail servers.

Instead:
- **CI runs a real Dovecot + Postfix instance in Docker**, giving genuine IMAP/SMTP protocol behavior to test against (IDLE, folder operations, flag changes, UID handling) without depending on external network access or third-party provider accounts.
- **Sync engine tests** (§3.1–3.4) run against this real server: simulate offline queueing by disconnecting the container mid-test, verify reconnection/IDLE re-issue behavior, and verify the conflict-resolution policy (§3.2) against genuine server state changes rather than simulated ones.
- **Provider-specific quirks** (e.g., Gmail's non-standard folder semantics, OAuth requirements from §6.1/§6.6) can't be fully covered by a local Dovecot instance — these are covered by a smaller set of manual/exploratory test passes against real provider accounts before release, rather than full CI automation, given the practical difficulty of automating OAuth consent flows in CI.

### 11.3 Fuzz/Property-Based Testing for Untrusted Input

Email content is fundamentally untrusted input, and this matters most for two specific paths:
- **MIME parsing** — fuzz-tested (e.g., via `cargo-fuzz`) against malformed/malicious MIME structures, since a parser crash or memory-safety issue triggered by a received email is a real attack surface.
- **HTML sanitization/scrubbing** (§7.6, §10.2/§10.3) — property-based tests asserting that no scrubbed output ever contains script tags, remote resource references (when blocking is active), or the specific content categories §10 promises are never logged/reported.

### 11.4 HTML Rendering Regression Corpus

Automated testing can't fully validate visual rendering quality across the huge variety of real-world email HTML (§7.6). Instead: maintain a **curated corpus of representative sample emails** — common patterns like nested-table layouts, Outlook conditional comments, inline `cid:` images, and tracking-pixel-style remote images — used as a manual/visual regression pass before releases, rather than pixel-diffed automated tests, which tend to be brittle against a rendering engine (WebKitGTK) that isn't fully within this project's control.

### 11.5 UI/Component Tests

GPUI component tests follow the same patterns Zed itself uses for testing GPUI views (headless rendering harness, state-driven assertions on view output) rather than inventing a separate UI testing approach — reusing established patterns here is lower-risk than designing new ones from scratch for an unfamiliar framework.

### 11.6 CI Structure

- **Fast unit tests** (§11.1) run on every push — no network, no containers, sub-second-to-seconds total runtime.
- **Integration tests** (§11.2) against the Dockerized Dovecot/Postfix instance run on every PR, slower but still fully automated and hermetic (no external network dependency).
- **Fuzz tests** (§11.3) run on a scheduled basis (e.g., nightly) rather than blocking every PR, given their longer runtime.
- **HTML regression corpus** (§11.4) and provider-specific manual passes (§11.2) are pre-release gates, not per-PR CI checks.

---

## 12. Schema Migrations

The schema defined across this document (`messages`/`folders`/`threads` in §2.4, the account table in §6.2, attachment metadata in §2.2) will inevitably evolve across releases. Each per-account SQLite database (§2.1) needs an explicit, safe migration path rather than assuming schema changes are handled ad hoc.

### 12.1 Approach

- **`refinery`** manages migrations against each per-account SQLite file, using its built-in schema-version tracking table rather than a hand-rolled version scheme.
- Since accounts are isolated into separate files (§2.1), **each account's database is migrated independently** — on app startup, every configured account's DB is checked against the current expected schema version and brought up to date before that account's sync worker (§3.1) starts.
- The global settings store (§7.5) follows the same pattern if implemented as `global.sqlite`; if implemented as a flat TOML file instead, "migration" there just means writing new fields with sensible defaults when parsing an older config, rather than running SQL migrations.

### 12.2 Safety

- **Each migration runs inside a transaction**, so it either fully applies or fully rolls back — no user ends up with a half-migrated, inconsistent database from an interrupted upgrade (app crash or forced quit mid-migration).
- **The per-account database file is copied/backed up before migration runs** on a detected version upgrade, so a migration bug doesn't put a user's local mail store at risk of unrecoverable corruption — directly serving the "reliable" principle (product spec §3) for the one operation most likely to touch every stored message at once.
- **Migrations are additive wherever feasible** (new columns/tables rather than destructive drops/renames), and application queries use explicit column lists rather than `SELECT *`, so the codebase stays resilient to schema growth over time rather than brittle to it.
- **Downgrades are explicitly unsupported.** If a user rolls back to an older app version after their database has been migrated forward, that's a known unsupported path — the pre-migration backup (above) is the recovery mechanism, not a formal downgrade migration.

### 12.3 Testing

Per §11.1, migrations get their own test coverage: fixture databases representing each prior schema version are checked into the test suite, and migration tests assert that upgrading each fixture to the current schema preserves all existing data correctly (no silently dropped messages, flags, or thread associations) — this is exactly the kind of change where a subtle bug could cause quiet data loss rather than a visible crash.

---

## 13. Dependency List

Consolidating the crate choices referenced individually throughout this document, plus the one previously-open decision (SQLite access library) resolved here alongside the migration tooling choice in §12.1:

| Purpose | Crate | Reference |
|---|---|---|
| Async runtime | `tokio` | §8.1 |
| UI framework | `gpui` (via `gpui-unofficial`, see §13.0.1) | §1, §7 |
| SQLite access | `rusqlite` (sync API, called via `spawn_blocking` from async contexts — SQLite operations are fast enough that this is simpler and more mature than fighting an async SQLite wrapper) | §2 |
| Schema migrations | `refinery` (pairs directly with `rusqlite`) | §12.1 |
| Full-text search | SQLite `FTS5` (built into SQLite, no separate crate) | §4 |
| IMAP client | `async-imap` | §3, §6.1 |
| SMTP client | `lettre` | §5, §6.1 |
| MIME parsing | `mail-parser` (or equivalent) | §2.4, §11.1, §11.3 |
| Embedded webview | `wry` | §7.6 |
| OS credential storage | `keyring` | §6.2 |
| System tray | `tray-icon` or `ksni` | §7.5 |
| App directory resolution | `dirs` | §1.2, §2.2, §7.5 |
| Structured logging | `tracing` | §10.3 |
| Fuzz testing | `cargo-fuzz` | §11.3 |
| GPUI component/widget kit | `gpuikit` | §7 |

This table is the single source of truth for dependency choices — individual sections reference it rather than re-justifying crate selection inline going forward.

### 13.0.1 GPUI Sourcing

Zed does not maintain official, current `gpui` releases on crates.io — the last official publish is stale (`0.2.2`), and the real, actively-developed GPUI lives only inside Zed's own monorepo. Since there is no current official crate to depend on, Posthaste depends on `gpui-unofficial` — a third-party auto-published mirror that tracks Zed's actual git history — via Cargo's package-rename mechanism so the codebase still refers to it as `gpui`:

```toml
[dependencies]
gpui = { package = "gpui-unofficial", version = "1.14" }
gpui_platform = { package = "gpui-platform-gpui-unofficial", version = "1.14", features = ["font-kit"] }
gpuikit = "0.9"
```

**This is a deliberate choice given the constraint, not an oversight to revisit:** with no official crates.io release to fall back to, the practical alternatives are (a) this mirror, or (b) a git dependency pinned directly to `zed-industries/zed`. The mirror is used here for ordinary semver-versioned dependency resolution and reproducible builds; a git dependency remains the fallback if the mirror maintainer stops publishing or a specific commit-level pin becomes necessary. Either way, this is a **single-maintainer supply-chain dependency** for the application's foundational UI framework — worth revisiting periodically (e.g., each major version bump) to confirm the mirror is still actively maintained, rather than treating the initial choice as permanent.

### 13.1 Minimal Contact Handling (Compose Autocomplete)

Compose (§5) needs recipient autocomplete, but the product spec explicitly rules out becoming a CRM or contact-management suite (§17). The scope here is intentionally minimal:

- **No user-facing "contacts" feature, no editable address book UI.** Contacts are never something the user adds, edits, or manages directly.
- A lightweight `contacts` table per account is derived implicitly from message history — populated from `From`/`To`/`Cc` addresses seen in synced mail (§2.4), tracking just `email`, `display_name` (most recently seen), `last_used_at`, and a `frequency_count` used purely for autocomplete ranking.
- This table exists **only to make compose autocomplete useful** (product spec §5's "Add recipients" should feel fast and not require retyping full addresses) — it is not exposed anywhere as a browsable list, and has no relationship management features (notes, grouping, merging duplicates) that would start to resemble a CRM.
- Ranking for autocomplete suggestions combines recency and frequency (e.g., a simple weighted score), rather than needing any separate design — this is a solved, low-stakes problem that doesn't need its own subsystem.

---

## 14. Implementation Roadmap

This document specs the full system, but building it in that order would mean assembling threading, search, tray integration, and telemetry infrastructure before ever confirming the core loop — read mail, reply, stay in sync — actually feels "fast" and "reliable" in practice, which inverts how product spec §19 defines success. The phases below sequence toward validating that core loop first.

### Phase 0: Foundations
- Cargo workspace structure; dependency list (§13) pinned.
- `refinery` migration scaffolding (§12) set up from day one, even with a near-empty initial schema — retrofitting migration tooling onto an already-evolved schema is far more painful than starting with it.
- Dockerized Dovecot/Postfix test harness (§11.2) stood up early, since sync is core from the first real feature, not something bolted on later.
- App named **Posthaste** (see naming note at the top of this document); GitHub org name resolved (bare `posthaste` is taken — see naming note) before repository creation. Initial license decision made separately (GPUI's own Apache 2.0 license only governs the UI framework dependency, not this application).

### Phase 1: Single-Account Core Loop (MVP)
- Manual account entry only (host/port/username/password) — defer autodiscovery and OAuth (§6.6) to Phase 2, since Gmail/Outlook's OAuth complexity isn't needed to validate the core loop against a plain IMAP/SMTP provider or self-hosted Dovecot.
- Single-account sync: IMAP IDLE/polling (§3.1), offline queue (§3.3), server-wins conflict resolution (§3.2) — one account only, no isolation concerns yet.
- Core schema: `messages`/`folders` (§2.4), no threading (`thread_id` column can exist but go unpopulated).
- Minimal GPUI UI: folder list, message list, reading pane — **plain text only**, deferring the sandboxed webview (§7.6) entirely. This is a deliberate scope cut: HTML rendering is a substantial subsystem, and the core loop can be validated without it.
- Basic compose/send (§5) without the full retry/backoff sophistication of §5.2 initially — a simple "retry a few times, then fail visibly" is enough to validate the pipeline shape before hardening it.
- Credential storage via OS keyring (§6.2) from the start — this is cheap to do correctly from day one and expensive to retrofit once account data exists.

**Exit criteria:** a single real account can be added, synced, read, and replied to reliably, offline and online, feeling fast per product spec §3 — before any other feature is built.

### Phase 2: Multi-Account + Account Setup
- Multi-account isolation (§6.3), unified inbox.
- Full account setup flow: autodiscovery and OAuth2 (§6.6) — this is where Gmail/Outlook support actually lands.
- Attachment handling: content-addressed storage (§2.2), eager/lazy fetch policy (§2.3).
- Draft persistence hardening (§5.3).

### Phase 3: Search + Threading
- FTS5 full-text search (§4.1, §4.3).
- Threading/conversation grouping (§3.5), including the reconciliation/merge logic.

### Phase 4: HTML Rendering
- Sandboxed webview integration (§7.6): JS-disabled, remote-content-blocked by default, HTML/Plain Text tabs.
- This is deliberately its own phase, isolated from Phase 1's plain-text-only MVP, given the security surface (§11.3's fuzz testing for MIME/HTML) that needs dedicated attention rather than being folded into general UI work.

### Phase 5: UI Polish + Power-User Features
- Full GPUI dock/tab/command-palette layout (§7.1–7.3), configurable keyboard shortcuts.
- Notifications (§7.4), system tray with the GNOME-fallback handling (§7.5).

### Phase 6: Reliability & Observability
- Crash reporting (§10.2), local logging (§10.3) — deliberately late, since these serve *production* reliability once there's a real user base generating real failure modes to observe, not the development-time debugging already covered by normal tooling.
- Backoff/retry hardening across sync, send, and account-failure paths (§3.4, §5.2, §6.4) to their fully-specified behavior.

### Phase 7: Packaging & Release
- Flatpak (primary) and AppImage builds (§9), including the portal-permission testing for keyring/tray/webview under sandboxing (§9.2).
- HTML rendering regression corpus (§11.4) and provider-specific manual test passes (§11.2) as pre-release gates.

### Parallel Workstreams (Not Sequential Phases)
- **Visual design system** (typography, color palette, iconography) — needed before Phase 1's UI work really begins, not after; this doc specs layout (§7.1) but not visual language, and that gap should close early rather than let Phase 1 UI work start on undefined visual foundations.
- **Accessibility** — GPUI's screen-reader/accessibility maturity should be evaluated before committing to UI patterns in Phase 1, since retrofitting accessibility onto an already-built UI is expensive; this isn't a phase of its own but a constraint that should inform every UI phase from the start.
