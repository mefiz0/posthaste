package send

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/mefiz0/posthaste/internal/store"
)

// ComposeInput is everything the compose UI knows about a message at the
// moment the user sends or saves it. It is the plain-data boundary between
// the app layer and the send pipeline; the outbox row it produces is what
// persists.
type ComposeInput struct {
	AccountID string
	From      mail.Address
	To        []mail.Address
	Cc        []mail.Address
	Bcc       []mail.Address
	Subject   string
	BodyText  string
	BodyHTML  string
	InReplyTo string
	// References is the raw References header value of the message being
	// replied to, kept verbatim so threading on the receiving side works.
	References     string
	AttachmentRefs []AttachmentRef
}

// AttachmentRef is one composed attachment as recorded on an outbox row: the
// content hash addressing its bytes in the blob store, plus the file name the
// MIME part must carry so recipients see the original attachment name.
type AttachmentRef struct {
	Hash string `json:"hash"`
	Name string `json:"name"`
}

// ComposeToOutbox maps compose input onto a persisted outbox row in the draft
// state. It performs no I/O and no validation beyond address round-tripping:
// deliverability is judged when the message is actually sent, and a draft
// must survive even while incomplete.
func ComposeToOutbox(input ComposeInput) store.OutboxMessage {
	return store.OutboxMessage{
		ID:               NewOutboxID(),
		AccountID:        input.AccountID,
		FromAddress:      input.From.Address,
		FromName:         input.From.Name,
		ToAddresses:      SerializeAddresses(input.To),
		CCAddresses:      SerializeAddresses(input.Cc),
		BCCAddresses:     SerializeAddresses(input.Bcc),
		Subject:          input.Subject,
		BodyText:         input.BodyText,
		BodyHTML:         input.BodyHTML,
		InReplyTo:        input.InReplyTo,
		References:       input.References,
		AttachmentHashes: SerializeAttachments(input.AttachmentRefs),
		State:            store.SendDraft,
	}
}

// Enqueue moves a persisted draft to the send queue with immediate due time.
// It resets the retry budget so a message re-sent after a failure gets the
// full schedule again, and clears the stale failure description. The caller
// wakes the account's worker afterwards; Enqueue itself does not.
func Enqueue(ctx context.Context, db *store.Store, now time.Time, m store.OutboxMessage) error {
	m.State = store.SendQueued
	m.NextAttemptAt = now
	m.Attempts = 0
	m.LastError = ""
	if err := db.UpsertOutbox(ctx, m); err != nil {
		return fmt.Errorf("send: enqueue %s: %w", m.ID, err)
	}
	return nil
}

// NewOutboxID returns a random RFC 4122 version 4 UUID string identifying an
// outbox row for its whole life, across restarts and retries.
func NewOutboxID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// The crypto source failing is unrecoverable for identifier
		// generation; a panic is honest about that instead of colliding IDs.
		panic("send: generate outbox id: " + err.Error())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	text := hex.EncodeToString(raw[:])
	return strings.Join([]string{
		text[0:8], text[8:12], text[12:16], text[16:20], text[20:32],
	}, "-")
}

// SerializeAddresses renders addresses as the comma-joined RFC 5322 form the
// outbox columns store. mail.Address.String quotes display names that
// contain specials, so ParseAddressList can always read it back.
func SerializeAddresses(addrs []mail.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	rendered := make([]string, len(addrs))
	for i := range addrs {
		rendered[i] = addrs[i].String()
	}
	return strings.Join(rendered, ", ")
}

// ParseAddresses reads back what SerializeAddresses wrote. Empty input
// yields a nil slice rather than an error: an empty header is normal, a
// malformed one is not.
func ParseAddresses(serialized string) ([]mail.Address, error) {
	serialized = strings.TrimSpace(serialized)
	if serialized == "" {
		return nil, nil
	}
	parsed, err := mail.ParseAddressList(serialized)
	if err != nil {
		return nil, fmt.Errorf("send: parse addresses %q: %w", serialized, err)
	}
	addrs := make([]mail.Address, len(parsed))
	for i, addr := range parsed {
		addrs[i] = *addr
	}
	return addrs, nil
}

// SerializeAttachments renders attachment references as a JSON array of
// {hash, name} objects. JSON over a delimiter join because file names are
// free-form text, and a malformed row must fail loudly instead of shipping a
// mangled hash to the blob store.
func SerializeAttachments(refs []AttachmentRef) string {
	if len(refs) == 0 {
		return ""
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		// A []AttachmentRef of plain strings cannot fail to marshal; the
		// branch exists so the invariant is explicit rather than assumed.
		return ""
	}
	return string(encoded)
}

// ParseAttachments reads back what SerializeAttachments wrote. Rows written
// before attachment names were stored carry a bare JSON array of hashes; those
// parse with empty names and the composer falls back to generic part names.
// Empty input yields a nil slice.
func ParseAttachments(serialized string) ([]AttachmentRef, error) {
	serialized = strings.TrimSpace(serialized)
	if serialized == "" {
		return nil, nil
	}
	var refs []AttachmentRef
	if err := json.Unmarshal([]byte(serialized), &refs); err == nil {
		return refs, nil
	}
	var hashes []string
	if err := json.Unmarshal([]byte(serialized), &hashes); err != nil {
		return nil, fmt.Errorf("send: parse attachment list: %w", err)
	}
	refs = make([]AttachmentRef, 0, len(hashes))
	for _, hash := range hashes {
		refs = append(refs, AttachmentRef{Hash: hash})
	}
	return refs, nil
}
