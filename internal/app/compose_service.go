package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mefiz0/posthaste/internal/attachment"
	"github.com/mefiz0/posthaste/internal/send"
	"github.com/mefiz0/posthaste/internal/store"
)

// maxComposeAttachmentBytes bounds each locally-attached file read into the
// blob store, matching the inline-view cap. maxComposeAttachmentTotalBytes
// bounds the combined size of one compose's attachments so a drawer full of
// large files cannot stall the UI goroutine that ingests them.
const (
	maxComposeAttachmentBytes      = 25 << 20
	maxComposeAttachmentTotalBytes = 50 << 20
)

// ComposeService is the bound compose surface: contacts, drafts, sending, and
// the outbox view.
type ComposeService struct {
	manager *Manager
}

// NewComposeService returns the compose service bound to the manager.
func NewComposeService(manager *Manager) *ComposeService {
	return &ComposeService{manager: manager}
}

// ListContacts returns autocomplete suggestions for the prefix, from one
// account or merged across all of them when the ID is zero.
func (s *ComposeService) ListContacts(ctx context.Context, accountID int64, prefix string) ([]ContactInfo, error) {
	runtimes, err := mailServiceRuntimes(s.manager, accountID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]ContactInfo, 0, 8)
	for _, rt := range runtimes {
		contacts, err := rt.store.SearchContacts(ctx, prefix, 8)
		if err != nil {
			return nil, err
		}
		for _, c := range contacts {
			key := strings.ToLower(c.Email)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ContactInfo{Name: c.DisplayName, Address: c.Email})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	if len(out) > 8 {
		out = out[:8]
	}
	return out, nil
}

// mailServiceRuntimes resolves the target runtimes for an optional account ID.
func mailServiceRuntimes(manager *Manager, accountID int64) ([]*accountRuntime, error) {
	if accountID == 0 {
		return manager.Runtimes(), nil
	}
	rt, err := manager.Runtime(accountID)
	if err != nil {
		return nil, err
	}
	return []*accountRuntime{rt}, nil
}

// SaveDraft creates or updates a draft in the account's outbox and mirrors it
// into the server's Drafts folder in the background, best effort.
func (s *ComposeService) SaveDraft(ctx context.Context, draft DraftInput) (DraftResult, error) {
	rt, row, err := s.resolveDraft(ctx, draft)
	if err != nil {
		return DraftResult{}, err
	}
	row.State = store.SendDraft
	if err := rt.store.UpsertOutbox(ctx, *row); err != nil {
		return DraftResult{}, err
	}

	if s.manager.attachBlobs != nil {
		refs, err := ingestComposeAttachments(ctx, draft.Attachments, s.manager.attachBlobs)
		if err != nil {
			return DraftResult{}, err
		}
		row.AttachmentHashes = send.SerializeAttachments(refs)
		if err := rt.store.UpsertOutbox(ctx, *row); err != nil {
			return DraftResult{}, err
		}
	}
	s.mirrorDraft(rt, *row)
	s.manager.Emit(EventMessagesChanged, MessagesChangedEvent{
		Type: EventMessagesChanged, AccountID: rt.ID(),
	})
	return DraftResult{ID: row.ID}, nil
}

// SendDraft queues a composed message for delivery. A saved draft keeps its
// identity; a fresh compose becomes a new outbox row.
func (s *ComposeService) SendDraft(ctx context.Context, draft DraftInput) (SendResult, error) {
	rt, row, err := s.resolveDraft(ctx, draft)
	if err != nil {
		return SendResult{}, err
	}
	if err := validateRecipients(*row); err != nil {
		return SendResult{}, err
	}
	if err := send.Enqueue(ctx, rt.store, s.manager.now(), *row); err != nil {
		return SendResult{}, err
	}
	rt.triggerSend()
	s.manager.Emit(EventSendState, SendStateEvent{
		Type:      EventSendState,
		AccountID: rt.ID(),
		DraftID:   row.ID,
		State:     string(store.SendQueued),
	})
	return SendResult{Queued: true}, nil
}

// DeleteDraft discards a draft without sending it.
func (s *ComposeService) DeleteDraft(ctx context.Context, accountID int64, draftID string) error {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return err
	}
	return rt.store.DeleteOutbox(ctx, draftID)
}

// GetOutbox lists the outgoing messages worth showing in the outbox view:
// failed deliveries and pending or retrying sends.
func (s *ComposeService) GetOutbox(ctx context.Context, accountID int64) ([]OutboxItem, error) {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return nil, err
	}
	failed, err := rt.store.ListOutbox(ctx, store.SendFailed)
	if err != nil {
		return nil, err
	}
	queued, err := rt.store.ListOutbox(ctx, store.SendQueued)
	if err != nil {
		return nil, err
	}
	out := make([]OutboxItem, 0, len(failed)+len(queued))
	for _, row := range failed {
		out = append(out, outboxItemFor(rt, row))
	}
	for _, row := range queued {
		out = append(out, outboxItemFor(rt, row))
	}
	return out, nil
}

// resolveDraft maps a compose request onto the account runtime and the outbox
// row it edits, creating a fresh row for a new compose.
func (s *ComposeService) resolveDraft(ctx context.Context, draft DraftInput) (*accountRuntime, *store.OutboxMessage, error) {
	var rt *accountRuntime
	if draft.AccountID != 0 {
		var err error
		if rt, err = s.manager.Runtime(draft.AccountID); err != nil {
			return nil, nil, err
		}
	} else if rt = s.manager.DefaultRuntime(); rt == nil {
		return nil, nil, errNoAccount
	}

	row := &store.OutboxMessage{}
	if draft.DraftID != "" {
		existing, err := rt.store.OutboxByID(ctx, draft.DraftID)
		switch {
		case err == nil:
			row = &existing
		case errors.Is(err, store.ErrNotFound):
			// The client referenced a draft that no longer exists locally
			// (for example after the row was sent); keep its identity so
			// repeated saves stay one draft.
			row.ID = draft.DraftID
		default:
			return nil, nil, err
		}
	} else {
		row.ID = send.NewOutboxID()
	}

	row.AccountID = accountIDString(rt.ID())
	row.FromAddress = rt.account.Email
	row.FromName = rt.account.DisplayName
	row.ToAddresses = store.AddressColumn(draft.ToAddresses)
	row.CCAddresses = store.AddressColumn(draft.CCAddresses)
	row.BCCAddresses = store.AddressColumn(draft.BCCAddresses)
	row.Subject = draft.Subject
	row.BodyText = draft.BodyText

	s.applyReplyHeaders(ctx, rt, row, draft.InReplyToMessageID)
	return rt, row, nil
}

// applyReplyHeaders fills the threading headers from the message being
// replied to, so the sent message joins the right conversation everywhere.
func (s *ComposeService) applyReplyHeaders(ctx context.Context, rt *accountRuntime, row *store.OutboxMessage, inReplyToMessageID string) {
	if strings.TrimSpace(inReplyToMessageID) == "" {
		return
	}
	parent, err := rt.store.MessageByID(ctx, idFromString(inReplyToMessageID))
	if err != nil {
		// The ID may also be a raw Message-ID header value.
		parent, err = rt.store.MessageByMessageID(ctx, inReplyToMessageID)
		if err != nil {
			return
		}
	}
	if parent.MessageIDHeader == "" {
		return
	}
	row.InReplyTo = "<" + parent.MessageIDHeader + ">"
	refs := store.AddressList(parent.References)
	refs = append(refs, "<"+parent.MessageIDHeader+">")
	row.References = strings.Join(refs, " ")
}

// mirrorDraft uploads the draft to the server's Drafts folder on a background
// goroutine. Failure is logged and otherwise ignored: the local copy is the
// source of truth until the message is sent.
func (s *ComposeService) mirrorDraft(rt *accountRuntime, row store.OutboxMessage) {
	if s.manager.attachBlobs == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.manager.rootCtx, time.Minute)
		defer cancel()

		raw, err := send.BuildRawMIME(row, send.NewBlobStore(s.manager.attachBlobs))
		if err != nil {
			// A draft with no recipients cannot be rendered yet; that is a
			// normal state for a fresh draft, not an error worth surfacing.
			return
		}
		folder, err := rt.store.FolderByType(ctx, store.FolderDrafts)
		if err != nil {
			return
		}
		if err := rt.appendRaw(ctx, folder.IMAPPath, raw, []string{"\\Draft", "\\Seen"}); err != nil {
			s.manager.logger.Error("app: mirror draft to Drafts folder", "account", rt.ID(), "err", err)
		}
	}()
}

// PickAttachments opens the native multi-select file dialog and returns the
// chosen files with their names and sizes. The dialog itself lives behind the
// shell-injected FilePicker hook; the engine only sees paths. An empty result
// means the user canceled or picked nothing.
func (s *ComposeService) PickAttachments(ctx context.Context) ([]PickedFile, error) {
	if s.manager.deps.FilePicker == nil {
		return nil, errors.New("app: no file dialog is available")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, err := s.manager.deps.FilePicker()
	if err != nil {
		return nil, err
	}
	// The dialog does not promise a stable selection order; sort so repeated
	// picks of the same files produce the same attachment list.
	sort.Strings(paths)
	out := make([]PickedFile, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("app: open attachment: %w", err)
		}
		out = append(out, PickedFile{
			Name:      filepath.Base(path),
			Path:      path,
			SizeBytes: info.Size(),
		})
	}
	return out, nil
}

// ingestComposeAttachments reads the locally-attached files the compose drawer
// picked into the content-addressed blob store, returning one reference per
// file with its hash and original file name. The caps run on the files as they
// are read, so a file grown between the size probe and the read cannot slip
// past the limits.
func ingestComposeAttachments(ctx context.Context, paths []string, blobs *attachment.Store) ([]send.AttachmentRef, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	total := int64(0)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("app: open attachment: %w", err)
		}
		if info.Size() > maxComposeAttachmentBytes {
			return nil, errors.New("app: attachment is too large")
		}
		total += info.Size()
	}
	if total > maxComposeAttachmentTotalBytes {
		return nil, fmt.Errorf("app: attachments exceed %d MiB in total", maxComposeAttachmentTotalBytes>>20)
	}

	refs := make([]send.AttachmentRef, 0, len(paths))
	read := int64(0)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("app: read attachment: %w", err)
		}
		if int64(len(data)) > maxComposeAttachmentBytes {
			return nil, errors.New("app: attachment is too large")
		}
		read += int64(len(data))
		if read > maxComposeAttachmentTotalBytes {
			return nil, fmt.Errorf("app: attachments exceed %d MiB in total", maxComposeAttachmentTotalBytes>>20)
		}
		hash, _, err := blobs.Put(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		refs = append(refs, send.AttachmentRef{Hash: hash, Name: filepath.Base(path)})
	}
	return refs, nil
}

// validateRecipients checks the row has at least one parsable recipient.
func validateRecipients(row store.OutboxMessage) error {
	count := 0
	for _, column := range []string{row.ToAddresses, row.CCAddresses, row.BCCAddresses} {
		if strings.TrimSpace(column) == "" {
			continue
		}
		parsed, err := mail.ParseAddressList(column)
		if err != nil {
			return fmt.Errorf("app: a recipient address is not valid: %w", err)
		}
		count += len(parsed)
	}
	if count == 0 {
		return errors.New("app: add at least one recipient")
	}
	return nil
}

// outboxItemFor maps an outbox row onto the bridge shape.
func outboxItemFor(rt *accountRuntime, row store.OutboxMessage) OutboxItem {
	return OutboxItem{
		OutboxID:   row.ID,
		AccountID:  rt.ID(),
		To:         row.ToAddresses,
		Subject:    row.Subject,
		State:      string(row.State),
		Attempts:   row.Attempts,
		Error:      row.LastError,
		CreatedISO: rfc3339(row.CreatedAt),
	}
}
