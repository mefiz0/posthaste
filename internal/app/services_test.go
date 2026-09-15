package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/mail"
	"github.com/mefiz0/posthaste/internal/store"
)

func TestMailServiceSearchAcrossAccounts(t *testing.T) {
	h := newHarness(t, 1)
	first := h.addAccount(t, "one@here.test")
	second := h.addAccount(t, "two@here.test")
	for _, id := range []int64{first, second} {
		accountID := id
		h.emitter.waitMatch(t, EventUnreadCount, 10*time.Second, func(payload any) bool {
			ev, ok := payload.(UnreadCountEvent)
			return ok && ev.AccountID == accountID && ev.UnreadCount == 1
		})
	}

	mailService := NewMailService(h.manager)
	results, err := mailService.Search(context.Background(), 0, SearchFilterInput{Text: "hello"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("search across accounts returned %d results, want 2", len(results))
	}
	for _, result := range results {
		if !strings.Contains(strings.ToLower(result.Subject), "hello") {
			t.Errorf("unexpected subject in results: %q", result.Subject)
		}
	}

	// Restricting the search to one account halves the results.
	onlyFirst, err := mailService.Search(context.Background(), first, SearchFilterInput{Text: "hello"})
	if err != nil {
		t.Fatalf("Search on one account: %v", err)
	}
	if len(onlyFirst) != 1 {
		t.Errorf("single-account search returned %d results, want 1", len(onlyFirst))
	}

	// An unread filter matches both seeded messages (they arrive unseen).
	yes := true
	unreadResults, err := mailService.Search(context.Background(), 0, SearchFilterInput{IsUnread: &yes})
	if err != nil {
		t.Fatalf("Search isUnread: %v", err)
	}
	if len(unreadResults) != 2 {
		t.Errorf("isUnread search returned %d results, want 2", len(unreadResults))
	}
}

func TestAttachmentServiceLazyFetch(t *testing.T) {
	h := newHarness(t, 0)
	id := h.addAccount(t, "me@here.test")
	rt, err := h.manager.Runtime(id)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}

	raw := "Message-ID: <att@other.test>\r\n" +
		"From: Alice <alice@other.test>\r\n" +
		"Subject: attachment\r\n" +
		"Date: Mon, 07 Jul 2025 10:00:00 +0000\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"See attached.\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/pdf; name=\"report.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		base64.StdEncoding.EncodeToString([]byte("%PDF-fake-bytes")) + "\r\n" +
		"--BOUND--\r\n"

	parsed, err := mail.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("mail.Parse: %v", err)
	}
	if len(parsed.Parts) == 0 {
		t.Fatal("parsed message has no parts")
	}
	part := parsed.Parts[len(parsed.Parts)-1]

	// Store the raw source the way the sync engine does: in the blob store,
	// with the message row pointing at the file path.
	rawHash, _, err := h.manager.rawBlobs.Put(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("rawBlobs.Put: %v", err)
	}

	ctx := context.Background()
	folderID, err := rt.store.UpsertFolder(ctx, store.Folder{Name: "INBOX", IMAPPath: "INBOX", Type: store.FolderInbox})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	messageID, err := rt.store.InsertMessage(ctx, store.NewMessage{
		FolderID:    folderID,
		UID:         9,
		Subject:     "attachment",
		Date:        time.Now(),
		RawMIMEPath: h.manager.rawBlobs.Path(rawHash),
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	attachmentID, err := rt.store.InsertAttachment(ctx, store.Attachment{
		MessageID:  messageID,
		Filename:   "report.pdf",
		MIMEType:   "application/pdf",
		SizeBytes:  int64(len(part.Data)),
		ContentID:  part.ContentID,
		FetchState: store.FetchNotFetched,
	})
	if err != nil {
		t.Fatalf("InsertAttachment: %v", err)
	}

	attachments := NewAttachmentService(h.manager)
	dataURL, err := attachments.GetAttachmentDataURL(ctx, id, messageID, attachmentID)
	if err != nil {
		t.Fatalf("GetAttachmentDataURL: %v", err)
	}
	if !strings.HasPrefix(dataURL, "data:application/pdf;base64,") {
		t.Errorf("data URL has the wrong shape: %.40s", dataURL)
	}
	encoded := strings.TrimPrefix(dataURL, "data:application/pdf;base64,")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("data URL payload is not base64: %v", err)
	}
	if string(decoded) != "%PDF-fake-bytes" {
		t.Errorf("decoded payload = %q", decoded)
	}

	// The row is now marked fetched and its bytes live in the blob store.
	row, err := rt.store.AttachmentByID(ctx, attachmentID)
	if err != nil {
		t.Fatalf("AttachmentByID: %v", err)
	}
	if row.FetchState != store.FetchFetched {
		t.Errorf("fetch state after lazy fetch = %q", row.FetchState)
	}
	// Lazy rows carry an empty content hash; the bytes are stored under the
	// hash of the extracted part.
	sum := sha256.Sum256(part.Data)
	if !h.manager.attachBlobs.Has(hex.EncodeToString(sum[:])) {
		t.Error("lazy fetch did not store the attachment blob")
	}
}

func TestSettingsServiceRoundTrip(t *testing.T) {
	h := newHarnessWithOptions(t, harnessOptions{notifications: true})
	settingsService := NewSettingsService(h.manager)

	current, err := settingsService.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !current.NotificationsEnabled {
		t.Error("expected notifications on by default")
	}

	current.MinimizeToTray = true
	current.VerboseLogging = true
	current.Keymap = map[string]string{"archive": "A"}
	if err := settingsService.SaveSettings(context.Background(), current); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	reloaded, err := settingsService.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings after save: %v", err)
	}
	if !reloaded.MinimizeToTray || !reloaded.VerboseLogging {
		t.Errorf("flags not persisted: %+v", reloaded)
	}
	if reloaded.Keymap["archive"] != "A" {
		t.Errorf("keymap not persisted: %v", reloaded.Keymap)
	}

	// The saved snapshot reached the settings store too.
	persisted, err := h.settings.Load()
	if err != nil {
		t.Fatalf("settings store load: %v", err)
	}
	if !persisted.MinimizeToTray {
		t.Error("settings file was not updated")
	}

	// Saving pushes the settings-changed event with the payload attached.
	h.emitter.waitFor(t, EventSettingsChanged, 5*time.Second)
}

func TestAppServiceQuitAndSyncNow(t *testing.T) {
	h := newHarness(t, 0)
	quitCalled := false
	h.manager.deps.QuitFunc = func() { quitCalled = true }

	appService := NewAppService(h.manager)
	appService.Quit(context.Background())
	if !quitCalled {
		t.Error("Quit did not reach the shell hook")
	}

	// SyncNow on an unknown account is a no-op, not a panic.
	appService.SyncNow(context.Background(), 12345)
	appService.SyncNow(context.Background(), 0)
}
