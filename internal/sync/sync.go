// Package sync is the per-account sync engine: one worker keeps one account's
// SQLite database and blob stores in step with its IMAP server. The worker
// owns a single connection at a time, replays the durable offline action
// queue before anything else on every pass, applies server state as the
// authority for flags and message existence, ingests new mail (raw MIME on
// disk, sanitized HTML, attachments, contacts, threads), and then parks in
// IMAP IDLE with a polling fallback.
//
// The worker talks to the server through the consumer-defined MailServer
// interface, so tests substitute a fake without a network. The only goroutine
// the worker spawns is the short-lived watcher around each IDLE park window,
// which is always joined before the next server call.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"

	"github.com/mefiz0/posthaste/internal/attachment"
	"github.com/mefiz0/posthaste/internal/backoff"
	"github.com/mefiz0/posthaste/internal/store"
	"github.com/mefiz0/posthaste/internal/thread"
)

// State is the high-level condition of one account's sync worker, surfaced to
// the UI so an account's status is always attributable to that account.
type State string

// Worker states.
const (
	StateSyncing    State = "syncing"
	StateIdle       State = "idle"
	StateOffline    State = "offline"
	StateAuthFailed State = "auth-failed"
	StatePaused     State = "paused"
)

// defaultEagerThreshold matches the product default: attachments at or below
// this size are downloaded during sync, larger ones on demand.
const defaultEagerThreshold = 2 * 1024 * 1024

// defaultPollFallback is used when neither the account nor the caller
// specifies a polling interval for servers without IDLE.
const defaultPollFallback = 5 * time.Minute

// Event is one notification from a sync worker. Exactly one of State, Notice,
// or Err is meaningful per event: State reports a condition change, Notice a
// transient user-facing remark, and Err the scrubbed text behind a failure
// state. Scrubbing rule for Err: errors in this package are built from host,
// port, folder path, and protocol text only — never message content.
type Event struct {
	AccountID  string
	State      State
	FolderPath string
	Notice     string
	Err        string
	// Progress, when set, describes a step within a pass. It is set instead of
	// State so the UI can show a sync breakdown rather than an opaque state.
	Progress *Progress
}

// Progress describes one step of a sync pass: a pass beginning or ending, or a
// folder starting or finishing. It is intentionally small so the UI can render
// a readable activity log.
type Progress struct {
	// Folder is the display name of the folder, empty for pass-level steps.
	Folder string
	// Phase is "pass-start", "folder-start", "folder-done", or "pass-done".
	Phase string
	// New is the number of messages fetched or changed in this step.
	New int
	// Total is the folder's server-side message count (or folder count for a
	// pass-level step).
	Total int
}

// Deps wires a Worker to one account's engine dependencies. The worker never
// reaches for globals; the owner supplies everything.
type Deps struct {
	// AccountID is the internal account identifier used in events and logs.
	// It must be set; the account's email address never travels through here.
	AccountID string
	// Account carries the non-sensitive account settings: server endpoint,
	// poll interval, and the attachment eager-fetch threshold.
	Account store.Account
	// AuthFunc returns a fresh SASL mechanism per connection attempt so
	// refreshed OAuth tokens are picked up on reconnect. It is used by
	// DialFactory; a custom Dial may ignore it.
	AuthFunc func(ctx context.Context) (sasl.Client, error)
	// Dial opens a fresh authenticated connection. Required.
	Dial ServerFactory
	// DB is this account's opened and migrated store.
	DB *store.Store
	// RawBlobs stores raw MIME sources (the account's MessagesDir store).
	RawBlobs *attachment.Store
	// AttachBlobs stores attachment bytes (the account's AttachmentsDir
	// store).
	AttachBlobs *attachment.Store
	// Notify receives events. It must not block for long; the worker calls it
	// inline. Nil disables notification.
	Notify func(Event)
	// Logger optionally receives scrubbed progress logging.
	Logger *slog.Logger
	// PollFallback is the polling period for servers without IDLE. Zero or
	// negative selects a five minute default.
	PollFallback time.Duration
	// Now injects the clock. Zero-valued fields fall back to time.Now.
	Now func() time.Time
	// IsAuthError classifies dial failures as authentication problems so they
	// can park the worker for explicit user action. Nil selects
	// DefaultIsAuthError.
	IsAuthError func(error) bool
	// Retry overrides the shared reconnect backoff policy. The zero value
	// selects backoff.DefaultPolicy; tests use it for fast retries.
	Retry backoff.Policy
}

// appendRequest is one serialized APPEND waiting for a between-pass window on
// the worker's connection.
type appendRequest struct {
	ctx    context.Context
	path   string
	raw    []byte
	flags  []string
	result chan error
}

// Worker is the per-account sync engine. Run owns exactly one goroutine (plus
// one joined watcher per IDLE window); TriggerSync and AppendRaw are safe to
// call from any goroutine.
type Worker struct {
	deps    Deps
	trigger chan struct{}
	appends chan appendRequest

	// triggerMu guards the pending trigger targets. A folder set schedules a
	// targeted pass; triggerFull schedules a pass over every folder. Both are
	// drained by the Run goroutine.
	triggerMu      sync.Mutex
	triggerFolders map[string]bool
	triggerFull    bool

	// threads is the conversation graph for this account. It is created and
	// used only inside Run.
	threads *thread.Index

	// pendingAppendFolders collects folders that received an APPEND and still
	// need a targeted sync to ingest the uploaded copy. Written and read only
	// by the Run goroutine.
	pendingAppendFolders map[string]bool
}

// New validates the dependencies and returns a Worker. It starts nothing:
// call Run on a dedicated goroutine.
func New(d Deps) (*Worker, error) {
	switch {
	case d.AccountID == "":
		return nil, errors.New("sync: no account id")
	case d.Dial == nil:
		return nil, errors.New("sync: no dial factory")
	case d.DB == nil:
		return nil, errors.New("sync: no store")
	case d.RawBlobs == nil:
		return nil, errors.New("sync: no raw message blob store")
	case d.AttachBlobs == nil:
		return nil, errors.New("sync: no attachment blob store")
	}
	if d.PollFallback <= 0 {
		d.PollFallback = defaultPollFallback
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Worker{
		deps:                 d,
		trigger:              make(chan struct{}, 1),
		appends:              make(chan appendRequest),
		triggerFolders:       make(map[string]bool),
		pendingAppendFolders: make(map[string]bool),
	}, nil
}

// Run drives the worker until ctx is cancelled: dial, replay queued actions,
// reconcile folders and messages, park in IDLE (or poll), and reconnect with
// the shared backoff on any connection failure. When dial failures are
// classified as authentication failures and the backoff budget is spent, the
// worker emits StateAuthFailed and parks until TriggerSync is called, because
// only a user action (such as re-entering credentials) can fix the cause. The
// same park applies to exhausted connectivity failures, surfaced as
// StateOffline. Run returns the context error after closing the connection.
func (w *Worker) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	w.threads = thread.NewIndex(0)
	if err := w.seedThreads(ctx); err != nil {
		return fmt.Errorf("sync: seed threads: %w", err)
	}

	// A paused account waits for an explicit trigger (the owner recreates the
	// worker when pause changes persistently; this handles transient pauses
	// without dropping the queue).
	if w.deps.Account.Paused {
		w.log("sync: account paused")
		w.emit(Event{State: StatePaused})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.trigger:
		}
	}

	retries := backoff.New(w.deps.Retry)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		server, dialErr := w.deps.Dial(ctx)
		if dialErr != nil {
			state := StateOffline
			if w.classifyAuthError(dialErr) {
				state = StateAuthFailed
			}
			w.log("sync: dial failed: " + scrubErrText(dialErr))
			w.emit(Event{State: state, Err: scrubErrText(dialErr)})
			if err := w.backoffOrPark(ctx, retries); err != nil {
				return err
			}
			continue
		}

		retries.Reset()
		w.emit(Event{State: StateSyncing})
		sessionErr := w.runSession(ctx, server)
		_ = server.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sessionErr == nil {
			// Unreachable: runSession loops internally until an error or
			// cancellation, but reconnecting is the safe response anyway.
			continue
		}
		w.log("sync: connection lost: " + scrubErrText(sessionErr))
		w.emit(Event{State: StateOffline, Err: scrubErrText(sessionErr)})
		if err := w.backoffOrPark(ctx, retries); err != nil {
			return err
		}
	}
}

// backoffOrPark waits for the next retry delay. When the backoff budget is
// spent it parks the worker until an explicit trigger or shutdown, so a
// persistently failing account stops dialing on its own and resumes only on
// user action.
func (w *Worker) backoffOrPark(ctx context.Context, retries *backoff.Backoff) error {
	err := retries.Wait(ctx, backoff.Sleep)
	if !errors.Is(err, backoff.ErrExhausted) {
		// Either the delay elapsed (retry now) or the context ended (propagate).
		return err
	}
	w.log("sync: retry budget spent, waiting for explicit retry")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.trigger:
		retries.Reset()
		return nil
	}
}

// TriggerSync nudges the worker to run a sync pass at the next safe point. It
// coalesces (never queues more than one nudge) and is safe to call at any
// time, including before Run starts and after it returns. An explicit trigger
// also resumes a worker parked in offline or auth-failed state.
func (w *Worker) TriggerSync() { w.scheduleTrigger("") }

// TriggerFolderSync nudges the worker to sync just one folder at the next safe
// point. Actions whose effect is confined to a folder (flag, move, delete)
// use it so they do not trigger a pass over every mailbox.
func (w *Worker) TriggerFolderSync(path string) { w.scheduleTrigger(path) }

func (w *Worker) scheduleTrigger(path string) {
	w.triggerMu.Lock()
	if path == "" {
		w.triggerFull = true
	} else {
		w.triggerFolders[path] = true
	}
	w.triggerMu.Unlock()
	select {
	case w.trigger <- struct{}{}:
	default:
	}
}

// takeTrigger drains the scheduled trigger targets: whether a full pass was
// requested and the set of folders to sync if not.
func (w *Worker) takeTrigger() (bool, map[string]bool) {
	w.triggerMu.Lock()
	defer w.triggerMu.Unlock()
	full := w.triggerFull
	folders := w.triggerFolders
	w.triggerFolders = make(map[string]bool)
	w.triggerFull = false
	return full, folders
}

// AppendRaw uploads raw on the worker's connection, waiting for the next
// between-pass window (APPEND cannot run while the connection is parked in
// IDLE, so the request wakes the worker and is served first). It blocks until
// the append completes, fails, or ctx ends. After Run has returned it blocks
// until ctx ends, so callers should always pass a cancellable context.
func (w *Worker) AppendRaw(ctx context.Context, path string, raw []byte, flags []string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sync: append to %s: %w", path, err)
	}
	request := appendRequest{
		ctx:    ctx,
		path:   path,
		raw:    raw,
		flags:  flags,
		result: make(chan error, 1),
	}
	select {
	case w.appends <- request:
	case <-ctx.Done():
		return fmt.Errorf("sync: append to %s: %w", path, ctx.Err())
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("sync: append to %s: %w", path, ctx.Err())
	}
}

// runAppend serves one queued APPEND and delivers the outcome. The result
// channel is buffered, so a caller that gave up never blocks the worker.
func (w *Worker) runAppend(server MailServer, request appendRequest) {
	// Remember where this APPEND landed so runSession can sync just that
	// folder back instead of running a full pass.
	w.pendingAppendFolders[request.path] = true
	err := server.Append(request.ctx, request.path, request.raw, request.flags)
	if err != nil {
		err = fmt.Errorf("sync: append to %s: %w", request.path, err)
	}
	select {
	case request.result <- err:
	default:
	}
}

// takePendingAppendFolders returns and clears the folders that received an
// APPEND since the last call.
func (w *Worker) takePendingAppendFolders() map[string]bool {
	if len(w.pendingAppendFolders) == 0 {
		return nil
	}
	pending := w.pendingAppendFolders
	w.pendingAppendFolders = make(map[string]bool)
	return pending
}

// emit delivers one event to the owner. Notify runs inline and must be
// non-blocking by contract.
func (w *Worker) emit(event Event) {
	event.AccountID = w.deps.AccountID
	if w.deps.Notify != nil {
		w.deps.Notify(event)
	}
}

// emitProgress reports one step of the current pass.
func (w *Worker) emitProgress(progress Progress) {
	w.emit(Event{Progress: &progress})
}

// log emits a scrubbed log line when a logger was provided. Values reaching
// this point are host, port, and protocol text only; the scrubbing handler in
// the logging package is still the last line of defense.
func (w *Worker) log(message string) {
	if w.deps.Logger != nil {
		w.deps.Logger.Info(message)
	}
}

// classifyAuthError reports whether a dial failure is an authentication
// problem using the injected classifier, defaulting to DefaultIsAuthError.
func (w *Worker) classifyAuthError(err error) bool {
	if w.deps.IsAuthError != nil {
		return w.deps.IsAuthError(err)
	}
	return DefaultIsAuthError(err)
}

// pollInterval is the effective polling period for this account.
func (w *Worker) pollInterval() time.Duration {
	if seconds := w.deps.Account.PollIntervalSeconds; seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return w.deps.PollFallback
}

// eagerThreshold is the attachment size at or below which bytes are fetched
// during sync.
func (w *Worker) eagerThreshold() int64 {
	if threshold := w.deps.Account.AttachmentEagerThresholdByte; threshold > 0 {
		return threshold
	}
	return defaultEagerThreshold
}

// now is the injected clock.
func (w *Worker) now() time.Time { return w.deps.Now() }

// scrubErrText reduces an error to a single bounded line for delivery to the
// UI and logs. Errors in this package carry protocol and host details, never
// message content, but the bound keeps a chatty server from flooding a
// notification.
func scrubErrText(err error) string {
	if err == nil {
		return ""
	}
	line := strings.ToValidUTF8(err.Error(), "\uFFFD")
	if cut := strings.IndexAny(line, "\r\n"); cut >= 0 {
		line = line[:cut]
	}
	const maxLen = 300
	if len(line) > maxLen {
		// Back up to a rune boundary so the tail is not half a character.
		cut := maxLen
		for cut > 0 && line[cut]&0xC0 == 0x80 {
			cut--
		}
		line = line[:cut]
	}
	return line
}
