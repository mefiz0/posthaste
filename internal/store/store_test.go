package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts", "acct.sqlite")
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenAppliesMigrations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	var name string
	err := s.DB().QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='messages_fts'`).Scan(&name)
	if err != nil {
		t.Fatalf("FTS table missing: %v", err)
	}

	if _, err := s.DB().ExecContext(ctx, `SELECT COUNT(*) FROM outbox`); err != nil {
		t.Fatalf("outbox table missing: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `SELECT COUNT(*) FROM threads`); err != nil {
		t.Fatalf("threads table missing: %v", err)
	}
}

func TestOpenIsIdempotentAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.sqlite")
	backupDir := filepath.Join(dir, "backups")

	s, err := Open(context.Background(), path, Options{BackupDir: backupDir})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening an up-to-date database must not create a backup.
	s2, err := Open(context.Background(), path, Options{BackupDir: backupDir})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	matches, _ := filepath.Glob(filepath.Join(backupDir, "*.bak"))
	if len(matches) != 0 {
		t.Fatalf("unexpected backup created for current schema: %v", matches)
	}
}

func TestFolderAndMessageLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	folderID, err := s.UpsertFolder(ctx, Folder{
		Name: "Inbox", IMAPPath: "INBOX", Type: FolderInbox, Delimiter: "/",
	})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	// Upserting the same path must keep the same ID.
	again, err := s.UpsertFolder(ctx, Folder{Name: "Inbox", IMAPPath: "INBOX", Type: FolderInbox})
	if err != nil {
		t.Fatalf("UpsertFolder again: %v", err)
	}
	if again != folderID {
		t.Fatalf("folder id changed: %d != %d", again, folderID)
	}

	msgID, err := s.InsertMessage(ctx, NewMessage{
		FolderID:        folderID,
		UID:             42,
		MessageIDHeader: "<one@example.com>",
		FromAddress:     "sarah@example.com",
		FromName:        "Sarah Chen",
		ToAddresses:     "me@example.com",
		Subject:         "Project proposal",
		Date:            time.Unix(1_700_000_000, 0),
		BodyText:        "Here is the revised proposal about the contract.",
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	got, err := s.MessageByID(ctx, msgID)
	if err != nil {
		t.Fatalf("MessageByID: %v", err)
	}
	if got.Subject != "Project proposal" || got.FromAddress != "sarah@example.com" {
		t.Fatalf("unexpected message: %+v", got)
	}
	if got.Flags.Has(FlagSeen) {
		t.Fatal("new message should be unread")
	}

	if err := s.ApplyFlagDelta(ctx, msgID, FlagSeen|FlagFlagged, 0); err != nil {
		t.Fatalf("ApplyFlagDelta: %v", err)
	}
	got, _ = s.MessageByID(ctx, msgID)
	if !got.Flags.Has(FlagSeen) || !got.Flags.Has(FlagFlagged) {
		t.Fatalf("flags not applied: %b", got.Flags)
	}

	unread, err := s.CountUnread(ctx, folderID)
	if err != nil || unread != 0 {
		t.Fatalf("CountUnread = %d, %v", unread, err)
	}
}

func TestSearchUsesFTSAndFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	inbox, _ := s.UpsertFolder(ctx, Folder{Name: "Inbox", IMAPPath: "INBOX", Type: FolderInbox})
	sent, _ := s.UpsertFolder(ctx, Folder{Name: "Sent", IMAPPath: "Sent", Type: FolderSent})

	mustInsert := func(m NewMessage) int64 {
		id, err := s.InsertMessage(ctx, m)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		return id
	}
	mustInsert(NewMessage{FolderID: inbox, UID: 1, Subject: "Contract renewal",
		FromAddress: "daniel@example.com", FromName: "Daniel Okafor",
		BodyText: "Attaching the updated contract for the 2027 term.",
		Date:     time.Unix(1_700_000_000, 0), HasAttachments: true})
	mustInsert(NewMessage{FolderID: inbox, UID: 2, Subject: "Lunch next week",
		FromAddress: "anna@example.com", BodyText: "Are you around Tuesday?",
		Date: time.Unix(1_700_100_000, 0)})
	mustInsert(NewMessage{FolderID: sent, UID: 3, Subject: "Re: Contract renewal",
		FromAddress: "me@example.com", ToAddresses: "daniel@example.com",
		BodyText: "Sounds good, signing now.", Date: time.Unix(1_700_200_000, 0)})

	results, err := s.Search(ctx, SearchFilter{Text: "contract"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("text search returned %d, want 2", len(results))
	}

	results, err = s.Search(ctx, SearchFilter{From: []string{"daniel"}})
	if err != nil {
		t.Fatalf("from search: %v", err)
	}
	if len(results) != 1 || results[0].UID != 1 {
		t.Fatalf("from search wrong: %+v", results)
	}
	results, err = s.Search(ctx, SearchFilter{To: []string{"daniel"}})
	if err != nil {
		t.Fatalf("to search: %v", err)
	}
	if len(results) != 1 || results[0].UID != 3 {
		t.Fatalf("to search wrong: %+v", results)
	}

	hasAttachment := true
	results, err = s.Search(ctx, SearchFilter{Text: "contract", HasAttachment: &hasAttachment})
	if err != nil {
		t.Fatalf("attachment search: %v", err)
	}
	if len(results) != 1 || results[0].UID != 1 {
		t.Fatalf("attachment filter wrong: %+v", results)
	}

	unread := true
	results, err = s.Search(ctx, SearchFilter{Text: "contract", IsUnread: &unread})
	if err != nil {
		t.Fatalf("unread search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("unread filter returned %d, want 2", len(results))
	}

	results, err = s.Search(ctx, SearchFilter{Text: "contract", FolderID: sent})
	if err != nil {
		t.Fatalf("folder search: %v", err)
	}
	if len(results) != 1 || results[0].FolderID != sent {
		t.Fatalf("folder filter wrong: %+v", results)
	}
}

func TestSearchEscapesQuotes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	folder, _ := s.UpsertFolder(ctx, Folder{Name: "Inbox", IMAPPath: "INBOX", Type: FolderInbox})
	if _, err := s.InsertMessage(ctx, NewMessage{FolderID: folder, UID: 1,
		Subject: `A "quoted" subject`, BodyText: "body"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(ctx, SearchFilter{Text: `"quoted"`}); err != nil {
		t.Fatalf("quoted search should not be a syntax error: %v", err)
	}
}

func TestActionQueueOrderAndAttempts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.EnqueueAction(ctx, Action{Kind: ActionArchive, MessageID: 1, UID: 7})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	second, _ := s.EnqueueAction(ctx, Action{Kind: ActionRead, MessageID: 1, UID: 7})

	actions, err := s.ListActions(ctx, 0)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(actions) != 2 || actions[0].ID != first || actions[1].ID != second {
		t.Fatalf("actions out of order: %+v", actions)
	}
	if err := s.RecordActionAttempt(ctx, first, "boom"); err != nil {
		t.Fatalf("record attempt: %v", err)
	}
	actions, _ = s.ListActions(ctx, 0)
	if actions[0].Attempts != 1 || actions[0].LastError != "boom" {
		t.Fatalf("attempt not recorded: %+v", actions[0])
	}
	if err := s.DeleteAction(ctx, first); err != nil {
		t.Fatalf("delete action: %v", err)
	}
	count, _ := s.CountActions(ctx)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestOutboxStateTransitions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	msg := OutboxMessage{ID: "draft-1", AccountID: "acct", ToAddresses: "a@b.c",
		Subject: "Hi", BodyText: "Hello", State: SendDraft}
	if err := s.UpsertOutbox(ctx, msg); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.OutboxByID(ctx, "draft-1")
	if err != nil {
		t.Fatalf("OutboxByID: %v", err)
	}
	if got.State != SendDraft {
		t.Fatalf("state = %s", got.State)
	}

	if err := s.UpdateOutboxState(ctx, "draft-1", SendQueued, 0, "", 0); err != nil {
		t.Fatalf("transition: %v", err)
	}
	queued, err := s.ListOutbox(ctx, SendQueued)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued list = %d, %v", len(queued), err)
	}

	due, err := s.NextDueSend(ctx, time.Now().Unix())
	if err != nil {
		t.Fatalf("NextDueSend: %v", err)
	}
	if due.ID != "draft-1" {
		t.Fatalf("due id = %s", due.ID)
	}

	if err := s.UpdateOutboxState(ctx, "draft-1", SendFailed, 3, "smtp 550", 0); err != nil {
		t.Fatalf("fail: %v", err)
	}
	failed, _ := s.OutboxByID(ctx, "draft-1")
	if failed.State != SendFailed || failed.Attempts != 3 || failed.LastError != "smtp 550" {
		t.Fatalf("failed state wrong: %+v", failed)
	}
}

func TestThreadMergeRepointsMessages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	folder, _ := s.UpsertFolder(ctx, Folder{Name: "Inbox", IMAPPath: "INBOX", Type: FolderInbox})

	target, err := s.InsertThread(ctx, Thread{SubjectNormalized: "contract renewal"})
	if err != nil {
		t.Fatalf("insert thread: %v", err)
	}
	source, _ := s.InsertThread(ctx, Thread{SubjectNormalized: "contract renewal"})

	msgID, _ := s.InsertMessage(ctx, NewMessage{FolderID: folder, UID: 1,
		Subject: "Contract", ThreadID: source, Date: time.Unix(1_700_000_000, 0)})

	if err := s.MergeThreads(ctx, source, target); err != nil {
		t.Fatalf("merge: %v", err)
	}
	msg, _ := s.MessageByID(ctx, msgID)
	if msg.ThreadID != target {
		t.Fatalf("message thread = %d, want %d", msg.ThreadID, target)
	}
	if _, err := s.ThreadByID(ctx, source); !errors.Is(err, ErrNotFound) {
		t.Fatalf("source thread should be gone, got %v", err)
	}
	thread, _ := s.ThreadByID(ctx, target)
	if thread.MessageCount != 1 {
		t.Fatalf("target count = %d, want 1", thread.MessageCount)
	}
}

func TestAttachmentsAndBlobRefs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	folder, _ := s.UpsertFolder(ctx, Folder{Name: "Inbox", IMAPPath: "INBOX", Type: FolderInbox})
	msgID, _ := s.InsertMessage(ctx, NewMessage{FolderID: folder, UID: 1, Subject: "x"})

	for i := 0; i < 2; i++ {
		if _, err := s.InsertAttachment(ctx, Attachment{
			MessageID: msgID, Filename: "contract.pdf", MIMEType: "application/pdf",
			SizeBytes: 100, ContentHash: "abc123", FetchState: FetchFetched,
		}); err != nil {
			t.Fatalf("insert attachment: %v", err)
		}
	}
	attachments, err := s.ListAttachments(ctx, msgID)
	if err != nil || len(attachments) != 2 {
		t.Fatalf("attachments = %d, %v", len(attachments), err)
	}

	counts, err := s.BlobReferenceCounts(ctx)
	if err != nil {
		t.Fatalf("refcounts: %v", err)
	}
	if counts["abc123"] != 2 {
		t.Fatalf("refcount = %d, want 2", counts["abc123"])
	}

	if err := s.DeleteMessage(ctx, msgID); err != nil {
		t.Fatalf("delete message: %v", err)
	}
	counts, _ = s.BlobReferenceCounts(ctx)
	if len(counts) != 0 {
		t.Fatalf("attachments should cascade: %+v", counts)
	}
}

func TestContactsRanking(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.RecordContact(ctx, "frequent@example.com", "Frequent"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordContact(ctx, "once@example.com", "Once"); err != nil {
		t.Fatal(err)
	}
	results, err := s.SearchContacts(ctx, "", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(results) != 2 || results[0].Email != "frequent@example.com" {
		t.Fatalf("ranking wrong: %+v", results)
	}
	results, _ = s.SearchContacts(ctx, "frequ", 10)
	if len(results) != 1 {
		t.Fatalf("prefix filter wrong: %+v", results)
	}
}

func TestAccountRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.GetAccount(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	want := Account{ID: "acct-1", Email: "me@example.com", IMAPHost: "imap.example.com",
		IMAPPort: 993, IMAPTLS: "tls", SMTPHost: "smtp.example.com", SMTPPort: 465,
		SMTPTLS: "tls", AuthMethod: "password", CredentialRef: "posthaste:acct-1",
		NotificationsEnabled: true, PollIntervalSeconds: 300,
		AttachmentEagerThresholdByte: 2 << 20}
	if err := s.SaveAccount(ctx, want); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	got, err := s.GetAccount(ctx)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.ID != want.ID || got.Email != want.Email || got.IMAPHost != want.IMAPHost ||
		got.CredentialRef != want.CredentialRef || !got.NotificationsEnabled {
		t.Fatalf("account mismatch: %+v", got)
	}
}
