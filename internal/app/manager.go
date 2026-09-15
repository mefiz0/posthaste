package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	"golang.org/x/oauth2"

	"github.com/mefiz0/posthaste/internal/attachment"
	"github.com/mefiz0/posthaste/internal/auth"
	"github.com/mefiz0/posthaste/internal/send"
	"github.com/mefiz0/posthaste/internal/settings"
	"github.com/mefiz0/posthaste/internal/store"
	mailsync "github.com/mefiz0/posthaste/internal/sync"
)

// Sync state names as the bridge reports them.
const (
	syncStateIdle    = "idle"
	syncStateSyncing = "syncing"
	syncStateOffline = "offline"
	syncStateError   = "error"
	syncStatePaused  = "paused"
)

// defaultPollIntervalSeconds is the per-account polling cadence until the
// account row carries its own value.
const defaultPollIntervalSeconds = 300

// Emitter pushes one named event with its payload to the UI event bus. The
// Wails shell bridges this to the application event system; tests record
// events with a fake.
type Emitter interface {
	Emit(name string, data any)
}

// SettingsStore reads and writes the global settings file.
type SettingsStore interface {
	Load() (settings.Settings, error)
	Save(cfg settings.Settings) error
}

// CredentialStore stores and retrieves account secrets in the OS keyring.
// The production implementation is *auth.Keyring; tests substitute an
// in-memory fake so the suite stays hermetic.
type CredentialStore interface {
	SaveCredential(ctx context.Context, ref, secret string) error
	Credential(ctx context.Context, ref string) (string, error)
	DeleteCredential(ctx context.Context, ref string) error
}

// MailerFactory builds the SMTP transport for one account. Overridable so
// tests drive send-state transitions without a network.
type MailerFactory func(account store.Account, blobs send.BlobOpener) send.Mailer

// SyncDialFactory builds the IMAP connection factory for one account from its
// credential resolver. Overridable so tests run sync workers against a fake
// server. A nil Deps field selects the production IMAP dialer.
type SyncDialFactory func(account store.Account, authFunc func(ctx context.Context) (sasl.Client, error)) (mailsync.ServerFactory, error)

// VerifyFunc performs the live IMAP verification for the setup flow.
// Overridable so tests never touch a network. A nil Deps field selects the
// real verification.
type VerifyFunc func(ctx context.Context, input ManualAccountInput) error

// Deps wires a Manager to its environment. Everything is injected; the
// manager reaches for no globals.
type Deps struct {
	// Paths is the resolved XDG directory layout.
	Paths settings.Paths
	// Settings reads and writes the global settings file.
	Settings SettingsStore
	// Logger receives scrubbed application logging.
	Logger *slog.Logger
	// Emitter forwards engine events to the UI. Nil disables event push.
	Emitter Emitter
	// Credentials is the OS keyring front end. Required for workers.
	Credentials CredentialStore
	// Notifier posts desktop notifications. Nil disables notifications.
	Notifier Notifier
	// QuitFunc terminates the application. Nil makes Quit a no-op.
	QuitFunc func()
	// BrowserOpenFunc opens a URL in the default browser. Nil disables it.
	BrowserOpenFunc func(raw string) error
	// FilePicker opens the native multi-select file dialog and returns the
	// chosen absolute paths. The shell injects it because file dialogs belong
	// to the windowing layer; nil disables attachment picking.
	FilePicker func() ([]string, error)
	// MailerFactory overrides the SMTP transport builder (tests).
	MailerFactory MailerFactory
	// SyncDialFactory overrides the IMAP connection builder (tests).
	SyncDialFactory SyncDialFactory
	// VerifyFunc overrides live credential verification (tests).
	VerifyFunc VerifyFunc
	// Now injects the clock. Zero value uses the wall clock.
	Now func() time.Time
	// OnSettingsChanged observes saved settings so the shell can react (for
	// example by recreating the tray icon). Nil is fine.
	OnSettingsChanged func(cfg settings.Settings)
}

// nopEmitter is the Emitter used when the shell supplies none.
type nopEmitter struct{}

func (nopEmitter) Emit(string, any) {}

// Manager owns every per-account runtime: the account registry, the open
// SQLite stores, and one supervised sync plus send worker pair per account.
// It is safe for concurrent use; service methods and worker callbacks run on
// many goroutines.
type Manager struct {
	deps   Deps
	logger *slog.Logger

	mu           sync.Mutex
	registry     *Registry
	accounts     map[int64]*accountRuntime
	settings     settings.Settings
	oauthFlows   map[string]*oauthFlow
	pendingOAuth map[string]string
	started      bool
	stopped      bool

	rootCtx    context.Context
	rootCancel context.CancelFunc

	rawBlobs    *attachment.Store
	attachBlobs *attachment.Store

	activityMu sync.Mutex
	activity   []SyncActivityEntry
}

// maxSyncActivity bounds the retained sync activity log.
const maxSyncActivity = 300

// recordActivity appends one retained sync activity line, dropping the oldest
// past the cap.
func (m *Manager) recordActivity(accountID int64, text, level string) {
	m.activityMu.Lock()
	defer m.activityMu.Unlock()
	m.activity = append(m.activity, SyncActivityEntry{
		At:        time.Now().UTC().Format(time.RFC3339),
		AccountID: accountID,
		Text:      text,
		Level:     level,
	})
	if len(m.activity) > maxSyncActivity {
		m.activity = m.activity[len(m.activity)-maxSyncActivity:]
	}
}

// SyncActivity returns a copy of the retained sync activity, oldest first.
func (m *Manager) SyncActivity() []SyncActivityEntry {
	m.activityMu.Lock()
	defer m.activityMu.Unlock()
	return append([]SyncActivityEntry(nil), m.activity...)
}

// NewManager validates the dependencies, loads the account registry and the
// settings snapshot, and returns a manager. It starts nothing; call Start.
func NewManager(deps Deps) *Manager {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Emitter == nil {
		deps.Emitter = nopEmitter{}
	}
	if deps.Notifier == nil {
		deps.Notifier = noopNotifier{}
	}
	if deps.Credentials == nil {
		deps.Logger.Error("app: no credential store configured")
	}
	if deps.Settings == nil {
		deps.Logger.Error("app: no settings store configured")
	}
	m := &Manager{
		deps:         deps,
		logger:       deps.Logger,
		accounts:     make(map[int64]*accountRuntime),
		oauthFlows:   make(map[string]*oauthFlow),
		pendingOAuth: make(map[string]string),
	}
	if deps.Settings != nil {
		if cfg, err := deps.Settings.Load(); err != nil {
			m.logger.Error("app: load settings", "err", err)
		} else {
			m.settings = cfg
		}
	}
	registry, err := LoadRegistry(m.registryPath())
	if err != nil {
		// A corrupt registry is surfaced loudly: proceeding with an empty
		// list would look like every account vanished.
		m.logger.Error("app: load account registry", "err", err)
	}
	m.registry = registry
	// Old builds stored 63-bit random account IDs that JavaScript rounds on
	// the way through the bridge; move them to safe values before anything
	// reads the registry.
	m.migrateAccountIDs()
	return m
}

// registryPath is where the account registry lives inside the config layout.
func (m *Manager) registryPath() string {
	return filepath.Join(m.deps.Paths.ConfigDir, "accounts.json")
}

// Start opens a store for every registered account and starts the worker pair
// for the unpaused ones. A paused account keeps its store open so its cached
// mail stays readable, matching the in-session pause behaviour; only its
// workers are left stopped. The context becomes the manager's lifetime
// context; Shutdown cancels everything it started.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started || m.stopped {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.rootCtx, m.rootCancel = context.WithCancel(ctx)
	rawBlobs, rawErr := attachment.Open(m.deps.Paths.MessagesDir)
	attachBlobs, attachErr := attachment.Open(m.deps.Paths.AttachmentsDir)
	m.rawBlobs, m.attachBlobs = rawBlobs, attachBlobs
	entries := m.registry.List()
	m.mu.Unlock()

	if rawErr != nil || attachErr != nil {
		m.logger.Error("app: open blob stores", "raw", rawErr, "attachments", attachErr)
		m.emitToast("error", "Mail storage could not be opened")
		return
	}

	for _, entry := range entries {
		if err := m.startAccount(entry); err != nil {
			m.logger.Error("app: start account", "account", entry.ID, "err", err)
			m.emitToast("error", "An account failed to start")
		}
	}
}

// Shutdown cancels every worker, waits for them to finish, closes the account
// stores, and cancels pending OAuth flows. Safe to call more than once.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	runtimes := make([]*accountRuntime, 0, len(m.accounts))
	for _, rt := range m.accounts {
		runtimes = append(runtimes, rt)
	}
	m.accounts = make(map[int64]*accountRuntime)
	flows := make([]*oauthFlow, 0, len(m.oauthFlows))
	for _, flow := range m.oauthFlows {
		flows = append(flows, flow)
	}
	m.mu.Unlock()

	for _, flow := range flows {
		flow.cancel()
	}
	for _, rt := range runtimes {
		rt.stop()
	}
	if m.rootCancel != nil {
		m.rootCancel()
	}
}

// Runtimes returns every live account runtime ordered by account ordinal.
func (m *Manager) Runtimes() []*accountRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.registry.List()
	out := make([]*accountRuntime, 0, len(m.accounts))
	for _, e := range entries {
		if rt, ok := m.accounts[e.ID]; ok {
			out = append(out, rt)
		}
	}
	return out
}

// Runtime returns the runtime for one account, or an error naming the account
// ID when it is unknown. Paused accounts keep their store open, so their mail
// stays readable while workers are stopped.
func (m *Manager) Runtime(id int64) (*accountRuntime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.accounts[id]
	if !ok {
		return nil, fmt.Errorf("app: account %d is not running", id)
	}
	return rt, nil
}

// DefaultRuntime returns the primary account's runtime, used when a compose
// request arrives without an explicit account.
func (m *Manager) DefaultRuntime() *accountRuntime {
	runtimes := m.Runtimes()
	for _, rt := range runtimes {
		if rt.entry.Ordinal == 1 {
			return rt
		}
	}
	if len(runtimes) > 0 {
		return runtimes[0]
	}
	return nil
}

// Accounts lists every registered account, paused ones included, ordered by
// ordinal.
func (m *Manager) Accounts() []RegistryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registry.List()
}

// RegistryEntry returns one registry entry.
func (m *Manager) RegistryEntry(id int64) (RegistryEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registry.ByID(id)
}

// Emit forwards one event to the UI emitter.
func (m *Manager) Emit(name string, data any) {
	m.deps.Emitter.Emit(name, data)
}

// CurrentSettings returns the cached settings snapshot.
func (m *Manager) CurrentSettings() settings.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

// SaveSettings persists a new settings snapshot, refreshes the cache, notifies
// the shell, and pushes the settings-changed event.
func (m *Manager) SaveSettings(cfg settings.Settings) error {
	m.mu.Lock()
	previous := m.settings
	m.settings = cfg
	m.mu.Unlock()

	if m.deps.Settings != nil {
		if err := m.deps.Settings.Save(cfg); err != nil {
			m.mu.Lock()
			m.settings = previous
			m.mu.Unlock()
			return fmt.Errorf("app: save settings: %w", err)
		}
	}
	if m.deps.OnSettingsChanged != nil {
		m.deps.OnSettingsChanged(cfg)
	}
	m.Emit(EventSettingsChanged, SettingsChangedEvent{
		Type:     EventSettingsChanged,
		Settings: settingsToInfo(cfg),
	})
	return nil
}

// TriggerSync nudges one account's sync worker, or every account when id is
// zero. An explicit trigger also resumes workers parked offline.
func (m *Manager) TriggerSync(id int64) {
	if id != 0 {
		if rt, err := m.Runtime(id); err == nil {
			rt.triggerSync()
		}
		return
	}
	for _, rt := range m.Runtimes() {
		rt.triggerSync()
	}
}

// Quit terminates the application through the shell-provided hook.
func (m *Manager) Quit() {
	if m.deps.QuitFunc != nil {
		m.deps.QuitFunc()
	}
}

// OpenURL opens an http(s) URL in the user's default browser through the
// shell-provided hook. Any other scheme is refused: URLs arrive from message
// content, which is hostile input.
func (m *Manager) OpenURL(raw string) error {
	if m.deps.BrowserOpenFunc == nil {
		return errors.New("app: no browser opener configured")
	}
	parsed, err := url.Parse(raw)
	switch {
	case err != nil:
		return fmt.Errorf("app: invalid URL: %w", err)
	case (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "":
		return errors.New("app: only http and https URLs can be opened")
	}
	return m.deps.BrowserOpenFunc(raw)
}

// startAccount opens one account's store and starts its workers.
func (m *Manager) startAccount(entry RegistryEntry) error {
	m.mu.Lock()
	if _, exists := m.accounts[entry.ID]; exists {
		m.mu.Unlock()
		return nil
	}
	rootCtx := m.rootCtx
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(rootCtx)
	rt := &accountRuntime{
		manager:         m,
		entry:           entry,
		ctx:             ctx,
		cancel:          cancel,
		lastUnreadTotal: -1,
		folderUnread:    make(map[int64]int),
	}

	account, err := m.openAccountStore(rt)
	if err != nil {
		cancel()
		return err
	}
	rt.account = account

	// A paused account stays readable but does not dial: its store is open
	// and its cached mail is available, only the workers are not started.
	if !account.Paused {
		if err := rt.startWorkers(); err != nil {
			rt.stop()
			return err
		}
	}

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		rt.stop()
		return errors.New("app: manager is shut down")
	}
	m.accounts[entry.ID] = rt
	m.mu.Unlock()

	if account.Paused {
		m.Emit(EventSyncState, SyncStateEvent{Type: EventSyncState, AccountID: entry.ID, State: syncStatePaused})
	}
	return nil
}

// openAccountStore opens (creating when needed) the account's database and
// makes sure its singleton account row exists.
func (m *Manager) openAccountStore(rt *accountRuntime) (store.Account, error) {
	path := m.deps.Paths.AccountDatabasePath(accountIDString(rt.entry.ID))
	st, err := store.Open(m.rootCtx, path, store.Options{
		BackupDir: m.deps.Paths.BackupsDir,
		Logger:    m.logger,
	})
	if err != nil {
		return store.Account{}, err
	}
	rt.store = st

	account, err := st.GetAccount(m.rootCtx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		account = m.newAccountRow(rt.entry)
		if err := st.SaveAccount(m.rootCtx, account); err != nil {
			return store.Account{}, err
		}
	case err != nil:
		return store.Account{}, err
	}
	account.Paused = rt.entry.Paused
	return account, nil
}

// newAccountRow builds the initial per-account row from its registry entry.
func (m *Manager) newAccountRow(entry RegistryEntry) store.Account {
	threshold := m.settings.AttachmentEagerThresholdBytes
	if threshold <= 0 {
		threshold = settings.DefaultAttachmentEagerThresholdBytes
	}
	return store.Account{
		ID:                           accountIDString(entry.ID),
		Email:                        entry.Email,
		DisplayName:                  entry.DisplayName,
		AuthMethod:                   auth.AuthMethodPassword,
		CredentialRef:                credentialRefFor(entry.ID),
		NotificationsEnabled:         true,
		PollIntervalSeconds:          defaultPollIntervalSeconds,
		AttachmentEagerThresholdByte: threshold,
		IsDefault:                    entry.Ordinal == 1,
		Paused:                       entry.Paused,
	}
}

// AddAccountManual adds a password-authenticated account: it verifies the
// credentials against the IMAP server, stores them in the keyring, creates
// the database and account row, registers the account, and starts workers.
func (m *Manager) AddAccountManual(ctx context.Context, input ManualAccountInput) (AccountInfo, error) {
	return m.addAccount(ctx, input, input.Password, false)
}

// AddAccountOAuth adds an OAuth-authenticated account from a completed
// consent flow. The token JSON is what the keyring stores; later refreshes
// rewrite it in place.
func (m *Manager) AddAccountOAuth(ctx context.Context, email, provider, tokenJSON string) (AccountInfo, error) {
	if email == "" {
		return AccountInfo{}, errors.New("app: email is required")
	}
	if tokenJSON == "" {
		return AccountInfo{}, errors.New("app: no OAuth token supplied")
	}
	if _, err := oauthProviderByName(provider); err != nil {
		return AccountInfo{}, err
	}
	return m.addAccount(ctx, ManualAccountInput{Email: email, Auth: "oauth"}, tokenJSON, true)
}

// addAccount is the shared account-creation path for both auth kinds.
func (m *Manager) addAccount(ctx context.Context, input ManualAccountInput, credential string, oauth bool) (AccountInfo, error) {
	if err := validateManualInput(input); err != nil {
		return AccountInfo{}, err
	}
	if m.deps.Credentials == nil {
		return AccountInfo{}, errors.New("app: no credential store configured")
	}
	if !oauth {
		verify := m.deps.VerifyFunc
		if verify == nil {
			verify = verifyIMAPCredentials
		}
		if err := verify(ctx, input); err != nil {
			return AccountInfo{}, err
		}
	}

	id, err := newAccountID()
	if err != nil {
		return AccountInfo{}, err
	}
	entry, err := m.registry.Add(RegistryEntry{
		ID:          id,
		Email:       input.Email,
		DisplayName: displayNameFor(input),
	})
	if err != nil {
		return AccountInfo{}, err
	}
	rollback := func() {
		_ = m.registry.Remove(id)
	}

	ref := credentialRefFor(id)
	if err := m.deps.Credentials.SaveCredential(ctx, ref, credential); err != nil {
		rollback()
		return AccountInfo{}, err
	}

	account := m.newAccountRow(entry)
	account.AuthMethod = authMethodFor(input.Auth, oauth)
	applyServerSettings(&account, input)

	m.mu.Lock()
	started := m.started && !m.stopped
	m.mu.Unlock()

	if !started {
		// The manager is not running its workers (or is shutting down):
		// persist the row paused so the next Start picks the account up.
		if err := m.persistAccountRow(account, true); err != nil {
			_ = m.deps.Credentials.DeleteCredential(ctx, ref)
			rollback()
			return AccountInfo{}, err
		}
	} else if err := m.startFreshAccount(entry, account); err != nil {
		_ = m.deps.Credentials.DeleteCredential(ctx, ref)
		rollback()
		return AccountInfo{}, err
	}

	m.Emit(EventAccountsChanged, AccountsChangedEvent{Type: EventAccountsChanged})
	m.Emit(EventFoldersChanged, FoldersChangedEvent{Type: EventFoldersChanged, AccountID: entry.ID})
	return accountInfoFor(entry), nil
}

// startFreshAccount opens the store, saves the row, and starts workers for a
// freshly added account.
func (m *Manager) startFreshAccount(entry RegistryEntry, account store.Account) error {
	ctx, cancel := context.WithCancel(m.rootCtx)
	rt := &accountRuntime{
		manager:         m,
		entry:           entry,
		ctx:             ctx,
		cancel:          cancel,
		lastUnreadTotal: -1,
		folderUnread:    make(map[int64]int),
	}
	st, err := store.Open(m.rootCtx, m.deps.Paths.AccountDatabasePath(accountIDString(entry.ID)), store.Options{
		BackupDir: m.deps.Paths.BackupsDir,
		Logger:    m.logger,
	})
	if err != nil {
		cancel()
		return err
	}
	rt.store = st
	if err := st.SaveAccount(m.rootCtx, account); err != nil {
		rt.stop()
		return err
	}
	rt.account = account
	if err := rt.startWorkers(); err != nil {
		rt.stop()
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		rt.stop()
		return errors.New("app: manager is shut down")
	}
	m.accounts[entry.ID] = rt
	return nil
}

// persistAccountRow writes an account row into its database without starting
// anything, creating the database when it does not exist yet.
func (m *Manager) persistAccountRow(account store.Account, paused bool) error {
	account.Paused = paused
	st, err := store.Open(context.Background(), m.deps.Paths.AccountDatabasePath(accountIDString(idFromString(account.ID))), store.Options{
		BackupDir: m.deps.Paths.BackupsDir,
		Logger:    m.logger,
	})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	return st.SaveAccount(context.Background(), account)
}

// RemoveAccount stops one account's workers, deletes its credential, database
// files, and registry entry, and refreshes the UI.
func (m *Manager) RemoveAccount(id int64) error {
	m.mu.Lock()
	rt := m.accounts[id]
	delete(m.accounts, id)
	m.mu.Unlock()

	if rt != nil {
		rt.stop()
	}
	if m.deps.Credentials != nil {
		// Prefer the reference stored on the account row: a rekeyed account
		// keeps the reference it was created with, which no longer matches
		// credentialRefFor(id).
		ref := credentialRefFor(id)
		if rt != nil && rt.account.CredentialRef != "" {
			ref = rt.account.CredentialRef
		}
		if err := m.deps.Credentials.DeleteCredential(context.Background(), ref); err != nil {
			m.logger.Error("app: delete credential", "account", id, "err", err)
		}
	}
	if err := m.removeDatabase(id); err != nil {
		m.logger.Error("app: remove database", "account", id, "err", err)
	}
	if err := m.registry.Remove(id); err != nil {
		return err
	}
	m.Emit(EventFoldersChanged, FoldersChangedEvent{Type: EventFoldersChanged, AccountID: id})
	m.Emit(EventAccountsChanged, AccountsChangedEvent{Type: EventAccountsChanged})
	return nil
}

// removeDatabase deletes the account's SQLite file and its WAL/SHM sidecars.
func (m *Manager) removeDatabase(id int64) error {
	base := m.deps.Paths.AccountDatabasePath(accountIDString(id))
	var firstErr error
	for _, path := range []string{base, base + "-wal", base + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("app: remove database: %w", firstErr)
	}
	return nil
}

// SetPaused pauses or resumes one account. Pausing stops the workers but
// keeps the store open so cached mail stays readable.
func (m *Manager) SetPaused(id int64, paused bool) error {
	if err := m.registry.Update(id, func(e *RegistryEntry) { e.Paused = paused }); err != nil {
		return err
	}
	rt, err := m.Runtime(id)
	if err != nil {
		// The account was registered while the manager was not running
		// workers; the registry is the source of truth for its paused state.
		return nil
	}

	ctx := context.Background()
	if paused {
		rt.account.Paused = true
		if err := rt.store.SetAccountPaused(ctx, true); err != nil {
			m.logger.Error("app: persist paused flag", "account", id, "err", err)
		}
		rt.stopWorkers()
		m.Emit(EventSyncState, SyncStateEvent{Type: EventSyncState, AccountID: id, State: syncStatePaused})
		return nil
	}

	rt.account.Paused = false
	if err := rt.store.SetAccountPaused(ctx, false); err != nil {
		m.logger.Error("app: persist resumed flag", "account", id, "err", err)
	}
	if err := rt.startWorkers(); err != nil {
		return err
	}
	m.Emit(EventSyncState, SyncStateEvent{Type: EventSyncState, AccountID: id, State: syncStateSyncing})
	return nil
}

// UpdateAccount applies the changed per-account preferences and restarts the
// workers when the values they read (poll interval, eager threshold) changed.
func (m *Manager) UpdateAccount(id int64, prefs AccountPrefs) error {
	rt, err := m.Runtime(id)
	if err != nil {
		return err
	}

	changed := rt.account
	if prefs.DisplayName != nil {
		changed.DisplayName = *prefs.DisplayName
		if err := m.registry.Update(id, func(e *RegistryEntry) { e.DisplayName = *prefs.DisplayName }); err != nil {
			return err
		}
	}
	if prefs.Signature != nil {
		changed.Signature = *prefs.Signature
	}
	if prefs.NotificationsEnabled != nil {
		changed.NotificationsEnabled = *prefs.NotificationsEnabled
	}
	if prefs.PollIntervalSeconds != nil && *prefs.PollIntervalSeconds > 0 {
		changed.PollIntervalSeconds = *prefs.PollIntervalSeconds
	}
	if prefs.AttachmentEagerThresholdBytes != nil && *prefs.AttachmentEagerThresholdBytes >= 0 {
		changed.AttachmentEagerThresholdByte = *prefs.AttachmentEagerThresholdBytes
	}

	if err := rt.store.SaveAccount(context.Background(), changed); err != nil {
		return err
	}
	previous := rt.account
	rt.account = changed

	needsRestart := previous.PollIntervalSeconds != changed.PollIntervalSeconds ||
		previous.AttachmentEagerThresholdByte != changed.AttachmentEagerThresholdByte
	if needsRestart {
		rt.stopWorkers()
		if err := rt.startWorkers(); err != nil {
			return err
		}
	}
	m.Emit(EventAccountsChanged, AccountsChangedEvent{Type: EventAccountsChanged})
	return nil
}

// handleSyncEvent is the sync worker's Notify callback: it runs on the worker
// goroutine, so it must stay quick and never block for long.
func (m *Manager) handleSyncEvent(rt *accountRuntime, ev mailsync.Event) {
	if ev.Progress != nil {
		m.Emit(EventSyncProgress, SyncProgressEvent{
			Type:      EventSyncProgress,
			AccountID: rt.entry.ID,
			Folder:    ev.Progress.Folder,
			Phase:     ev.Progress.Phase,
			New:       ev.Progress.New,
			Total:     ev.Progress.Total,
			At:        time.Now().UTC().Format(time.RFC3339),
		})
		m.recordActivity(rt.entry.ID, syncProgressText(ev.Progress), syncProgressLevel(ev.Progress))
		return
	}

	previous := rt.lastSyncStateValue()
	rt.setLastSyncState(ev.State)

	if ev.Notice != "" {
		m.emitToast("info", ev.Notice)
	}
	if ev.State != previous || ev.Err != "" {
		m.Emit(EventSyncState, SyncStateEvent{
			Type:      EventSyncState,
			AccountID: rt.entry.ID,
			State:     mapSyncState(ev.State),
			Detail:    ev.Err,
		})
		m.recordActivity(rt.entry.ID, syncStateText(ev), syncStateLevel(ev))
	}
	if ev.State == mailsync.StateAuthFailed && previous != mailsync.StateAuthFailed {
		m.deps.Notifier.Notify("Account needs attention",
			"Sign-in for "+rt.entry.DisplayName+" failed. Re-enter the credentials to resume.")
	}
	if ev.State == mailsync.StateIdle {
		m.afterSyncPass(rt)
	}
}

// afterSyncPass runs at the end of every completed sync pass: it refreshes
// folder metadata in the UI, updates unread counts, and fires the new-mail
// notification when the account's unread total grew.
func (m *Manager) afterSyncPass(rt *accountRuntime) {
	ctx, cancel := context.WithTimeout(m.rootCtx, 30*time.Second)
	defer cancel()

	folders, err := rt.store.ListFolders(ctx)
	if err != nil {
		m.logger.Error("app: list folders after sync", "account", rt.entry.ID, "err", err)
		return
	}
	m.Emit(EventFoldersChanged, FoldersChangedEvent{Type: EventFoldersChanged, AccountID: rt.entry.ID})
	m.Emit(EventMessagesChanged, MessagesChangedEvent{Type: EventMessagesChanged, AccountID: rt.entry.ID})

	total := 0
	for _, f := range folders {
		total += f.UnreadCount
		globalID := globalFolderID(rt.entry.Ordinal, f.ID)
		if previous, ok := rt.cachedUnread(globalID); !ok || previous != f.UnreadCount {
			rt.rememberUnread(globalID, f.UnreadCount)
			m.Emit(EventUnreadCount, UnreadCountEvent{
				Type:        EventUnreadCount,
				AccountID:   rt.entry.ID,
				FolderID:    globalID,
				UnreadCount: f.UnreadCount,
			})
		}
	}

	previousTotal := rt.takeUnreadTotal()
	rt.setUnreadTotal(total)
	if previousTotal >= 0 && total > previousTotal {
		m.notifyNewMail(rt, total-previousTotal)
	}
}

// notifyNewMail fires the per-account new-mail notification, collapsing a
// burst of arriving messages into one notification per sync pass.
func (m *Manager) notifyNewMail(rt *accountRuntime, newCount int) {
	m.mu.Lock()
	enabled := m.settings.NotificationsEnabled
	m.mu.Unlock()
	if !enabled || !rt.account.NotificationsEnabled {
		return
	}
	plural := "messages"
	if newCount == 1 {
		plural = "message"
	}
	m.deps.Notifier.Notify("New mail",
		fmt.Sprintf("%d new %s for %s", newCount, plural, rt.entry.DisplayName))
}

// handleSendEvent is the send worker's Notify callback.
func (m *Manager) handleSendEvent(rt *accountRuntime, ev send.Event) {
	m.Emit(EventSendState, SendStateEvent{
		Type:      EventSendState,
		AccountID: rt.entry.ID,
		DraftID:   ev.OutboxID,
		State:     string(ev.State),
		Error:     ev.Err,
	})

	switch ev.State {
	case store.SendSent:
		// The Sent copy is best effort and must never delay or fail the send
		// pipeline, so it runs on its own goroutine and only logs failures.
		go m.copyToSent(rt, ev.OutboxID)
	case store.SendFailed:
		m.emitToast("error", "A message failed to send — check the Outbox")
		m.deps.Notifier.Notify("Message not sent",
			"A message for "+rt.entry.DisplayName+" could not be delivered. Open the Outbox to retry.")
	}
}

// copyToSent renders the sent message's final MIME and appends it to the
// account's Sent folder, retrying a few times because connections to some
// providers are slow and drop appends. A failed copy is logged, never surfaced
// as an error: the message itself was delivered and a later sync reconciles
// the folder.
func (m *Manager) copyToSent(rt *accountRuntime, outboxID string) {
	loadCtx, cancelLoad := context.WithTimeout(m.rootCtx, 30*time.Second)
	row, err := rt.store.OutboxByID(loadCtx, outboxID)
	if err != nil {
		cancelLoad()
		m.logger.Error("app: load sent message for the Sent copy", "account", rt.entry.ID, "err", err)
		return
	}
	raw, err := send.BuildRawMIME(row, send.NewBlobStore(m.attachBlobs))
	if err != nil {
		cancelLoad()
		m.logger.Error("app: render Sent copy", "account", rt.entry.ID, "err", err)
		return
	}
	folder, err := rt.store.FolderByType(loadCtx, store.FolderSent)
	cancelLoad()
	if err != nil {
		m.logger.Info("app: no Sent folder for the sent copy", "account", rt.entry.ID)
		return
	}

	// The IMAP connection to some providers is slow and drops appends; retry
	// a couple of times with a backoff before giving up. Delivered mail is
	// never at risk: a missing copy is reconciled by a later sync.
	const attempts = 3
	for attempt := 1; attempt <= attempts; attempt++ {
		appendCtx, cancel := context.WithTimeout(m.rootCtx, 2*time.Minute)
		appendErr := rt.appendRaw(appendCtx, folder.IMAPPath, raw, []string{"\\Seen"})
		cancel()
		if appendErr == nil {
			return
		}
		m.logger.Error("app: append Sent copy", "account", rt.entry.ID, "attempt", attempt, "err", appendErr)
		if m.rootCtx.Err() != nil {
			return
		}
		select {
		case <-time.After(time.Duration(attempt) * 5 * time.Second):
		case <-m.rootCtx.Done():
			return
		}
	}
}

// emitToast pushes a transient notice to the UI.
func (m *Manager) emitToast(level, message string) {
	m.Emit(EventToast, ToastEvent{Type: EventToast, Level: level, Message: message})
}

// syncAuthFunc returns the credential resolver the sync worker's dial factory
// uses: a fresh SASL mechanism per connection attempt, so rotated passwords
// and refreshed tokens are picked up on reconnect.
func (m *Manager) syncAuthFunc(rt *accountRuntime) func(ctx context.Context) (sasl.Client, error) {
	return func(ctx context.Context) (sasl.Client, error) {
		secret, err := m.deps.Credentials.Credential(ctx, rt.account.CredentialRef)
		if err != nil {
			return nil, fmt.Errorf("app: load credential: %w", err)
		}
		return m.mechanismForAccount(ctx, rt.account, secret)
	}
}

// sendAuthProvider adapts the credential path to the send worker's per-attempt
// provider.
func (m *Manager) sendAuthProvider(rt *accountRuntime) send.AuthProvider {
	return func(ctx context.Context) (string, string, string, bool, error) {
		username := rt.account.SMTPUsername
		if username == "" {
			username = rt.account.IMAPUsername
		}
		secret, err := m.deps.Credentials.Credential(ctx, rt.account.CredentialRef)
		if err != nil {
			return "", "", "", false, fmt.Errorf("app: load credential: %w", err)
		}
		if !isOAuthMethod(rt.account.AuthMethod) {
			return username, secret, rt.account.AuthMethod, false, nil
		}
		token, err := m.liveOAuthToken(ctx, rt.account, secret)
		if err != nil {
			return "", "", "", false, err
		}
		return username, token, rt.account.AuthMethod, true, nil
	}
}

// mechanismForAccount turns a stored secret into a SASL mechanism, silently
// refreshing OAuth tokens when the stored access token has expired.
func (m *Manager) mechanismForAccount(ctx context.Context, account store.Account, secret string) (sasl.Client, error) {
	if !isOAuthMethod(account.AuthMethod) {
		mechanism, err := auth.SASLMechanism(account.AuthMethod, account.IMAPUsername, secret, false)
		if err != nil {
			return nil, fmt.Errorf("app: build SASL mechanism: %w", err)
		}
		return mechanism, nil
	}
	token, err := m.liveOAuthToken(ctx, account, secret)
	if err != nil {
		return nil, err
	}
	mechanism, err := auth.SASLMechanism(account.AuthMethod, account.IMAPUsername, token, true)
	if err != nil {
		return nil, fmt.Errorf("app: build SASL mechanism: %w", err)
	}
	return mechanism, nil
}

// liveOAuthToken parses the stored OAuth token JSON, refreshes it when
// expired, and persists the refreshed token back to the keyring so restarts
// keep working without a new consent prompt.
func (m *Manager) liveOAuthToken(ctx context.Context, account store.Account, stored string) (string, error) {
	var token oauth2.Token
	if err := json.Unmarshal([]byte(stored), &token); err != nil {
		return "", fmt.Errorf("app: parse stored OAuth token: %w", err)
	}
	if token.Valid() {
		return token.AccessToken, nil
	}
	if token.RefreshToken == "" {
		return "", errors.New("app: OAuth token expired and no refresh token is stored")
	}
	cfg, err := m.oauthConfigFor(account)
	if err != nil {
		return "", err
	}
	refreshed, err := auth.RefreshToken(ctx, cfg, token.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("app: refresh OAuth token: %w", err)
	}
	encoded, err := json.Marshal(refreshed)
	if err != nil {
		return "", fmt.Errorf("app: encode refreshed OAuth token: %w", err)
	}
	if err := m.deps.Credentials.SaveCredential(ctx, account.CredentialRef, string(encoded)); err != nil {
		// The refreshed token still works for this session even if the
		// keyring write failed; keep authenticating and log the loss.
		m.logger.Error("app: store refreshed OAuth token", "account", account.ID, "err", err)
	}
	return refreshed.AccessToken, nil
}

// oauthConfigFor resolves the OAuth2 endpoints for an account from its IMAP
// host.
func (m *Manager) oauthConfigFor(account store.Account) (auth.OAuthConfig, error) {
	provider, ok := oauthProviderForHost(account.IMAPHost)
	if !ok {
		return auth.OAuthConfig{}, fmt.Errorf("app: no OAuth provider for host %q", account.IMAPHost)
	}
	return oauthConfigForProvider(provider)
}

// mapSyncState maps the sync engine's states onto the bridge's state names.
// An authentication park is an error state to the UI; the detail text
// explains the credential problem.
func mapSyncState(state mailsync.State) string {
	switch state {
	case mailsync.StateSyncing:
		return syncStateSyncing
	case mailsync.StateIdle:
		return syncStateIdle
	case mailsync.StateOffline:
		return syncStateOffline
	case mailsync.StateAuthFailed:
		return syncStateError
	case mailsync.StatePaused:
		return syncStatePaused
	default:
		return syncStateIdle
	}
}

// syncProgressText renders one progress step as a human-readable log line.
func syncProgressText(progress *mailsync.Progress) string {
	switch progress.Phase {
	case "pass-start":
		return fmt.Sprintf("checking %d folders…", progress.Total)
	case "folder-start":
		return fmt.Sprintf("%s — %d messages", progress.Folder, progress.Total)
	case "folder-done":
		if progress.New > 0 {
			return fmt.Sprintf("%s — %d new", progress.Folder, progress.New)
		}
		return fmt.Sprintf("%s — up to date", progress.Folder)
	case "pass-done":
		return "pass complete"
	default:
		return progress.Phase
	}
}

// syncProgressLevel maps a progress step to a log severity.
func syncProgressLevel(progress *mailsync.Progress) string {
	if progress.Phase == "pass-done" {
		return "success"
	}
	if progress.Phase == "folder-done" && progress.New > 0 {
		return "success"
	}
	return "info"
}

// syncStateText renders a sync-state transition as a human-readable line.
func syncStateText(ev mailsync.Event) string {
	switch ev.State {
	case mailsync.StateSyncing:
		return "sync started"
	case mailsync.StateIdle:
		return "sync complete"
	case mailsync.StateOffline:
		if ev.Err != "" {
			return "offline — retrying: " + ev.Err
		}
		return "offline — retrying"
	case mailsync.StatePaused:
		return "paused"
	case mailsync.StateAuthFailed:
		if ev.Err != "" {
			return "account needs attention: " + ev.Err
		}
		return "account needs attention"
	default:
		return string(ev.State)
	}
}

// syncStateLevel maps a sync-state transition to a log severity.
func syncStateLevel(ev mailsync.Event) string {
	switch ev.State {
	case mailsync.StateIdle:
		return "success"
	case mailsync.StateOffline, mailsync.StatePaused:
		return "warn"
	case mailsync.StateAuthFailed:
		return "error"
	default:
		return "info"
	}
}

// isOAuthMethod reports whether an account row's auth method expects an OAuth
// bearer token rather than a password.
func isOAuthMethod(authMethod string) bool {
	return authMethod == auth.AuthMethodXOAUTH2 || authMethod == auth.AuthMethodOAUTHBEARER
}

// authMethodFor maps the setup flow's auth selector onto the account row's
// auth method label.
func authMethodFor(inputAuth string, oauth bool) string {
	if oauth || inputAuth == "oauth" {
		return auth.AuthMethodXOAUTH2
	}
	return auth.AuthMethodPassword
}

// applyServerSettings copies the setup input's endpoints onto an account row.
func applyServerSettings(account *store.Account, input ManualAccountInput) {
	account.IMAPHost = input.IMAP.Host
	account.IMAPPort = input.IMAP.Port
	account.IMAPTLS = input.IMAP.Security
	account.IMAPUsername = input.IMAP.Username
	account.SMTPHost = input.SMTP.Host
	account.SMTPPort = input.SMTP.Port
	account.SMTPTLS = input.SMTP.Security
	account.SMTPUsername = input.SMTP.Username
}

// credentialRefFor is the deterministic keyring reference for an account.
func credentialRefFor(id int64) string {
	return auth.CredentialRef(accountIDString(id))
}

// displayNameFor derives the visible account name from the setup input.
func displayNameFor(input ManualAccountInput) string {
	if input.DisplayName != "" {
		return input.DisplayName
	}
	local, _, _ := strings.Cut(input.Email, "@")
	return local
}

// idFromString parses an account row's ID back to its bridge form. Account
// IDs are generated here, so the parse only fails on a corrupted row.
func idFromString(id string) int64 {
	parsed, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

// validateManualInput checks the fields every new account needs.
func validateManualInput(input ManualAccountInput) error {
	if input.Email == "" {
		return errors.New("app: email is required")
	}
	if input.IMAP.Host == "" || input.IMAP.Port <= 0 {
		return errors.New("app: the IMAP server settings are incomplete")
	}
	if input.SMTP.Host == "" || input.SMTP.Port <= 0 {
		return errors.New("app: the SMTP server settings are incomplete")
	}
	return nil
}

// accountInfoFor renders a registry entry as the bridge Account shape.
func accountInfoFor(entry RegistryEntry) AccountInfo {
	return AccountInfo{
		ID:          entry.ID,
		Email:       entry.Email,
		DisplayName: entry.DisplayName,
		IsDefault:   entry.Ordinal == 1,
		Paused:      entry.Paused,
		Color:       entry.Color,
	}
}
