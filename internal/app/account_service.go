package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/emersion/go-sasl"
	"golang.org/x/oauth2"

	"github.com/mefiz0/posthaste/internal/auth"
	"github.com/mefiz0/posthaste/internal/imapx"
)

// appPasswordHelp maps providers that commonly need app-specific passwords to
// their setup documentation, so the setup form can link the reason a normal
// password may fail.
var appPasswordHelp = map[string]string{
	"Yahoo":   "https://help.yahoo.com/kb/generate-app-password-slm15221.html",
	"iCloud":  "https://support.apple.com/102654",
	"Outlook": "https://support.microsoft.com/app-passwords",
}

// oauthFlow is one in-flight OAuth2 consent: the loopback listener lives
// until the redirect lands, the user gives up, or the app shuts down. The
// outcome is pushed to the UI as an event rather than held open on a binding
// call, so a consent that takes minutes ties up nothing.
type oauthFlow struct {
	email string
	// cancel stops the loopback listener, ending the wait goroutine.
	cancel func()
	// canceled records that the cancellation came from the user's cancel
	// button rather than a wedged listener, for a friendlier message.
	canceled bool
}

// AccountService is the bound account-management surface: listing, setup
// (discovery, verification, OAuth), and lifecycle (pause, update, removal).
type AccountService struct {
	manager *Manager
}

// NewAccountService returns the account service bound to the manager.
func NewAccountService(manager *Manager) *AccountService {
	return &AccountService{manager: manager}
}

// ListAccounts returns every registered account, paused ones included.
func (s *AccountService) ListAccounts(ctx context.Context) []AccountInfo {
	entries := s.manager.Accounts()
	out := make([]AccountInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, accountInfoFor(e))
	}
	return out
}

// Discover resolves an email address to concrete server settings. A nil
// result with no error means nothing was discovered and the setup flow falls
// back to the manual form.
func (s *AccountService) Discover(ctx context.Context, email string) (*DiscoveredConfigInfo, error) {
	cfg, err := auth.NewSystemDiscoverer().Discover(ctx, email)
	if err != nil {
		// Every discovery stage failed (or the address is malformed): the
		// documented response is the pre-filled manual form, not an error.
		return nil, nil
	}
	info := &DiscoveredConfigInfo{
		Email:         email,
		ProviderName:  cfg.Provider,
		RequiresOAuth: cfg.AuthMethod != auth.AuthMethodPassword,
		IMAP:          serverConfigInfo(cfg.IMAP),
		SMTP:          serverConfigInfo(cfg.SMTP),
	}
	if help, ok := appPasswordHelp[cfg.Provider]; ok && !info.RequiresOAuth {
		info.AppPasswordURL = help
	}
	return info, nil
}

// serverConfigInfo maps an engine server config onto the bridge shape.
func serverConfigInfo(cfg auth.ConfigServer) ServerConfigInfo {
	return ServerConfigInfo{
		Host:     cfg.Host,
		Port:     cfg.Port,
		Security: cfg.TLS,
		Username: cfg.Username,
	}
}

// VerifyCredentials performs the live verification the setup flow requires
// before an account may be saved: one real IMAP login plus a folder listing.
func (s *AccountService) VerifyCredentials(ctx context.Context, input ManualAccountInput) error {
	if input.Auth == "oauth" {
		// OAuth credentials cannot be verified before the consent flow
		// completes; the completed flow verifies implicitly.
		return nil
	}
	verify := s.manager.deps.VerifyFunc
	if verify == nil {
		verify = verifyIMAPCredentials
	}
	return verify(ctx, input)
}

// saslMechanismAdapter adapts a ready SASL client to the authenticator the
// IMAP layer expects.
type saslMechanismAdapter struct{ client sasl.Client }

// Mechanism returns the underlying SASL client.
func (a saslMechanismAdapter) Mechanism() sasl.Client { return a.client }

// verifyIMAPCredentials dials the input's IMAP server, authenticates, lists
// folders once, and closes the connection. Failures are reported verbatim
// minus the secret; hostnames are account metadata and safe to surface.
func verifyIMAPCredentials(ctx context.Context, input ManualAccountInput) error {
	if input.Auth != "oauth" && input.Password == "" {
		return errors.New("app: enter the account password to verify")
	}
	var authHandler imapx.SASLAuth
	switch input.Auth {
	case "oauth":
		authHandler = saslMechanismAdapter{client: auth.NewXOAUTH2Client(input.IMAP.Username, input.Password)}
	default:
		authHandler = imapx.PasswordAuth(input.IMAP.Username, input.Password)
	}

	client, err := imapx.Dial(ctx, imapx.Config{
		Host:     input.IMAP.Host,
		Port:     input.IMAP.Port,
		TLS:      imapx.TLSMode(input.IMAP.Security),
		Username: input.IMAP.Username,
		Auth:     authHandler,
	})
	if err != nil {
		return err
	}
	if _, err := client.List(ctx); err != nil {
		_ = client.Close(context.WithoutCancel(ctx))
		return fmt.Errorf("app: folder listing failed: %w", err)
	}
	return client.Close(context.WithoutCancel(ctx))
}

// StartOAuth opens the provider consent screen in the default browser and
// registers a pending flow. The flow's outcome arrives as an oauth-complete
// event carrying the same state ID; CancelOAuth abandons a pending flow.
func (s *AccountService) StartOAuth(ctx context.Context, email string) (OAuthStart, error) {
	provider, ok := oauthProviderForEmail(email)
	if !ok {
		return OAuthStart{}, errors.New("app: no OAuth provider for this address; use manual setup")
	}
	cfg, err := oauthConfigForProvider(provider)
	if err != nil {
		return OAuthStart{}, err
	}

	authURL, wait, cancel, err := auth.StartFlow(ctx, cfg)
	if err != nil {
		return OAuthStart{}, err
	}

	stateID, err := newFlowID()
	if err != nil {
		cancel()
		return OAuthStart{}, err
	}
	flow := &oauthFlow{
		email:  email,
		cancel: cancel,
	}

	s.manager.mu.Lock()
	s.manager.oauthFlows[stateID] = flow
	s.manager.mu.Unlock()

	go func() {
		var result OAuthResultInfo
		token, waitErr := wait(context.Background())
		switch {
		case waitErr == nil:
			encoded, marshalErr := marshalToken(token)
			if marshalErr != nil {
				result = OAuthResultInfo{Success: false, Error: marshalErr.Error()}
			} else {
				s.manager.mu.Lock()
				s.manager.pendingOAuth[email] = encoded
				s.manager.mu.Unlock()
				result = OAuthResultInfo{Success: true}
			}
		case s.oauthFlowCanceled(stateID):
			result = OAuthResultInfo{Success: false, Error: "the sign-in was canceled"}
		default:
			result = OAuthResultInfo{Success: false, Error: waitErr.Error()}
		}
		flow.cancel()

		s.manager.mu.Lock()
		delete(s.manager.oauthFlows, stateID)
		s.manager.mu.Unlock()

		s.manager.Emit(EventOAuthComplete, OAuthCompleteEvent{
			Type:    EventOAuthComplete,
			StateID: stateID,
			OK:      result.Success,
			Error:   result.Error,
		})
	}()

	// Hand the consent screen to the browser directly so the flow proceeds
	// without depending on the frontend's timing.
	if opener := s.manager.deps.BrowserOpenFunc; opener != nil {
		if err := opener(authURL); err != nil {
			s.manager.logger.Error("app: open OAuth consent URL", "err", err)
		}
	}
	return OAuthStart{AuthURL: authURL, StateID: stateID}, nil
}

// CancelOAuth abandons the pending flow with the given state ID, stopping its
// loopback listener. A flow that already resolved is not an error: the cancel
// button may legitimately race the redirect.
func (s *AccountService) CancelOAuth(_ context.Context, stateID string) error {
	s.manager.mu.Lock()
	flow, ok := s.manager.oauthFlows[stateID]
	if ok {
		flow.canceled = true
	}
	s.manager.mu.Unlock()
	if !ok {
		return nil
	}
	flow.cancel()
	return nil
}

// oauthFlowCanceled reports whether the flow was abandoned through
// CancelOAuth. The flag is advisory: it only picks the failure wording.
func (s *AccountService) oauthFlowCanceled(stateID string) bool {
	s.manager.mu.Lock()
	flow, ok := s.manager.oauthFlows[stateID]
	s.manager.mu.Unlock()
	return ok && flow.canceled
}

// AddAccount adds a fully specified account, password or OAuth. For OAuth the
// token staged by a completed flow is what lands in the keyring.
func (s *AccountService) AddAccount(ctx context.Context, input ManualAccountInput) (AccountInfo, error) {
	credential := input.Password
	if input.Auth == "oauth" {
		s.manager.mu.Lock()
		staged, ok := s.manager.pendingOAuth[input.Email]
		if ok {
			delete(s.manager.pendingOAuth, input.Email)
		}
		s.manager.mu.Unlock()
		if !ok {
			return AccountInfo{}, errors.New("app: complete the provider authorization first")
		}
		credential = staged
	}
	return s.manager.addAccount(ctx, input, credential, input.Auth == "oauth")
}

// AddAccountOAuth registers an OAuth account from raw token JSON, for callers
// that completed a flow outside the staged pending map.
func (s *AccountService) AddAccountOAuth(ctx context.Context, email, provider, tokenJSON string) (AccountInfo, error) {
	return s.manager.AddAccountOAuth(ctx, email, provider, tokenJSON)
}

// RemoveAccount permanently removes an account: workers stop, the credential
// is deleted from the keyring, and the local database files are removed.
func (s *AccountService) RemoveAccount(ctx context.Context, id int64) error {
	return s.manager.RemoveAccount(id)
}

// SetAccountPaused pauses or resumes an account's background workers.
func (s *AccountService) SetAccountPaused(ctx context.Context, id int64, paused bool) error {
	return s.manager.SetPaused(id, paused)
}

// UpdateAccount applies changed per-account preferences.
func (s *AccountService) UpdateAccount(ctx context.Context, id int64, prefs AccountPrefs) error {
	return s.manager.UpdateAccount(id, prefs)
}

// oauthConfigForProvider builds the OAuth2 configuration for a named
// provider, reading its client ID from the environment.
func oauthConfigForProvider(provider string) (auth.OAuthConfig, error) {
	return auth.NewOAuthConfig(auth.Provider(provider), os.Getenv)
}

// oauthProviderByName validates a provider name and returns its fixed config.
func oauthProviderByName(name string) (auth.ProviderConfig, error) {
	return auth.ProviderConfigFor(auth.Provider(name))
}

// newFlowID draws a random identifier for a pending OAuth flow.
func newFlowID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("app: generate flow id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// marshalToken renders an OAuth token as the JSON stored in the keyring.
func marshalToken(token *oauth2.Token) (string, error) {
	encoded, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("app: encode OAuth token: %w", err)
	}
	return string(encoded), nil
}
