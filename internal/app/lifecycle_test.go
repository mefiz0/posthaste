package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/store"
	mailsync "github.com/mefiz0/posthaste/internal/sync"
)

func TestGlobalFolderIDCodecRoundTrip(t *testing.T) {
	for ordinal := int64(1); ordinal < 40; ordinal++ {
		for _, local := range []int64{1, 2, 999, 1 << 20} {
			global := globalFolderID(int(ordinal), local)
			gotOrdinal, gotLocal := decodeFolderID(global)
			if gotOrdinal != int(ordinal) || gotLocal != local {
				t.Fatalf("decode(%d) = (%d, %d), want (%d, %d)",
					global, gotOrdinal, gotLocal, ordinal, local)
			}
		}
	}
	// Different accounts never produce the same global ID for their local
	// folder 1, which is what keeps the unified sidebar unambiguous.
	if globalFolderID(1, 1) == globalFolderID(2, 1) {
		t.Fatal("folder IDs collide across accounts")
	}
}

func TestManagerSyncPassProducesEventsAndMessages(t *testing.T) {
	h := newHarness(t, 1)
	id := h.addAccount(t, "me@here.test")

	h.emitter.waitFor(t, EventFoldersChanged, 10*time.Second)
	h.emitter.waitFor(t, EventMessagesChanged, 10*time.Second)
	unread := h.emitter.waitMatch(t, EventUnreadCount, 10*time.Second, func(payload any) bool {
		ev, ok := payload.(UnreadCountEvent)
		return ok && ev.AccountID == id && ev.UnreadCount == 1
	})
	if unread.Name != EventUnreadCount {
		t.Fatalf("unexpected event %q", unread.Name)
	}

	// The sync-state stream shows the worker went through a pass.
	var sawSyncing, sawIdle bool
	for _, ev := range h.emitter.all() {
		if ev.Name != EventSyncState {
			continue
		}
		state := ev.Payload.(SyncStateEvent)
		if state.AccountID != id {
			continue
		}
		switch state.State {
		case syncStateSyncing:
			sawSyncing = true
		case syncStateIdle:
			sawIdle = true
		}
	}
	if !sawSyncing || !sawIdle {
		t.Errorf("sync-state events missing syncing=%v idle=%v (stream: %s)", sawSyncing, sawIdle, h.emitter.names())
	}

	rt, err := h.manager.Runtime(id)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	folders, err := rt.store.ListFolders(context.Background())
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	var inbox *store.Folder
	for i := range folders {
		if folders[i].Type == store.FolderInbox {
			inbox = &folders[i]
		}
	}
	if inbox == nil {
		t.Fatal("no inbox folder was reconciled")
	}
	messages, err := rt.store.ListMessages(context.Background(), store.MessageQuery{FolderID: inbox.ID})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("inbox holds %d messages, want 1", len(messages))
	}
	if messages[0].Subject != "Hello there" {
		t.Errorf("ingested subject = %q", messages[0].Subject)
	}

	// The account info lists the new account as default with a colour.
	accounts := h.manager.Accounts()
	if len(accounts) != 1 || !accountInfoFor(accounts[0]).IsDefault {
		t.Errorf("accounts = %+v, want one default account", accounts)
	}
}

func TestManagerListMessagesUnifiedMerge(t *testing.T) {
	h := newHarness(t, 1)
	first := h.addAccount(t, "one@here.test")
	second := h.addAccount(t, "two@here.test")

	// Wait until both accounts finished their first pass.
	h.emitter.waitMatch(t, EventUnreadCount, 10*time.Second, func(payload any) bool {
		ev, ok := payload.(UnreadCountEvent)
		return ok && ev.AccountID == first && ev.UnreadCount == 1
	})
	h.emitter.waitMatch(t, EventUnreadCount, 10*time.Second, func(payload any) bool {
		ev, ok := payload.(UnreadCountEvent)
		return ok && ev.AccountID == second && ev.UnreadCount == 1
	})

	mail := NewMailService(h.manager)
	ctx := context.Background()

	waitForMerged := func(want int) []MessageSummaryInfo {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			merged, err := mail.ListMessages(ctx, 0, MessageView{}, 0)
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}
			if len(merged) >= want {
				return merged
			}
			if time.Now().After(deadline) {
				t.Fatalf("unified merge never reached %d messages (got %d)", want, len(merged))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Each account's server holds one message; the merge is cross-account
	// with globally unique folder IDs and per-account colours.
	merged := waitForMerged(2)
	folderIDs := map[int64]bool{merged[0].FolderID: true, merged[1].FolderID: true}
	if len(folderIDs) != 2 {
		t.Errorf("folder IDs collide across accounts: %v", folderIDs)
	}
	for _, summary := range merged {
		if summary.AccountColor == "" {
			t.Errorf("summary for message %d misses the account colour", summary.ID)
		}
		if _, local := decodeFolderID(summary.FolderID); local <= 0 {
			t.Errorf("folder ID did not decode: %d", summary.FolderID)
		}
	}

	// A newer message arrives on the first account's server; after a
	// triggered pass the merge shows it first.
	h.serverFor(first).addMessage(strings.Replace(testRawMessage,
		"<one@other.test>", "<newer@other.test>", 1), time.Now().Add(time.Hour))
	rt, err := h.manager.Runtime(first)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	rt.triggerSync()
	h.emitter.waitMatch(t, EventMessagesChanged, 10*time.Second, func(payload any) bool {
		ev, ok := payload.(MessagesChangedEvent)
		return ok && ev.AccountID == first
	})

	merged = waitForMerged(3)
	if merged[0].ID == merged[1].ID || merged[0].DateISO < merged[2].DateISO {
		t.Errorf("merge not sorted newest first: %v", merged)
	}
}

func TestManagerSetFlagsAppliesAndQueues(t *testing.T) {
	h := newHarness(t, 1)
	id := h.addAccount(t, "me@here.test")
	rt, err := h.manager.Runtime(id)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	h.emitter.waitFor(t, EventFoldersChanged, 10*time.Second)

	// Pause the workers so the queued action is not replayed and removed
	// before the test can inspect the queue.
	if err := h.manager.SetPaused(id, true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}

	ctx := context.Background()
	folderID, err := rt.store.UpsertFolder(ctx, store.Folder{Name: "INBOX", IMAPPath: "INBOX", Type: store.FolderInbox})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	messageID, err := rt.store.InsertMessage(ctx, store.NewMessage{
		FolderID: folderID, UID: 5, Subject: "flag me",
		Date: time.Now(), Flags: store.FlagFlagged,
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	mail := NewMailService(h.manager)
	seen := true
	flagged := false
	if err := mail.SetFlags(ctx, id, messageID, FlagPatch{Seen: &seen, Flagged: &flagged}); err != nil {
		t.Fatalf("SetFlags: %v", err)
	}

	message, err := rt.store.MessageByID(ctx, messageID)
	if err != nil {
		t.Fatalf("MessageByID: %v", err)
	}
	if !message.Flags.Has(store.FlagSeen) || message.Flags.Has(store.FlagFlagged) {
		t.Errorf("flags after patch = %d, want seen set and flagged cleared", message.Flags)
	}
	actions, err := rt.store.ListActions(ctx, 10)
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("queue holds %d actions, want 1", len(actions))
	}
	if !actions[0].AddFlags.Has(store.FlagSeen) || !actions[0].RemoveFlags.Has(store.FlagFlagged) {
		t.Errorf("queued action flags add=%d remove=%d", actions[0].AddFlags, actions[0].RemoveFlags)
	}
}

func TestManagerPauseRemoveLifecycle(t *testing.T) {
	h := newHarness(t, 1)
	id := h.addAccount(t, "me@here.test")
	h.emitter.waitFor(t, EventFoldersChanged, 10*time.Second)

	if err := h.manager.SetPaused(id, true); err != nil {
		t.Fatalf("SetPaused(true): %v", err)
	}
	ev := h.emitter.waitMatch(t, EventSyncState, 5*time.Second, func(payload any) bool {
		state, ok := payload.(SyncStateEvent)
		return ok && state.AccountID == id && state.State == syncStatePaused
	})
	if ev.Name != EventSyncState {
		t.Fatalf("unexpected event %q", ev.Name)
	}
	entry, ok := h.manager.RegistryEntry(id)
	if !ok || !entry.Paused {
		t.Errorf("paused flag not persisted to the registry: %+v", entry)
	}

	// Paused accounts keep their store readable.
	mail := NewMailService(h.manager)
	if _, err := mail.ListFolders(context.Background(), id); err != nil {
		t.Errorf("paused account folders unreadable: %v", err)
	}

	if err := h.manager.SetPaused(id, false); err != nil {
		t.Fatalf("SetPaused(false): %v", err)
	}
	h.emitter.waitFor(t, EventFoldersChanged, 10*time.Second)

	dbPath := h.paths.AccountDatabasePath(accountIDString(id))
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database missing before removal: %v", err)
	}
	if err := h.manager.RemoveAccount(id); err != nil {
		t.Fatalf("RemoveAccount: %v", err)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("database still present after removal: %v", err)
	}
	ref := credentialRefFor(id)
	if h.credentials.has(ref) {
		t.Error("credential survived account removal")
	}
	if entries := h.manager.Accounts(); len(entries) != 0 {
		t.Errorf("registry still holds %d entries", len(entries))
	}
}

func TestManagerDraftSendFlow(t *testing.T) {
	h := newHarness(t, 0)
	id := h.addAccount(t, "me@here.test")
	h.emitter.waitFor(t, EventFoldersChanged, 10*time.Second)

	ctx := context.Background()
	compose := NewComposeService(h.manager)

	// A draft starts in the draft state and never touches the mailer.
	saved, err := compose.SaveDraft(ctx, DraftInput{
		AccountID:   id,
		ToAddresses: []string{"grace@other.test"},
		Subject:     "Plans",
		BodyText:    "Coffee tomorrow?",
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	rt, err := h.manager.Runtime(id)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	row, err := rt.store.OutboxByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("OutboxByID: %v", err)
	}
	if row.State != store.SendDraft {
		t.Errorf("saved draft state = %q, want draft", row.State)
	}

	// Sending queues the row; the worker delivers it through the fake mailer
	// and the manager copies the final MIME into the Sent folder.
	queued, err := compose.SendDraft(ctx, DraftInput{
		DraftID:     saved.ID,
		AccountID:   id,
		ToAddresses: []string{"grace@other.test"},
		Subject:     "Plans",
		BodyText:    "Coffee tomorrow?",
	})
	if err != nil {
		t.Fatalf("SendDraft: %v", err)
	}
	if !queued.Queued {
		t.Error("SendDraft reported queued=false")
	}

	sentEvent := h.emitter.waitMatch(t, EventSendState, 10*time.Second, func(payload any) bool {
		ev, ok := payload.(SendStateEvent)
		return ok && ev.AccountID == id && ev.DraftID == saved.ID && ev.State == string(store.SendSent)
	})
	if sentEvent.Name != EventSendState {
		t.Fatalf("unexpected event %q", sentEvent.Name)
	}

	deadline := time.Now().Add(10 * time.Second)
	server := h.serverFor(id)
	for server.appendCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Sent copy was never appended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	appends := server.snapshotAppends()
	sentCopy := false
	for _, a := range appends {
		if a.Path == "Sent" && strings.Contains(string(a.Raw), "Coffee tomorrow?") {
			sentCopy = true
		}
	}
	if !sentCopy {
		t.Errorf("no usable Sent copy in appends: %+v", appends)
	}

	if h.mailer.deliveries() != 1 {
		t.Errorf("mailer delivered %d messages, want 1", h.mailer.deliveries())
	}
}

func TestManagerNotifyNewMailOnUnreadGrowth(t *testing.T) {
	h := newHarnessWithOptions(t, harnessOptions{notifications: true})
	id := h.addAccount(t, "me@here.test")
	rt, err := h.manager.Runtime(id)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	h.emitter.waitFor(t, EventFoldersChanged, 10*time.Second)

	// Pause the workers so only this test drives sync events; the pause wait
	// guarantees the real worker's pass is fully finished first.
	if err := h.manager.SetPaused(id, true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}

	ctx := context.Background()
	if _, err := rt.store.UpsertFolder(ctx, store.Folder{
		Name: "INBOX", IMAPPath: "INBOX", Type: store.FolderInbox, UnreadCount: 3,
	}); err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	// The first pass sets the baseline without notifying; growth notifies.
	h.manager.handleSyncEvent(rt, mailsync.Event{State: mailsync.StateIdle})
	if got := h.notifier.count("New mail"); got != 0 {
		t.Fatalf("first pass produced %d notifications, want 0", got)
	}
	folderID, err := rt.store.UpsertFolder(ctx, store.Folder{
		Name: "INBOX", IMAPPath: "INBOX", Type: store.FolderInbox, UnreadCount: 5,
	})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	_ = folderID
	h.manager.handleSyncEvent(rt, mailsync.Event{State: mailsync.StateIdle})
	if got := h.notifier.count("New mail"); got != 1 {
		t.Errorf("growth produced %d notifications, want 1", got)
	}

	// A repeated idle with the same total does not notify again.
	h.manager.handleSyncEvent(rt, mailsync.Event{State: mailsync.StateIdle})
	if got := h.notifier.count("New mail"); got != 1 {
		t.Errorf("stable total produced %d notifications, want 1", got)
	}
}

func TestManagerAuthFailedNotifiesOnceAndMapsToError(t *testing.T) {
	h := newHarness(t, 0)
	id := h.addAccount(t, "me@here.test")
	rt, err := h.manager.Runtime(id)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}

	rt.setLastSyncState(mailsync.StateSyncing)
	h.manager.handleSyncEvent(rt, mailsync.Event{
		State: mailsync.StateAuthFailed, Err: "authentication failed",
	})
	if got := h.notifier.count("Account needs attention"); got != 1 {
		t.Fatalf("auth-failed transition produced %d notifications, want 1", got)
	}
	ev := h.emitter.waitMatch(t, EventSyncState, 5*time.Second, func(payload any) bool {
		state, ok := payload.(SyncStateEvent)
		return ok && state.AccountID == id && state.State == syncStateError
	})
	state := ev.Payload.(SyncStateEvent)
	if state.Detail != "authentication failed" {
		t.Errorf("sync-state event = %+v, want error detail", state)
	}

	// A second consecutive auth-failed event is not a new transition.
	h.manager.handleSyncEvent(rt, mailsync.Event{
		State: mailsync.StateAuthFailed, Err: "authentication failed",
	})
	if got := h.notifier.count("Account needs attention"); got != 1 {
		t.Errorf("repeated auth-failed produced %d notifications, want 1", got)
	}
}

func TestManagerOpenURLValidation(t *testing.T) {
	h := newHarness(t, 0)
	var opened []string
	h.manager.deps.BrowserOpenFunc = func(raw string) error {
		opened = append(opened, raw)
		return nil
	}

	if err := h.manager.OpenURL("javascript:alert(1)"); err == nil {
		t.Error("javascript URL was accepted")
	}
	if err := h.manager.OpenURL("file:///etc/passwd"); err == nil {
		t.Error("file URL was accepted")
	}
	if err := h.manager.OpenURL("https://example.test/page"); err != nil {
		t.Fatalf("https URL rejected: %v", err)
	}
	if len(opened) != 1 || opened[0] != "https://example.test/page" {
		t.Errorf("opened = %v", opened)
	}
}
