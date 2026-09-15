package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/oauth2"
)

// Provider names a supported OAuth2 identity provider.
type Provider string

// Supported OAuth2 providers. Both deprecated plain-password IMAP login, so
// OAuth2 support is what makes them reachable at all.
const (
	ProviderGmail Provider = "gmail"
	ProviderM365  Provider = "microsoft365"
)

// Environment variables that must supply OAuth client IDs. Posthaste ships no
// secrets, so a build or launch environment has to provide them; the setup
// flow fails with an error naming the missing variable rather than silently
// degrading to a flow that can never succeed.
const (
	GmailClientIDEnvVar = "POSTHASTE_GMAIL_CLIENT_ID"
	M365ClientIDEnvVar  = "POSTHASTE_M365_CLIENT_ID"
)

// ErrStateMismatch is returned when the loopback redirect carries a state
// that does not match the one sent with the authorization URL. That can be a
// forged callback or cross-flow interference, so the whole flow fails closed.
var ErrStateMismatch = errors.New("auth: OAuth2 redirect state mismatch")

// ProviderConfig is the fixed OAuth2 shape of one provider: endpoints, mail
// scopes, and how the token endpoint expects client credentials.
type ProviderConfig struct {
	Provider Provider
	AuthURL  string
	TokenURL string
	Scopes   []string
	// AuthStyle pins the token-request credential placement instead of
	// letting the client probe, which saves a round trip and keeps requests
	// predictable for providers behind CDNs.
	AuthStyle oauth2.AuthStyle
	// ClientIDEnvVar names the environment variable holding the client ID.
	ClientIDEnvVar string
}

// ProviderConfigFor returns the endpoints and scopes for a known provider.
func ProviderConfigFor(provider Provider) (ProviderConfig, error) {
	switch provider {
	case ProviderGmail:
		return ProviderConfig{
			Provider: ProviderGmail,
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
			// The full-mail scope covers IMAP and SMTP alike; Gmail has no
			// narrower scope for either protocol.
			Scopes:         []string{"https://mail.google.com/"},
			AuthStyle:      oauth2.AuthStyleInParams,
			ClientIDEnvVar: GmailClientIDEnvVar,
		}, nil
	case ProviderM365:
		return ProviderConfig{
			Provider: ProviderM365,
			AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			// offline_access is what yields the refresh token the silent
			// refresh in sync workers depends on; without it every token
			// expiry would need a fresh browser prompt.
			Scopes: []string{
				"offline_access",
				"https://outlook.office.com/IMAP.AccessAsUser.All",
				"https://outlook.office.com/SMTP.Send",
				"User.Read",
			},
			AuthStyle:      oauth2.AuthStyleInParams,
			ClientIDEnvVar: M365ClientIDEnvVar,
		}, nil
	default:
		return ProviderConfig{}, fmt.Errorf("auth: unknown OAuth2 provider %q", string(provider))
	}
}

// OAuthConfig is everything one OAuth2 flow needs: the provider's fixed
// configuration, the client ID, and the port the loopback redirect listener
// should use.
type OAuthConfig struct {
	ProviderConfig
	// ClientID is the OAuth2 public client ID for this build.
	ClientID string
	// RedirectPort pins the loopback callback port. Zero lets StartFlow pick
	// a free ephemeral port; the loopback redirect spec for native apps
	// allows any port on 127.0.0.1.
	RedirectPort int
}

// NewOAuthConfig builds an OAuthConfig for provider, reading the client ID
// from the environment. A nil getenv means the process environment. The error
// names the missing variable so the setup UI can explain exactly what the
// launch environment must provide.
func NewOAuthConfig(provider Provider, getenv func(string) string) (OAuthConfig, error) {
	pc, err := ProviderConfigFor(provider)
	if err != nil {
		return OAuthConfig{}, err
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	clientID := getenv(pc.ClientIDEnvVar)
	if clientID == "" {
		return OAuthConfig{}, errors.New(
			"auth: environment variable " + pc.ClientIDEnvVar +
				" is not set; Posthaste ships no OAuth client IDs, so the launch environment must provide one")
	}
	return OAuthConfig{ProviderConfig: pc, ClientID: clientID}, nil
}

// loopbackShutdown bounds how long cancel waits for in-flight redirect
// requests before the listener is force-closed.
const loopbackShutdown = 2 * time.Second

// successPage is shown in the user's browser after consent completes.
const successPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Posthaste</title></head>
<body style="font-family: sans-serif; text-align: center; padding: 3em;">
<h1>Account authorized</h1>
<p>Posthaste is signed in. You can close this window and return to the app.</p>
</body>
</html>`

// loopbackResult carries the outcome of the browser redirect into wait.
type loopbackResult struct {
	code string
	err  error
}

// StartFlow prepares a loopback OAuth2 authorization. It binds a one-shot HTTP
// listener on 127.0.0.1, generates the random state and the PKCE S256 pair,
// and returns the authorization URL, which the caller opens in the user's
// default browser (an embedded webview is deliberately avoided so this process
// never sees the provider password). wait blocks until the provider redirects
// back with the code, exchanges it at the token endpoint, and returns the
// token; it fails early on a denied consent, a state mismatch, a dead
// listener, or an expired context. cancel releases the listener and must be
// called even after wait succeeded.
func StartFlow(ctx context.Context, cfg OAuthConfig) (authURL string, wait func(ctx context.Context) (*oauth2.Token, error), cancel func(), err error) {
	if cfg.ClientID == "" {
		return "", nil, nil, errors.New("auth: start OAuth2 flow: empty client ID")
	}
	port := ""
	if cfg.RedirectPort > 0 {
		port = strconv.Itoa(cfg.RedirectPort)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return "", nil, nil, fmt.Errorf("auth: start OAuth2 flow: bind loopback listener: %w", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		_ = listener.Close()
		return "", nil, nil, fmt.Errorf("auth: start OAuth2 flow: generate state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)
	verifier := oauth2.GenerateVerifier()

	oc := &oauth2.Config{
		ClientID:    cfg.ClientID,
		RedirectURL: fmt.Sprintf("http://127.0.0.1:%d/callback", localPort),
		Scopes:      cfg.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   cfg.AuthURL,
			TokenURL:  cfg.TokenURL,
			AuthStyle: cfg.AuthStyle,
		},
	}

	results := make(chan loopbackResult, 1)
	// send delivers exactly one result; duplicate or late callbacks are
	// dropped instead of blocking the handler goroutine.
	send := func(res loopbackResult) {
		select {
		case results <- res:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if providerErr := q.Get("error"); providerErr != "" {
			send(loopbackResult{err: fmt.Errorf("auth: provider denied the authorization: %s", providerErr)})
			http.Error(w, "Authorization failed. You can close this window and try again.", http.StatusBadRequest)
			return
		}
		if q.Get("state") != state {
			// Fail the whole flow instead of keep listening: a mismatched
			// state may be a forged callback, and the listener must not
			// absorb authorization codes it cannot attribute.
			send(loopbackResult{err: fmt.Errorf("auth: %w", ErrStateMismatch)})
			http.Error(w, "Invalid state. You can close this window and try again.", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			send(loopbackResult{err: errors.New("auth: redirect carried no authorization code")})
			http.Error(w, "Missing authorization code. You can close this window and try again.", http.StatusBadRequest)
			return
		}
		send(loopbackResult{code: code})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(successPage))
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	cancel = func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), loopbackShutdown)
		defer stop()
		// Shutdown drains in-flight requests; Close releases the listener in
		// case Shutdown already failed or the socket is otherwise wedged.
		_ = server.Shutdown(shutdownCtx)
		_ = listener.Close()
	}

	authURL = oc.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	wait = func(waitCtx context.Context) (*oauth2.Token, error) {
		var res loopbackResult
		select {
		case res = <-results:
		case serveErr := <-serveErr:
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				return nil, fmt.Errorf("auth: OAuth2 loopback server failed: %w", serveErr)
			}
			return nil, errors.New("auth: OAuth2 flow ended before the redirect arrived")
		case <-waitCtx.Done():
			return nil, fmt.Errorf("auth: OAuth2 flow canceled: %w", waitCtx.Err())
		}
		if res.err != nil {
			return nil, res.err
		}
		token, err := oc.Exchange(waitCtx, res.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("auth: exchange OAuth2 authorization code: %w", err)
		}
		return token, nil
	}
	return authURL, wait, cancel, nil
}

// RefreshToken exchanges a stored refresh token for a fresh access token at
// the provider's token endpoint. Sync and send workers call this when a cached
// access token has expired, so background reconnection never needs a user
// prompt.
func RefreshToken(ctx context.Context, cfg OAuthConfig, refreshToken string) (*oauth2.Token, error) {
	source, err := TokenSource(ctx, cfg, refreshToken)
	if err != nil {
		return nil, err
	}
	token, err := source.Token()
	if err != nil {
		return nil, fmt.Errorf("auth: refresh OAuth2 token: %w", err)
	}
	return token, nil
}

// TokenSource returns a source that serves the cached access token until just
// before it expires and then refreshes it silently with the stored refresh
// token, so IMAP and SMTP authentication always have a live token without
// re-prompting. The context flows into each refresh request.
func TokenSource(ctx context.Context, cfg OAuthConfig, refreshToken string) (oauth2.TokenSource, error) {
	if refreshToken == "" {
		return nil, errors.New("auth: token source: empty refresh token")
	}
	oc := &oauth2.Config{
		ClientID: cfg.ClientID,
		Scopes:   cfg.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   cfg.AuthURL,
			TokenURL:  cfg.TokenURL,
			AuthStyle: cfg.AuthStyle,
		},
	}
	// Config.TokenSource wraps a refresh-only source in oauth2's reuse source,
	// which caches the token and refreshes before expiry.
	return oc.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}), nil
}
