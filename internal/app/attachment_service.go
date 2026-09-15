package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mefiz0/posthaste/internal/mail"
	"github.com/mefiz0/posthaste/internal/store"
)

// maxInlineAttachmentBytes caps what the bridge will base64 into a data URL
// or copy for opening: larger attachments are meant for the lazy-fetch path
// with explicit save, not for memory blowups in the webview.
const maxInlineAttachmentBytes = 25 << 20

// AttachmentService serves attachment bytes: inline data URLs for the reading
// pane, on-demand fetches for lazy attachments, and open/save handoffs to the
// desktop.
type AttachmentService struct {
	manager *Manager
}

// NewAttachmentService returns the attachment service bound to the manager.
func NewAttachmentService(manager *Manager) *AttachmentService {
	return &AttachmentService{manager: manager}
}

// GetAttachmentDataURL returns the attachment as a base64 data URL for the
// sandboxed reading pane. Lazy attachments are fetched from the stored raw
// message first.
func (s *AttachmentService) GetAttachmentDataURL(ctx context.Context, accountID, messageID, attachmentID int64) (string, error) {
	rt, attachment, err := s.resolve(ctx, accountID, messageID, attachmentID)
	if err != nil {
		return "", err
	}
	data, err := s.attachmentBytes(ctx, rt, messageID, attachment)
	if err != nil {
		return "", err
	}
	if len(data) > maxInlineAttachmentBytes {
		return "", errors.New("app: attachment is too large to display inline")
	}
	mimeType := attachment.MIMEType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// OpenAttachment copies the attachment to a temporary file and hands it to
// the desktop's default handler.
func (s *AttachmentService) OpenAttachment(ctx context.Context, accountID, messageID, attachmentID int64) error {
	rt, attachment, err := s.resolve(ctx, accountID, messageID, attachmentID)
	if err != nil {
		return err
	}
	data, err := s.attachmentBytes(ctx, rt, messageID, attachment)
	if err != nil {
		return err
	}
	path, err := writeToTemp(data, attachment.Filename)
	if err != nil {
		return err
	}
	if err := exec.Command("xdg-open", path).Start(); err != nil {
		return fmt.Errorf("app: open attachment: %w", err)
	}
	s.manager.emitToast("info", "Opening "+attachment.Filename)
	return nil
}

// SaveAttachment writes the attachment to the user's download directory,
// suffixing the name on conflicts.
func (s *AttachmentService) SaveAttachment(ctx context.Context, accountID, messageID, attachmentID int64) error {
	rt, attachment, err := s.resolve(ctx, accountID, messageID, attachmentID)
	if err != nil {
		return err
	}
	data, err := s.attachmentBytes(ctx, rt, messageID, attachment)
	if err != nil {
		return err
	}
	dir, err := downloadDir()
	if err != nil {
		return err
	}
	path, err := saveConflictFree(dir, attachment.Filename, data)
	if err != nil {
		return err
	}
	s.manager.emitToast("info", "Saved "+filepath.Base(path))
	return nil
}

// resolve loads the account runtime, message, and attachment row.
func (s *AttachmentService) resolve(ctx context.Context, accountID, messageID, attachmentID int64) (*accountRuntime, store.Attachment, error) {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return nil, store.Attachment{}, err
	}
	attachment, err := rt.store.AttachmentByID(ctx, attachmentID)
	if err != nil {
		return nil, store.Attachment{}, err
	}
	if attachment.MessageID != messageID {
		return nil, store.Attachment{}, errors.New("app: attachment does not belong to this message")
	}
	return rt, attachment, nil
}

// attachmentBytes returns the attachment's bytes, fetching them from the
// message's stored raw MIME when the blob is not yet local.
func (s *AttachmentService) attachmentBytes(ctx context.Context, rt *accountRuntime, messageID int64, attachment store.Attachment) ([]byte, error) {
	blobs := s.manager.attachBlobs
	if blobs == nil {
		return nil, errors.New("app: attachment store is not open")
	}
	if attachment.FetchState != store.FetchNotFetched {
		if reader, err := blobs.Open(attachment.ContentHash); err == nil {
			defer func() { _ = reader.Close() }()
			if data, readErr := io.ReadAll(reader); readErr == nil {
				return data, nil
			}
		}
		// The row claims fetched but the blob is gone (evicted or removed);
		// fall through to a refetch from the raw message.
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fetched, err := s.fetchLazyAttachment(ctx, rt, messageID, attachment)
	if err != nil {
		return nil, err
	}
	return fetched.data, nil
}

// fetchedAttachment carries the outcome of a lazy fetch.
type fetchedAttachment struct {
	hash string
	data []byte
}

// fetchLazyAttachment extracts one attachment's bytes from the message's raw
// MIME source, stores them in the blob store, and marks the row fetched.
func (s *AttachmentService) fetchLazyAttachment(ctx context.Context, rt *accountRuntime, messageID int64, attachment store.Attachment) (fetchedAttachment, error) {
	blobs := s.manager.attachBlobs
	if blobs == nil {
		return fetchedAttachment{}, errors.New("app: attachment store is not open")
	}
	message, err := rt.store.MessageByID(ctx, messageID)
	if err != nil {
		return fetchedAttachment{}, err
	}
	if message.RawMIMEPath == "" {
		return fetchedAttachment{}, errors.New("app: the message source is not available locally")
	}
	raw, err := os.ReadFile(message.RawMIMEPath)
	if err != nil {
		return fetchedAttachment{}, fmt.Errorf("app: read message source: %w", err)
	}
	parsed, err := mail.Parse(raw)
	if err != nil {
		return fetchedAttachment{}, err
	}
	part, ok := findMIMEPart(parsed, attachment)
	if !ok {
		return fetchedAttachment{}, errors.New("app: the attachment is not present in the message source")
	}

	hash, _, err := blobs.Put(bytes.NewReader(part.Data))
	if err != nil {
		return fetchedAttachment{}, err
	}
	if err := rt.store.MarkAttachmentFetched(ctx, attachment.ID); err != nil {
		// The bytes are stored; the flag is bookkeeping and the next access
		// retries it.
		s.manager.logger.Error("app: mark attachment fetched", "account", rt.ID(), "err", err)
	}
	return fetchedAttachment{hash: hash, data: part.Data}, nil
}

// findMIMEPart locates the parsed part matching an attachment row: by
// content-id for inline parts, else by filename, else by media type.
func findMIMEPart(parsed *mail.Parsed, attachment store.Attachment) (mail.Part, bool) {
	if attachment.ContentID != "" {
		for _, part := range parsed.Parts {
			if part.ContentID == attachment.ContentID {
				return part, true
			}
		}
	}
	if attachment.Filename != "" {
		for _, part := range parsed.Parts {
			if part.Filename == attachment.Filename {
				return part, true
			}
		}
	}
	for _, part := range parsed.Parts {
		if strings.EqualFold(part.MIMEType, attachment.MIMEType) {
			return part, true
		}
	}
	return mail.Part{}, false
}

// writeToTemp writes data to a temporary file carrying the attachment's
// extension so desktop handlers see the right type.
func writeToTemp(data []byte, filename string) (string, error) {
	ext := filepath.Ext(filename)
	tmp, err := os.CreateTemp("", "posthaste-*"+ext)
	if err != nil {
		return "", fmt.Errorf("app: create temp file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("app: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("app: close temp file: %w", err)
	}
	return name, nil
}

// downloadDir resolves the user's download directory, creating it when
// missing.
func downloadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("app: resolve home directory: %w", err)
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("app: create download directory: %w", err)
	}
	return dir, nil
}

// saveConflictFree writes data under dir/filename, adding a numeric suffix
// when the name is taken.
func saveConflictFree(dir, filename string, data []byte) (string, error) {
	name := filepath.Base(filename)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "attachment"
	}
	candidate := filepath.Join(dir, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			break
		}
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		candidate = filepath.Join(dir, stem+"-"+strconv.Itoa(i)+ext)
	}
	if err := os.WriteFile(candidate, data, 0o600); err != nil {
		return "", fmt.Errorf("app: save attachment: %w", err)
	}
	return candidate, nil
}
