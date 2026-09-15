package send

import (
	"bytes"
	"context"
	"fmt"

	"github.com/mefiz0/posthaste/internal/store"
)

// BuildRawMIME renders an outbox row into the final RFC 5322 message bytes,
// byte-for-byte what a delivery through GoMailer would transmit. The caller
// uses it to store a copy of a sent message in the Sent folder. Blobs supplies
// the attachment bytes referenced by the row's content hashes; passing nil
// disables attachment loading. Composition failures match Deliver's
// classification, so unsendable input is reported with ErrInvalidMessage.
func BuildRawMIME(m store.OutboxMessage, blobs BlobOpener) ([]byte, error) {
	mailer := GoMailer{blobs: blobs}
	message, closers, err := mailer.compose(context.Background(), m)
	defer closeAll(closers)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if _, err := message.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("send: render message: %w", err)
	}
	return buf.Bytes(), nil
}
