// Package send owns the outgoing-mail pipeline: the durable queue of composed
// messages in the account's outbox, the state machine that moves each message
// from draft through queued and sending to sent or failed, and the SMTP
// transport that delivers them.
//
// The state machine is deliberately transport-agnostic: the worker drives a
// small Mailer interface, so retries, permanent-failure detection, and
// scrubbing are all testable without a network. A queued send is never
// dropped by mailbox state; only delivery results change its fate.
package send

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"time"

	"github.com/mefiz0/posthaste/internal/attachment"
	"github.com/mefiz0/posthaste/internal/store"
)

// TLSMode selects how the connection to the SMTP server is secured. The
// values match the TLS settings an account row carries for its IMAP side, so
// the setup flow can use one vocabulary for both.
type TLSMode string

// TLS modes. TLSNone exists for localhost test containers only; real accounts
// use TLSImplicit (TLS from the first byte, typically port 465) or
// TLSStartTLS (upgrade on a plain port, typically 587).
const (
	TLSNone     TLSMode = "none"
	TLSStartTLS TLSMode = "starttls"
	TLSImplicit TLSMode = "tls"
)

// TransportConfig describes one SMTP endpoint and how to reach it.
type TransportConfig struct {
	Host string
	Port int
	TLS  TLSMode
	// Username is the fallback login when the AuthProvider yields an empty
	// username. Usually identical to the account's SMTP username.
	Username string
}

// Mailer delivers one composed message. The send worker defines this
// interface so the retry state machine can be tested against a fake; the
// real implementation dials SMTP through go-mail.
type Mailer interface {
	// Deliver sends one composed message. It returns a deterministic error
	// classification: permanent SMTP rejections arrive wrapped in Rejection
	// (matchable through ErrRejected), and unsendable input errors carry
	// ErrInvalidMessage, so Permanent can sort them from transient faults.
	Deliver(ctx context.Context, m store.OutboxMessage, auth AuthProvider) error
}

// AuthProvider supplies the credentials for one delivery attempt. It is
// called per attempt so OAuth access tokens can be refreshed between tries.
// authMethod is one of the account auth method labels; isOAuth reports
// whether the secret is a bearer token rather than a password.
type AuthProvider func(ctx context.Context) (username, secret string, authMethod string, isOAuth bool, err error)

// BlobOpener provides read access to the content-addressed attachment store.
// The outbox row carries attachment hashes only; delivery streams the bytes
// through this interface at send time.
type BlobOpener interface {
	Open(ctx context.Context, hash string) (io.ReadCloser, error)
}

// BlobStore adapts an *attachment.Store to BlobOpener. It exists because the
// store's own Open has no context parameter; reads are local disk access, so
// the context only guards entry into the call.
type BlobStore struct {
	store *attachment.Store
}

// NewBlobStore returns a BlobOpener over the shared attachment store.
func NewBlobStore(s *attachment.Store) BlobStore {
	return BlobStore{store: s}
}

// Open returns a reader over the blob with the given content hash.
func (b BlobStore) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("send: open blob: %w", err)
	}
	return b.store.Open(hash)
}

// Sentinel errors describing delivery outcomes. Errors from this package are
// wrapped with the "send: " prefix; these sentinels classify the cause.
var (
	// ErrRejected marks an SMTP-level rejection by the server. It wraps (or
	// is wrapped in) a *Rejection carrying the numeric reply code.
	ErrRejected = errors.New("send: message rejected by server")
	// ErrInvalidMessage marks input that can never be delivered as-is: a
	// missing sender, unparsable recipients, or unusable attachment
	// references. Retrying cannot fix it, so it fails the message at once.
	ErrInvalidMessage = errors.New("send: message is invalid")
)

// Rejection is a typed SMTP rejection with the reply code extracted, so
// callers can inspect why a server refused a message without parsing text.
type Rejection struct {
	// Code is the SMTP reply code, e.g. 550.
	Code int
	// Enhanced is the dotted enhanced status code when the server supplied
	// one, e.g. "5.1.1".
	Enhanced string
	// Text is the human-readable reply. It may quote recipient addresses and
	// must be scrubbed before storage or display.
	Text string
}

// Error satisfies the error interface.
func (e *Rejection) Error() string {
	if e == nil {
		return ErrRejected.Error()
	}
	if e.Enhanced != "" {
		return fmt.Sprintf("%s (smtp %d %s): %s", ErrRejected, e.Code, e.Enhanced, e.Text)
	}
	return fmt.Sprintf("%s (smtp %d): %s", ErrRejected, e.Code, e.Text)
}

// Is reports every rejection as ErrRejected so errors.Is works through the
// wrapper without matching on message text.
func (e *Rejection) Is(target error) bool { return target == ErrRejected }

// mailAddressPointers converts stored address values to the pointer slice the
// composition calls expect, keeping one address representation across the
// package.
func mailAddressPointers(addrs []mail.Address) []*mail.Address {
	pointers := make([]*mail.Address, len(addrs))
	for i := range addrs {
		pointers[i] = &addrs[i]
	}
	return pointers
}

// toUnix renders a timestamp the way the store's outbox columns do: second
// resolution UTC, with the zero time stored as zero rather than a negative
// year-1 count.
func toUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}
