package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeProber scripts every network lookup autodiscovery can perform and
// counts the calls, so tests can assert both the results and that earlier
// stages short-circuit later ones.
type fakeProber struct {
	mxHosts    map[string][]string   // domain -> MX hosts
	srvRecords map[string][]*net.SRV // "service/tcp/domain" -> records
	httpBodies map[string][]byte     // url -> body
	httpErr    map[string]error
	probeOK    map[string]bool // "host:port" -> reachable
	mxCalls    int
	srvCalls   int
	getCalls   int
	probeCalls int
}

func (f *fakeProber) LookupMX(ctx context.Context, domain string) ([]*net.MX, error) {
	f.mxCalls++
	hosts, ok := f.mxHosts[strings.ToLower(domain)]
	if !ok {
		return nil, fmt.Errorf("fake: no MX records for %s", domain)
	}
	mxs := make([]*net.MX, 0, len(hosts))
	for _, host := range hosts {
		mxs = append(mxs, &net.MX{Host: host, Pref: 10})
	}
	return mxs, nil
}

func (f *fakeProber) LookupSRV(ctx context.Context, service, proto, domain string) (string, []*net.SRV, error) {
	f.srvCalls++
	records, ok := f.srvRecords[service+"/tcp/"+strings.ToLower(domain)]
	if !ok {
		return "", nil, fmt.Errorf("fake: no SRV records for %s/tcp/%s", service, domain)
	}
	return "srv.example.test.", records, nil
}

func (f *fakeProber) Get(ctx context.Context, url string) ([]byte, error) {
	f.getCalls++
	if err, ok := f.httpErr[url]; ok {
		return nil, err
	}
	body, ok := f.httpBodies[url]
	if !ok {
		return nil, fmt.Errorf("fake: no autoconfig document at %s", url)
	}
	return body, nil
}

func (f *fakeProber) ProbeIMAP(ctx context.Context, host string, port int, useTLS bool) error {
	f.probeCalls++
	if f.probeOK[net.JoinHostPort(host, strconv.Itoa(port))] {
		return nil
	}
	return fmt.Errorf("fake: %s:%d unreachable", host, port)
}

func (f *fakeProber) noNetworkCalls() bool {
	return f.mxCalls == 0 && f.srvCalls == 0 && f.getCalls == 0 && f.probeCalls == 0
}

// srvFor builds a single SRV record set for the fake.
func srvFor(target string, port uint16) []*net.SRV {
	return []*net.SRV{{Target: target, Port: port, Priority: 1, Weight: 0}}
}

const ispdbExampleOrg = `<?xml version="1.0"?>
<clientConfig version="1.1">
  <emailprovider id="example.org">
    <domain>example.org</domain>
    <incomingServer type="pop3" hostname="pop.example.org" port="995">
      <socketType>SSL</socketType>
    </incomingServer>
    <incomingServer type="imap" hostname="imap.example.org" port="993">
      <socketType>SSL</socketType>
      <username>%EMAILADDRESS%</username>
    </incomingServer>
    <outgoingServer type="smtp" hostname="smtp.example.org" port="587">
      <socketType>STARTTLS</socketType>
      <username>%EMAILLOCALPART%</username>
    </outgoingServer>
  </emailprovider>
</clientConfig>`

func TestDiscoverPresetDirectHit(t *testing.T) {
	prober := &fakeProber{
		// Every network answer is present; the preset stage must win before
		// any of them is consulted.
		srvRecords: map[string][]*net.SRV{"imaps/tcp/gmail.com": srvFor("attacker.example.test", 993)},
		probeOK:    map[string]bool{"attacker.example.test:993": true},
	}
	d := &Discoverer{Prober: prober}

	cfg, err := d.Discover(context.Background(), "user@gmail.com")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.Provider != "Gmail" {
		t.Errorf("Provider = %q, want Gmail", cfg.Provider)
	}
	if cfg.Source != SourcePreset {
		t.Errorf("Source = %q, want %q", cfg.Source, SourcePreset)
	}
	if cfg.AuthMethod != AuthMethodXOAUTH2 {
		t.Errorf("AuthMethod = %q, want %q", cfg.AuthMethod, AuthMethodXOAUTH2)
	}
	if cfg.IMAP.Host != "imap.gmail.com" || cfg.IMAP.Port != 993 || cfg.IMAP.TLS != TLSTLS {
		t.Errorf("IMAP = %+v, want imap.gmail.com:993 tls", cfg.IMAP)
	}
	if cfg.SMTP.Host != "smtp.gmail.com" || cfg.SMTP.Port != 465 || cfg.SMTP.TLS != TLSTLS {
		t.Errorf("SMTP = %+v, want smtp.gmail.com:465 tls", cfg.SMTP)
	}
	if cfg.IMAP.Username != "user@gmail.com" || cfg.SMTP.Username != "user@gmail.com" {
		t.Errorf("usernames = %q/%q, want the full address on both", cfg.IMAP.Username, cfg.SMTP.Username)
	}
	if !prober.noNetworkCalls() {
		t.Errorf("preset hit must make no network calls, got mx=%d srv=%d get=%d probe=%d",
			prober.mxCalls, prober.srvCalls, prober.getCalls, prober.probeCalls)
	}
}

func TestDiscoverPresetCaseAndTrailingDot(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		provider string
	}{
		{name: "uppercase domain", email: "someone@OUTLOOK.COM", provider: "Outlook"},
		{name: "trailing dot", email: "someone@yahoo.com.", provider: "Yahoo"},
		{name: "me.com maps to iCloud", email: "someone@me.com", provider: "iCloud"},
		{name: "googlemail.com maps to Gmail", email: "someone@googlemail.com", provider: "Gmail"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := (&Discoverer{Prober: &fakeProber{}}).Discover(context.Background(), tc.email)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if cfg.Provider != tc.provider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tc.provider)
			}
			if cfg.Source != SourcePreset {
				t.Errorf("Source = %q, want %q", cfg.Source, SourcePreset)
			}
		})
	}
}

func TestDiscoverMXPreset(t *testing.T) {
	tests := []struct {
		name       string
		mxHosts    []string
		provider   string
		authMethod string
	}{
		{
			name:       "Google Workspace routes through google.com MX",
			mxHosts:    []string{"smtp.google.com.", "alt.example.co."},
			provider:   "Gmail",
			authMethod: AuthMethodXOAUTH2,
		},
		{
			name:       "legacy Google MX hosts are recognised",
			mxHosts:    []string{"aspmx.l.google.com."},
			provider:   "Gmail",
			authMethod: AuthMethodXOAUTH2,
		},
		{
			name:       "Microsoft 365 routes through protection.outlook.com MX",
			mxHosts:    []string{"example-co.mail.protection.outlook.com."},
			provider:   "Outlook",
			authMethod: AuthMethodXOAUTH2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prober := &fakeProber{mxHosts: map[string][]string{"example.co": tc.mxHosts}}
			d := &Discoverer{Prober: prober}

			cfg, err := d.Discover(context.Background(), "team@example.co")
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if cfg.Source != SourceMXPreset {
				t.Errorf("Source = %q, want %q", cfg.Source, SourceMXPreset)
			}
			if cfg.Provider != tc.provider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tc.provider)
			}
			if cfg.AuthMethod != tc.authMethod {
				t.Errorf("AuthMethod = %q, want %q", cfg.AuthMethod, tc.authMethod)
			}
			if cfg.IMAP.Username != "team@example.co" {
				t.Errorf("IMAP username = %q, want team@example.co", cfg.IMAP.Username)
			}
		})
	}
}

func TestDiscoverSRVRecords(t *testing.T) {
	prober := &fakeProber{
		srvRecords: map[string][]*net.SRV{
			"imaps/tcp/example.net":       srvFor("mail.example.net.", 2200),
			"submissions/tcp/example.net": srvFor("smtp.example.net.", 2465),
		},
	}
	d := &Discoverer{Prober: prober}

	cfg, err := d.Discover(context.Background(), "anyone@example.net")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.Source != SourceSRV {
		t.Errorf("Source = %q, want %q", cfg.Source, SourceSRV)
	}
	if cfg.IMAP.Host != "mail.example.net" || cfg.IMAP.Port != 2200 || cfg.IMAP.TLS != TLSTLS {
		t.Errorf("IMAP = %+v, want mail.example.net:2200 tls", cfg.IMAP)
	}
	if cfg.SMTP.Host != "smtp.example.net" || cfg.SMTP.Port != 2465 || cfg.SMTP.TLS != TLSTLS {
		t.Errorf("SMTP = %+v, want smtp.example.net:2465 tls", cfg.SMTP)
	}
	if cfg.AuthMethod != AuthMethodPassword {
		t.Errorf("AuthMethod = %q, want %q", cfg.AuthMethod, AuthMethodPassword)
	}
}

func TestDiscoverSRVResultWinsOverGuess(t *testing.T) {
	prober := &fakeProber{
		srvRecords: map[string][]*net.SRV{
			"imaps/tcp/example.net": srvFor("srv-mail.example.net.", 993),
		},
		// A guess target that would also succeed must never be consulted.
		probeOK: map[string]bool{"imap.example.net:993": true},
	}
	d := &Discoverer{Prober: prober}

	cfg, err := d.Discover(context.Background(), "anyone@example.net")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.Source != SourceSRV {
		t.Errorf("Source = %q, want %q", cfg.Source, SourceSRV)
	}
	if cfg.IMAP.Host != "srv-mail.example.net" {
		t.Errorf("IMAP host = %q, want srv-mail.example.net", cfg.IMAP.Host)
	}
	if prober.probeCalls != 0 {
		t.Errorf("probe calls = %d, want 0 (SRV results are trusted without a probe)", prober.probeCalls)
	}
}

func TestDiscoverSRVSMTPFallback(t *testing.T) {
	prober := &fakeProber{
		srvRecords: map[string][]*net.SRV{
			"imaps/tcp/example.net": srvFor("mail.example.net.", 993),
		},
	}
	d := &Discoverer{Prober: prober}

	cfg, err := d.Discover(context.Background(), "anyone@example.net")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.SMTP.Host != "smtp.example.net" || cfg.SMTP.Port != 587 || cfg.SMTP.TLS != TLSStartTLS {
		t.Errorf("SMTP = %+v, want the smtp.example.net:587 starttls default", cfg.SMTP)
	}
}

func TestDiscoverSRVDotTargetIsNoService(t *testing.T) {
	prober := &fakeProber{
		srvRecords: map[string][]*net.SRV{
			"imaps/tcp/example.net": srvFor(".", 993),
		},
		probeOK: map[string]bool{"imap.example.net:993": true},
	}
	d := &Discoverer{Prober: prober}

	cfg, err := d.Discover(context.Background(), "anyone@example.net")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// A "." target explicitly publishes no service, so the flow must fall
	// through to the guess stage instead of trusting the empty record.
	if cfg.Source != SourceGuess {
		t.Errorf("Source = %q, want %q", cfg.Source, SourceGuess)
	}
}

func TestDiscoverISPDB(t *testing.T) {
	prober := &fakeProber{
		httpBodies: map[string][]byte{
			ispdbURL + "example.org": []byte(ispdbExampleOrg),
		},
	}
	d := &Discoverer{Prober: prober}

	cfg, err := d.Discover(context.Background(), "jane@example.org")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.Source != SourceISPDB {
		t.Errorf("Source = %q, want %q", cfg.Source, SourceISPDB)
	}
	if cfg.IMAP.Host != "imap.example.org" {
		t.Errorf("IMAP host = %q, want imap.example.org (the imap entry, not pop3)", cfg.IMAP.Host)
	}
	if cfg.IMAP.Port != 993 || cfg.IMAP.TLS != TLSTLS {
		t.Errorf("IMAP = %+v, want port 993 implicit TLS", cfg.IMAP)
	}
	if cfg.IMAP.Username != "jane@example.org" {
		t.Errorf("IMAP username = %q, want the %%EMAILADDRESS%% template resolved", cfg.IMAP.Username)
	}
	if cfg.SMTP.Host != "smtp.example.org" || cfg.SMTP.Port != 587 || cfg.SMTP.TLS != TLSStartTLS {
		t.Errorf("SMTP = %+v, want smtp.example.org:587 starttls", cfg.SMTP)
	}
	if cfg.SMTP.Username != "jane" {
		t.Errorf("SMTP username = %q, want the %%EMAILLOCALPART%% template resolved", cfg.SMTP.Username)
	}
	if cfg.AuthMethod != AuthMethodPassword {
		t.Errorf("AuthMethod = %q, want %q", cfg.AuthMethod, AuthMethodPassword)
	}
}

func TestDiscoverGuessVerifiedByProbe(t *testing.T) {
	prober := &fakeProber{
		probeOK: map[string]bool{"imap.example.org:993": true},
	}
	d := &Discoverer{Prober: prober}

	cfg, err := d.Discover(context.Background(), "anyone@example.org")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.Source != SourceGuess {
		t.Errorf("Source = %q, want %q", cfg.Source, SourceGuess)
	}
	if cfg.IMAP.Host != "imap.example.org" || cfg.IMAP.Port != 993 || cfg.IMAP.TLS != TLSTLS {
		t.Errorf("IMAP = %+v, want imap.example.org:993 tls", cfg.IMAP)
	}
	if cfg.SMTP.Host != "smtp.example.org" || cfg.SMTP.Port != 587 {
		t.Errorf("SMTP = %+v, want smtp.example.org:587", cfg.SMTP)
	}
	if prober.probeCalls != 1 {
		t.Errorf("probe calls = %d, want 1 (first guess already verified)", prober.probeCalls)
	}
}

func TestDiscoverGuessProbeFailureFallsThrough(t *testing.T) {
	prober := &fakeProber{
		// Only the second hostname pattern answers the probe.
		probeOK: map[string]bool{"mail.example.org:993": true},
	}
	d := &Discoverer{Prober: prober}

	cfg, err := d.Discover(context.Background(), "anyone@example.org")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.Source != SourceGuess || cfg.IMAP.Host != "mail.example.org" {
		t.Fatalf("got Source=%q host=%q, want the verified mail.example.org guess",
			cfg.Source, cfg.IMAP.Host)
	}
	if prober.probeCalls != 2 {
		t.Errorf("probe calls = %d, want 2 (failed guess falls through to the next)", prober.probeCalls)
	}
}

func TestDiscoverTotalFailure(t *testing.T) {
	d := &Discoverer{Prober: &fakeProber{}}
	_, err := d.Discover(context.Background(), "anyone@unreachable.invalid")
	if !errors.Is(err, ErrNotDiscovered) {
		t.Fatalf("Discover: err = %v, want ErrNotDiscovered", err)
	}
}

func TestDiscoverPrecedenceOrder(t *testing.T) {
	// Every stage would succeed; the earliest one must always win.
	prober := &fakeProber{
		mxHosts:    map[string][]string{"example.co": {"smtp.google.com."}},
		srvRecords: map[string][]*net.SRV{"imaps/tcp/example.co": srvFor("srv.example.co.", 993)},
		httpBodies: map[string][]byte{ispdbURL + "example.co": []byte(ispdbExampleOrg)},
		probeOK:    map[string]bool{"imap.example.co:993": true},
	}
	d := &Discoverer{Prober: prober}

	tests := []struct {
		name      string
		email     string
		want      Source
		wantCalls func(fakeProber) bool
	}{
		{
			name:  "preset beats mx, srv, ispdb, guess",
			email: "someone@gmail.com",
			want:  SourcePreset,
		},
		{
			name:  "mx-preset beats srv, ispdb, guess",
			email: "someone@example.co",
			want:  SourceMXPreset,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset the counters so each case starts from a clean slate.
			prober.mxCalls, prober.srvCalls, prober.getCalls, prober.probeCalls = 0, 0, 0, 0
			cfg, err := d.Discover(context.Background(), tc.email)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if cfg.Source != tc.want {
				t.Errorf("Source = %q, want %q", cfg.Source, tc.want)
			}
		})
	}
	if prober.getCalls != 0 || prober.probeCalls != 0 {
		t.Errorf("later stages must not run after an early hit, got get=%d probe=%d",
			prober.getCalls, prober.probeCalls)
	}
	// The MX stage ran for example.co; SRV must not have run after it won.
	if prober.srvCalls != 0 {
		t.Errorf("SRV calls = %d, want 0 (mx-preset hit must stop the flow)", prober.srvCalls)
	}
}

func TestDiscoverInvalidEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
	}{
		{name: "no at sign", email: "not-an-email"},
		{name: "empty local part", email: "@example.org"},
		{name: "empty domain", email: "user@"},
		{name: "second at sign", email: "user@ex@ample.org"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&Discoverer{Prober: &fakeProber{}}).Discover(context.Background(), tc.email)
			if err == nil {
				t.Fatal("Discover: want error for invalid address")
			}
			if errors.Is(err, ErrNotDiscovered) {
				t.Fatalf("Discover: invalid address must not surface as ErrNotDiscovered, got %v", err)
			}
		})
	}
}

func TestDiscoverNoProberConfigured(t *testing.T) {
	if _, err := (&Discoverer{}).Discover(context.Background(), "user@example.org"); err == nil {
		t.Fatal("Discover with nil Prober: want error")
	}
}

func TestDiscoverStageTimeout(t *testing.T) {
	prober := &fakeProber{}
	d := &Discoverer{Prober: prober, Timeout: 25 * time.Millisecond}
	// The fake never blocks; this just asserts a zero/short timeout still
	// drives the flow to the documented ErrNotDiscovered result.
	_, err := d.Discover(context.Background(), "anyone@timeout.invalid")
	if !errors.Is(err, ErrNotDiscovered) {
		t.Fatalf("Discover: err = %v, want ErrNotDiscovered", err)
	}
}
