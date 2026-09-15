package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"

	"github.com/mefiz0/posthaste/internal/auth"
	"github.com/mefiz0/posthaste/internal/send"
	"github.com/mefiz0/posthaste/internal/settings"
	"github.com/mefiz0/posthaste/internal/store"
	mailsync "github.com/mefiz0/posthaste/internal/sync"
)

// ---------- fakes ----------

type recordedEvent struct {
	Name    string
	Payload any
}

// fakeEmitter records every event and lets tests wait for the next one of a
// given name.
type fakeEmitter struct {
	mu     sync.Mutex
	events []recordedEvent
	wake   chan struct{}
}

func newFakeEmitter() *fakeEmitter {
	return &fakeEmitter{wake: make(chan struct{}, 128)}
}

func (e *fakeEmitter) Emit(name string, data any) {
	e.mu.Lock()
	e.events = append(e.events, recordedEvent{Name: name, Payload: data})
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *fakeEmitter) all() []recordedEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]recordedEvent(nil), e.events...)
}

// waitFor blocks until an event of name arrives or the timeout passes.
func (e *fakeEmitter) waitFor(t *testing.T, name string, timeout time.Duration) recordedEvent {
	t.Helper()
	return e.waitMatch(t, name, timeout, nil)
}

// waitMatch blocks until an event of name whose payload passes match arrives,
// or the timeout passes. The whole recorded stream is scanned, so events that
// arrived before the call still satisfy it.
func (e *fakeEmitter) waitMatch(t *testing.T, name string, timeout time.Duration, match func(payload any) bool) recordedEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		e.mu.Lock()
		for _, ev := range e.events {
			if ev.Name != name {
				continue
			}
			if match == nil || match(ev.Payload) {
				e.mu.Unlock()
				return ev
			}
		}
		e.mu.Unlock()
		select {
		case <-e.wake:
		case <-deadline:
			t.Fatalf("timed out waiting for event %q; seen: %s", name, e.names())
		}
	}
}

func (e *fakeEmitter) names() string {
	var names []string
	for _, ev := range e.all() {
		names = append(names, ev.Name)
	}
	return strings.Join(names, ",")
}

// fakeCredentials is an in-memory keyring.
type fakeCredentials struct {
	mu      sync.Mutex
	secrets map[string]string
	deleted []string
}

func newFakeCredentials() *fakeCredentials {
	return &fakeCredentials{secrets: make(map[string]string)}
}

func (c *fakeCredentials) SaveCredential(_ context.Context, ref, secret string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.secrets[ref] = secret
	return nil
}

func (c *fakeCredentials) Credential(_ context.Context, ref string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	secret, ok := c.secrets[ref]
	if !ok {
		return "", auth.ErrCredentialNotFound
	}
	return secret, nil
}

func (c *fakeCredentials) DeleteCredential(_ context.Context, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.secrets, ref)
	c.deleted = append(c.deleted, ref)
	return nil
}

func (c *fakeCredentials) has(ref string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.secrets[ref]
	return ok
}

// fakeSettingsStore is an in-memory settings file.
type fakeSettingsStore struct {
	mu  sync.Mutex
	cfg settings.Settings
}

func (s *fakeSettingsStore) Load() (settings.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, nil
}

func (s *fakeSettingsStore) Save(cfg settings.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	return nil
}

// fakeNotifier records desktop notifications.
type fakeNotifier struct {
	mu     sync.Mutex
	titles []string
}

func (n *fakeNotifier) Notify(title, body string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.titles = append(n.titles, title)
}

func (n *fakeNotifier) count(title string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	total := 0
	for _, t := range n.titles {
		if t == title {
			total++
		}
	}
	return total
}

// fakeMailServer is an in-memory IMAP server: an INBOX (optionally with
// messages), a Sent folder, and a Drafts folder.
type fakeMailServer struct {
	mu       sync.Mutex
	messages []mailsync.ServerMessage
	appends  []appendRecord
	uidNext  uint32
	closed   bool
}

type appendRecord struct {
	Path  string
	Raw   []byte
	Flags []string
}

func (s *fakeMailServer) addMessage(raw string, date time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uidNext++
	uid := s.uidNext
	cut := strings.Index(raw, "\r\n\r\n")
	header := raw
	if cut >= 0 {
		header = raw[:cut+4]
	}
	s.messages = append(s.messages, mailsync.ServerMessage{
		UID:    uid,
		Date:   date,
		Header: []byte(header),
		Body:   []byte(raw),
	})
}

func (s *fakeMailServer) ListFolders(context.Context) ([]mailsync.ServerFolder, error) {
	return []mailsync.ServerFolder{
		{Path: "INBOX", Role: "inbox"},
		{Path: "Sent", Role: "sent"},
		{Path: "Drafts", Role: "drafts"},
	}, nil
}

func (s *fakeMailServer) Select(_ context.Context, path string) (mailsync.FolderStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total, unseen := 0, 0
	if path == "INBOX" {
		total = len(s.messages)
		unseen = total
	}
	return mailsync.FolderStatus{UIDValidity: 7, UIDNext: s.uidNext + 1, Total: total, Unseen: unseen}, nil
}

func (s *fakeMailServer) FetchHeaders(_ context.Context, path string, fromUID uint32) ([]mailsync.ServerMessage, error) {
	if path != "INBOX" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []mailsync.ServerMessage
	for _, m := range s.messages {
		if m.UID >= fromUID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *fakeMailServer) FetchFlags(_ context.Context, path string, fromUID uint32) ([]mailsync.ServerMessage, error) {
	if path != "INBOX" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []mailsync.ServerMessage
	for _, m := range s.messages {
		if m.UID >= fromUID {
			out = append(out, mailsync.ServerMessage{UID: m.UID, Flags: m.Flags})
		}
	}
	return out, nil
}

func (s *fakeMailServer) FetchBodies(_ context.Context, path string, uids []uint32) ([]mailsync.ServerMessage, error) {
	if path != "INBOX" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []mailsync.ServerMessage
	for _, m := range s.messages {
		for _, uid := range uids {
			if m.UID == uid {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func (s *fakeMailServer) SetFlags(context.Context, string, uint32, []string, []string) error {
	return nil
}

func (s *fakeMailServer) Move(context.Context, string, uint32, string) error { return nil }

func (s *fakeMailServer) Delete(context.Context, string, uint32) error { return nil }

func (s *fakeMailServer) Append(_ context.Context, path string, raw []byte, flags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appends = append(s.appends, appendRecord{Path: path, Raw: append([]byte(nil), raw...), Flags: flags})
	return nil
}

func (s *fakeMailServer) SupportsIdle(context.Context) (bool, error) { return false, nil }

func (s *fakeMailServer) Idle(context.Context, string) error {
	return errors.New("fake: IDLE not supported")
}

func (s *fakeMailServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeMailServer) appendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.appends)
}

// snapshotAppends returns a copy of the recorded APPENDs.
func (s *fakeMailServer) snapshotAppends() []appendRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]appendRecord(nil), s.appends...)
}

// deliveries reports how many messages the fake mailer delivered.
func (m *fakeMailer) deliveries() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.delivered
}

// fakeMailer records deliveries and always succeeds.
type fakeMailer struct {
	mu        sync.Mutex
	delivered int
}

func (m *fakeMailer) Deliver(context.Context, store.OutboxMessage, send.AuthProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivered++
	return nil
}

// ---------- harness ----------

const testRawMessage = "Message-ID: <one@other.test>\r\n" +
	"From: Alice Example <alice@other.test>\r\n" +
	"To: me@here.test\r\n" +
	"Subject: Hello there\r\n" +
	"Date: Mon, 07 Jul 2025 10:00:00 +0000\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hi from Alice.\r\n"

// harness bundles one started manager with its fakes. Every account gets its
// own fake mail server so per-account isolation holds in tests too.
type harness struct {
	manager     *Manager
	emitter     *fakeEmitter
	credentials *fakeCredentials
	settings    *fakeSettingsStore
	notifier    *fakeNotifier
	mailer      *fakeMailer

	serversMu sync.Mutex
	servers   map[int64]*fakeMailServer
	seeds     int

	paths  settings.Paths
	cancel context.CancelFunc
}

// newHarness builds a manager wired to fakes and starts it, with each
// account's fake server seeded with seedMessages inbox messages.
func newHarness(t *testing.T, seedMessages int) *harness {
	t.Helper()
	return newHarnessWithOptions(t, harnessOptions{seedMessages: seedMessages})
}

type harnessOptions struct {
	seedMessages  int
	notifications bool
}

func newHarnessWithOptions(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	base := t.TempDir()
	paths := settings.Paths{
		ConfigDir:      filepath.Join(base, "config"),
		DataDir:        filepath.Join(base, "data"),
		CacheDir:       filepath.Join(base, "cache"),
		StateDir:       filepath.Join(base, "state"),
		LogDir:         filepath.Join(base, "state", "logs"),
		AccountsDir:    filepath.Join(base, "data", "accounts"),
		AttachmentsDir: filepath.Join(base, "data", "attachments"),
		MessagesDir:    filepath.Join(base, "data", "messages"),
		BackupsDir:     filepath.Join(base, "data", "backups"),
	}
	for _, dir := range []string{paths.ConfigDir, paths.AccountsDir, paths.AttachmentsDir, paths.MessagesDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	h := &harness{
		emitter:     newFakeEmitter(),
		credentials: newFakeCredentials(),
		settings:    &fakeSettingsStore{cfg: settings.Default()},
		notifier:    &fakeNotifier{},
		mailer:      &fakeMailer{},
		servers:     make(map[int64]*fakeMailServer),
		seeds:       opts.seedMessages,
		paths:       paths,
	}
	h.settings.cfg.NotificationsEnabled = opts.notifications

	cfgStore := h.settings
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel

	h.manager = NewManager(Deps{
		Paths:       paths,
		Settings:    cfgStore,
		Emitter:     h.emitter,
		Credentials: h.credentials,
		Notifier:    h.notifier,
		MailerFactory: func(store.Account, send.BlobOpener) send.Mailer {
			return h.mailer
		},
		SyncDialFactory: func(account store.Account, _ func(ctx context.Context) (sasl.Client, error)) (mailsync.ServerFactory, error) {
			server := h.serverFor(idFromString(account.ID))
			return func(context.Context) (mailsync.MailServer, error) {
				return server, nil
			}, nil
		},
		VerifyFunc: func(context.Context, ManualAccountInput) error { return nil },
	})
	h.manager.Start(ctx)
	t.Cleanup(func() {
		h.manager.Shutdown()
		cancel()
	})
	return h
}

// addAccount registers and starts one account through the public path.
func (h *harness) addAccount(t *testing.T, email string) int64 {
	t.Helper()
	info, err := h.manager.AddAccountManual(context.Background(), ManualAccountInput{
		Email:       email,
		Password:    "secret",
		DisplayName: strings.Split(email, "@")[0],
		Auth:        "password",
		IMAP:        ServerConfigInfo{Host: "imap.test", Port: 993, Security: "tls", Username: email},
		SMTP:        ServerConfigInfo{Host: "smtp.test", Port: 465, Security: "tls", Username: email},
	})
	if err != nil {
		t.Fatalf("AddAccountManual: %v", err)
	}
	return info.ID
}

// serverFor returns (creating when needed) the fake server for one account.
func (h *harness) serverFor(id int64) *fakeMailServer {
	h.serversMu.Lock()
	defer h.serversMu.Unlock()
	server, ok := h.servers[id]
	if !ok {
		server = &fakeMailServer{}
		for i := 0; i < h.seeds; i++ {
			server.addMessage(testRawMessage, time.Now().Add(-time.Duration(i)*time.Minute))
		}
		h.servers[id] = server
	}
	return server
}

// relaunch builds and starts a second manager over the same on-disk state and
// fakes, simulating an application restart. It reuses the harness's fakes so
// event and credential assertions keep working; the test cleans it up.
func (h *harness) relaunch(t *testing.T) *Manager {
	t.Helper()
	manager := NewManager(Deps{
		Paths:       h.paths,
		Settings:    h.settings,
		Emitter:     h.emitter,
		Credentials: h.credentials,
		Notifier:    h.notifier,
		MailerFactory: func(store.Account, send.BlobOpener) send.Mailer {
			return h.mailer
		},
		SyncDialFactory: func(account store.Account, _ func(context.Context) (sasl.Client, error)) (mailsync.ServerFactory, error) {
			server := h.serverFor(idFromString(account.ID))
			return func(context.Context) (mailsync.MailServer, error) {
				return server, nil
			}, nil
		},
		VerifyFunc: func(context.Context, ManualAccountInput) error { return nil },
	})
	manager.Start(context.Background())
	t.Cleanup(manager.Shutdown)
	return manager
}

// TestManagerRetainsSyncActivity verifies the manager keeps a sync activity
// log the UI can fetch even after the pass that produced it has finished.
func TestManagerRetainsSyncActivity(t *testing.T) {
	h := newHarness(t, 1)
	h.addAccount(t, "me@here.test")
	h.emitter.waitFor(t, EventMessagesChanged, 10*time.Second)
	h.emitter.waitMatch(t, EventSyncState, 5*time.Second, func(payload any) bool {
		ev, ok := payload.(SyncStateEvent)
		return ok && ev.State == syncStateIdle
	})

	activity := h.manager.SyncActivity()
	if len(activity) == 0 {
		t.Fatal("no sync activity retained")
	}
	sawFolder := false
	for _, entry := range activity {
		if entry.Text == "INBOX — 1 new" {
			sawFolder = true
		}
	}
	if !sawFolder {
		t.Fatalf("activity missing the folder result: %+v", activity)
	}
}

// TestManagerStartKeepsPausedAccountReadable guards the restart path: a paused
// account is skipped by the worker start but must still get an open store, or
// the UI can list it and then fail every per-account call with "not running".
func TestManagerStartKeepsPausedAccountReadable(t *testing.T) {
	h := newHarness(t, 1)
	id := h.addAccount(t, "me@here.test")
	h.emitter.waitFor(t, EventMessagesChanged, 10*time.Second)

	pausedEvents := func() int {
		count := 0
		for _, ev := range h.emitter.all() {
			if ev.Name != EventSyncState {
				continue
			}
			if state, ok := ev.Payload.(SyncStateEvent); ok && state.AccountID == id && state.State == syncStatePaused {
				count++
			}
		}
		return count
	}

	if err := h.manager.SetPaused(id, true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	h.manager.Shutdown()
	before := pausedEvents()

	restarted := h.relaunch(t)
	rt, err := restarted.Runtime(id)
	if err != nil {
		t.Fatalf("paused account has no runtime after restart: %v", err)
	}
	folders, err := rt.store.ListFolders(context.Background())
	if err != nil {
		t.Fatalf("ListFolders on paused account: %v", err)
	}
	if len(folders) == 0 {
		t.Fatal("paused account has no readable folders after restart")
	}

	// The restart must announce the paused state rather than start a pass.
	if got := pausedEvents(); got <= before {
		t.Errorf("restart did not announce the paused state (%d events before, %d after)", before, got)
	}
}
