//go:build integration

// Round-trip tests against the real Dovecot harness from docker-compose.yml.
// They skip cleanly whenever the harness is not configured or unreachable, so
// the fast suite never depends on Docker.
package imapx_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/imapx"
)

const testMessageFormat = "From: sender@posthaste.local\r\nTo: %s\r\nSubject: %s\r\nDate: Mon, 14 Sep 2026 10:00:00 +0000\r\nMessage-ID: <%s@posthaste.local>\r\n\r\nPlain text body for %s.\r\n"

// connectHarness skips the test when the harness is not configured or not
// reachable, and otherwise returns an authenticated client. Only the address
// is mandatory; user and password fall back to the harness defaults so tests
// only need POSTHASTE_TEST_IMAP_ADDR set.
func connectHarness(t *testing.T) *imapx.Client {
	t.Helper()

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("POSTHASTE_TEST_IMAP_ADDR %q is not host:port: %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("POSTHASTE_TEST_IMAP_ADDR %q has a non-numeric port: %v", addr, err)
	}

	client, err := imapx.Dial(ctx, imapx.Config{
		Host:     host,
		Port:     port,
		TLS:      imapx.TLSNone,
		Username: username,
		Auth:     imapx.PasswordAuth(username, password),
	})
	if err != nil {
		t.Fatalf("imapx.Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client
}

func TestImapRoundTrip(t *testing.T) {
	client := connectHarness(t)
	username := os.Getenv("POSTHASTE_TEST_IMAP_USER")
	if username == "" {
		username = "test@posthaste.local"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// LIST: the harness always exposes INBOX.
	mailboxes, err := client.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var inboxSeen, delim string
	for _, mbox := range mailboxes {
		if strings.EqualFold(mbox.Path, "INBOX") {
			inboxSeen = mbox.Path
			delim = mbox.Delimiter
		}
	}
	if inboxSeen == "" {
		t.Fatalf("List did not report INBOX: %+v", mailboxes)
	}
	if delim == "" {
		delim = "/"
	}

	// CREATE a uniquely named nested folder so runs never interfere.
	run := fmt.Sprintf("itest-%d", time.Now().UnixNano())
	folder := strings.Join([]string{"posthaste", run, "target"}, delim)
	if err := client.EnsureFolder(ctx, folder); err != nil {
		t.Fatalf("EnsureFolder(%q): %v", folder, err)
	}

	// APPEND a message into INBOX and remember its UID.
	subject := "imapx round trip " + run
	raw := fmt.Sprintf(testMessageFormat, username, subject, run, run)
	appendCtx, appendCancel := context.WithTimeout(ctx, 15*time.Second)
	uid, err := client.Append(appendCtx, "INBOX", []byte(raw), nil, time.Now())
	appendCancel()
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if uid == 0 {
		t.Fatalf("Append did not report a UID (server without UIDPLUS/IMAP4rev2?)")
	}

	// SELECT INBOX.
	status, err := client.Select(ctx, "INBOX")
	if err != nil {
		t.Fatalf("Select(INBOX): %v", err)
	}
	if status.Messages < 1 {
		t.Errorf("Select(INBOX).Messages = %d, want at least 1", status.Messages)
	}

	// FETCH header-only: header present, body absent, and \Seen must NOT be
	// set by the fetch itself (BODY.PEEK semantics).
	messages := fetchSet(t, client, ctx, "INBOX", uid, imapx.FetchOptions{})
	msg := messages[0]
	if !strings.Contains(strings.ToLower(string(msg.Header)), strings.ToLower("subject: "+subject)) {
		t.Errorf("fetched header missing expected subject, got %q", msg.Header)
	}
	if msg.Body != nil {
		t.Errorf("header-only fetch returned a body of %d bytes", len(msg.Body))
	}
	if msg.Size <= 0 {
		t.Errorf("fetched size = %d, want > 0", msg.Size)
	}
	if msg.InternalDate.IsZero() {
		t.Error("fetched internal date is zero")
	}
	if hasFlag(msg.Flags, "\\Seen") {
		t.Error("peek fetch set \\Seen")
	}

	// FETCH the full raw message.
	messages = fetchSet(t, client, ctx, "INBOX", uid, imapx.FetchOptions{WithBody: true})
	if len(messages) != 1 {
		t.Fatalf("full fetch returned %d messages, want 1", len(messages))
	}
	if !strings.Contains(string(messages[0].Body), "Plain text body for "+run) {
		t.Errorf("full fetch body missing expected text, got %q", messages[0].Body)
	}
	if len(messages[0].Header) == 0 {
		t.Error("full fetch did not populate the header field")
	}

	// STORE flags: add \Flagged, verify, remove again.
	if err := client.StoreFlags(ctx, "INBOX", imapx.UIDSingle(uid), []string{"\\Flagged"}, nil); err != nil {
		t.Fatalf("StoreFlags add: %v", err)
	}
	messages = fetchSet(t, client, ctx, "INBOX", uid, imapx.FetchOptions{})
	if len(messages) != 1 || !hasFlag(messages[0].Flags, "\\Flagged") {
		t.Errorf("\\Flagged not stored, flags = %v", flagSummary(messages))
	}
	if err := client.StoreFlags(ctx, "INBOX", imapx.UIDSingle(uid), nil, []string{"\\Flagged"}); err != nil {
		t.Fatalf("StoreFlags remove: %v", err)
	}

	// MOVE the message into the test folder and verify it arrived there.
	if err := client.EnsureFolder(ctx, folder); err != nil {
		t.Fatalf("re-EnsureFolder: %v", err)
	}
	if err := client.Move(ctx, "INBOX", imapx.UIDSingle(uid), folder); err != nil {
		t.Fatalf("Move: %v", err)
	}
	var newest imapx.UIDSet
	newest.Add(0) // "*": everything up to the newest message
	moved, err := client.FetchByUID(ctx, folder, newest, imapx.FetchOptions{})
	if err != nil {
		t.Fatalf("FetchByUID in destination: %v", err)
	}
	movedSet := subjectsIn(moved, subject)
	if movedSet.Empty() {
		t.Errorf("moved message not found in %q, got %d message(s)", folder, len(moved))
	}

	// EXPUNGE the moved message from the test folder.
	if err := client.ExpungeUIDs(ctx, folder, movedSet); err != nil {
		t.Fatalf("ExpungeUIDs: %v", err)
	}
	left, err := client.FetchByUID(ctx, folder, newest, imapx.FetchOptions{})
	if err != nil {
		t.Fatalf("FetchByUID after expunge: %v", err)
	}
	if !subjectsIn(left, subject).Empty() {
		t.Errorf("message still present in %q after expunge", folder)
	}
}

// fetchSet fetches the single given UID and expects exactly one message back.
func fetchSet(t *testing.T, client *imapx.Client, ctx context.Context, path string, uid uint32, opts imapx.FetchOptions) []imapx.Message {
	t.Helper()

	fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
	defer fetchCancel()
	messages, err := client.FetchByUID(fetchCtx, path, imapx.UIDSingle(uid), opts)
	if err != nil {
		t.Fatalf("FetchByUID from %s (uid %d): %v", path, uid, err)
	}
	if len(messages) != 1 {
		t.Fatalf("fetch from %s returned %d messages, want 1", path, len(messages))
	}
	return messages
}

func hasFlag(flags []string, want string) bool {
	for _, flag := range flags {
		if strings.EqualFold(flag, want) {
			return true
		}
	}
	return false
}

// subjectsIn collects the UIDs of messages whose header mentions subject.
func subjectsIn(messages []imapx.Message, subject string) imapx.UIDSet {
	var set imapx.UIDSet
	for _, m := range messages {
		if strings.Contains(strings.ToLower(string(m.Header)), strings.ToLower("subject: "+subject)) {
			set.Add(m.UID)
		}
	}
	return set
}

func flagSummary(messages []imapx.Message) [][]string {
	var out [][]string
	for _, m := range messages {
		out = append(out, m.Flags)
	}
	return out
}

func TestImapSupportsIdle(t *testing.T) {
	client := connectHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	supported, err := client.SupportsIdle(ctx)
	if err != nil {
		t.Fatalf("SupportsIdle: %v", err)
	}
	if !supported {
		t.Error("Dovecot harness should advertise IDLE")
	}
}

// TestImapIdleWakesOnNewMail parks the connection in IDLE on INBOX and then
// delivers real mail through the harness's postfix so the server has to wake
// the IDLE. It also checks that cancelling the context ends the park.
func TestImapIdleWakesOnNewMail(t *testing.T) {
	client := connectHarness(t)
	rootCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	idleCtx, idleCancel := context.WithCancel(rootCtx)
	woke := make(chan error, 1)
	go func() { woke <- client.Idle(idleCtx, "INBOX") }()

	// Give the IDLE command time to be issued before the mail lands; a
	// nudge arriving before IDLE starts would be drained as stale.
	time.Sleep(time.Second)
	if err := smtpDeliver(t, "idle wake "+fmt.Sprint(time.Now().UnixNano())); err != nil {
		t.Fatalf("smtp delivery: %v", err)
	}

	select {
	case err := <-woke:
		if err != nil {
			t.Fatalf("Idle did not wake cleanly on new mail: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Idle still parked 20s after new mail arrived")
	}

	// A second park must end when the context is cancelled.
	idleCtx2, idleCancel2 := context.WithCancel(rootCtx)
	woke2 := make(chan error, 1)
	go func() { woke2 <- client.Idle(idleCtx2, "INBOX") }()
	time.Sleep(500 * time.Millisecond)
	idleCancel2()
	select {
	case err := <-woke2:
		if err == nil {
			t.Error("Idle returned nil after context cancellation, want a context error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Idle still parked 10s after context cancellation")
	}
	idleCancel()
}

// smtpDeliver pushes one message through the harness's postfix on the
// published SMTP port; postfix then writes it into the Maildir that dovecot
// serves, which is what wakes the IDLE.
func smtpDeliver(t *testing.T, subject string) error {
	t.Helper()

	addr := os.Getenv("POSTHASTE_TEST_SMTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:1025"
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial smtp %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	readReply := func(wantPrefix string) error {
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			return fmt.Errorf("read smtp reply: %w", err)
		}
		// Multiline replies (EHLO) end with the final status line, so
		// validate the last non-empty line only.
		lines := strings.Split(strings.TrimRight(string(buf[:n]), "\r\n"), "\r\n")
		last := lines[len(lines)-1]
		if !strings.HasPrefix(last, wantPrefix) {
			return fmt.Errorf("smtp reply %q, want final line with prefix %q", string(buf[:n]), wantPrefix)
		}
		return nil
	}
	send := func(cmd string) error {
		_, err := conn.Write([]byte(cmd + "\r\n"))
		return err
	}

	if err := readReply("220"); err != nil {
		return err
	}
	if err := send("EHLO posthaste-test"); err != nil {
		return err
	}
	if err := readReply("250"); err != nil {
		return err
	}
	if err := send("MAIL FROM:<harness@posthaste.local>"); err != nil {
		return err
	}
	if err := readReply("250"); err != nil {
		return err
	}
	if err := send("RCPT TO:<test@posthaste.local>"); err != nil {
		return err
	}
	if err := readReply("250"); err != nil {
		return err
	}
	if err := send("DATA"); err != nil {
		return err
	}
	if err := readReply("354"); err != nil {
		return err
	}
	body := "From: harness@posthaste.local\r\nTo: test@posthaste.local\r\nSubject: " + subject + "\r\n\r\nwake up\r\n.\r\n"
	// The body already ends with the DATA terminator, so it goes out as a
	// raw write; send would append another CRLF and postfix would read the
	// resulting empty line as a broken command.
	if _, err := conn.Write([]byte(body)); err != nil {
		return err
	}
	if err := readReply("250"); err != nil {
		return err
	}
	return send("QUIT")
}
