//go:build integration

// End-to-end proof that the real composition works: a Manager wired to the
// production IMAP dialer drives the real sync worker against the Dovecot
// harness from docker-compose.yml, and every mutation it makes locally lands
// on the server. It skips cleanly whenever the harness is not configured or
// unreachable, so the fast suite never depends on Docker.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/imapx"
	"github.com/mefiz0/posthaste/internal/settings"
	"github.com/mefiz0/posthaste/internal/store"
)

// Deadlines are generous because the harness runs a real server, but nothing
// here sleeps a fixed interval: every wait polls a probe.
const (
	e2eSettleTimeout   = 90 * time.Second // first full sync of a fresh account
	e2eIdleWakeTimeout = 45 * time.Second // server push after an APPEND
	e2eReplayTimeout   = 45 * time.Second // queued action reaches the server
	e2eServerOpTimeout = 20 * time.Second // one raw IMAP round trip
	e2ePollEvery       = 200 * time.Millisecond
)

// e2eEnv bundles the manager under test with the raw harness client used to
// seed mail and verify server-side outcomes.
type e2eEnv struct {
	t         *testing.T
	manager   *Manager
	mail      *MailService
	emitter   *fakeEmitter
	logs      *e2eLogSink
	accountID int64

	rootCtx context.Context

	host     string
	port     int
	username string
	password string

	run        string
	raw        *imapx.Client
	targetPath string

	// baselineGoroutines is the count before the manager started, so the
	// post-shutdown check can tell leaked workers from pre-existing noise.
	baselineGoroutines int
}

// e2eLogSink is a slog handler that records error-level messages so the test
// can assert the run stayed free of engine errors.
type e2eLogSink struct {
	mu     sync.Mutex
	errors []string
}

func (s *e2eLogSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *e2eLogSink) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		s.mu.Lock()
		s.errors = append(s.errors, r.Message)
		s.mu.Unlock()
	}
	return nil
}

func (s *e2eLogSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *e2eLogSink) WithGroup(string) slog.Handler      { return s }

func (s *e2eLogSink) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.errors...)
}

// e2eEventually polls probe until it reports success or the deadline passes.
// The last probe detail is surfaced on timeout so failures explain themselves.
func e2eEventually(t *testing.T, timeout time.Duration, what string, probe func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	detail := "not probed"
	for {
		ok, d := probe()
		if ok {
			return
		}
		detail = d
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s: %s", timeout, what, detail)
		}
		time.Sleep(e2ePollEvery)
	}
}

// e2eMessage renders a minimal RFC 5322 message with CRLF line endings.
func e2eMessage(messageID, subject, inReplyTo, from, date, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", messageID)
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\nReferences: <%s>\r\n", inReplyTo, inReplyTo)
	}
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", "test@posthaste.local")
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", date)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}

// newE2EEnv builds everything the e2e needs: temp paths, a raw harness client
// pre-seeded with two messages, and a started Manager wired to the production
// dialer (no SyncDialFactory override) and the real credential verification
// (no VerifyFunc override).
func newE2EEnv(t *testing.T, host string, port int, username, password string) *e2eEnv {
	t.Helper()

	run := strconv.FormatInt(time.Now().UnixNano(), 36)
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
	if err := settings.EnsureDirs(paths); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	opCtx, opCancel := context.WithTimeout(context.Background(), e2eServerOpTimeout)
	t.Cleanup(opCancel)
	raw, err := imapx.Dial(opCtx, imapx.Config{
		Host:     host,
		Port:     port,
		TLS:      imapx.TLSNone,
		Username: username,
		Auth:     imapx.PasswordAuth(username, password),
	})
	if err != nil {
		t.Fatalf("imapx.Dial to harness: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(context.Background()) })

	// Seed before the manager starts so the very first sync pass picks the
	// messages up, and create the move-target folder so the folder
	// reconciliation lists it from the beginning.
	rootID := "e2e-root-" + run + "@posthaste.local"
	replyID := "e2e-reply-" + run + "@posthaste.local"
	rootDate := time.Now().UTC().Add(-2 * time.Hour).Format("Mon, 02 Jan 2006 15:04:05 +0000")
	replyDate := time.Now().UTC().Add(-1 * time.Hour).Format("Mon, 02 Jan 2006 15:04:05 +0000")
	for _, seed := range []struct {
		path string
		raw  []byte
	}{
		{"INBOX", e2eMessage(rootID, "E2E Thread Root "+run, "", "E2E Root <root@posthaste.local>", rootDate,
			"Plain text body for run "+run+".")},
		{"INBOX", e2eMessage(replyID, "E2E Thread Reply "+run, rootID, "E2E Reply <reply@posthaste.local>", replyDate,
			"Reply body for run "+run+".")},
	} {
		appendCtx, appendCancel := context.WithTimeout(context.Background(), e2eServerOpTimeout)
		if _, err := raw.Append(appendCtx, seed.path, seed.raw, nil, time.Time{}); err != nil {
			t.Fatalf("seed Append to %s: %v", seed.path, err)
		}
		appendCancel()
	}
	targetPath := "e2e-target-" + run
	if err := raw.EnsureFolder(opCtx, targetPath); err != nil {
		t.Fatalf("EnsureFolder(%q): %v", targetPath, err)
	}

	env := &e2eEnv{
		t:          t,
		mail:       nil, // set below, once the manager exists
		emitter:    newFakeEmitter(),
		logs:       &e2eLogSink{},
		rootCtx:    context.Background(),
		host:       host,
		port:       port,
		username:   username,
		password:   password,
		run:        run,
		raw:        raw,
		targetPath: targetPath,
	}

	manager := NewManager(Deps{
		Paths:       paths,
		Settings:    &fakeSettingsStore{cfg: settings.Default()},
		Logger:      slog.New(env.logs),
		Emitter:     env.emitter,
		Credentials: newFakeCredentials(),
		Notifier:    &fakeNotifier{},
		// SyncDialFactory and VerifyFunc stay nil on purpose: the nil
		// defaults are the production IMAP dialer and the live credential
		// verification, which is exactly what this test exists to exercise.
	})
	env.manager = manager
	env.mail = NewMailService(manager)

	env.baselineGoroutines = runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	t.Cleanup(func() {
		manager.Shutdown()
		cancel()
	})
	return env
}

// addAccount registers the harness account through the same path the Wails
// service uses, with the real verification dial included.
func (e *e2eEnv) addAccount() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.rootCtx, 60*time.Second)
	defer cancel()
	info, err := e.manager.AddAccountManual(ctx, ManualAccountInput{
		Email:    e.username,
		Password: e.password,
		Auth:     "password",
		IMAP: ServerConfigInfo{
			Host: e.host, Port: e.port, Security: string(imapx.TLSNone), Username: e.username,
		},
		// SMTP settings are mandatory input validation, but sending is out of
		// scope here: the send worker stays idle with an empty outbox.
		SMTP: ServerConfigInfo{
			Host: "127.0.0.1", Port: 1025, Security: string(imapx.TLSNone), Username: e.username,
		},
	})
	if err != nil {
		e.t.Fatalf("AddAccountManual: %v", err)
	}
	e.accountID = info.ID
}

// opCtx bounds one raw IMAP round trip.
func (e *e2eEnv) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(e.rootCtx, e2eServerOpTimeout)
}

// folderByPath returns the locally synced folder with the given IMAP path.
func (e *e2eEnv) folderByPath(path string) (FolderInfo, bool) {
	ctx, cancel := context.WithTimeout(e.rootCtx, 5*time.Second)
	defer cancel()
	folders, err := e.mail.ListFolders(ctx, e.accountID)
	if err != nil {
		return FolderInfo{}, false
	}
	for _, f := range folders {
		if f.IMAPPath == path {
			return f, true
		}
	}
	return FolderInfo{}, false
}

// inboxSummaries lists the inbox through the mail service; an account whose
// first sync has not landed yet yields an empty list rather than an error, so
// callers can poll it.
func (e *e2eEnv) inboxSummaries() ([]MessageSummaryInfo, string) {
	inbox, ok := e.folderByPath("INBOX")
	if !ok {
		return nil, "no inbox folder synced yet"
	}
	ctx, cancel := context.WithTimeout(e.rootCtx, 5*time.Second)
	defer cancel()
	messages, err := e.mail.ListMessages(ctx, e.accountID, MessageView{FolderID: inbox.ID}, 0)
	if err != nil {
		return nil, "ListMessages: " + err.Error()
	}
	return messages, "inbox has 0 messages"
}

// summaryWithSubject finds the one message whose subject matches exactly.
func (e *e2eEnv) summaryWithSubject(subject string) (MessageSummaryInfo, bool) {
	messages, _ := e.inboxSummaries()
	for _, m := range messages {
		if m.Subject == subject {
			return m, true
		}
	}
	return MessageSummaryInfo{}, false
}

// serverMessages fetches every message in an IMAP folder through the raw
// harness client, headers only.
func (e *e2eEnv) serverMessages(path string) []imapx.Message {
	ctx, cancel := e.opCtx()
	defer cancel()
	messages, err := e.raw.FetchByUID(ctx, path, imapx.UIDRangeSet(1, 0), imapx.FetchOptions{})
	if err != nil {
		return nil
	}
	return messages
}

// serverHasMessage reports whether a message with the given Message-ID sits in
// the IMAP folder, with a detail string for polling probes.
func (e *e2eEnv) serverHasMessage(path, messageID string) (bool, string) {
	messages := e.serverMessages(path)
	needle := strings.ToLower("message-id: <" + messageID + ">")
	for _, m := range messages {
		if strings.Contains(strings.ToLower(string(m.Header)), needle) {
			return true, fmt.Sprintf("found in %s (uid %d)", path, m.UID)
		}
	}
	if len(messages) == 0 {
		return false, path + " is empty"
	}
	return false, fmt.Sprintf("%s has %d messages, none with %s", path, len(messages), messageID)
}

// serverFlags returns the IMAP flags of the message with the given Message-ID.
func (e *e2eEnv) serverFlags(path, messageID string) ([]string, bool) {
	needle := strings.ToLower("message-id: <" + messageID + ">")
	for _, m := range e.serverMessages(path) {
		if strings.Contains(strings.ToLower(string(m.Header)), needle) {
			return m.Flags, true
		}
	}
	return nil, false
}

// cleanupServerMail best-effort removes the messages this run appended, so
// repeated runs against one harness volume stay independent.
func (e *e2eEnv) cleanupServerMail() {
	e.t.Helper()
	for _, path := range []string{"INBOX", e.targetPath} {
		var set imapx.UIDSet
		for _, m := range e.serverMessages(path) {
			if strings.Contains(strings.ToLower(string(m.Header)), "message-id: <e2e-") {
				set.Add(m.UID)
			}
		}
		if set.String() == "" {
			continue
		}
		ctx, cancel := e.opCtx()
		if err := e.raw.ExpungeUIDs(ctx, path, set); err != nil {
			e.t.Logf("best-effort expunge in %s failed: %v", path, err)
		}
		cancel()
	}
}

func TestE2EManagerRealIMAP(t *testing.T) {
	addr := os.Getenv("POSTHASTE_TEST_IMAP_ADDR")
	if addr == "" {
		t.Skip("POSTHASTE_TEST_IMAP_ADDR not set; integration harness is down")
	}
	username := os.Getenv("POSTHASTE_TEST_IMAP_USER")
	if username == "" {
		username = "test@posthaste.local"
	}
	password := os.Getenv("POSTHASTE_TEST_IMAP_PASSWORD")
	if password == "" {
		password = "posthaste"
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("integration harness unreachable at %s: %v", addr, err)
	}
	_ = conn.Close()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("POSTHASTE_TEST_IMAP_ADDR %q is not host:port: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("POSTHASTE_TEST_IMAP_ADDR %q has a non-numeric port: %v", addr, err)
	}

	env := newE2EEnv(t, host, port, username, password)
	run := env.run
	rootSubject := "E2E Thread Root " + run
	replySubject := "E2E Thread Reply " + run
	liveSubject := "E2E Live " + run
	rootID := "e2e-root-" + run + "@posthaste.local"
	liveID := "e2e-live-" + run + "@posthaste.local"

	env.addAccount()

	t.Run("initial sync settles with folders and seeded messages", func(t *testing.T) {
		// The first pass must reconcile the server's folder list, including
		// the user folder created before the account existed, and ingest both
		// seeded messages with bodies and thread assignments.
		e2eEventually(t, e2eSettleTimeout, "seeded messages in the synced inbox", func() (bool, string) {
			messages, detail := env.inboxSummaries()
			found := 0
			for _, m := range messages {
				if m.Subject == rootSubject || m.Subject == replySubject {
					found++
				}
			}
			return found == 2, fmt.Sprintf("%s (found %d of 2)", detail, found)
		})

		inbox, ok := env.folderByPath("INBOX")
		if !ok {
			t.Fatal("INBOX missing from the synced folder list")
		}
		if inbox.Type != "inbox" {
			t.Errorf("INBOX folder type = %q, want %q", inbox.Type, "inbox")
		}
		target, ok := env.folderByPath(env.targetPath)
		if !ok {
			t.Fatalf("user folder %q missing from the synced folder list", env.targetPath)
		}
		if target.Type != "user" {
			t.Errorf("folder %q type = %q, want %q", env.targetPath, target.Type, "user")
		}

		for _, want := range []struct {
			subject string
			from    string
			body    string
		}{
			{rootSubject, "root@posthaste.local", "Plain text body for run " + run},
			{replySubject, "reply@posthaste.local", "Reply body for run " + run},
		} {
			summary, ok := env.summaryWithSubject(want.subject)
			if !ok {
				t.Fatalf("message %q missing from the inbox listing", want.subject)
			}
			if summary.FromAddress != want.from {
				t.Errorf("message %q from = %q, want %q", want.subject, summary.FromAddress, want.from)
			}
			detail, err := env.mail.GetMessage(mustCtx(t), env.accountID, summary.ID)
			if err != nil {
				t.Fatalf("GetMessage(%q): %v", want.subject, err)
			}
			if !strings.Contains(detail.BodyText, want.body) {
				t.Errorf("message %q body text = %q, want it to contain %q", want.subject, detail.BodyText, want.body)
			}
		}

		// The worker must have parked in idle after the first pass.
		env.emitter.waitMatch(t, EventSyncState, 15*time.Second, func(payload any) bool {
			ev, ok := payload.(SyncStateEvent)
			return ok && ev.AccountID == env.accountID && ev.State == "idle"
		})
	})

	var rootSummary MessageSummaryInfo
	t.Run("thread groups the seeded reply with its root", func(t *testing.T) {
		root, ok := env.summaryWithSubject(rootSubject)
		if !ok {
			t.Fatalf("message %q missing", rootSubject)
		}
		reply, ok := env.summaryWithSubject(replySubject)
		if !ok {
			t.Fatalf("message %q missing", replySubject)
		}
		if root.ThreadID == 0 {
			t.Fatal("root message has no thread assigned")
		}
		if root.ThreadID != reply.ThreadID {
			t.Errorf("thread IDs diverge: root %d, reply %d", root.ThreadID, reply.ThreadID)
		}
		rootSummary = root
	})

	t.Run("live append wakes idle and syncs", func(t *testing.T) {
		// The worker is parked in IDLE on INBOX at this point; the harness
		// append must wake it through the server's EXISTS push, not a poll.
		appendCtx, appendCancel := context.WithTimeout(context.Background(), e2eServerOpTimeout)
		liveRaw := e2eMessage(liveID, liveSubject, "", "E2E Live <live@posthaste.local>",
			time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000"), "Live body for run "+run+".")
		if _, err := env.raw.Append(appendCtx, "INBOX", liveRaw, nil, time.Time{}); err != nil {
			t.Fatalf("live Append: %v", err)
		}
		appendCancel()

		idleWake := true
		if !e2eAppearsWithin(t, env, liveSubject, e2eIdleWakeTimeout) {
			idleWake = false
			t.Errorf("message appended live did not arrive within %s via the IDLE wake", e2eIdleWakeTimeout)
			// Fall back to an explicit sync so the remaining subtests still
			// exercise their assertions against a complete inbox.
			env.manager.TriggerSync(env.accountID)
			e2eEventually(t, e2eReplayTimeout, "live message after explicit trigger", func() (bool, string) {
				_, ok := env.summaryWithSubject(liveSubject)
				return ok, "live message not in the inbox listing"
			})
		}
		if !idleWake {
			return
		}
		summary, ok := env.summaryWithSubject(liveSubject)
		if !ok {
			t.Fatalf("message %q missing after sync", liveSubject)
		}
		if summary.Flags.Seen {
			t.Error("newly synced message is already marked seen")
		}
	})

	t.Run("set flags applies locally and replays to the server", func(t *testing.T) {
		summary, ok := env.summaryWithSubject(liveSubject)
		if !ok {
			t.Fatalf("message %q missing", liveSubject)
		}
		seen := true
		if err := env.mail.SetFlags(mustCtx(t), env.accountID, summary.ID, FlagPatch{Seen: &seen}); err != nil {
			t.Fatalf("SetFlags: %v", err)
		}

		// The local flag flips optimistically, before any server contact.
		e2eEventually(t, 5*time.Second, "local \\Seen flag", func() (bool, string) {
			fresh, ok := env.summaryWithSubject(liveSubject)
			return ok && fresh.Flags.Seen, "local flag not seen yet"
		})

		// The queued action must drain from the durable queue and surface as
		// \Seen on the real server.
		e2eEventually(t, e2eReplayTimeout, "server \\Seen replay", func() (bool, string) {
			flags, ok := env.serverFlags("INBOX", liveID)
			if !ok {
				return false, "message not found on the server"
			}
			for _, flag := range flags {
				if strings.EqualFold(flag, "\\Seen") {
					return true, "seen on the server"
				}
			}
			return false, fmt.Sprintf("server flags = %v, want \\Seen", flags)
		})
		e2eEventually(t, e2eReplayTimeout, "action queue drained", func() (bool, string) {
			rt, err := env.manager.Runtime(env.accountID)
			if err != nil {
				return false, err.Error()
			}
			ctx, cancel := context.WithTimeout(env.rootCtx, 5*time.Second)
			defer cancel()
			count, err := rt.store.CountActions(ctx)
			if err != nil {
				return false, err.Error()
			}
			return count == 0, fmt.Sprintf("%d actions still queued", count)
		})
	})

	t.Run("move replays to the server", func(t *testing.T) {
		target, ok := env.folderByPath(env.targetPath)
		if !ok {
			t.Fatalf("folder %q missing", env.targetPath)
		}
		if err := env.mail.MoveMessage(mustCtx(t), env.accountID, rootSummary.ID, target.ID); err != nil {
			t.Fatalf("MoveMessage: %v", err)
		}

		e2eEventually(t, e2eReplayTimeout, "move landing locally and on the server", func() (bool, string) {
			_, stillInInbox := env.summaryWithSubject(rootSubject)
			if stillInInbox {
				return false, "message still listed in the local inbox"
			}
			if ok, detail := env.serverHasMessage(env.targetPath, rootID); !ok {
				return false, "server target folder: " + detail
			}
			if stillThere, _ := env.serverHasMessage("INBOX", rootID); stillThere {
				return false, "server INBOX still has the message"
			}
			return true, ""
		})

		// The moved message's row must now live in the target folder locally,
		// exactly once: the optimistic row and the server's copy are the same
		// message, so the next pass must renumber the row instead of adding a
		// duplicate beside it.
		ctx, cancel := context.WithTimeout(env.rootCtx, 5*time.Second)
		defer cancel()
		messages, err := env.mail.ListMessages(ctx, env.accountID, MessageView{FolderID: target.ID}, 0)
		if err != nil {
			t.Fatalf("ListMessages(target): %v", err)
		}
		listed := 0
		for _, m := range messages {
			if m.Subject == rootSubject {
				listed++
			}
		}
		if listed != 1 {
			t.Errorf("moved message listed %d times in the local %q folder, want exactly once", listed, env.targetPath)
		}

		// Settle a full pass, then inspect the store directly: the target
		// folder holds a single row under a real server UID and the inbox
		// holds no copy. A further pass must neither duplicate nor drop the
		// renumbered row.
		settlePass(t, env)
		assertMovedRowSettled(t, env, env.targetPath, rootID)
		settlePass(t, env)
		assertMovedRowSettled(t, env, env.targetPath, rootID)
	})

	env.manager.Shutdown()

	if errs := env.logs.seen(); len(errs) > 0 {
		t.Errorf("manager logged %d error-level records during the run: %s", len(errs), strings.Join(errs, "; "))
	}
	e2eAssertGoroutinesSettled(t, env.baselineGoroutines)
	env.cleanupServerMail()
}

// e2eAppearsWithin polls for the live message and reports whether it arrived
// inside the window, so the caller can distinguish an IDLE wake from the
// explicit-trigger fallback.
func e2eAppearsWithin(t *testing.T, env *e2eEnv, subject string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := env.summaryWithSubject(subject); ok {
			return true
		}
		time.Sleep(e2ePollEvery)
	}
	return false
}

// e2eAssertGoroutinesSettled checks, tolerantly, that the workers wind down
// after shutdown instead of leaking. The small slack absorbs runtime and test
// framework goroutines that are not the manager's business.
func e2eAssertGoroutinesSettled(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= baseline+8 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine count after shutdown = %d, want at most %d (baseline %d); workers may leak", now, baseline+8, baseline)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// settlePass triggers one account sync pass and returns once that pass has
// finished parking again, matched by an idle sync-state event recorded after
// the call rather than any earlier one.
func settlePass(t *testing.T, env *e2eEnv) {
	t.Helper()
	before := len(env.emitter.all())
	env.manager.TriggerSync(env.accountID)
	e2eEventually(t, 15*time.Second, "a fresh idle sync-state event after the triggered pass", func() (bool, string) {
		events := env.emitter.all()
		for _, ev := range events[before:] {
			if state, ok := ev.Payload.(SyncStateEvent); ok &&
				state.AccountID == env.accountID && state.State == "idle" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("no idle event recorded after offset %d", before)
	})
}

// assertMovedRowSettled inspects the account store after a settled pass: the
// target folder holds exactly one row of the moved message, carrying a real
// server UID plus its body and thread, and the inbox holds no copy. Folders
// are resolved by IMAP path because the mail service's folder ids are the
// cross-account encoded ones, not the store's row ids.
func assertMovedRowSettled(t *testing.T, env *e2eEnv, targetPath, messageID string) {
	t.Helper()
	rt, err := env.manager.Runtime(env.accountID)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(env.rootCtx, 5*time.Second)
	defer cancel()
	target, err := rt.store.FolderByPath(ctx, targetPath)
	if err != nil {
		t.Fatalf("store FolderByPath(%q): %v", targetPath, err)
	}
	targetRows, err := rt.store.ListMessages(ctx, store.MessageQuery{FolderID: target.ID, Limit: 200})
	if err != nil {
		t.Fatalf("store ListMessages(target): %v", err)
	}
	copies := 0
	for _, m := range targetRows {
		if m.MessageIDHeader != messageID {
			continue
		}
		copies++
		if m.UID == 0 {
			t.Error("moved message row still has no server UID after a settled pass")
		}
		if m.RawMIMEPath == "" || m.ThreadID == 0 {
			t.Error("moved message row lost its body or thread")
		}
	}
	if copies != 1 {
		t.Errorf("target folder holds %d rows of the moved message, want 1", copies)
	}
	inbox, err := rt.store.FolderByPath(ctx, "INBOX")
	if err != nil {
		t.Fatalf("store FolderByPath(INBOX): %v", err)
	}
	inboxRows, err := rt.store.ListMessages(ctx, store.MessageQuery{FolderID: inbox.ID, Limit: 200})
	if err != nil {
		t.Fatalf("store ListMessages(inbox): %v", err)
	}
	for _, m := range inboxRows {
		if m.MessageIDHeader == messageID {
			t.Error("moved message still present in the local inbox after the move")
		}
	}
}

// mustCtx returns a short-lived context for one service call.
func mustCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}
