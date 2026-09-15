package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	stdsync "sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"

	"github.com/mefiz0/posthaste/internal/attachment"
	"github.com/mefiz0/posthaste/internal/backoff"
	"github.com/mefiz0/posthaste/internal/store"
)

// testNow anchors every injected clock in the suite so timestamps are
// deterministic.
var testNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// fakeServer is an in-memory MailServer. The sync worker is its only caller
// during a run, but tests mutate it between passes, so every method and
// helper takes the lock. Idle deliberately does not: it must stay parked
// while tests mutate folder contents and nudge it.
type fakeServer struct {
	stdsync.Mutex

	folders map[string]*fakeFolder
	order   []string
	idleOK  bool
	nudge   chan struct{}

	calls       []string
	headerCalls []string
	appends     []fakeAppend

	// setFlagsErr fails every SetFlags until cleared; failNextFlags fails
	// exactly one. Both simulate transient (connection-level) faults; the
	// vanished case is expressed through "no such ..." wording instead.
	setFlagsErr   error
	failNextFlags error
	// fetchBodiesErr fails every full-body fetch, simulating a slow link that
	// times out mid-pass.
	fetchBodiesErr error

	closed bool
}

// fakeFolder is one mailbox in the fake server.
type fakeFolder struct {
	path        string
	delimiter   string
	role        string
	uidValidity uint32
	messages    []*fakeMessage
	nextUID     uint32
}

// fakeMessage is one stored message. header is the header block including its
// terminating blank line; body is the complete raw source.
type fakeMessage struct {
	uid    uint32
	flags  []string
	size   int64
	date   time.Time
	header []byte
	body   []byte
}

// fakeAppend records one APPEND call.
type fakeAppend struct {
	path  string
	raw   []byte
	flags []string
}

func newFakeServer(idleSupported bool) *fakeServer {
	return &fakeServer{
		folders: map[string]*fakeFolder{},
		idleOK:  idleSupported,
		nudge:   make(chan struct{}, 1),
	}
}

func (f *fakeServer) addFolder(path, role string, uidValidity uint32) *fakeFolder {
	f.Lock()
	defer f.Unlock()
	folder := &fakeFolder{path: path, delimiter: "/", role: role, uidValidity: uidValidity, nextUID: 1}
	f.folders[path] = folder
	f.order = append(f.order, path)
	return folder
}

func (f *fakeServer) addMessage(path string, raw []byte, flags []string) *fakeMessage {
	f.Lock()
	defer f.Unlock()
	folder := f.folders[path]
	header, _ := splitHeaderBlock(raw)
	msg := &fakeMessage{
		uid:    folder.nextUID,
		flags:  flags,
		size:   int64(len(raw)),
		date:   testNow,
		header: header,
		body:   raw,
	}
	folder.nextUID++
	folder.messages = append(folder.messages, msg)
	return msg
}

// resetFolder simulates the server rebuilding a mailbox: same path, new
// UIDVALIDITY, empty UID space.
func (f *fakeServer) resetFolder(path string, uidValidity uint32) {
	f.Lock()
	defer f.Unlock()
	folder := f.folders[path]
	folder.uidValidity = uidValidity
	folder.messages = nil
	folder.nextUID = 1
}

func (f *fakeServer) setFlagsDirect(path string, uid uint32, flags []string) {
	f.Lock()
	defer f.Unlock()
	if m := f.folders[path].byUID(uid); m != nil {
		m.flags = flags
	}
}

func (f *fakeServer) expunge(path string, uid uint32) {
	f.Lock()
	defer f.Unlock()
	folder := f.folders[path]
	for i, m := range folder.messages {
		if m.uid == uid {
			folder.messages = append(folder.messages[:i], folder.messages[i+1:]...)
			return
		}
	}
}

// Nudge wakes a parked Idle call, standing in for a server-side change report.
func (f *fakeServer) Nudge() {
	select {
	case f.nudge <- struct{}{}:
	default:
	}
}

// recordedCalls snapshots the call log.
func (f *fakeServer) recordedCalls() []string {
	f.Lock()
	defer f.Unlock()
	return append([]string(nil), f.calls...)
}

// recordedHeaderCalls snapshots which folders had their headers fetched.
func (f *fakeServer) recordedHeaderCalls() []string {
	f.Lock()
	defer f.Unlock()
	return append([]string(nil), f.headerCalls...)
}

func (f *fakeServer) recordedAppends() []fakeAppend {
	f.Lock()
	defer f.Unlock()
	return append([]fakeAppend(nil), f.appends...)
}

// folderMessageCount snapshots one folder's message count under the lock.
func (f *fakeServer) folderMessageCount(path string) int {
	f.Lock()
	defer f.Unlock()
	return len(f.folders[path].messages)
}

// serverIsClosed reports whether Close ran.
func (f *fakeServer) serverIsClosed() bool {
	f.Lock()
	defer f.Unlock()
	return f.closed
}

func (folder *fakeFolder) byUID(uid uint32) *fakeMessage {
	for _, m := range folder.messages {
		if m.uid == uid {
			return m
		}
	}
	return nil
}

func (folder *fakeFolder) unseenCount() int {
	unseen := 0
	for _, m := range folder.messages {
		if !parseFlags(m.flags).Has(store.FlagSeen) {
			unseen++
		}
	}
	return unseen
}

func (m *fakeMessage) toServerMessage(withBody bool) ServerMessage {
	out := ServerMessage{
		UID:    m.uid,
		Flags:  append([]string(nil), m.flags...),
		Size:   m.size,
		Date:   m.date,
		Header: m.header,
	}
	if withBody {
		out.Body = m.body
	}
	return out
}

func (f *fakeServer) ListFolders(ctx context.Context) ([]ServerFolder, error) {
	f.Lock()
	defer f.Unlock()
	out := make([]ServerFolder, 0, len(f.order))
	for _, path := range f.order {
		folder := f.folders[path]
		out = append(out, ServerFolder{Path: folder.path, Delimiter: folder.delimiter, Role: folder.role})
	}
	return out, nil
}

func (f *fakeServer) Select(ctx context.Context, path string) (FolderStatus, error) {
	f.Lock()
	defer f.Unlock()
	folder, ok := f.folders[path]
	if !ok {
		return FolderStatus{}, fmt.Errorf("sync: select folder %s: no such mailbox", path)
	}
	return FolderStatus{
		UIDValidity: folder.uidValidity,
		UIDNext:     folder.nextUID,
		Total:       len(folder.messages),
		Unseen:      folder.unseenCount(),
	}, nil
}

func (f *fakeServer) FetchHeaders(ctx context.Context, path string, fromUID uint32) ([]ServerMessage, error) {
	f.Lock()
	defer f.Unlock()
	f.headerCalls = append(f.headerCalls, path)
	folder, ok := f.folders[path]
	if !ok {
		return nil, fmt.Errorf("sync: fetch headers from %s: no such mailbox", path)
	}
	if fromUID < 1 {
		fromUID = 1
	}
	var out []ServerMessage
	for _, m := range folder.messages {
		if m.uid >= fromUID {
			out = append(out, m.toServerMessage(false))
		}
	}
	return out, nil
}

func (f *fakeServer) FetchFlags(ctx context.Context, path string, fromUID uint32) ([]ServerMessage, error) {
	f.Lock()
	defer f.Unlock()
	folder, ok := f.folders[path]
	if !ok {
		return nil, fmt.Errorf("sync: fetch flags from %s: no such mailbox", path)
	}
	if fromUID < 1 {
		fromUID = 1
	}
	var out []ServerMessage
	for _, m := range folder.messages {
		if m.uid >= fromUID {
			out = append(out, ServerMessage{UID: m.uid, Flags: append([]string(nil), m.flags...)})
		}
	}
	return out, nil
}

func (f *fakeServer) FetchBodies(ctx context.Context, path string, uids []uint32) ([]ServerMessage, error) {
	f.Lock()
	defer f.Unlock()
	if f.fetchBodiesErr != nil {
		return nil, f.fetchBodiesErr
	}
	folder, ok := f.folders[path]
	if !ok {
		return nil, fmt.Errorf("sync: fetch bodies from %s: no such mailbox", path)
	}
	want := make(map[uint32]bool, len(uids))
	for _, uid := range uids {
		want[uid] = true
	}
	var out []ServerMessage
	for _, m := range folder.messages {
		if want[m.uid] {
			out = append(out, m.toServerMessage(true))
		}
	}
	return out, nil
}

func (f *fakeServer) SetFlags(ctx context.Context, path string, uid uint32, add, remove []string) error {
	f.Lock()
	defer f.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("flags %s %d +%v -%v", path, uid, add, remove))
	if f.failNextFlags != nil {
		err := f.failNextFlags
		f.failNextFlags = nil
		return err
	}
	if f.setFlagsErr != nil {
		return f.setFlagsErr
	}
	folder, ok := f.folders[path]
	if !ok {
		return fmt.Errorf("sync: set flags on %s: no such mailbox", path)
	}
	m := folder.byUID(uid)
	if m == nil {
		return fmt.Errorf("sync: set flags on %s: no such message (uid %d)", path, uid)
	}
	for _, flag := range add {
		m.flags = appendFlag(m.flags, flag)
	}
	for _, flag := range remove {
		m.flags = removeFlag(m.flags, flag)
	}
	return nil
}

func (f *fakeServer) Move(ctx context.Context, path string, uid uint32, destPath string) error {
	f.Lock()
	defer f.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("move %s %d -> %s", path, uid, destPath))
	source, ok := f.folders[path]
	if !ok {
		return fmt.Errorf("sync: move from %s: no such mailbox", path)
	}
	dest, ok := f.folders[destPath]
	if !ok {
		return fmt.Errorf("sync: move from %s to %s: [TRYCREATE] no such mailbox", path, destPath)
	}
	m := source.byUID(uid)
	if m == nil {
		return fmt.Errorf("sync: move from %s: no such message (uid %d)", path, uid)
	}
	for i, candidate := range source.messages {
		if candidate == m {
			source.messages = append(source.messages[:i], source.messages[i+1:]...)
			break
		}
	}
	m.uid = dest.nextUID
	dest.nextUID++
	dest.messages = append(dest.messages, m)
	return nil
}

func (f *fakeServer) Delete(ctx context.Context, path string, uid uint32) error {
	f.Lock()
	defer f.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("delete %s %d", path, uid))
	folder, ok := f.folders[path]
	if !ok {
		return fmt.Errorf("sync: delete from %s: no such mailbox", path)
	}
	if folder.byUID(uid) == nil {
		return fmt.Errorf("sync: delete from %s: no such message (uid %d)", path, uid)
	}
	f.expungeLocked(path, uid)
	return nil
}

func (f *fakeServer) expungeLocked(path string, uid uint32) {
	folder := f.folders[path]
	for i, m := range folder.messages {
		if m.uid == uid {
			folder.messages = append(folder.messages[:i], folder.messages[i+1:]...)
			return
		}
	}
}

func (f *fakeServer) Append(ctx context.Context, path string, raw []byte, flags []string) error {
	f.Lock()
	defer f.Unlock()
	folder, ok := f.folders[path]
	if !ok {
		return fmt.Errorf("sync: append to %s: [TRYCREATE] no such mailbox", path)
	}
	f.appends = append(f.appends, fakeAppend{
		path:  path,
		raw:   append([]byte(nil), raw...),
		flags: append([]string(nil), flags...),
	})
	header, _ := splitHeaderBlock(raw)
	folder.messages = append(folder.messages, &fakeMessage{
		uid:    folder.nextUID,
		flags:  append([]string(nil), flags...),
		size:   int64(len(raw)),
		date:   testNow,
		header: header,
		body:   append([]byte(nil), raw...),
	})
	folder.nextUID++
	return nil
}

func (f *fakeServer) SupportsIdle(ctx context.Context) (bool, error) {
	f.Lock()
	defer f.Unlock()
	return f.idleOK, nil
}

func (f *fakeServer) Idle(ctx context.Context, path string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("sync: idle on %s: %w", path, ctx.Err())
	case <-f.nudge:
		return nil
	}
}

func (f *fakeServer) Close() error {
	f.Lock()
	defer f.Unlock()
	f.closed = true
	return nil
}

// appendFlag adds one flag name unless already present.
func appendFlag(flags []string, flag string) []string {
	for _, existing := range flags {
		if existing == flag {
			return flags
		}
	}
	return append(flags, flag)
}

// removeFlag drops one flag name.
func removeFlag(flags []string, flag string) []string {
	out := flags[:0]
	for _, existing := range flags {
		if existing != flag {
			out = append(out, existing)
		}
	}
	return out
}

// splitHeaderBlock cuts raw into the header block (including the blank line
// that ends it) and the rest.
func splitHeaderBlock(raw []byte) (header []byte, rest []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+4], raw[i+4:]
	}
	return raw, nil
}

// fakeFactory hands out the same fake server per dial and counts dials so
// tests can prove when the worker stops dialing.
type fakeFactory struct {
	server  *fakeServer
	dialErr error

	mu    stdsync.Mutex
	dials int
}

func (ff *fakeFactory) Dial(ctx context.Context) (MailServer, error) {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	ff.dials++
	if ff.dialErr != nil {
		return nil, ff.dialErr
	}
	return ff.server, nil
}

func (ff *fakeFactory) dialCount() int {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.dials
}

// eventCapture collects worker events without ever blocking the worker. The
// buffer comfortably exceeds anything the tests let a worker emit. Only the
// test goroutine reads the channel, so the per-state counters need no lock.
type eventCapture struct {
	ch   chan Event
	seen map[State]int
}

func newEventCapture() *eventCapture {
	return &eventCapture{ch: make(chan Event, 512), seen: map[State]int{}}
}

func (c *eventCapture) notify(event Event) {
	select {
	case c.ch <- event:
	default:
	}
}

// waitForState reads events until the cumulative occurrence count of want
// reaches n (counts carry across calls, so waiting for occurrence 2 in a
// second call observes the second event of that state in the stream),
// failing the test if it does not arrive in time.
func (c *eventCapture) waitForState(t *testing.T, want State, occurrence int) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-c.ch:
			c.seen[event.State]++
			if event.State == want && c.seen[want] == occurrence {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for state %q (occurrence %d, seen %d)", want, occurrence, c.seen[want])
		}
	}
}

// waitForProgress reads events until one carries the wanted progress phase,
// failing the test if it does not arrive in time.
func (c *eventCapture) waitForProgress(t *testing.T, phase string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-c.ch:
			c.seen[event.State]++
			if event.Progress != nil && event.Progress.Phase == phase {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for progress phase %q", phase)
		}
	}
}

// drain returns everything currently buffered.
func (c *eventCapture) drain() []Event {
	var out []Event
	for {
		select {
		case event := <-c.ch:
			out = append(out, event)
		default:
			return out
		}
	}
}

// testEnv bundles everything one worker test needs. Workers are not started
// automatically; call start after staging the initial server state.
type testEnv struct {
	t           *testing.T
	db          *store.Store
	rawBlobs    *attachment.Store
	attachBlobs *attachment.Store
	server      *fakeServer
	factory     *fakeFactory
	events      *eventCapture
	worker      *Worker
	cancel      context.CancelFunc
	done        chan error
	stopped     bool
}

func newTestEnv(t *testing.T, idleSupported bool) *testEnv {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(context.Background(), dir+"/accounts/acct.sqlite", store.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	db.SetClock(func() time.Time { return testNow })

	rawBlobs, err := attachment.Open(dir + "/messages")
	if err != nil {
		t.Fatalf("open raw blobs: %v", err)
	}
	attachBlobs, err := attachment.Open(dir + "/attachments")
	if err != nil {
		t.Fatalf("open attachment blobs: %v", err)
	}

	server := newFakeServer(idleSupported)
	factory := &fakeFactory{server: server}
	events := newEventCapture()

	worker, err := New(Deps{
		AccountID: "acct-1",
		Account: store.Account{
			ID:                           "acct-1",
			Email:                        "user@example.com",
			IMAPHost:                     "imap.example",
			IMAPPort:                     993,
			IMAPTLS:                      "tls",
			IMAPUsername:                 "user",
			PollIntervalSeconds:          0,
			AttachmentEagerThresholdByte: 8, // tiny so eager/lazy split is visible
		},
		AuthFunc: func(ctx context.Context) (sasl.Client, error) {
			return sasl.NewPlainClient("", "user", "secret"), nil
		},
		Dial:         factory.Dial,
		DB:           db,
		RawBlobs:     rawBlobs,
		AttachBlobs:  attachBlobs,
		Notify:       events.notify,
		PollFallback: 5 * time.Millisecond,
		Now:          func() time.Time { return testNow },
		Retry: backoff.Policy{
			Initial:     time.Millisecond,
			Max:         2 * time.Millisecond,
			Multiplier:  2,
			MaxAttempts: 3,
		},
	})
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}

	env := &testEnv{
		t:           t,
		db:          db,
		rawBlobs:    rawBlobs,
		attachBlobs: attachBlobs,
		server:      server,
		factory:     factory,
		events:      events,
		worker:      worker,
	}
	t.Cleanup(func() { _ = db.Close() })
	return env
}

// start runs the worker on its own goroutine and guarantees shutdown on test
// end.
func (env *testEnv) start() {
	env.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	env.cancel = cancel
	env.done = make(chan error, 1)
	go func() { env.done <- env.worker.Run(ctx) }()
	env.t.Cleanup(func() {
		cancel()
		if env.stopped {
			return
		}
		select {
		case <-env.done:
		case <-time.After(5 * time.Second):
			env.t.Error("sync worker did not stop after cancellation")
		}
	})
}

// stop cancels the worker and returns Run's result.
func (env *testEnv) stop() error {
	env.cancel()
	select {
	case err := <-env.done:
		env.stopped = true
		return err
	case <-time.After(5 * time.Second):
		env.t.Fatal("sync worker did not stop after cancellation")
		return nil
	}
}

// serverIsClosed exposes the close flag through the environment.
func (env *testEnv) serverIsClosed() bool { return env.server.serverIsClosed() }

func (env *testEnv) folderByPath(path string) store.Folder {
	env.t.Helper()
	folder, err := env.db.FolderByPath(context.Background(), path)
	if err != nil {
		env.t.Fatalf("folder %s: %v", path, err)
	}
	return folder
}

func (env *testEnv) messageByUID(folderID int64, uid uint32) store.Message {
	env.t.Helper()
	message, err := env.db.MessageByUID(context.Background(), folderID, uid)
	if err != nil {
		env.t.Fatalf("message uid %d: %v", uid, err)
	}
	return message
}

func (env *testEnv) countMessages(folderID int64) int {
	env.t.Helper()
	count, err := env.db.CountMessages(context.Background(), folderID)
	if err != nil {
		env.t.Fatalf("count messages: %v", err)
	}
	return count
}

func (env *testEnv) countActions() int {
	env.t.Helper()
	count, err := env.db.CountActions(context.Background())
	if err != nil {
		env.t.Fatalf("count actions: %v", err)
	}
	return count
}

// upsertFolderRow seeds the local folders table as a previous session would
// have left it.
func (env *testEnv) upsertFolderRow(path, role string, uidValidity, uidNext uint32) store.Folder {
	env.t.Helper()
	id, err := env.db.UpsertFolder(context.Background(), store.Folder{
		Name:        path,
		IMAPPath:    path,
		Type:        folderType(role),
		Delimiter:   "/",
		UIDValidity: uidValidity,
		UIDNext:     uidNext,
	})
	if err != nil {
		env.t.Fatalf("upsert folder %s: %v", path, err)
	}
	folder, err := env.db.FolderByID(context.Background(), id)
	if err != nil {
		env.t.Fatalf("load folder %s: %v", path, err)
	}
	return folder
}

// plainTextMessage builds a minimal RFC 5322 text/plain message.
func plainTextMessage(messageID, inReplyTo string, references []string, from, to, subject, date, body string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Message-ID: %s\r\n", messageID)
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", inReplyTo)
	}
	if len(references) > 0 {
		fmt.Fprintf(&b, "References: %s\r\n", joinMessageIDs(references))
	}
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n", from, to, subject, date)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return b.Bytes()
}

func joinMessageIDs(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += " "
		}
		out += "<" + id + ">"
	}
	return out
}

// richMimeMessage builds an HTML message with a remote image reference, an
// inline cid: image, and one oversized attachment.
func richMimeMessage() []byte {
	return []byte("Message-ID: <rich@example>\r\n" +
		"From: Alice Example <alice@example.com>\r\n" +
		"To: Bob Recipient <bob@example.com>\r\n" +
		"Cc: Carol CC <carol@example.com>\r\n" +
		"Subject: Newsletter\r\n" +
		"Date: Tue, 01 Sep 2026 10:00:00 +0000\r\n" +
		"Content-Type: multipart/mixed; boundary=\"outer\"\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: multipart/alternative; boundary=\"inner\"\r\n" +
		"\r\n" +
		"--inner\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"plain newsletter body\r\n" +
		"--inner\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>hello <b>html</b></p><script>alert(1)</script>" +
		"<img src=\"https://remote.example/pixel.png\">" +
		"<img src=\"cid:inline1@example\">\r\n" +
		"--inner--\r\n" +
		"--outer\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Disposition: inline; filename=\"logo.png\"\r\n" +
		"Content-ID: <inline1@example>\r\n" +
		"\r\n" +
		"PNGDATA\r\n" +
		"--outer\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"big.pdf\"\r\n" +
		"\r\n" +
		"%PDF-1.4 payload well beyond the eager threshold\r\n" +
		"--outer--\r\n")
}

// quietWindow asserts that no event arrives while the worker should be
// parked. The timer is an absence window, not a synchronization point.
func quietWindow(t *testing.T, c *eventCapture, d time.Duration) Event {
	t.Helper()
	select {
	case event := <-c.ch:
		return event
	case <-time.After(d):
		return Event{}
	}
}

// errConnectionLost is the canned transient failure.
var errConnectionLost = errors.New("connection reset by peer")

// errAuthRejected is the canned authentication failure.
var errAuthRejected = errors.New("imapx: authenticate with imap.example: AUTHENTICATIONFAILED")
