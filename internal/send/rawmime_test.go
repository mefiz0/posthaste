package send

import (
	"errors"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

func TestBuildRawMIMERendersDeliverableBytes(t *testing.T) {
	raw, err := BuildRawMIME(sampleOutbox(), nil)
	if err != nil {
		t.Fatalf("BuildRawMIME: %v", err)
	}
	output := string(raw)

	for _, want := range []string{
		`From: "Ada Lovelace" <ada@sender.test>`,
		`"Grace Hopper" <grace@other.test>`,
		"Subject: Reports",
		"In-Reply-To: <original@other.test>",
		"Hello there",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("rendered message misses %q\n%s", want, output)
		}
	}
	if strings.Contains(output, "quiet@fourth.test") {
		t.Errorf("rendered message exposes the Bcc recipient:\n%s", output)
	}
	// The rendered message must be self-contained so an IMAP APPEND of these
	// bytes produces a valid stored message.
	if !strings.Contains(output, "MIME-Version") {
		t.Errorf("rendered message misses the MIME-Version header:\n%s", output)
	}
}

func TestBuildRawMIMEAttachmentsAndValidation(t *testing.T) {
	hash := strings.Repeat("a", 64)
	blobs := fakeBlobs{content: map[string]string{hash: "first file"}}
	m := sampleOutbox()
	m.AttachmentHashes = SerializeAttachments([]AttachmentRef{{Hash: hash, Name: "report.pdf"}})

	raw, err := BuildRawMIME(m, blobs)
	if err != nil {
		t.Fatalf("BuildRawMIME: %v", err)
	}
	// The attachment's file name must survive into the final MIME so the
	// recipient sees the original name, not a numbered placeholder.
	if !attachmentNameInMIME(t, string(raw), "report.pdf") {
		t.Errorf("rendered message misses the report.pdf attachment part:\n%s", string(raw))
	}

	// A hash the blob store cannot resolve fails the render instead of
	// producing a partial message.
	m.AttachmentHashes = SerializeAttachments([]AttachmentRef{{Hash: strings.Repeat("b", 64)}})
	if _, err := BuildRawMIME(m, blobs); err == nil {
		t.Error("BuildRawMIME with missing blob = nil error, want failure")
	}

	// Rows stored before names were recorded still attach, under a generic
	// numbered name.
	m.AttachmentHashes = SerializeAttachments([]AttachmentRef{{Hash: hash}})
	raw, err = BuildRawMIME(m, blobs)
	if err != nil {
		t.Fatalf("BuildRawMIME legacy refs: %v", err)
	}
	if !attachmentNameInMIME(t, string(raw), "attachment-1") {
		t.Errorf("rendered message misses the generic attachment part:\n%s", string(raw))
	}

	m = sampleOutbox()
	m.ToAddresses, m.CCAddresses, m.BCCAddresses = "", "", ""
	if _, err := BuildRawMIME(m, nil); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("BuildRawMIME without recipients error = %v, want it to wrap ErrInvalidMessage", err)
	}
}

// attachmentNameInMIME parses the rendered message and reports whether some
// part carries the wanted file name in its disposition or type parameters.
func attachmentNameInMIME(t *testing.T, raw, wanted string) bool {
	t.Helper()
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse rendered MIME: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		t.Fatalf("rendered message is not multipart:\n%s", raw)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	for {
		part, err := parts.NextPart()
		if err != nil {
			return false
		}
		for _, header := range []string{"Content-Disposition", "Content-Type"} {
			_, partParams, paramErr := mime.ParseMediaType(part.Header.Get(header))
			if paramErr != nil {
				continue
			}
			if partParams["filename"] == wanted || partParams["name"] == wanted {
				return true
			}
		}
	}
}
