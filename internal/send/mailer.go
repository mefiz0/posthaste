package send

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/textproto"
	"time"

	"github.com/emersion/go-sasl"
	gomail "github.com/wneessen/go-mail"
	"github.com/wneessen/go-mail/smtp"

	"github.com/mefiz0/posthaste/internal/auth"
	"github.com/mefiz0/posthaste/internal/store"
)

// GoMailer is the real Mailer: it composes MIME with go-mail and hands the
// message to the account's SMTP server. One GoMailer serves one account's
// endpoint; credentials are fetched per attempt through the AuthProvider so
// OAuth tokens can refresh between retries.
type GoMailer struct {
	config    TransportConfig
	blobs     BlobOpener
	transport time.Duration
}

// transportTimeout bounds the go-mail-level socket operations. The worker's
// per-attempt context is the real ceiling; this keeps an unresponsive
// server from stalling a socket operation when a caller forgets a deadline.
const transportTimeout = 30 * time.Second

// NewMailer returns a Mailer delivering through the given endpoint. Blobs
// supplies attachment bytes; passing nil disables attachment loading.
func NewMailer(config TransportConfig, blobs BlobOpener) *GoMailer {
	return &GoMailer{config: config, blobs: blobs, transport: transportTimeout}
}

// Deliver composes the outbox row into MIME and sends it. Every failure is
// classified deterministically: unsendable input carries ErrInvalidMessage,
// server rejections arrive as *Rejection, and the rest is transient.
func (g *GoMailer) Deliver(ctx context.Context, m store.OutboxMessage, credentials AuthProvider) error {
	username, secret, authMethod, isOAuth, err := credentials(ctx)
	if err != nil {
		return fmt.Errorf("send: resolve credentials: %w", err)
	}
	if username == "" {
		username = g.config.Username
	}
	if username == "" {
		return fmt.Errorf("send: %w: no SMTP username", ErrInvalidMessage)
	}

	mechanism, err := auth.SASLMechanism(authMethod, username, secret, isOAuth)
	if err != nil {
		return fmt.Errorf("send: %w: select SASL mechanism: %w", ErrInvalidMessage, err)
	}

	message, closers, err := g.compose(ctx, m)
	defer closeAll(closers)
	if err != nil {
		return err
	}

	client, err := gomail.NewClient(g.config.Host, g.options(mechanism)...)
	if err != nil {
		return fmt.Errorf("send: create SMTP client: %w", err)
	}
	if err := client.DialAndSendWithContext(ctx, message); err != nil {
		return wrapDelivery(err)
	}
	return nil
}

// options builds the go-mail client options for one delivery: endpoint
// security first, then the SASL mechanism resolved from the account's auth
// method, which carries its own credentials.
func (g *GoMailer) options(mechanism sasl.Client) []gomail.Option {
	opts := []gomail.Option{
		gomail.WithPort(g.config.Port),
		gomail.WithTimeout(g.transport),
	}
	switch g.config.TLS {
	case TLSNone:
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	case TLSImplicit:
		// WithPort was applied first, so it pins the account's port before
		// WithSSLPort could substitute the implicit-TLS default.
		opts = append(opts, gomail.WithSSLPort(false))
	case TLSStartTLS, "":
		// Empty falls back to STARTTLS because the app requires TLS on
		// every real account; a blank setting is a misconfigured test.
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	opts = append(opts, gomail.WithSMTPAuthCustom(newSASLAuth(mechanism)))
	return opts
}

// compose renders the outbox row into a go-mail message. The returned
// closers cover attachment readers: go-mail streams them during delivery,
// so they must stay open until the message has been written, not merely
// composed.
func (g *GoMailer) compose(ctx context.Context, m store.OutboxMessage) (*gomail.Msg, []io.Closer, error) {
	if m.FromAddress == "" {
		return nil, nil, fmt.Errorf("send: %w: empty sender", ErrInvalidMessage)
	}

	to, err := ParseAddresses(m.ToAddresses)
	if err != nil {
		return nil, nil, fmt.Errorf("send: %w: bad recipient: %w", ErrInvalidMessage, err)
	}
	cc, err := ParseAddresses(m.CCAddresses)
	if err != nil {
		return nil, nil, fmt.Errorf("send: %w: bad recipient: %w", ErrInvalidMessage, err)
	}
	bcc, err := ParseAddresses(m.BCCAddresses)
	if err != nil {
		return nil, nil, fmt.Errorf("send: %w: bad recipient: %w", ErrInvalidMessage, err)
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		return nil, nil, fmt.Errorf("send: %w: no recipients", ErrInvalidMessage)
	}

	message := gomail.NewMsg()
	message.FromMailAddress(&mail.Address{Name: m.FromName, Address: m.FromAddress})
	message.ToMailAddress(mailAddressPointers(to)...)
	message.CcMailAddress(mailAddressPointers(cc)...)
	message.BccMailAddress(mailAddressPointers(bcc)...)
	message.Subject(m.Subject)

	// Threading headers keep the original casing verbatim: message IDs are
	// opaque tokens other clients match exactly.
	if m.InReplyTo != "" {
		message.SetGenHeader(gomail.HeaderInReplyTo, m.InReplyTo)
	}
	if m.References != "" {
		message.SetGenHeader(gomail.HeaderReferences, m.References)
	}

	message.SetBodyString(gomail.TypeTextPlain, m.BodyText)
	if m.BodyHTML != "" {
		message.AddAlternativeString(gomail.TypeTextHTML, m.BodyHTML)
	}

	closers, err := g.attach(ctx, message, m)
	if err != nil {
		closeAll(closers)
		return nil, nil, err
	}
	return message, closers, nil
}

// attach streams each referenced blob into the message as an attachment. The
// outbox row carries each blob's content hash and the file name to present;
// rows written before names were recorded fall back to numbered names, since
// the hash alone identifies the bytes.
func (g *GoMailer) attach(ctx context.Context, message *gomail.Msg, m store.OutboxMessage) ([]io.Closer, error) {
	refs, err := ParseAttachments(m.AttachmentHashes)
	if err != nil {
		return nil, fmt.Errorf("send: %w: %w", ErrInvalidMessage, err)
	}
	if len(refs) == 0 || g.blobs == nil {
		return nil, nil
	}

	var closers []io.Closer
	for i, ref := range refs {
		reader, err := g.blobs.Open(ctx, ref.Hash)
		if err != nil {
			return closers, fmt.Errorf("send: open attachment %d: %w", i+1, err)
		}
		closers = append(closers, reader)
		name := ref.Name
		if name == "" {
			name = fmt.Sprintf("attachment-%d", i+1)
		}
		if err := message.AttachReader(name, reader); err != nil {
			return closers, fmt.Errorf("send: %w: attach %s: %w", ErrInvalidMessage, name, err)
		}
	}
	return closers, nil
}

// closeAll releases attachment readers, tolerating nils so callers can defer
// unconditionally.
func closeAll(closers []io.Closer) {
	for _, closer := range closers {
		if closer != nil {
			_ = closer.Close()
		}
	}
}

// wrapDelivery re-tags a go-mail delivery failure with this package's typed
// rejection, so Permanent sees one error shape regardless of where inside
// go-mail the server refused us.
func wrapDelivery(err error) error {
	var sendErr *gomail.SendError
	if errors.As(err, &sendErr) {
		if code := sendErr.ErrorCode(); code != 0 {
			return fmt.Errorf("send: deliver: %w", &Rejection{
				Code:     code,
				Enhanced: sendErr.EnhancedStatusCode(),
				Text:     sendErr.Error(),
			})
		}
	}
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return fmt.Errorf("send: deliver: %w", &Rejection{Code: protoErr.Code, Text: protoErr.Msg})
	}
	return fmt.Errorf("send: deliver: %w", err)
}

// saslAuth adapts a go-sasl client to the smtp.Auth interface go-mail's
// custom authentication hook expects. The two interfaces differ in that
// smtp.Auth receives server info and a "more" flag; the SASL mechanisms the
// auth package produces (PLAIN, XOAUTH2, OAUTHBEARER) need neither.
type saslAuth struct {
	client sasl.Client
}

// newSASLAuth wraps a resolved SASL mechanism for go-mail.
func newSASLAuth(client sasl.Client) smtp.Auth {
	return saslAuth{client: client}
}

// Start begins the exchange with the mechanism's name and initial response.
func (a saslAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return a.client.Start()
}

// Next answers one server challenge; a false "more" ends the exchange and
// the SASL mechanisms in use expect no further data.
func (a saslAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	return a.client.Next(fromServer)
}
