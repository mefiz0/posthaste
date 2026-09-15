package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mefiz0/posthaste/internal/sanitize"
	"github.com/mefiz0/posthaste/internal/store"
)

const remoteHTML = "<p>Hi</p><img src=\"https://tracker.example.com/pixel.gif\" alt=\"\">"

// seedHTMLMessage stores one HTML message the way the sync engine does: raw
// source in the blob store, sanitized HTML on the row, and the row pointing
// back at the source file. Each call needs a distinct UID within the folder.
func seedHTMLMessage(t *testing.T, h *harness, accountID int64, uid uint32, html string) store.Message {
	t.Helper()
	rt, err := h.manager.Runtime(accountID)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	raw := "Message-ID: <html@other.test>\r\n" +
		"From: Mails <mails@other.test>\r\n" +
		"Subject: html body\r\n" +
		"Date: Mon, 07 Jul 2025 10:00:00 +0000\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		html + "\r\n"
	hash, _, err := h.manager.rawBlobs.Put(strings.NewReader(raw))
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
		UID:         uid,
		Subject:     "html body",
		Date:        time.Now(),
		RawMIMEPath: h.manager.rawBlobs.Path(hash),
		BodyHTML:    sanitize.HTML(html, false),
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	message, err := rt.store.MessageByID(ctx, messageID)
	if err != nil {
		t.Fatalf("MessageByID: %v", err)
	}
	return message
}

func TestGetMessageHTMLOptIn(t *testing.T) {
	h := newHarness(t, 0)
	accountID := h.addAccount(t, "me@here.test")
	mailService := NewMailService(h.manager)
	message := seedHTMLMessage(t, h, accountID, 42, remoteHTML)
	ctx := context.Background()

	// The default variant serves the sanitized-at-rest HTML: the remote
	// reference is gone.
	blocked, err := mailService.GetMessageHTML(ctx, accountID, message.ID, false)
	if err != nil {
		t.Fatalf("GetMessageHTML blocked: %v", err)
	}
	if blocked != message.BodyHTML {
		t.Errorf("blocked HTML = %q, want the stored sanitized body %q", blocked, message.BodyHTML)
	}
	if strings.Contains(blocked, "https://") {
		t.Errorf("blocked HTML still carries a remote reference: %q", blocked)
	}

	// The opt-in re-sanitizes the raw source with remote loads allowed; the
	// stored row stays untouched.
	allowed, err := mailService.GetMessageHTML(ctx, accountID, message.ID, true)
	if err != nil {
		t.Fatalf("GetMessageHTML allowed: %v", err)
	}
	if !strings.Contains(allowed, "https://tracker.example.com/pixel.gif") {
		t.Errorf("opt-in HTML lost the remote reference: %q", allowed)
	}
	if message.BodyHTML == allowed {
		t.Error("opt-in overwrote the sanitized-at-rest HTML")
	}

	if _, err := mailService.GetMessageHTML(ctx, accountID, 987654, false); err == nil {
		t.Error("GetMessageHTML for an unknown message = nil error")
	}
}

func TestGetMessageHTMLRequiresRawSource(t *testing.T) {
	h := newHarness(t, 0)
	accountID := h.addAccount(t, "me@here.test")
	mailService := NewMailService(h.manager)
	message := seedHTMLMessage(t, h, accountID, 42, remoteHTML)

	rt, err := h.manager.Runtime(accountID)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	if err := rt.store.DeleteMessage(context.Background(), message.ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	// Re-insert without a raw source to hit the missing-source guard.
	ctx := context.Background()
	folderID, err := rt.store.UpsertFolder(ctx, store.Folder{Name: "INBOX", IMAPPath: "INBOX", Type: store.FolderInbox})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	orphanID, err := rt.store.InsertMessage(ctx, store.NewMessage{
		FolderID: folderID,
		UID:      43,
		Subject:  "orphan",
		Date:     time.Now(),
		BodyHTML: message.BodyHTML,
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	if _, err := mailService.GetMessageHTML(ctx, accountID, orphanID, true); err == nil {
		t.Error("opt-in without a raw source = nil error, want a clear failure")
	} else if !strings.Contains(err.Error(), "message source") {
		t.Errorf("error = %v, want it to name the missing message source", err)
	}
}

func TestGetMessageHTMLSizeCap(t *testing.T) {
	h := newHarness(t, 0)
	accountID := h.addAccount(t, "me@here.test")
	mailService := NewMailService(h.manager)
	// One oversized paragraph pushes the sanitized output past the cap.
	message := seedHTMLMessage(t, h, accountID, 46, "<p>"+strings.Repeat("word ", maxMessageHTMLBytes/5)+"</p>")

	if _, err := mailService.GetMessageHTML(context.Background(), accountID, message.ID, true); err == nil {
		t.Error("opt-in for oversized HTML = nil error, want a cap failure")
	}
}

func TestGetMessageReportsRemoteContent(t *testing.T) {
	h := newHarness(t, 0)
	accountID := h.addAccount(t, "me@here.test")
	mailService := NewMailService(h.manager)
	ctx := context.Background()

	withRemote := seedHTMLMessage(t, h, accountID, 42, remoteHTML)
	detail, err := mailService.GetMessage(ctx, accountID, withRemote.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !detail.HasRemoteContent {
		t.Error("GetMessage.HasRemoteContent = false, want true for a tracking pixel")
	}

	withoutRemote := seedHTMLMessage(t, h, accountID, 44, "<p>Just text</p>")
	detail, err = mailService.GetMessage(ctx, accountID, withoutRemote.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if detail.HasRemoteContent {
		t.Error("GetMessage.HasRemoteContent = true for a remote-free message")
	}
}
