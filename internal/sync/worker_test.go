package sync

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/store"
)

// TestInitialSyncIngestsEverything covers a first full pass end to end:
// folders mirrored, messages with parsed bodies and sanitized HTML, raw MIME
// on disk, attachments eager vs lazy by threshold, contacts, and threads.
func TestInitialSyncIngestsEverything(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 7)
	env.server.addMessage("INBOX", richMimeMessage(), []string{"\\Seen"})
	env.server.addMessage("INBOX",
		plainTextMessage("<plain@x>", "", nil,
			"Bob Sender <bob@example.com>", "Alice <alice@example.com>",
			"Hi there", "Tue, 01 Sep 2026 09:00:00 +0000", "first body"), nil)
	env.server.addMessage("INBOX",
		plainTextMessage("<reply@x>", "<plain@x>", []string{"<plain@x>"},
			"Carol Reply <carol@example.com>", "bob@example.com",
			"Re: Hi there", "Tue, 01 Sep 2026 11:00:00 +0000", "the reply"), nil)
	env.start()

	env.events.waitForState(t, StateSyncing, 1)
	env.events.waitForState(t, StateIdle, 1)

	folder := env.folderByPath("INBOX")
	if folder.Type != store.FolderInbox {
		t.Fatalf("folder type = %q, want inbox", folder.Type)
	}
	if folder.UIDValidity != 7 {
		t.Fatalf("uid validity = %d, want 7", folder.UIDValidity)
	}
	if folder.UIDNext != 4 {
		t.Fatalf("uid next = %d, want 4", folder.UIDNext)
	}
	if folder.TotalCount != 3 {
		t.Fatalf("total count = %d, want 3", folder.TotalCount)
	}
	if got := env.countMessages(folder.ID); got != 3 {
		t.Fatalf("stored messages = %d, want 3", got)
	}

	// The rich message: sanitized HTML, derived text, raw MIME on disk.
	rich := env.messageByUID(folder.ID, 1)
	if !strings.Contains(rich.BodyHTML, "<b>html</b>") {
		t.Fatalf("body html lost benign markup: %q", rich.BodyHTML)
	}
	if strings.Contains(rich.BodyHTML, "script") || strings.Contains(rich.BodyHTML, "alert") {
		t.Fatalf("body html kept script content: %q", rich.BodyHTML)
	}
	if strings.Contains(rich.BodyHTML, "remote.example") {
		t.Fatalf("body html kept a remote reference: %q", rich.BodyHTML)
	}
	if !strings.Contains(rich.BodyHTML, "cid:inline1@example") {
		t.Fatalf("body html dropped the cid reference: %q", rich.BodyHTML)
	}
	if !strings.Contains(rich.BodyText, "plain newsletter body") {
		t.Fatalf("body text = %q", rich.BodyText)
	}
	if !rich.HasAttachments {
		t.Fatal("rich message not marked as having attachments")
	}
	if rich.Subject != "Newsletter" || rich.FromAddress != "alice@example.com" {
		t.Fatalf("header fields wrong: subject %q from %q", rich.Subject, rich.FromAddress)
	}
	if rich.RawMIMEPath == "" {
		t.Fatal("raw mime path empty")
	}
	stored, err := os.ReadFile(rich.RawMIMEPath)
	if err != nil {
		t.Fatalf("read raw mime: %v", err)
	}
	if string(stored) != string(richMimeMessage()) {
		t.Fatal("stored raw mime differs from the server copy")
	}

	// Attachments: inline image within the threshold is eager, the big PDF
	// stays lazy with metadata only.
	attachments, err := env.db.ListAttachments(context.Background(), rich.ID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(attachments))
	}
	var inline, big *store.Attachment
	for i := range attachments {
		switch attachments[i].Filename {
		case "logo.png":
			inline = &attachments[i]
		case "big.pdf":
			big = &attachments[i]
		}
	}
	if inline == nil || big == nil {
		t.Fatalf("attachment rows incomplete: %+v", attachments)
	}
	if inline.FetchState != store.FetchFetched || !env.attachBlobs.Has(inline.ContentHash) {
		t.Fatalf("inline image not fetched eagerly: %+v", inline)
	}
	if !inline.IsInline || inline.ContentID != "inline1@example" {
		t.Fatalf("inline image metadata wrong: %+v", inline)
	}
	if big.FetchState != store.FetchNotFetched || big.ContentHash != "" || big.StoragePath != "" {
		t.Fatalf("big attachment should be metadata-only: %+v", big)
	}
	if big.SizeBytes <= 8 {
		t.Fatalf("big attachment size = %d, want above threshold", big.SizeBytes)
	}

	// Contacts recorded from From/To/Cc.
	contacts, err := env.db.ListContacts(context.Background(), 50)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range contacts {
		seen[c.Email] = true
	}
	for _, want := range []string{"alice@example.com", "bob@example.com", "carol@example.com"} {
		if !seen[want] {
			t.Fatalf("contact %s missing (have %v)", want, seen)
		}
	}

	// The reply shares a thread with the original; the newsletter stands alone.
	plainMsg := env.messageByUID(folder.ID, 2)
	replyMsg := env.messageByUID(folder.ID, 3)
	if plainMsg.ThreadID == 0 || plainMsg.ThreadID != replyMsg.ThreadID {
		t.Fatalf("reply threading broken: original %d, reply %d", plainMsg.ThreadID, replyMsg.ThreadID)
	}
	if rich.ThreadID == 0 || rich.ThreadID == plainMsg.ThreadID {
		t.Fatalf("newsletter should have its own thread, got %d vs %d", rich.ThreadID, plainMsg.ThreadID)
	}
}

// TestThreadMergeOnLateAncestor feeds a reply that references an unknown
// message before the referenced message itself arrives; the subject-fallback
// thread and the chain-anchored thread must end up merged into one.
func TestThreadMergeOnLateAncestor(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 3)
	env.server.addMessage("INBOX",
		plainTextMessage("<a@x>", "", nil, "A <a@x>", "b@x",
			"Hello", "Tue, 01 Sep 2026 09:00:00 +0000", "original"), nil)
	env.server.addMessage("INBOX",
		plainTextMessage("<b@x>", "<x@x>", []string{"<x@x>"}, "B <b@x>", "a@x",
			"Re: Hello", "Tue, 01 Sep 2026 10:00:00 +0000", "reply before root"), nil)
	env.server.addMessage("INBOX",
		plainTextMessage("<x@x>", "", nil, "X <x@x>", "a@x",
			"Hello", "Tue, 01 Sep 2026 11:00:00 +0000", "late root"), nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)

	folder := env.folderByPath("INBOX")
	threadIDs := map[int64]bool{}
	for uid := uint32(1); uid <= 3; uid++ {
		m := env.messageByUID(folder.ID, uid)
		if m.ThreadID == 0 {
			t.Fatalf("message uid %d has no thread", uid)
		}
		threadIDs[m.ThreadID] = true
	}
	if len(threadIDs) != 1 {
		t.Fatalf("messages spread over %d threads, want 1 merge", len(threadIDs))
	}
	threads, err := env.db.ListThreads(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("thread rows = %d, want 1", len(threads))
	}
	if threads[0].MessageCount != 3 {
		t.Fatalf("thread message count = %d, want 3", threads[0].MessageCount)
	}
}

// TestUIDValidityChangeWipesAndRefetches verifies that a server-side mailbox
// rebuild discards every stored message and repopulates from scratch.
func TestUIDValidityChangeWipesAndRefetches(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 7)
	env.server.addMessage("INBOX",
		plainTextMessage("<old-1@x>", "", nil, "A <a@x>", "b@x", "Old one",
			"Tue, 01 Sep 2026 09:00:00 +0000", "old body"), nil)
	env.server.addMessage("INBOX",
		plainTextMessage("<old-2@x>", "", nil, "A <a@x>", "b@x", "Old two",
			"Tue, 01 Sep 2026 09:30:00 +0000", "old body two"), nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	folder := env.folderByPath("INBOX")
	if got := env.countMessages(folder.ID); got != 2 {
		t.Fatalf("pre-wipe messages = %d, want 2", got)
	}

	env.server.resetFolder("INBOX", 9)
	env.server.addMessage("INBOX",
		plainTextMessage("<new-1@x>", "", nil, "B <b@x>", "a@x", "Fresh start",
			"Tue, 01 Sep 2026 12:00:00 +0000", "new body"), nil)
	env.server.Nudge()
	env.events.waitForState(t, StateIdle, 2)

	folder = env.folderByPath("INBOX")
	if folder.UIDValidity != 9 {
		t.Fatalf("uid validity = %d, want 9", folder.UIDValidity)
	}
	if got := env.countMessages(folder.ID); got != 1 {
		t.Fatalf("post-wipe messages = %d, want 1", got)
	}
	fresh := env.messageByUID(folder.ID, 1)
	if fresh.MessageIDHeader != "new-1@x" {
		t.Fatalf("surviving message is %q, want the refetched one", fresh.MessageIDHeader)
	}
	if _, err := env.db.MessageByMessageID(context.Background(), "old-1@x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old message survived the wipe: %v", err)
	}
}

// TestIdleNudgeFetchesNewMail verifies the parked worker wakes, downloads the
// new message including its body, and parks again.
func TestIdleNudgeFetchesNewMail(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	folder := env.folderByPath("INBOX")

	env.server.addMessage("INBOX",
		plainTextMessage("<two@x>", "", nil, "A <a@x>", "b@x", "Two",
			"Tue, 01 Sep 2026 10:00:00 +0000", "second"), nil)
	env.server.Nudge()
	env.events.waitForState(t, StateIdle, 2)

	second := env.messageByUID(folder.ID, 2)
	if second.Subject != "Two" || second.RawMIMEPath == "" || second.ThreadID == 0 {
		t.Fatalf("new message incompletely ingested: %+v", second)
	}
}

// TestServerWinsFlagsAndExpunge verifies server authority once nothing is
// queued: flags are overwritten from the server and messages expunged
// elsewhere disappear locally.
func TestServerWinsFlagsAndExpunge(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), []string{"\\Flagged"})
	env.server.addMessage("INBOX",
		plainTextMessage("<two@x>", "", nil, "A <a@x>", "b@x", "Two",
			"Tue, 01 Sep 2026 10:00:00 +0000", "second"), nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	folder := env.folderByPath("INBOX")

	env.server.setFlagsDirect("INBOX", 1, []string{"\\Seen"})
	env.server.expunge("INBOX", 2)
	env.server.Nudge()
	env.events.waitForState(t, StateIdle, 2)

	if got := env.countMessages(folder.ID); got != 1 {
		t.Fatalf("messages after server-side expunge = %d, want 1", got)
	}
	one := env.messageByUID(folder.ID, 1)
	if !one.Flags.Has(store.FlagSeen) || one.Flags.Has(store.FlagFlagged) {
		t.Fatalf("flags not overwritten from server: %v", one.Flags)
	}
}

// TestQueueReplayOrderAndLocalOutcome replays a flag change and a move made
// while parked, in queue order, and verifies the server result flows back
// into the local store on the following full pass.
func TestQueueReplayOrderAndLocalOutcome(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Archive", "archive", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.server.addMessage("INBOX",
		plainTextMessage("<two@x>", "", nil, "A <a@x>", "b@x", "Two",
			"Tue, 01 Sep 2026 10:00:00 +0000", "second"), nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	inbox := env.folderByPath("INBOX")
	archive := env.folderByPath("Archive")

	// Two offline changes, flag first: both applied optimistically to the
	// local store, both recorded durably in the queue.
	flagged := env.messageByUID(inbox.ID, 1)
	moved := env.messageByUID(inbox.ID, 2)
	if err := env.db.ApplyFlagDelta(context.Background(), flagged.ID, store.FlagSeen, 0); err != nil {
		t.Fatalf("optimistic flag: %v", err)
	}
	if err := env.db.MoveMessage(context.Background(), moved.ID, archive.ID); err != nil {
		t.Fatalf("optimistic move: %v", err)
	}
	if _, err := env.db.EnqueueAction(context.Background(), store.Action{
		Kind: store.ActionFlag, MessageID: flagged.ID, UID: 1, FolderID: inbox.ID,
		AddFlags: store.FlagSeen,
	}); err != nil {
		t.Fatalf("enqueue flag: %v", err)
	}
	if _, err := env.db.EnqueueAction(context.Background(), store.Action{
		Kind: store.ActionMove, MessageID: moved.ID, UID: 2, FolderID: inbox.ID,
		TargetFolderID: archive.ID,
	}); err != nil {
		t.Fatalf("enqueue move: %v", err)
	}

	env.server.Nudge()
	env.events.waitForState(t, StateIdle, 2)

	calls := env.server.recordedCalls()
	if len(calls) < 2 {
		t.Fatalf("expected the flag and move to reach the server, got %v", calls)
	}
	if !strings.HasPrefix(calls[0], "flags INBOX 1") {
		t.Fatalf("first replayed action = %q, want the flag change", calls[0])
	}
	if !strings.HasPrefix(calls[1], "move INBOX 2 -> Archive") {
		t.Fatalf("second replayed action = %q, want the move", calls[1])
	}
	if got := env.countActions(); got != 0 {
		t.Fatalf("actions left queued = %d, want 0", got)
	}
	if got := env.server.folderMessageCount("Archive"); got != 1 {
		t.Fatalf("server archive messages = %d, want 1", got)
	}

	// The message the server still has keeps its queued flag state; the flag
	// sync that follows the replay must not undo it.
	one := env.dbMessage(flagged.ID)
	if !one.Flags.Has(store.FlagSeen) {
		t.Fatalf("queued flag lost after sync: %v", one.Flags)
	}

	// A full pass folds the server-side move into the local archive folder.
	env.worker.TriggerSync()
	env.events.waitForState(t, StateIdle, 3)
	archived := env.dbMessage(moved.ID)
	if archived.FolderID != archive.ID {
		t.Fatalf("moved message still in folder %d", archived.FolderID)
	}
}

// TestMoveReplayAdoptsMovedRowInsteadOfDuplicating replays an offline move
// and verifies the target folder's next pass renumbers the optimistic row
// from the server instead of inserting a second copy of the same message.
func TestMoveReplayAdoptsMovedRowInsteadOfDuplicating(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Archive", "archive", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first body"), []string{"\\Flagged"})
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	inbox := env.folderByPath("INBOX")
	archive := env.folderByPath("Archive")
	moved := env.messageByUID(inbox.ID, 1)
	originalThread := moved.ThreadID
	if originalThread == 0 {
		t.Fatal("precondition failed: message has no thread")
	}

	// The user moves the message offline; before the replay reaches the
	// server, the message is flagged down there, so the row must end up with
	// the server's flags and not the stale local ones.
	if err := env.db.MoveMessage(context.Background(), moved.ID, archive.ID); err != nil {
		t.Fatalf("optimistic move: %v", err)
	}
	if _, err := env.db.EnqueueAction(context.Background(), store.Action{
		Kind: store.ActionMove, MessageID: moved.ID, UID: 1, FolderID: inbox.ID,
		TargetFolderID: archive.ID,
	}); err != nil {
		t.Fatalf("enqueue move: %v", err)
	}
	env.server.setFlagsDirect("INBOX", 1, nil)

	env.server.Nudge()
	env.events.waitForState(t, StateIdle, 2)

	if got := env.server.folderMessageCount("Archive"); got != 1 {
		t.Fatalf("server archive messages = %d, want 1", got)
	}

	// The wake's light pass only touched the parked folder, so a full pass
	// brings the target folder up to date with the moved message.
	env.worker.TriggerSync()
	env.events.waitForState(t, StateIdle, 3)

	// The moved row is renumbered from the server, not duplicated: the folder
	// holds exactly one row, carrying the server UID, flags, body, and thread.
	if got := env.countMessages(archive.ID); got != 1 {
		t.Fatalf("local archive rows = %d, want 1 (a duplicate was ingested)", got)
	}
	adopted := env.dbMessage(moved.ID)
	if adopted.FolderID != archive.ID {
		t.Fatalf("moved row folder = %d, want %d", adopted.FolderID, archive.ID)
	}
	if adopted.UID != 1 {
		t.Fatalf("moved row uid = %d, want the server-assigned 1", adopted.UID)
	}
	if adopted.Flags != 0 {
		t.Fatalf("moved row flags = %v, want the server flags (none)", adopted.Flags)
	}
	if adopted.RawMIMEPath == "" || adopted.BodyText != "first body" {
		t.Fatalf("moved row lost its body: %+v", adopted)
	}
	if adopted.ThreadID != originalThread {
		t.Fatalf("moved row thread = %d, want original %d", adopted.ThreadID, originalThread)
	}
	if got := env.countMessages(inbox.ID); got != 0 {
		t.Fatalf("local inbox rows = %d, want 0", got)
	}

	// A further pass must neither re-ingest nor reshuffle the adopted row.
	env.worker.TriggerSync()
	env.events.waitForState(t, StateIdle, 4)
	if got := env.countMessages(archive.ID); got != 1 {
		t.Fatalf("archive rows after a settled pass = %d, want 1", got)
	}
	if again := env.dbMessage(moved.ID); again.UID != 1 || again.FolderID != archive.ID {
		t.Fatalf("adopted row drifted on the next pass: folder %d uid %d", again.FolderID, again.UID)
	}
}

// TestAdoptionAcrossFoldersAfterServerPreemptsMove covers the UID-less row
// whose move another client already performed: the replayed move finds no
// source message and is dropped, and the message's new server-side home
// adopts the optimistic row instead of inserting a second copy of it.
func TestAdoptionAcrossFoldersAfterServerPreemptsMove(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Target", "user", 5)
	env.server.addFolder("Archive", "archive", 5)
	raw := plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
		"Tue, 01 Sep 2026 09:00:00 +0000", "first body")
	env.server.addMessage("INBOX", raw, nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	inbox := env.folderByPath("INBOX")
	target := env.folderByPath("Target")
	archive := env.folderByPath("Archive")
	moved := env.messageByUID(inbox.ID, 1)
	originalThread := moved.ThreadID

	// The user moves the message to Target while offline; meanwhile another
	// client already moved it to Archive on the server.
	if err := env.db.MoveMessage(context.Background(), moved.ID, target.ID); err != nil {
		t.Fatalf("optimistic move: %v", err)
	}
	if _, err := env.db.EnqueueAction(context.Background(), store.Action{
		Kind: store.ActionMove, MessageID: moved.ID, UID: 1, FolderID: inbox.ID,
		TargetFolderID: target.ID,
	}); err != nil {
		t.Fatalf("enqueue move: %v", err)
	}
	env.server.expunge("INBOX", 1)
	env.server.addMessage("Archive", raw, []string{"\\Seen"})

	env.server.Nudge()
	env.worker.TriggerSync()
	env.events.waitForState(t, StateIdle, 2)

	// The replayed move found no source message: server truth won and the
	// queued action was dropped.
	if got := env.countActions(); got != 0 {
		t.Fatalf("actions left queued = %d, want the vanished move dropped", got)
	}
	// Archive's pass adopted the UID-less row: it moved from Target to
	// Archive under the server UID with the server flags, keeping the body
	// and thread from the original ingest.
	if got := env.countMessages(archive.ID); got != 1 {
		t.Fatalf("archive rows = %d, want 1", got)
	}
	if got := env.countMessages(target.ID); got != 0 {
		t.Fatalf("target rows = %d, want the preempted optimistic row gone", got)
	}
	adopted := env.dbMessage(moved.ID)
	if adopted.FolderID != archive.ID || adopted.UID != 1 {
		t.Fatalf("adopted row folder = %d uid = %d, want archive under uid 1", adopted.FolderID, adopted.UID)
	}
	if !adopted.Flags.Has(store.FlagSeen) {
		t.Fatalf("adopted row flags = %v, want the server flags", adopted.Flags)
	}
	if adopted.RawMIMEPath == "" || adopted.BodyText != "first body" {
		t.Fatalf("adopted row lost its body: %+v", adopted)
	}
	if adopted.ThreadID != originalThread {
		t.Fatalf("adopted row thread = %d, want original %d", adopted.ThreadID, originalThread)
	}
}

// TestSameMessageInTwoFoldersKeepsBothRows verifies that one Message-ID
// delivered to two folders ingests as two rows: only a UID-less row is
// adoptable across folders, so a legitimately copied message (sent/drafts
// style) is never merged away on a later pass.
func TestSameMessageInTwoFoldersKeepsBothRows(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Sent", "sent", 5)
	raw := plainTextMessage("<copy@x>", "", nil, "A <a@x>", "b@x", "Copy",
		"Tue, 01 Sep 2026 09:00:00 +0000", "same content")
	env.server.addMessage("INBOX", raw, nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	inbox := env.folderByPath("INBOX")
	inboxRow := env.messageByUID(inbox.ID, 1)

	// The same Message-ID shows up in Sent afterwards, as an independent
	// server-side copy with its own UID space. A triggered full pass brings
	// the Sent folder up to date.
	env.server.addMessage("Sent", raw, []string{"\\Seen"})
	env.worker.TriggerSync()
	env.events.waitForState(t, StateIdle, 2)

	sent := env.folderByPath("Sent")
	if got := env.countMessages(sent.ID); got != 1 {
		t.Fatalf("sent rows = %d, want 1", got)
	}
	sentRow := env.messageByUID(sent.ID, 1)
	if sentRow.ID == inboxRow.ID {
		t.Fatal("sent copy adopted the inbox row instead of ingesting its own")
	}
	if !sentRow.Flags.Has(store.FlagSeen) {
		t.Fatalf("sent copy flags = %v, want the server flags", sentRow.Flags)
	}
	if got := env.countMessages(inbox.ID); got != 1 {
		t.Fatalf("inbox rows = %d, want the original copy untouched", got)
	}
	if again := env.dbMessage(inboxRow.ID); again.FolderID != inbox.ID || again.UID != 1 {
		t.Fatalf("inbox copy drifted: folder %d uid %d", again.FolderID, again.UID)
	}
}

// TestMessageWithoutMessageIDNeverAdopts verifies the pathological-match
// guard: rows without a Message-ID header identify nothing, so they are never
// adopted and the server copy ingests as a fresh row instead.
func TestMessageWithoutMessageIDNeverAdopts(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Target", "user", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("", "", nil, "A <a@x>", "b@x", "Anonymous",
			"Tue, 01 Sep 2026 09:00:00 +0000", "no message id"), nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	inbox := env.folderByPath("INBOX")
	target := env.folderByPath("Target")
	moved := env.messageByUID(inbox.ID, 1)
	if moved.MessageIDHeader != "" {
		t.Fatalf("headerless message stored Message-ID %q", moved.MessageIDHeader)
	}

	// The same move happens optimistically and, through the replay, on the
	// server: the target folder receives the message under a new UID with
	// still no Message-ID to match on.
	if err := env.db.MoveMessage(context.Background(), moved.ID, target.ID); err != nil {
		t.Fatalf("optimistic move: %v", err)
	}
	if _, err := env.db.EnqueueAction(context.Background(), store.Action{
		Kind: store.ActionMove, MessageID: moved.ID, UID: 1, FolderID: inbox.ID,
		TargetFolderID: target.ID,
	}); err != nil {
		t.Fatalf("enqueue move: %v", err)
	}

	env.server.Nudge()
	env.worker.TriggerSync()
	env.events.waitForState(t, StateIdle, 2)

	if got := env.countMessages(target.ID); got != 2 {
		t.Fatalf("target rows = %d, want 2 (the UID-less row plus a fresh row for the unidentifiable message)", got)
	}
	optimistic := env.dbMessage(moved.ID)
	if optimistic.UID != 0 || optimistic.FolderID != target.ID {
		t.Fatalf("UID-less row changed: folder %d uid %d", optimistic.FolderID, optimistic.UID)
	}
	fresh := env.messageByUID(target.ID, 1)
	if fresh.ID == moved.ID {
		t.Fatal("fresh row adopted the UID-less row despite the empty Message-ID")
	}
}

// TestTransientFailureReconnectsAndReplays pushes a queued action through one
// connection failure and proves the worker goes offline, redials, and
// completes the replay without losing or reordering the queue.
func TestTransientFailureReconnectsAndReplays(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.start()

	env.events.waitForState(t, StateIdle, 1)
	inbox := env.folderByPath("INBOX")
	msg := env.messageByUID(inbox.ID, 1)
	env.server.failNextFlags = errConnectionLost
	if _, err := env.db.EnqueueAction(context.Background(), store.Action{
		Kind: store.ActionFlag, MessageID: msg.ID, UID: 1, FolderID: inbox.ID,
		AddFlags: store.FlagSeen,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	env.server.Nudge()
	offline := env.events.waitForState(t, StateOffline, 1)
	if !strings.Contains(offline.Err, "connection reset") {
		t.Fatalf("offline event err = %q, want the transient failure", offline.Err)
	}
	env.events.waitForState(t, StateIdle, 2)

	if env.factory.dialCount() != 2 {
		t.Fatalf("dials = %d, want exactly one reconnect", env.factory.dialCount())
	}
	if got := env.countActions(); got != 0 {
		t.Fatalf("actions left queued = %d, want 0", got)
	}
	calls := env.server.recordedCalls()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "flags INBOX 1") || !strings.HasPrefix(calls[1], "flags INBOX 1") {
		t.Fatalf("call log = %v, want two flag attempts on the same message", calls)
	}
	one := env.dbMessage(msg.ID)
	if !one.Flags.Has(store.FlagSeen) {
		t.Fatalf("replayed flag not reflected locally: %v", one.Flags)
	}
}

// TestAuthFailureParksUntilTrigger proves that persistent authentication
// failures stop dialing once the backoff budget is spent and only resume on
// an explicit trigger.
func TestAuthFailureParksUntilTrigger(t *testing.T) {
	env := newTestEnv(t, true)
	env.factory.dialErr = errAuthRejected
	env.start()

	// The env policy allows three retries: four failing dials (initial plus
	// three) exhaust the budget, and the worker must then sit still.
	const dialsPerRound = 4
	env.events.waitForState(t, StateAuthFailed, dialsPerRound)
	if got := env.factory.dialCount(); got != dialsPerRound {
		t.Fatalf("dials before parking = %d, want %d", got, dialsPerRound)
	}
	if event := quietWindow(t, env.events, 40*time.Millisecond); event.State != "" {
		t.Fatalf("worker kept working while parked: %+v", event)
	}

	env.worker.TriggerSync()
	env.events.waitForState(t, StateAuthFailed, 2*dialsPerRound)
	if got := env.factory.dialCount(); got != 2*dialsPerRound {
		t.Fatalf("dials after trigger = %d, want %d", got, 2*dialsPerRound)
	}
	if event := quietWindow(t, env.events, 40*time.Millisecond); event.State != "" {
		t.Fatalf("worker kept dialing while parked: %+v", event)
	}

	if err := env.stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// TestPollFallbackWhenIdleUnsupported verifies the ticker path drives repeated
// passes when the server has no IDLE.
func TestPollFallbackWhenIdleUnsupported(t *testing.T) {
	env := newTestEnv(t, false)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.start()

	// Initial pass, then at least two poll-driven passes.
	env.events.waitForState(t, StateSyncing, 3)
	env.events.waitForState(t, StateIdle, 3)
	if got := env.countMessages(env.folderByPath("INBOX").ID); got != 1 {
		t.Fatalf("messages = %d, want 1", got)
	}
}

// TestAppendRawServedBetweenPasses verifies a queued APPEND interrupts IDLE,
// runs on the worker's connection, and reports back to the caller.
func TestAppendRawServedBetweenPasses(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Sent", "sent", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.start()
	env.events.waitForState(t, StateIdle, 1)

	baseline := len(env.server.recordedHeaderCalls())

	raw := plainTextMessage("<sent@x>", "", nil, "me@example.com", "a@x",
		"Sent copy", "Tue, 01 Sep 2026 12:00:00 +0000", "outgoing")
	if err := env.worker.AppendRaw(context.Background(), "Sent", raw, []string{"\\Seen"}); err != nil {
		t.Fatalf("AppendRaw: %v", err)
	}

	appends := env.server.recordedAppends()
	if len(appends) != 1 {
		t.Fatalf("appends = %d, want 1", len(appends))
	}
	if appends[0].path != "Sent" || string(appends[0].raw) != string(raw) {
		t.Fatalf("append mismatch: path %q body match %v", appends[0].path, string(appends[0].raw) == string(raw))
	}

	// The APPEND wakes the worker and the following pass folds the copy into
	// the local Sent folder, without an explicit trigger or a full pass.
	env.events.waitForState(t, StateIdle, 2)
	sent := env.folderByPath("Sent")
	if got := env.countMessages(sent.ID); got != 1 {
		t.Fatalf("sent messages = %d, want 1", got)
	}
	if stored := env.messageByUID(sent.ID, 1); stored.MessageIDHeader != "sent@x" || !stored.Flags.Has(store.FlagSeen) {
		t.Fatalf("appended message ingested wrong: %+v", stored)
	}

	// Only the appended folder is re-listed; INBOX must not be fetched again.
	resync := env.server.recordedHeaderCalls()[baseline:]
	for _, path := range resync {
		if path == "INBOX" {
			t.Fatalf("append triggered an INBOX resync: %v", resync)
		}
	}
	if len(resync) == 0 || resync[len(resync)-1] != "Sent" {
		t.Fatalf("append did not resync Sent: %v", resync)
	}
}

// TestSteadyStatePassUsesFlagsOnlyForReconcile guards the traffic reduction:
// a pass with nothing new fetches each folder's headers once (windowing from
// the stored cursor) and reconciles flags with a flags-only fetch, instead of
// pulling every header in the mailbox a second time.
func TestSteadyStatePassUsesFlagsOnlyForReconcile(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.start()
	env.events.waitForState(t, StateIdle, 1)
	baseline := len(env.server.recordedHeaderCalls())

	env.worker.TriggerSync()
	env.events.waitForState(t, StateIdle, 2)

	resync := env.server.recordedHeaderCalls()[baseline:]
	if len(resync) != 1 || resync[0] != "INBOX" {
		t.Fatalf("steady-state header fetches = %v, want just the INBOX window", resync)
	}
}

// TestBodyFetchFailureDoesNotBlockOtherFolders proves headers for every
// folder are reconciled before bodies are downloaded, so a timing-out body
// transfer in one folder (INBOX) still lets another (Sent) show its messages.
func TestBodyFetchFailureDoesNotBlockOtherFolders(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Sent", "sent", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "Inbox one",
			"Tue, 01 Sep 2026 09:00:00 +0000", "inbox"), nil)
	env.server.addMessage("Sent",
		plainTextMessage("<sent@x>", "", nil, "me@x", "a@x", "Sent one",
			"Tue, 01 Sep 2026 09:05:00 +0000", "sent"), nil)
	env.server.fetchBodiesErr = errors.New("sync: fetch bodies: read tcp: i/o timeout")
	env.start()
	env.events.waitForState(t, StateIdle, 1)

	sent := env.folderByPath("Sent")
	if got := env.countMessages(sent.ID); got != 1 {
		t.Fatalf("Sent headers = %d, want 1 despite INBOX body failure", got)
	}
	inbox := env.folderByPath("INBOX")
	if got := env.countMessages(inbox.ID); got != 1 {
		t.Fatalf("INBOX headers = %d, want 1", got)
	}
}

// TestSyncEmitsFolderProgress verifies a pass reports folder-level progress so
// the UI can show a breakdown instead of an opaque "syncing".
func TestSyncEmitsFolderProgress(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.start()

	start := env.events.waitForProgress(t, "pass-start")
	if start.Progress.Total != 1 {
		t.Fatalf("pass-start folders = %d, want 1", start.Progress.Total)
	}
	progress := env.events.waitForProgress(t, "folder-done")
	if progress.Progress.Folder != "INBOX" || progress.Progress.New != 1 {
		t.Fatalf("folder-done = %+v, want INBOX with 1 new", progress.Progress)
	}
}

// TestTriggerFolderSyncTargetsOneFolder verifies an action confined to one
// folder only re-lists that folder, not every mailbox.
func TestTriggerFolderSyncTargetsOneFolder(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Sent", "sent", 5)
	env.start()
	env.events.waitForState(t, StateIdle, 1)
	baseline := len(env.server.recordedHeaderCalls())

	env.worker.TriggerFolderSync("Sent")
	env.events.waitForState(t, StateIdle, 2)

	resync := env.server.recordedHeaderCalls()[baseline:]
	for _, path := range resync {
		if path == "INBOX" {
			t.Fatalf("folder trigger resynced INBOX: %v", resync)
		}
	}
	if len(resync) == 0 || resync[len(resync)-1] != "Sent" {
		t.Fatalf("folder trigger did not resync Sent: %v", resync)
	}
}

// TestTriggerSyncRunsEveryFolder verifies a manual refresh still scans all
// folders.
func TestTriggerSyncRunsEveryFolder(t *testing.T) {
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addFolder("Sent", "sent", 5)
	env.start()
	env.events.waitForState(t, StateIdle, 1)
	baseline := len(env.server.recordedHeaderCalls())

	env.worker.TriggerSync()
	env.events.waitForState(t, StateIdle, 2)

	resync := env.server.recordedHeaderCalls()[baseline:]
	seen := make(map[string]bool)
	for _, path := range resync {
		seen[path] = true
	}
	if !seen["INBOX"] || !seen["Sent"] {
		t.Fatalf("full trigger did not scan every folder: %v", resync)
	}
}

// TestAppendRawWithoutRunner documents that AppendRaw without a running
// worker fails via the context instead of hanging or panicking.
func TestAppendRawWithoutRunner(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := env.worker.AppendRaw(ctx, "Sent", []byte("x"), nil); err == nil {
		t.Fatal("AppendRaw with a dead context should fail")
	}
}

// TestShutdownStopsCleanly proves Run returns promptly on cancellation
// without leaking the goroutines it created.
func TestShutdownStopsCleanly(t *testing.T) {
	before := runtime.NumGoroutine()
	env := newTestEnv(t, true)
	env.server.addFolder("INBOX", "inbox", 5)
	env.server.addMessage("INBOX",
		plainTextMessage("<one@x>", "", nil, "A <a@x>", "b@x", "One",
			"Tue, 01 Sep 2026 09:00:00 +0000", "first"), nil)
	env.start()
	env.events.waitForState(t, StateIdle, 1) // parked inside IDLE now

	if err := env.stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if !env.serverIsClosed() {
		t.Fatal("server connection not closed on shutdown")
	}

	// The IDLE watcher is joined before Run returns; allow the runtime a
	// bounded settling window to reap exited goroutines before counting.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Fatalf("goroutines after shutdown = %d, baseline %d", got, before)
	}
}

// dbMessage loads a row by id, failing the test when missing.
func (env *testEnv) dbMessage(id int64) store.Message {
	env.t.Helper()
	message, err := env.db.MessageByID(context.Background(), id)
	if err != nil {
		env.t.Fatalf("message %d: %v", id, err)
	}
	return message
}
