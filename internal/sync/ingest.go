package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mefiz0/posthaste/internal/mail"
	"github.com/mefiz0/posthaste/internal/sanitize"
	"github.com/mefiz0/posthaste/internal/store"
	"github.com/mefiz0/posthaste/internal/thread"
)

// wireFlags pairs local flag bits with their IMAP names. It drives both
// directions of the mapping so the two never drift apart.
var wireFlags = []struct {
	bit  store.Flags
	name string
}{
	{store.FlagSeen, "\\Seen"},
	{store.FlagAnswered, "\\Answered"},
	{store.FlagFlagged, "\\Flagged"},
	{store.FlagDraft, "\\Draft"},
	{store.FlagDeleted, "\\Deleted"},
}

// seenFlagName is the IMAP name of the read marker, used by queued read and
// unread actions.
const seenFlagName = "\\Seen"

// parseFlags maps IMAP flag names onto the local bitmask; unknown flags are
// ignored because the client does not track them.
func parseFlags(flags []string) store.Flags {
	var out store.Flags
	for _, flag := range flags {
		name := strings.ToLower(strings.TrimSpace(flag))
		for _, wf := range wireFlags {
			if strings.ToLower(wf.name) == name {
				out = out.With(wf.bit)
			}
		}
	}
	return out
}

// flagNames maps a local flag bitmask onto IMAP flag names.
func flagNames(flags store.Flags) []string {
	var names []string
	for _, wf := range wireFlags {
		if flags.Has(wf.bit) {
			names = append(names, wf.name)
		}
	}
	return names
}

// parsedHeader parses a server message's header block. A missing or corrupt
// block still yields an empty Parsed, so the row can be created and the UID
// cursor advance past a broken message.
func parsedHeader(header []byte) *mail.Parsed {
	if len(header) == 0 {
		return &mail.Parsed{}
	}
	if parsed, err := mail.Parse(header); err == nil {
		return parsed
	}
	return &mail.Parsed{}
}

// insertPlaceholder stores the header row of a new message so the message
// appears in the list immediately; the body pass fills in content, threads,
// contacts, and attachments afterwards.
func (w *Worker) insertPlaceholder(ctx context.Context, folder store.Folder, sm ServerMessage, parsed *mail.Parsed) (int64, error) {
	rowID, err := w.deps.DB.InsertMessage(ctx, store.NewMessage{
		FolderID:        folder.ID,
		UID:             sm.UID,
		MessageIDHeader: parsed.MessageID,
		InReplyTo:       parsed.InReplyTo,
		References:      store.AddressColumn(parsed.References),
		FromAddress:     parsed.FromAddress,
		FromName:        parsed.FromName,
		ToAddresses:     formatAddresses(parsed.To),
		CCAddresses:     formatAddresses(parsed.CC),
		Subject:         parsed.Subject,
		Date:            messageDate(parsed, sm.Date),
		Flags:           parseFlags(sm.Flags),
		SizeBytes:       sm.Size,
	})
	if err != nil {
		return 0, err
	}
	return rowID, nil
}

// adoptMatchingRow re-homes an existing local row for the server message
// being ingested, so the folder does not grow a second row for a message it
// already holds. A row matches when it sits in this folder under the same
// Message-ID, or when it carries the same Message-ID with UID 0 — the state
// an optimistic move leaves behind. The server's MOVE reply does not
// reliably report the new UID, so the target folder's next pass is the
// simplest place where that row becomes first-class: it takes the server's
// UID, folder, flags, and size while keeping its body, attachments, and
// thread. Rows in other folders that still have a real UID never match, so
// the same message living in two folders keeps both rows; a row without a
// Message-ID can never match either, because it identifies nothing. The
// store enforces both rules (FindAdoptableMessage), and because every row
// lives in this account's own database, adoption can never cross accounts.
// It returns the adopted row together with whether the caller must still
// fetch its body (true only when the earlier ingest never completed).
func (w *Worker) adoptMatchingRow(ctx context.Context, folder store.Folder, sm ServerMessage, messageID string) (store.Message, bool, error) {
	existing, err := w.deps.DB.FindAdoptableMessage(ctx, folder.ID, messageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Message{}, false, nil
		}
		return store.Message{}, false, err
	}
	if err := w.deps.DB.AdoptMessage(ctx, existing.ID, folder.ID, sm.UID, parseFlags(sm.Flags), sm.Size); err != nil {
		return store.Message{}, false, err
	}
	return existing, needsBody(existing), nil
}

// needsBody reports whether a message row's ingestion never completed, so the
// body pass must run for it again.
func needsBody(m store.Message) bool {
	return m.RawMIMEPath == "" || m.ThreadID == 0
}

// processMessage completes one message row: parse the raw MIME, store it as a
// blob, record derived bodies (HTML sanitized at ingest), attachment
// metadata, contacts, and the thread assignment. It runs at least once for
// every message and is safe to re-run after a crash mid-pass.
func (w *Worker) processMessage(ctx context.Context, folder store.Folder, rowID int64, raw []byte) error {
	parsed := &mail.Parsed{}
	if len(raw) > 0 {
		if p, err := mail.Parse(raw); err == nil {
			parsed = p
		}
	}

	rawPath := ""
	if len(raw) > 0 {
		contentHash, _, err := w.deps.RawBlobs.Put(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("sync: store raw message: %w", err)
		}
		rawPath = w.deps.RawBlobs.Path(contentHash)
	}

	current, err := w.deps.DB.MessageByID(ctx, rowID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The row was wiped (UIDVALIDITY reset) mid-pass; nothing to finish.
			return nil
		}
		return err
	}

	current.MessageIDHeader = parsed.MessageID
	current.InReplyTo = parsed.InReplyTo
	current.References = store.AddressColumn(parsed.References)
	current.FromAddress = parsed.FromAddress
	current.FromName = parsed.FromName
	current.ToAddresses = formatAddresses(parsed.To)
	current.CCAddresses = formatAddresses(parsed.CC)
	current.Subject = parsed.Subject
	if !parsed.Date.IsZero() {
		current.Date = parsed.Date
	}
	if err := w.deps.DB.UpdateMessageHeaders(ctx, current); err != nil {
		return err
	}

	// body_html is sanitized here, at the single point where untrusted HTML
	// enters the database, with remote references stripped.
	cleanHTML := sanitize.HTML(parsed.BodyHTML, false)

	hasAttachments := false
	if err := w.storeAttachments(ctx, rowID, parsed.Parts, &hasAttachments); err != nil {
		return err
	}
	if err := w.deps.DB.UpdateMessageContent(ctx, rowID, parsed.BodyText, cleanHTML, rawPath, hasAttachments); err != nil {
		return err
	}
	if err := w.recordContacts(ctx, parsed); err != nil {
		return err
	}
	return w.assignThread(ctx, rowID, parsed)
}

// storeAttachments records every MIME part of a message. Parts at or below
// the eager threshold are stored in the blob store now so they are available
// offline; larger ones keep metadata only and are fetched on demand. Inline
// cid: parts follow the same threshold: above it they stay lazy and the
// reading pane shows an unloaded image until fetched, which keeps one huge
// inline image from ballooning eager sync.
func (w *Worker) storeAttachments(ctx context.Context, rowID int64, parts []mail.Part, hasAttachments *bool) error {
	if len(parts) == 0 {
		return nil
	}
	existing, err := w.deps.DB.ListAttachments(ctx, rowID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		// Re-running a half-finished ingestion must not duplicate rows.
		for _, a := range existing {
			if !a.IsInline {
				*hasAttachments = true
			}
		}
		return nil
	}

	threshold := w.eagerThreshold()
	for _, part := range parts {
		if !part.IsInline {
			*hasAttachments = true
		}
		row := store.Attachment{
			MessageID:  rowID,
			Filename:   part.Filename,
			MIMEType:   part.MIMEType,
			SizeBytes:  int64(len(part.Data)),
			ContentID:  part.ContentID,
			IsInline:   part.IsInline,
			FetchState: store.FetchNotFetched,
		}
		if len(part.Data) > 0 && int64(len(part.Data)) <= threshold {
			contentHash, size, err := w.deps.AttachBlobs.Put(bytes.NewReader(part.Data))
			if err != nil {
				return fmt.Errorf("sync: store attachment: %w", err)
			}
			row.ContentHash = contentHash
			row.StoragePath = w.deps.AttachBlobs.Path(contentHash)
			row.SizeBytes = size
			row.FetchState = store.FetchFetched
			row.LastAccessedAt = w.now()
		}
		if _, err := w.deps.DB.InsertAttachment(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

// recordContacts feeds the autocomplete index from the message's address
// headers.
func (w *Worker) recordContacts(ctx context.Context, parsed *mail.Parsed) error {
	if err := w.deps.DB.RecordContact(ctx, parsed.FromAddress, parsed.FromName); err != nil {
		return err
	}
	for _, addr := range parsed.To {
		if err := w.deps.DB.RecordContact(ctx, addr.Address, addr.Name); err != nil {
			return err
		}
	}
	for _, addr := range parsed.CC {
		if err := w.deps.DB.RecordContact(ctx, addr.Address, addr.Name); err != nil {
			return err
		}
	}
	return nil
}

// assignThread groups the message into a conversation, persisting new threads
// as the index asks for them and applying any merges the index reports.
func (w *Worker) assignThread(ctx context.Context, rowID int64, parsed *mail.Parsed) error {
	meta := thread.Meta{
		RowID:      rowID,
		MessageID:  parsed.MessageID,
		InReplyTo:  parsed.InReplyTo,
		References: parsed.References,
		Subject:    parsed.Subject,
		Date:       parsed.Date,
	}
	result, err := w.threads.Ingest(meta, func(subject string) (int64, error) {
		return w.deps.DB.InsertThread(ctx, store.Thread{SubjectNormalized: subject, LatestDate: meta.Date})
	})
	if err != nil {
		return fmt.Errorf("sync: assign thread: %w", err)
	}
	if err := w.deps.DB.UpdateThreadID(ctx, rowID, result.ThreadID); err != nil {
		return err
	}
	for _, sourceID := range result.MergedInto {
		if err := w.deps.DB.MergeThreads(ctx, sourceID, result.ThreadID); err != nil {
			return err
		}
	}
	return w.deps.DB.RecomputeThreadMeta(ctx, result.ThreadID)
}

// seedThreads hydrates the conversation graph from persisted messages so a
// restarted worker keeps grouping without recomputing history. Rows without a
// thread are left alone; they are ingested when their pending ingestion
// completes.
func (w *Worker) seedThreads(ctx context.Context) error {
	for offset := 0; ; offset += listPage {
		threads, err := w.deps.DB.ListThreads(ctx, listPage, offset)
		if err != nil {
			return err
		}
		for _, th := range threads {
			messages, err := w.deps.DB.ListMessagesByThread(ctx, th.ID)
			if err != nil {
				return err
			}
			for _, m := range messages {
				w.threads.Seed(threadMeta(m), th.ID)
			}
		}
		if len(threads) < listPage {
			return nil
		}
	}
}

// threadMeta projects a stored message onto the grouping input.
func threadMeta(m store.Message) thread.Meta {
	return thread.Meta{
		RowID:      m.ID,
		MessageID:  m.MessageIDHeader,
		InReplyTo:  m.InReplyTo,
		References: store.AddressList(m.References),
		Subject:    m.Subject,
		Date:       m.Date,
	}
}

// messageDate prefers the Date header and falls back to the server's
// internal date when the header is absent or unparsable.
func messageDate(parsed *mail.Parsed, fallback time.Time) time.Time {
	if !parsed.Date.IsZero() {
		return parsed.Date
	}
	return fallback
}

// formatAddresses renders parsed addresses for a message address column: each
// entry as "Name <address>", or the bare address when no name was given.
func formatAddresses(addrs []mail.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr.Name == "" {
			parts = append(parts, addr.Address)
			continue
		}
		parts = append(parts, addr.Name+" <"+addr.Address+">")
	}
	return store.AddressColumn(parts)
}
