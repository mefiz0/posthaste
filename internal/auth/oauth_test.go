package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newTokenServer stands in for a provider token endpoint, asserting the form
// fields of every request before answering with response.
func newTokenServer(t *testing.T, checks func(form url.Values), response string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token endpoint: parse form: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		checks(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server
}

func testOAuthConfig(t *testing.T, provider Provider, tokenURL string) OAuthConfig {
	t.Helper()
	pc, err := ProviderConfigFor(provider)
	if err != nil {
		t.Fatalf("ProviderConfigFor(%q): %v", provider, err)
	}
	pc.TokenURL = tokenURL
	pc.AuthURL = "https://accounts.example.test/authorize"
	return OAuthConfig{ProviderConfig: pc, ClientID: "test-client-id"}
}

func TestNewOAuthConfig(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		envVar   string
	}{
		{name: "gmail client id comes from its env var", provider: ProviderGmail, envVar: GmailClientIDEnvVar},
		{name: "m365 client id comes from its env var", provider: ProviderM365, envVar: M365ClientIDEnvVar},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == tc.envVar {
					return "client-id-123"
				}
				return ""
			}
			cfg, err := NewOAuthConfig(tc.provider, getenv)
			if err != nil {
				t.Fatalf("NewOAuthConfig: %v", err)
			}
			if cfg.ClientID != "client-id-123" {
				t.Fatalf("ClientID = %q, want %q", cfg.ClientID, "client-id-123")
			}
			if cfg.AuthURL == "" || cfg.TokenURL == "" {
				t.Fatalf("endpoints not populated: auth=%q token=%q", cfg.AuthURL, cfg.TokenURL)
			}
			if len(cfg.Scopes) == 0 {
				t.Fatal("scopes not populated")
			}
		})
	}

	t.Run("missing client id fails naming the env var", func(t *testing.T) {
		_, err := NewOAuthConfig(ProviderGmail, func(string) string { return "" })
		if err == nil {
			t.Fatal("NewOAuthConfig with unset env var: want error")
		}
		if !strings.Contains(err.Error(), GmailClientIDEnvVar) {
			t.Fatalf("error %q must name the missing variable %s", err, GmailClientIDEnvVar)
		}
	})

	t.Run("unknown provider is rejected", func(t *testing.T) {
		_, err := NewOAuthConfig(Provider("nope"), func(string) string { return "x" })
		if err == nil {
			t.Fatal("NewOAuthConfig with unknown provider: want error")
		}
	})
}

func TestStartFlowAuthURL(t *testing.T) {
	tokenServer := newTokenServer(t, func(url.Values) {}, `{}`)
	cfg := testOAuthConfig(t, ProviderGmail, tokenServer.URL)

	authURL, _, cancel, err := StartFlow(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer cancel()

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL %q: %v", authURL, err)
	}
	q := u.Query()
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want code", got)
	}
	if got := q.Get("client_id"); got != "test-client-id" {
		t.Errorf("client_id = %q, want test-client-id", got)
	}
	if q.Get("state") == "" {
		t.Error("state must be a non-empty random value")
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	if q.Get("code_challenge") == "" {
		t.Error("code_challenge must be set for PKCE")
	}
	scopes := strings.Split(q.Get("scope"), " ")
	if !sliceHas(scopes, "https://mail.google.com/") {
		t.Errorf("scope %v must contain the Gmail mail scope", scopes)
	}

	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect_uri %q: %v", q.Get("redirect_uri"), err)
	}
	if redirect.Hostname() != "127.0.0.1" {
		t.Errorf("redirect host = %q, want 127.0.0.1", redirect.Hostname())
	}
	if redirect.Path != "/callback" {
		t.Errorf("redirect path = %q, want /callback", redirect.Path)
	}
}

func sliceHas(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// loopbackPort extracts the callback port from an auth URL's redirect_uri.
func loopbackPort(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	redirect, err := url.Parse(u.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect_uri: %v", err)
	}
	return redirect.Port()
}

func TestStartFlowSuccessfulExchange(t *testing.T) {
	var expectedRedirect string
	tokenServer := newTokenServer(t, func(form url.Values) {
		if got := form.Get("grant_type"); got != "authorization_code" {
			t.Errorf("grant_type = %q, want authorization_code", got)
		}
		if got := form.Get("code"); got != "the-code" {
			t.Errorf("code = %q, want the-code", got)
		}
		if form.Get("code_verifier") == "" {
			t.Error("code_verifier must accompany the S256 challenge")
		}
		if got := form.Get("client_id"); got != "test-client-id" {
			t.Errorf("client_id = %q, want test-client-id", got)
		}
		if got := form.Get("redirect_uri"); got != expectedRedirect {
			t.Errorf("redirect_uri = %q, want %q", got, expectedRedirect)
		}
	}, `{"access_token":"at-1","token_type":"Bearer","refresh_token":"rt-1","expires_in":3600}`)
	cfg := testOAuthConfig(t, ProviderGmail, tokenServer.URL)

	authURL, wait, cancel, err := StartFlow(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer cancel()

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	expectedRedirect = u.Query().Get("redirect_uri")
	state := u.Query().Get("state")
	callback := fmt.Sprintf("http://127.0.0.1:%s/callback?code=the-code&state=%s",
		loopbackPort(t, authURL), url.QueryEscape(state))

	resp, err := http.Get(callback)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read callback body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "close this window") {
		t.Fatalf("callback page %q must tell the user to close the window", body)
	}

	token, err := wait(context.Background())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if token.AccessToken != "at-1" {
		t.Errorf("token.AccessToken = %q, want at-1", token.AccessToken)
	}
	if token.RefreshToken != "rt-1" {
		t.Errorf("token.RefreshToken = %q, want rt-1", token.RefreshToken)
	}
}

func TestStartFlowStateMismatchRejected(t *testing.T) {
	tokenServer := newTokenServer(t, func(form url.Values) {
		t.Error("token endpoint must not be reached after a state mismatch")
	}, `{}`)
	cfg := testOAuthConfig(t, ProviderGmail, tokenServer.URL)

	authURL, wait, cancel, err := StartFlow(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer cancel()

	callback := fmt.Sprintf("http://127.0.0.1:%s/callback?code=evil&state=forged", loopbackPort(t, authURL))
	resp, err := http.Get(callback)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400 on state mismatch", resp.StatusCode)
	}

	_, err = wait(context.Background())
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("wait: err = %v, want ErrStateMismatch", err)
	}
}

func TestStartFlowProviderDenied(t *testing.T) {
	tokenServer := newTokenServer(t, func(url.Values) {}, `{}`)
	cfg := testOAuthConfig(t, ProviderGmail, tokenServer.URL)

	authURL, wait, cancel, err := StartFlow(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer cancel()

	state, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	callback := fmt.Sprintf("http://127.0.0.1:%s/callback?error=access_denied&state=%s",
		loopbackPort(t, authURL), url.QueryEscape(state.Query().Get("state")))
	resp, err := http.Get(callback)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_ = resp.Body.Close()

	_, err = wait(context.Background())
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("wait: err = %v, want the provider's denial reason", err)
	}
}

func TestStartFlowCancelShutsDownListener(t *testing.T) {
	tokenServer := newTokenServer(t, func(url.Values) {}, `{}`)
	cfg := testOAuthConfig(t, ProviderGmail, tokenServer.URL)

	authURL, wait, cancel, err := StartFlow(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	port := loopbackPort(t, authURL)
	cancel() // must return promptly and release everything

	// The listener is closed, so dialing it must now fail rather than hang.
	if resp, err := http.Get("http://127.0.0.1:" + port + "/callback"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("GET on cancelled flow's port: want connection error")
	}
	if _, err := wait(context.Background()); err == nil {
		t.Fatal("wait after cancel: want error")
	}
}

func TestStartFlowWaitHonoursContext(t *testing.T) {
	tokenServer := newTokenServer(t, func(url.Values) {}, `{}`)
	cfg := testOAuthConfig(t, ProviderGmail, tokenServer.URL)

	_, wait, cancel, err := StartFlow(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer cancel()

	ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if _, err := wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait: err = %v, want context.DeadlineExceeded", err)
	}
}

func TestStartFlowRequiresClientID(t *testing.T) {
	cfg := testOAuthConfig(t, ProviderGmail, "https://tokens.example.test")
	cfg.ClientID = ""
	if _, _, _, err := StartFlow(context.Background(), cfg); err == nil {
		t.Fatal("StartFlow with empty client ID: want error")
	}
}

func TestRefreshToken(t *testing.T) {
	var calls int
	tokenServer := newTokenServer(t, func(form url.Values) {
		calls++
		if got := form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := form.Get("refresh_token"); got != "stored-refresh-token" {
			t.Errorf("refresh_token = %q, want stored-refresh-token", got)
		}
		if got := form.Get("client_id"); got != "test-client-id" {
			t.Errorf("client_id = %q, want test-client-id", got)
		}
	}, `{"access_token":"at-2","token_type":"Bearer","refresh_token":"rotated-rt","expires_in":3600}`)
	cfg := testOAuthConfig(t, ProviderM365, tokenServer.URL)

	token, err := RefreshToken(context.Background(), cfg, "stored-refresh-token")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if token.AccessToken != "at-2" {
		t.Errorf("token.AccessToken = %q, want at-2", token.AccessToken)
	}
	if calls != 1 {
		t.Errorf("token endpoint calls = %d, want 1", calls)
	}

	if _, err := RefreshToken(context.Background(), cfg, ""); err == nil {
		t.Fatal("RefreshToken with empty refresh token: want error")
	}
}

func TestTokenSourceCachesAndRefreshes(t *testing.T) {
	var calls int
	tokenServer := newTokenServer(t, func(form url.Values) { calls++ },
		`{"access_token":"at-cached","token_type":"Bearer","expires_in":3600}`)
	cfg := testOAuthConfig(t, ProviderGmail, tokenServer.URL)

	source, err := TokenSource(context.Background(), cfg, "stored-refresh-token")
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	first, err := source.Token()
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	second, err := source.Token()
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if calls != 1 {
		t.Errorf("token endpoint calls = %d, want 1 (second Token must be served from cache)", calls)
	}
	if first.AccessToken != second.AccessToken {
		t.Errorf("cached token changed: %q then %q", first.AccessToken, second.AccessToken)
	}
}

func TestTokenSourceRefreshesAfterExpiry(t *testing.T) {
	var calls int
	tokenServer := newTokenServer(t, func(form url.Values) { calls++ },
		`{"access_token":"at-expired","token_type":"Bearer","expires_in":1}`)
	cfg := testOAuthConfig(t, ProviderGmail, tokenServer.URL)

	source, err := TokenSource(context.Background(), cfg, "stored-refresh-token")
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	// expires_in of one second is inside the library's ten-second early-expiry
	// margin, so every call must silently re-refresh rather than hand out a
	// token that is about to die; no sleeping needed to observe it.
	if _, err := source.Token(); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if _, err := source.Token(); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if calls != 2 {
		t.Errorf("token endpoint calls = %d, want 2 (expired token must refresh silently)", calls)
	}
}

func TestTokenSourceEmptyRefreshToken(t *testing.T) {
	cfg := testOAuthConfig(t, ProviderGmail, "https://tokens.example.test")
	if _, err := TokenSource(context.Background(), cfg, ""); err == nil {
		t.Fatal("TokenSource with empty refresh token: want error")
	}
}
