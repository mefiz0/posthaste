package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/mefiz0/posthaste/internal/store"
	"github.com/mefiz0/posthaste/internal/thread"
)

// TestReplayAppliesInOrderAndClears walks the queue through a successful
// replay: every action reaches the server oldest-first and leaves the queue.
func TestReplayAppliesInOrderAndClears(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Archive", "archive", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)

	inbox := env.upsertFolderRow("INBOX", "inbox", 5, 2)
	archive := env.upsertFolderRow("Archive", "archive", 5, 1)
	for _, tc := range []struct {
		kind   store.ActionKind
		uid    uint32
		add    store.Flags
		target int64
		want   string
	}{
		{store.ActionRead, 1, 0, 0, "flags INBOX 1 +[\\Seen] -[]"},
		{store.ActionFlag, 1, store.FlagFlagged, 0, "flags INBOX 1 +[\\Flagged] -[]"},
		{store.ActionUnread, 1, 0, 0, "flags INBOX 1 +[] -[\\Seen]"},
		{store.ActionMove, 1, 0, archive.ID, "move INBOX 1 -> Archive"},
	} {
		if _, err := env.db.EnqueueAction(context.Background(), store.Action{
			Kind: tc.kind, UID: tc.uid, FolderID: inbox.ID,
			AddFlags: tc.add, TargetFolderID: tc.target,
		}); err != nil {
			t.Fatalf("enqueue %s: %v", tc.kind, err)
		}
	}

	if err := env.worker.replayQueue(context.Background(), env.server); err != nil {
		t.Fatalf("replayQueue: %v", err)
	}

	calls := env.server.recordedCalls()
	if len(calls) != 4 {
		t.Fatalf("calls = %v, want the four queued actions", calls)
	}
	for i, want := range []string{
		"flags INBOX 1 +[\\Seen] -[]",
		"flags INBOX 1 +[\\Flagged] -[]",
		"flags INBOX 1 +[] -[\\Seen]",
		"move INBOX 1 -> Archive",
	} {
		if calls[i] != want {
			t.Fatalf("call %d = %q, want %q (replay order broken)", i, calls[i], want)
		}
	}
	if got := env.countActions(); got != 0 {
		t.Fatalf("actions left = %d, want 0", got)
	}
	if got := env.server.folderMessageCount("Archive"); got != 1 {
		t.Fatalf("archive messages = %d, want 1", got)
	}
}

// TestReplayDropsVanishedActionWithNotice proves that an action whose message
// disappeared server-side is dropped, not retried, and that the user is told
// through a generic notice.
func TestReplayDropsVanishedActionWithNotice(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	inbox := env.upsertFolderRow("INBOX", "inbox", 5, 2)

	if _, err := env.db.EnqueueAction(context.Background(), store.Action{
		Kind: store.ActionFlag, UID: 99, FolderID: inbox.ID, AddFlags: store.FlagSeen,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := env.worker.replayQueue(context.Background(), env.server); err != nil {
		t.Fatalf("replayQueue: %v", err)
	}

	if got := env.countActions(); got != 0 {
		t.Fatalf("vanished action kept in queue (%d left)", got)
	}
	var notices []string
	for _, event := range env.events.drain() {
		if event.Notice != "" {
			notices = append(notices, event.Notice)
		}
	}
	if len(notices) != 1 || notices[0] != droppedActionNotice {
		t.Fatalf("notices = %q, want exactly the generic drop notice", notices)
	}
}

// TestReplayStopsOnTransientError proves a transient failure keeps the queue
// intact, records the attempt, and stops before any later action runs.
func TestReplayStopsOnTransientError(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.server.addMessage("INBOX",
		plainTextMessage("<two@x>", "", nil, "A <a@x>", "b@x", "Two",
			"Tue, 01 Sep 2026 10:00:00 +0000", "second"), nil)
	inbox := env.upsertFolderRow("INBOX", "inbox", 5, 3)

	for _, uid := range []uint32{1, 2} {
		if _, err := env.db.EnqueueAction(context.Background(), store.Action{
			Kind: store.ActionRead, UID: uid, FolderID: inbox.ID,
		}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	env.server.setFlagsErr = errConnectionLost
	err := env.worker.replayQueue(context.Background(), env.server)
	if err == nil || !errors.Is(err, errConnectionLost) {
		t.Fatalf("replayQueue error = %v, want the transient failure", err)
	}

	actions, err := env.db.ListActions(context.Background(), 0)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("actions kept = %d, want 2", len(actions))
	}
	if actions[0].Attempts != 1 || actions[1].Attempts != 0 {
		t.Fatalf("attempts = %d/%d, want 1/0 (first tried, second untouched)",
			actions[0].Attempts, actions[1].Attempts)
	}
	if calls := env.server.recordedCalls(); len(calls) != 1 {
		t.Fatalf("calls = %v, want only the failed first action", calls)
	}

	// Once the fault clears, the same queue replays from the front.
	env.server.setFlagsErr = nil
	if err := env.worker.replayQueue(context.Background(), env.server); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if got := env.countActions(); got != 0 {
		t.Fatalf("actions left after recovery = %d, want 0", got)
	}
}

// TestQueuedActionsBlockFlagSync proves the sync never overwrites optimistic
// local state while offline actions are still queued, and resumes doing so
// once the queue drains.
func TestQueuedActionsBlockFlagSync(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), []string{"\\Seen"})
	env.worker.threads = thread.NewIndex(0)
	folder := env.upsertFolderRow("INBOX", "inbox", 5, 2)

	// A local row as the app would leave it after an optimistic unread: flags
	// cleared locally, an unread action queued for the server.
	rowID, err := env.db.InsertMessage(context.Background(), store.NewMessage{
		FolderID: folder.ID, UID: 1, Subject: "One",
	})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if _, err := env.db.EnqueueAction(context.Background(), store.Action{
		Kind: store.ActionUnread, UID: 1, FolderID: folder.ID, RemoveFlags: store.FlagSeen,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Server still shows the message as read; with the action queued the sync
	// must not push that state over the optimistic edit.
	if _, err := env.worker.syncFolder(context.Background(), env.server, folder); err != nil {
		t.Fatalf("syncFolder with queued action: %v", err)
	}
	if flags := env.dbMessage(rowID).Flags; flags.Has(store.FlagSeen) {
		t.Fatalf("flag sync fought a queued action: flags = %v", flags)
	}

	// Drain the queue (the replay pushes the unread state to the server) and
	// have another client set \Seen server-side again: with the queue empty,
	// the very same sync must now apply server authority over the local row.
	if err := env.worker.replayQueue(context.Background(), env.server); err != nil {
		t.Fatalf("replay: %v", err)
	}
	env.server.setFlagsDirect("INBOX", 1, []string{seenFlagName})
	if _, err := env.worker.syncFolder(context.Background(), env.server, folder); err != nil {
		t.Fatalf("syncFolder after replay: %v", err)
	}
	if flags := env.dbMessage(rowID).Flags; !flags.Has(store.FlagSeen) {
		t.Fatalf("server flags not applied after queue drained: %v", flags)
	}
}

// TestDefaultIsAuthError covers the classification heuristic.
func TestDefaultIsAuthError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"imap auth rejected", errors.New("imapx: authenticate with host: AUTHENTICATIONFAILED"), true},
		{"authfailed code", errors.New("imapx: greeting: AUTHFAILED"), true},
		{"missing keyring secret", errors.New("sync: fetch credentials: keyring: secret not found"), true},
		{"connection refused", errors.New("sync: dial imap.example: dial tcp: connection refused"), false},
		{"dns failure", errors.New("sync: dial imap.example: no such host"), false},
		{"tls handshake", errors.New("sync: dial imap.example: TLS handshake timeout"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := DefaultIsAuthError(tc.err); got != tc.want {
			t.Errorf("%s: DefaultIsAuthError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIsVanishedError covers the drop classification for failed actions.
func TestIsVanishedError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"sentinel", errVanished, true},
		{"no such message", errors.New("imapx: store flags: NO such message"), true},
		{"trycreate", errors.New("imapx: move: [TRYCREATE] mailbox missing"), true},
		{"does not exist", errors.New("imapx: expunge: message does not exist"), true},
		{"transient", errConnectionLost, false},
		{"timeout", errors.New("imapx: fetch: timeout"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isVanishedError(tc.err); got != tc.want {
			t.Errorf("%s: isVanishedError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestFlagMapping covers the IMAP flag mapping in both directions.
func TestFlagMapping(t *testing.T) {
	flags := parseFlags([]string{"\\Seen", "\\Flagged", "$Label1", "  \\answered  "})
	if !flags.Has(store.FlagSeen) || !flags.Has(store.FlagFlagged) || !flags.Has(store.FlagAnswered) {
		t.Fatalf("parseFlags missed known flags: %v", flags)
	}
	if flags.Has(store.FlagDraft) || flags.Has(store.FlagDeleted) {
		t.Fatalf("parseFlags invented flags: %v", flags)
	}
	names := flagNames(flags)
	want := []string{"\\Seen", "\\Answered", "\\Flagged"}
	if len(names) != len(want) {
		t.Fatalf("flagNames = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("flagNames = %v, want %v", names, want)
		}
	}
}
