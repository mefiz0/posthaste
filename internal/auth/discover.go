package auth

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Source identifies which autodiscovery stage produced a configuration. The
// stages run in a fixed order and the first success wins, so the source also
// records how much the result should be trusted.
type Source string

// Autodiscovery sources, in resolution order.
const (
	// SourcePreset is a direct match of the address domain against the
	// known-provider table.
	SourcePreset Source = "preset"
	// SourceMXPreset is a custom domain whose MX records route through a
	// hosted provider such as Google Workspace or Microsoft 365.
	SourceMXPreset Source = "mx-preset"
	// SourceSRV is DNS SRV records published as RFC 6186 describes.
	SourceSRV Source = "srv"
	// SourceISPDB is the Mozilla ISPDB autoconfig database.
	SourceISPDB Source = "ispdb"
	// SourceGuess is a common hostname pattern that a live connection probe
	// verified before being trusted.
	SourceGuess Source = "guess"
)

// TLS modes for a discovered server, matching the account table's tls column.
const (
	TLSNone     = "none"
	TLSStartTLS = "starttls"
	TLSTLS      = "tls"
)

// ErrNotDiscovered is returned when every autodiscovery stage fails. The
// setup flow responds by opening the manual configuration form pre-filled
// with the best guesses, so the user corrects fields instead of typing
// everything from scratch.
var ErrNotDiscovered = errors.New("auth: no mail configuration discovered")

// defaultStageTimeout bounds each discovery stage individually. Autodiscovery
// is best-effort UI guidance: it must fail fast into the manual form rather
// than hang the setup dialog.
const defaultStageTimeout = 10 * time.Second

// ispdbURL is Mozilla's autoconfig database, keyed by domain.
const ispdbURL = "https://autoconfig.thunderbird.net/v1.1/"

// maxISPDBBodyBytes caps an autoconfig document read so a hostile or broken
// endpoint cannot exhaust memory.
const maxISPDBBodyBytes = 1 << 20

// ConfigServer is the resolved settings for one mail server, IMAP or SMTP.
type ConfigServer struct {
	Host     string
	Port     int
	TLS      string // TLSNone, TLSStartTLS, or TLSTLS
	Username string
}

// Config is a discovered account configuration. It is setup-time guidance,
// not permanent truth: a provider's published settings can change, so only
// what the user confirms is saved into the account table.
type Config struct {
	// Provider is a human-readable provider name, or "" for a generic server.
	Provider   string
	Source     Source
	AuthMethod string // AuthMethodPassword or AuthMethodXOAUTH2
	IMAP       ConfigServer
	SMTP       ConfigServer
}

// Prober is the network surface autodiscovery needs. It is injectable so the
// whole flow can be driven in tests without DNS, HTTP, or connectivity.
type Prober interface {
	// LookupMX resolves the domain's mail exchangers.
	LookupMX(ctx context.Context, domain string) ([]*net.MX, error)
	// LookupSRV resolves one RFC 6186 service record set.
	LookupSRV(ctx context.Context, service, proto, domain string) (string, []*net.SRV, error)
	// Get performs a bounded-time HTTP GET and returns the response body.
	Get(ctx context.Context, url string) ([]byte, error)
	// ProbeIMAP dials host:port and completes a TLS handshake when useTLS is
	// set, verifying a guessed server before the guess is trusted.
	ProbeIMAP(ctx context.Context, host string, port int, useTLS bool) error
}

// Discoverer resolves an email address to concrete IMAP/SMTP settings by
// running the ordered autodiscovery stages.
type Discoverer struct {
	Prober Prober
	// Timeout bounds each stage individually; zero means ten seconds.
	Timeout time.Duration
}

// NewSystemDiscoverer returns a Discoverer wired to the real network.
func NewSystemDiscoverer() *Discoverer {
	return &Discoverer{Prober: NewSystemProber()}
}

// SystemProber implements Prober against the real network: the system DNS
// resolver, a bounded HTTP client for ISPDB fetches, and TLS dials for IMAP
// verification.
type SystemProber struct {
	httpClient *http.Client
}

// NewSystemProber returns the production Prober.
func NewSystemProber() Prober {
	return &SystemProber{httpClient: &http.Client{Timeout: 10 * time.Second}}
}

// LookupMX resolves mail exchangers through the system resolver.
func (p *SystemProber) LookupMX(ctx context.Context, domain string) ([]*net.MX, error) {
	return net.DefaultResolver.LookupMX(ctx, domain)
}

// LookupSRV resolves service records through the system resolver.
func (p *SystemProber) LookupSRV(ctx context.Context, service, proto, domain string) (string, []*net.SRV, error) {
	return net.DefaultResolver.LookupSRV(ctx, service, proto, domain)
}

// Get fetches url with the prober's HTTP client and reads the body up to the
// ISPDB size cap.
func (p *SystemProber) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: fetch autoconfig: %w", err)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: fetch autoconfig: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: fetch autoconfig: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxISPDBBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("auth: fetch autoconfig: %w", err)
	}
	return body, nil
}

// ProbeIMAP dials host:port and, when useTLS is set, completes a TLS
// handshake so a guessed server is only trusted after a live connection
// succeeds. Server hostnames are account metadata, not secrets, so including
// one in an error is safe to log.
func (p *SystemProber) ProbeIMAP(ctx context.Context, host string, port int, useTLS bool) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !useTLS {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("auth: probe %s: %w", addr, err)
		}
		_ = conn.Close()
		return nil
	}
	conn, err := (&tls.Dialer{NetDialer: dialer}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("auth: probe %s: %w", addr, err)
	}
	_ = conn.Close()
	return nil
}

// providerPreset is the hardcoded configuration for one large provider.
type providerPreset struct {
	provider   string
	domains    []string
	authMethod string
	imap       ConfigServer
	smtp       ConfigServer
}

// providerPresets covers the handful of providers that host most mailboxes.
// Gmail and Microsoft 365 deprecated password IMAP entirely, hence the OAuth
// auth method; Yahoo and iCloud still accept app-specific passwords.
var providerPresets = []providerPreset{
	{
		provider:   "Gmail",
		domains:    []string{"gmail.com", "googlemail.com"},
		authMethod: AuthMethodXOAUTH2,
		imap:       ConfigServer{Host: "imap.gmail.com", Port: 993, TLS: TLSTLS},
		smtp:       ConfigServer{Host: "smtp.gmail.com", Port: 465, TLS: TLSTLS},
	},
	{
		provider:   "Outlook",
		domains:    []string{"outlook.com", "hotmail.com", "hotmail.co.uk", "live.com", "msn.com", "passport.com"},
		authMethod: AuthMethodXOAUTH2,
		imap:       ConfigServer{Host: "outlook.office365.com", Port: 993, TLS: TLSTLS},
		smtp:       ConfigServer{Host: "smtp.office365.com", Port: 587, TLS: TLSStartTLS},
	},
	{
		provider:   "Yahoo",
		domains:    []string{"yahoo.com", "ymail.com", "rocketmail.com"},
		authMethod: AuthMethodPassword,
		imap:       ConfigServer{Host: "imap.mail.yahoo.com", Port: 993, TLS: TLSTLS},
		smtp:       ConfigServer{Host: "smtp.mail.yahoo.com", Port: 465, TLS: TLSTLS},
	},
	{
		provider:   "iCloud",
		domains:    []string{"icloud.com", "me.com", "mac.com"},
		authMethod: AuthMethodPassword,
		imap:       ConfigServer{Host: "imap.mail.me.com", Port: 993, TLS: TLSTLS},
		smtp:       ConfigServer{Host: "smtp.mail.me.com", Port: 587, TLS: TLSStartTLS},
	},
}

// Discover resolves email to concrete server settings. Stages run in a fixed
// order — known-provider preset by exact domain, MX records mapping a custom
// domain to a hosted provider, RFC 6186 SRV records, the Mozilla ISPDB, then
// hostname guesses verified by a live probe — and the first success wins. A
// stage's failure falls through to the next one; only when every stage fails
// is ErrNotDiscovered returned. Errors never contain the email address, so
// they can be logged without leaking it.
func (d *Discoverer) Discover(ctx context.Context, email string) (Config, error) {
	if d.Prober == nil {
		return Config{}, errors.New("auth: discover: no prober configured")
	}
	_, domain, err := splitAddress(email)
	if err != nil {
		return Config{}, err
	}

	timeout := d.Timeout
	if timeout <= 0 {
		timeout = defaultStageTimeout
	}

	// Stage 1: known-provider preset matched by exact domain. No network.
	if cfg, ok := presetForDomain(domain, email); ok {
		return cfg, nil
	}

	// Stage 2: MX records routing a custom domain through a hosted provider.
	mxs, err := d.lookupMX(ctx, domain, timeout)
	if err == nil {
		for _, mx := range mxs {
			if preset, ok := presetForMXHost(mx.Host); ok {
				return presetConfig(preset, SourceMXPreset, email), nil
			}
		}
	}

	// Stage 3: RFC 6186 SRV records.
	if cfg, ok := d.srvConfig(ctx, domain, email, timeout); ok {
		return cfg, nil
	}

	// Stage 4: Mozilla ISPDB.
	if cfg, ok := d.ispdbConfig(ctx, domain, email, timeout); ok {
		return cfg, nil
	}

	// Stage 5: common hostname patterns, verified by a live probe.
	if cfg, ok := d.guessConfig(ctx, domain, email, timeout); ok {
		return cfg, nil
	}

	return Config{}, fmt.Errorf("auth: discover: %w", ErrNotDiscovered)
}

// lookupMX bounds the MX lookup with the stage timeout.
func (d *Discoverer) lookupMX(ctx context.Context, domain string, timeout time.Duration) ([]*net.MX, error) {
	stageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return d.Prober.LookupMX(stageCtx, domain)
}

// splitAddress validates an email address and returns its local part plus a
// normalized (lowercase, trailing-dot-free) domain.
func splitAddress(email string) (string, string, error) {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", "", errors.New("auth: discover: invalid email address")
	}
	return local, strings.ToLower(strings.TrimSuffix(domain, ".")), nil
}

// presetForDomain matches the address domain against the preset table.
func presetForDomain(domain, email string) (Config, bool) {
	for _, preset := range providerPresets {
		for _, presetDomain := range preset.domains {
			if domain == presetDomain {
				return presetConfig(preset, SourcePreset, email), true
			}
		}
	}
	return Config{}, false
}

// presetForMXHost maps a mail exchanger host to a known hosted provider, or
// reports false when it belongs to none. Google Workspace and Microsoft 365
// are matched because custom domains route through their well-known MX hosts;
// the zones used as suffixes are controlled by the providers themselves.
func presetForMXHost(host string) (providerPreset, bool) {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	google := h == "smtp.google.com" ||
		strings.HasSuffix(h, ".google.com") ||
		strings.HasSuffix(h, ".googlemail.com")
	outlook := strings.HasSuffix(h, ".outlook.com") ||
		strings.HasSuffix(h, ".protection.outlook.com")
	switch {
	case google:
		return presetByName("Gmail")
	case outlook:
		return presetByName("Outlook")
	default:
		return providerPreset{}, false
	}
}

// presetByName finds a preset entry by its provider name.
func presetByName(name string) (providerPreset, bool) {
	for _, preset := range providerPresets {
		if preset.provider == name {
			return preset, true
		}
	}
	return providerPreset{}, false
}

// presetConfig turns a preset into a discovered configuration. The full
// address is the login username on every preset provider, so the user never
// has to type it twice.
func presetConfig(preset providerPreset, source Source, email string) Config {
	preset.imap.Username = email
	preset.smtp.Username = email
	return Config{
		Provider:   preset.provider,
		Source:     source,
		AuthMethod: preset.authMethod,
		IMAP:       preset.imap,
		SMTP:       preset.smtp,
	}
}

// srvConfig builds a configuration from RFC 6186 SRV records. The IMAP side
// must resolve (implicit TLS first, STARTTLS as the only alternative); the
// SMTP side falls back to the standard submission default when the domain
// publishes no SMTP records.
func (d *Discoverer) srvConfig(ctx context.Context, domain, email string, timeout time.Duration) (Config, bool) {
	imap, ok := d.srvServer(ctx, domain, "imaps", TLSTLS, email, timeout)
	if !ok {
		imap, ok = d.srvServer(ctx, domain, "imap", TLSStartTLS, email, timeout)
		if !ok {
			return Config{}, false
		}
	}

	smtp, ok := d.srvServer(ctx, domain, "submissions", TLSTLS, email, timeout)
	if !ok {
		smtp, ok = d.srvServer(ctx, domain, "submission", TLSStartTLS, email, timeout)
	}
	if !ok {
		smtp = ConfigServer{Host: "smtp." + domain, Port: 587, TLS: TLSStartTLS, Username: email}
	}
	return Config{
		Source:     SourceSRV,
		AuthMethod: AuthMethodPassword,
		IMAP:       imap,
		SMTP:       smtp,
	}, true
}

// srvServer resolves one SRV service set and converts the lowest-priority
// record into a ConfigServer. A "." target means the domain explicitly
// publishes no service there and is treated like a failed lookup.
func (d *Discoverer) srvServer(ctx context.Context, domain, service, tlsMode, email string, timeout time.Duration) (ConfigServer, bool) {
	stageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, addrs, err := d.Prober.LookupSRV(stageCtx, service, "tcp", domain)
	if err != nil || len(addrs) == 0 {
		return ConfigServer{}, false
	}
	best := addrs[0]
	for _, record := range addrs[1:] {
		if record.Priority < best.Priority {
			best = record
		}
	}
	target := strings.ToLower(strings.TrimSuffix(best.Target, "."))
	if target == "" || target == "." {
		return ConfigServer{}, false
	}
	return ConfigServer{
		Host:     target,
		Port:     int(best.Port),
		TLS:      tlsMode,
		Username: email,
	}, true
}

// ispdbServer is one server element of the ISPDB autoconfig XML. type,
// hostname, and port are attributes; socketType and username are child
// elements, matching the format Thunderbird publishes.
type ispdbServer struct {
	Type       string `xml:"type,attr"`
	Hostname   string `xml:"hostname,attr"`
	Port       int    `xml:"port,attr"`
	SocketType string `xml:"socketType"`
	Username   string `xml:"username"`
}

// ispdbDocument is the minimal slice of the ISPDB XML format the setup flow
// needs: the first IMAP incoming server and the first SMTP outgoing server.
type ispdbDocument struct {
	IncomingServers []ispdbServer `xml:"emailprovider>incomingServer"`
	OutgoingServers []ispdbServer `xml:"emailprovider>outgoingServer"`
}

// ispdbConfig fetches and parses the domain's ISPDB autoconfig document.
func (d *Discoverer) ispdbConfig(ctx context.Context, domain, email string, timeout time.Duration) (Config, bool) {
	stageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := d.Prober.Get(stageCtx, ispdbURL+domain)
	if err != nil {
		return Config{}, false
	}
	var doc ispdbDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return Config{}, false
	}
	var imap, smtp ConfigServer
	imapFound, smtpFound := false, false
	for _, in := range doc.IncomingServers {
		if in.Type == "imap" {
			imap, imapFound = ispdbServerToConfig(in, email), true
			break
		}
	}
	for _, out := range doc.OutgoingServers {
		if out.Type == "smtp" {
			smtp, smtpFound = ispdbServerToConfig(out, email), true
			break
		}
	}
	if !imapFound || !smtpFound {
		return Config{}, false
	}
	return Config{
		Source:     SourceISPDB,
		AuthMethod: AuthMethodPassword,
		IMAP:       imap,
		SMTP:       smtp,
	}, true
}

// ispdbServerToConfig converts one ISPDB server element.
func ispdbServerToConfig(s ispdbServer, email string) ConfigServer {
	return ConfigServer{
		Host:     s.Hostname,
		Port:     s.Port,
		TLS:      ispdbTLSMode(s.SocketType),
		Username: ispdbUsername(s.Username, email),
	}
}

// ispdbTLSMode maps the ISPDB socketType attribute onto the account table's
// TLS vocabulary. Unknown modes map to implicit TLS, the strictest safe
// choice, rather than risking a plaintext connection.
func ispdbTLSMode(socketType string) string {
	switch strings.ToLower(socketType) {
	case "starttls":
		return TLSStartTLS
	case "plain", "none":
		return TLSNone
	default: // "ssl", "tls", and anything unrecognised.
		return TLSTLS
	}
}

// ispdbUsername resolves the %EMAILADDRESS%-style templates the ISPDB uses.
func ispdbUsername(template, email string) string {
	local, domain, _ := strings.Cut(email, "@")
	return strings.NewReplacer(
		"%EMAILADDRESS%", email,
		"%EMAILLOCALPART%", local,
		"%EMAILDOMAIN%", domain,
	).Replace(template)
}

// guessConfig tries the common hostname patterns and only trusts the first
// one a live IMAP connection accepts, so a parked or wildcard DNS entry
// cannot redirect the user's mail to a stranger's server.
func (d *Discoverer) guessConfig(ctx context.Context, domain, email string, timeout time.Duration) (Config, bool) {
	for _, host := range []string{"imap." + domain, "mail." + domain} {
		stageCtx, cancel := context.WithTimeout(ctx, timeout)
		err := d.Prober.ProbeIMAP(stageCtx, host, 993, true)
		cancel()
		if err != nil {
			continue
		}
		return Config{
			Source:     SourceGuess,
			AuthMethod: AuthMethodPassword,
			IMAP:       ConfigServer{Host: host, Port: 993, TLS: TLSTLS, Username: email},
			SMTP:       ConfigServer{Host: "smtp." + domain, Port: 587, TLS: TLSStartTLS, Username: email},
		}, true
	}
	return Config{}, false
}
