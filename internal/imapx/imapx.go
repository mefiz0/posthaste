// Package imapx is a thin wrapper around go-imap/v2 that owns the IMAP
// connection and session concerns for the sync engine.
//
// Its own API never exposes go-imap types: every server response is mapped
// into package-local structs, so consumers and their tests never touch the
// wire protocol. Message data is always fetched with PEEK variants so reading
// a message never sets \Seen as a side effect.
//
// A Client is meant to be owned by a single sync worker goroutine and is not
// safe for concurrent use. Every operation selects the mailbox it needs, so
// callers never track the server-side selected mailbox themselves.
package imapx

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
)

const (
	dialTimeout    = 15 * time.Second
	commandTimeout = 30 * time.Second
)

// TLSMode selects how the connection to the server is secured.
type TLSMode string

// TLS modes. TLSNone is for localhost test containers only; real accounts use
// TLSImplicit (implicit TLS, typically port 993) or TLSStartTLS (upgrade on a
// plain port, typically 143).
const (
	TLSNone     TLSMode = "none"
	TLSStartTLS TLSMode = "starttls"
	TLSImplicit TLSMode = "tls"
)

// Config describes one IMAP endpoint and how to authenticate against it.
type Config struct {
	Host string
	Port int
	TLS  TLSMode
	// TLSSkipVerify disables server certificate validation. This exists for
	// self-signed test containers; never enable it for a real account.
	TLSSkipVerify bool
	Username      string
	// Auth is the single authenticator used during login. Required.
	Auth SASLAuth
}

// SASLAuth supplies the SASL mechanism used to authenticate a connection.
// It lets callers provide password or XOAUTH2 (or any other) credentials
// without this package knowing about credential storage or OAuth. The server
// must advertise the corresponding AUTH= capability.
type SASLAuth interface {
	Mechanism() sasl.Client
}

// saslAuth adapts a ready sasl.Client to SASLAuth.
type saslAuth struct {
	client sasl.Client
}

func (a saslAuth) Mechanism() sasl.Client { return a.client }

// PasswordAuth returns a SASLAuth that authenticates username with a plain
// password via the SASL PLAIN mechanism.
func PasswordAuth(username, password string) SASLAuth {
	return saslAuth{client: sasl.NewPlainClient("", username, password)}
}

// Client is an authenticated IMAP session for one account.
type Client struct {
	conn   net.Conn
	client *imapclient.Client
	nudges chan struct{}

	// selectedPath/selectedReadOnly remember the current selection so
	// operations do not pay for a redundant SELECT. Only touched from the
	// worker goroutine that owns the connection.
	selectedPath     string
	selectedReadOnly bool
}

// Dial connects to the server, upgrades TLS when the configuration asks for
// it, authenticates, and returns a ready session. The context bounds dialing,
// the TLS handshake, and the login exchange; afterwards it only bounds the
// operations the caller starts.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("imapx: dial: no host configured")
	}
	if cfg.Port <= 0 {
		return nil, fmt.Errorf("imapx: dial: no port configured")
	}
	if cfg.Auth == nil {
		return nil, fmt.Errorf("imapx: dial: no authentication configured")
	}

	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("imapx: dial %s: %w", address, err)
	}

	c := &Client{conn: conn, nudges: make(chan struct{}, 1)}
	stop := c.watchCancel(ctx)
	defer stop()

	opts := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				// An EXISTS report during IDLE is the "new mail arrived"
				// signal; hand it to the parked Idle call as a nudge.
				if data.NumMessages != nil {
					select {
					case c.nudges <- struct{}{}:
					default:
					}
				}
			},
		},
	}

	switch cfg.TLS {
	case TLSNone:
		c.client = imapclient.New(conn, opts)
	case TLSImplicit:
		tlsConn := tls.Client(conn, tlsConfig(cfg))
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("imapx: TLS handshake with %s: %w", address, err)
		}
		c.client = imapclient.New(tlsConn, opts)
	case TLSStartTLS:
		opts.TLSConfig = tlsConfig(cfg)
		if c.client, err = imapclient.NewStartTLS(conn, opts); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("imapx: STARTTLS with %s: %w", address, err)
		}
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("imapx: dial %s: unknown TLS mode %q", address, cfg.TLS)
	}

	if err := c.client.WaitGreeting(); err != nil {
		_ = c.Close(context.Background())
		return nil, fmt.Errorf("imapx: greeting from %s: %w", address, err)
	}
	if err := c.client.Authenticate(cfg.Auth.Mechanism()); err != nil {
		_ = c.Close(context.Background())
		return nil, fmt.Errorf("imapx: authenticate with %s: %w", address, err)
	}
	return c, nil
}

// Close logs out and tears down the connection. Best effort: errors during
// logout are ignored because the session is being discarded either way, so
// callers can safely defer this. Closing an already-closed Client is a no-op.
func (c *Client) Close(ctx context.Context) error {
	if c.client != nil {
		end := c.beginOp(ctx)
		_ = c.client.Logout().Wait()
		end()
		c.client = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	return nil
}

// capabilities returns the server capability set, fetching it if the server
// did not include capabilities in its greeting. Several commands (MOVE,
// UID EXPUNGE, IDLE) behave differently depending on advertised extensions.
func (c *Client) capabilities() (imap.CapSet, error) {
	if caps := c.client.Caps(); len(caps) > 0 {
		return caps, nil
	}
	caps, err := c.client.Capability().Wait()
	if err != nil {
		return nil, fmt.Errorf("imapx: capability: %w", err)
	}
	return caps, nil
}

// tlsConfig builds the TLS settings for the configured endpoint.
func tlsConfig(cfg Config) *tls.Config {
	if cfg.TLSSkipVerify {
		return &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed test containers
	}
	return &tls.Config{ServerName: cfg.Host}
}
