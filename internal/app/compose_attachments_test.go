package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mefiz0/posthaste/internal/send"
)

func TestPickAttachmentsUsesInjectedDialog(t *testing.T) {
	h := newHarness(t, 0)
	composeService := NewComposeService(h.manager)

	if _, err := composeService.PickAttachments(context.Background()); err == nil {
		t.Fatal("PickAttachments without a dialog hook = nil error, want a clear failure")
	}

	dir := t.TempDir()
	first := filepath.Join(dir, "notes.txt")
	second := filepath.Join(dir, "data.csv")
	for path, content := range map[string]string{first: "hello", second: "a,b,c"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	// The dialog may report files in any order; a reversed selection must
	// still produce a deterministic, named result.
	h.manager.deps.FilePicker = func() ([]string, error) {
		return []string{second, first}, nil
	}

	picked, err := NewComposeService(h.manager).PickAttachments(context.Background())
	if err != nil {
		t.Fatalf("PickAttachments: %v", err)
	}
	if len(picked) != 2 {
		t.Fatalf("picked %d files, want 2", len(picked))
	}
	if picked[0].Name != "data.csv" || picked[0].Path != second || picked[0].SizeBytes != 5 {
		t.Errorf("picked[0] = %+v, want data.csv with its size", picked[0])
	}
	if picked[1].Name != "notes.txt" || picked[1].Path != first || picked[1].SizeBytes != 5 {
		t.Errorf("picked[1] = %+v, want notes.txt with its size", picked[1])
	}

	// A dialog failure surfaces to the caller.
	h.manager.deps.FilePicker = func() ([]string, error) {
		return nil, errors.New("dialog exploded")
	}
	if _, err := NewComposeService(h.manager).PickAttachments(context.Background()); err == nil {
		t.Error("PickAttachments with a failing dialog = nil error")
	}
}

func TestSaveDraftIngestsAttachments(t *testing.T) {
	h := newHarness(t, 0)
	accountID := h.addAccount(t, "me@here.test")

	dir := t.TempDir()
	var paths []string
	for _, spec := range []struct{ name, content string }{
		{"report.pdf", "%PDF-fake"},
		{"table.csv", "a,b\n1,2\n"},
	} {
		path := filepath.Join(dir, spec.name)
		if err := os.WriteFile(path, []byte(spec.content), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		paths = append(paths, path)
	}

	composeService := NewComposeService(h.manager)
	result, err := composeService.SaveDraft(context.Background(), DraftInput{
		AccountID:   accountID,
		ToAddresses: []string{"peer@other.test"},
		Subject:     "with attachments",
		BodyText:    "see attached",
		Attachments: paths,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	rt, err := h.manager.Runtime(accountID)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	row, err := rt.store.OutboxByID(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("OutboxByID: %v", err)
	}
	refs, err := send.ParseAttachments(row.AttachmentHashes)
	if err != nil {
		t.Fatalf("ParseAttachments: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("row carries %d attachment refs, want 2", len(refs))
	}
	for i, ref := range refs {
		if ref.Name != filepath.Base(paths[i]) {
			t.Errorf("ref %d name = %q, want %q", i, ref.Name, filepath.Base(paths[i]))
		}
		if !h.manager.attachBlobs.Has(ref.Hash) {
			t.Errorf("ref %d blob %s is not in the store", i, ref.Hash)
		}
	}

	// The rendered MIME carries the original file names, so recipients and
	// the Sent copy see them.
	raw, err := send.BuildRawMIME(row, send.NewBlobStore(h.manager.attachBlobs))
	if err != nil {
		t.Fatalf("BuildRawMIME: %v", err)
	}
	for _, name := range []string{"report.pdf", "table.csv"} {
		if !strings.Contains(string(raw), name) {
			t.Errorf("rendered MIME misses the attachment name %q:\n%s", name, raw)
		}
	}
}

func TestIngestComposeAttachmentsCaps(t *testing.T) {
	dir := t.TempDir()

	// A sparse file reports a huge size without occupying disk; the per-file
	// cap fires before any read.
	oversized := filepath.Join(dir, "big.bin")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := file.Truncate(maxComposeAttachmentBytes + 1); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	_ = file.Close()
	if _, err := ingestComposeAttachments(context.Background(), []string{oversized}, nil); err == nil {
		t.Error("ingest of an oversized file = nil error, want a cap failure")
	}

	// Two files within their individual caps but over the combined cap.
	sparse := func(name string) string {
		path := filepath.Join(dir, name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := file.Truncate(30 << 20); err != nil {
			t.Fatalf("Truncate: %v", err)
		}
		_ = file.Close()
		return path
	}
	first, second := sparse("first.bin"), sparse("second.bin")
	if _, err := ingestComposeAttachments(context.Background(), []string{first, second}, nil); err == nil {
		t.Error("ingest past the total cap = nil error, want a cap failure")
	}

	if _, err := ingestComposeAttachments(context.Background(), []string{filepath.Join(dir, "missing.bin")}, nil); err == nil {
		t.Error("ingest of a missing file = nil error")
	}

	if refs, err := ingestComposeAttachments(context.Background(), nil, nil); err != nil || len(refs) != 0 {
		t.Errorf("ingest of no files = %v, %d refs, want no error and no refs", err, len(refs))
	}
}
