package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/emersion/go-sasl"

	"github.com/mefiz0/posthaste/internal/backoff"
	"github.com/mefiz0/posthaste/internal/send"
	"github.com/mefiz0/posthaste/internal/store"
	mailsync "github.com/mefiz0/posthaste/internal/sync"
)

// workerRestartPolicy bounds how fast a crashed worker is restarted. A worker
// that ran healthily for a while restarts its backoff from scratch.
func workerRestartPolicy() backoff.Policy {
	return backoff.Policy{
		Initial:     2 * time.Second,
		Max:         time.Minute,
		Multiplier:  2,
		Jitter:      0.25,
		MaxAttempts: 0, // A crashed worker is always restarted, just slower.
	}
}

// workerHealthyRun is how long a worker must have run before its next crash
// restarts the backoff from the initial delay again.
const workerHealthyRun = time.Minute

// accountRuntime is one account's live state: its open store, the supervised
// sync and send workers, and the small caches the event handlers need to
// detect transitions. Services reach into it for queries; the workers own the
// connection state.
type accountRuntime struct {
	manager *Manager
	entry   RegistryEntry
	account store.Account
	store   *store.Store

	ctx    context.Context
	cancel context.CancelFunc

	workersMu     sync.Mutex
	workersCancel context.CancelFunc
	workersDone   sync.WaitGroup
	syncWorker    *mailsync.Worker
	sendWorker    *send.Worker

	stateMu         sync.Mutex
	lastSyncState   mailsync.State
	lastUnreadTotal int
	folderUnread    map[int64]int
}

// ID is the account's bridge-level ID.
func (rt *accountRuntime) ID() int64 { return rt.entry.ID }

// Ordinal is the account's stable creation order, used for folder ID packing.
func (rt *accountRuntime) Ordinal() int { return rt.entry.Ordinal }

// DisplayName is the account's visible name.
func (rt *accountRuntime) DisplayName() string { return rt.entry.DisplayName }

// Info renders the account as the bridge Account shape.
func (rt *accountRuntime) Info() AccountInfo { return accountInfoFor(rt.entry) }

// startWorkers builds and launches the sync and send worker pair. Any
// previously running pair must have been stopped first.
func (rt *accountRuntime) startWorkers() error {
	rt.workersMu.Lock()
	defer rt.workersMu.Unlock()
	if rt.syncWorker != nil {
		return errors.New("app: account workers are already running")
	}
	if rt.manager.deps.Credentials == nil {
		return errors.New("app: no credential store configured")
	}
	if rt.manager.rawBlobs == nil || rt.manager.attachBlobs == nil {
		return errors.New("app: blob stores are not open")
	}

	workerCtx, cancel := context.WithCancel(rt.ctx)

	syncWorker, err := rt.manager.buildSyncWorker(rt)
	if err != nil {
		cancel()
		return fmt.Errorf("app: build sync worker: %w", err)
	}
	sendWorker, err := rt.manager.buildSendWorker(rt)
	if err != nil {
		cancel()
		return fmt.Errorf("app: build send worker: %w", err)
	}

	rt.workersCancel = cancel
	rt.syncWorker = syncWorker
	rt.sendWorker = sendWorker

	rt.workersDone.Add(2)
	go rt.supervise(workerCtx, "sync", syncWorker.Run)
	go rt.supervise(workerCtx, "send", sendWorker.Run)
	return nil
}

// stopWorkers cancels the worker pair and waits for both goroutines to exit.
func (rt *accountRuntime) stopWorkers() {
	rt.workersMu.Lock()
	cancel := rt.workersCancel
	rt.workersCancel = nil
	rt.syncWorker = nil
	rt.sendWorker = nil
	rt.workersMu.Unlock()

	if cancel == nil {
		return
	}
	cancel()

	done := make(chan struct{})
	go func() {
		rt.workersDone.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// The workers exit on context cancellation; a wedged exit is logged
		// by the supervisor and must not block account removal forever.
		rt.manager.logger.Error("app: account workers did not stop in time", "account", rt.entry.ID)
	}
}

// supervise runs one worker until its context ends, restarting it with
// backoff when it crashes. Run functions return the context error on clean
// shutdown, which ends the loop.
func (rt *accountRuntime) supervise(ctx context.Context, name string, run func(context.Context) error) {
	defer rt.workersDone.Done()
	restarts := backoff.New(workerRestartPolicy())
	for {
		startedAt := rt.manager.now()
		err := run(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(startedAt) > workerHealthyRun {
			restarts.Reset()
		}
		rt.manager.logger.Error("app: worker crashed, restarting",
			"account", rt.entry.ID, "worker", name, "err", err)

		if err := restarts.Wait(ctx, backoff.Sleep); err != nil {
			if errors.Is(err, backoff.ErrExhausted) {
				// A worker crash-looping at the cap still gets restarted,
				// just at the capped delay, so a recovered server is picked
				// up without user action.
				restarts.Reset()
				continue
			}
			return
		}
	}
}

// stop tears the whole runtime down: workers first, then the store.
func (rt *accountRuntime) stop() {
	rt.cancel()
	rt.stopWorkers()
	if rt.store != nil {
		_ = rt.store.Close()
		rt.store = nil
	}
}

// triggerSync nudges the sync worker. It is safe before Run starts and after
// it returned.
func (rt *accountRuntime) triggerSync() {
	rt.workersMu.Lock()
	worker := rt.syncWorker
	rt.workersMu.Unlock()
	if worker != nil {
		worker.TriggerSync()
	}
}

// triggerFolderSync nudges the sync worker to sync just one folder, for
// actions whose effect is confined to it.
func (rt *accountRuntime) triggerFolderSync(path string) {
	rt.workersMu.Lock()
	worker := rt.syncWorker
	rt.workersMu.Unlock()
	if worker != nil {
		worker.TriggerFolderSync(path)
	}
}

// triggerSend nudges the send worker.
func (rt *accountRuntime) triggerSend() {
	rt.workersMu.Lock()
	worker := rt.sendWorker
	rt.workersMu.Unlock()
	if worker != nil {
		worker.Trigger()
	}
}

// appendRaw uploads raw MIME with the given IMAP flags to a folder path on
// the sync worker's connection, waiting for the next between-pass window.
func (rt *accountRuntime) appendRaw(ctx context.Context, path string, raw []byte, flags []string) error {
	rt.workersMu.Lock()
	worker := rt.syncWorker
	rt.workersMu.Unlock()
	if worker == nil {
		return errors.New("app: sync worker is not running")
	}
	return worker.AppendRaw(ctx, path, raw, flags)
}

// lastSyncStateValue returns the previous sync state for transition checks.
func (rt *accountRuntime) lastSyncStateValue() mailsync.State {
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	return rt.lastSyncState
}

// setLastSyncState records the current sync state.
func (rt *accountRuntime) setLastSyncState(state mailsync.State) {
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	rt.lastSyncState = state
}

// cachedUnread returns the last emitted unread count for a global folder ID.
func (rt *accountRuntime) cachedUnread(globalFolderID int64) (int, bool) {
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	count, ok := rt.folderUnread[globalFolderID]
	return count, ok
}

// rememberUnread records the last emitted unread count for a global folder ID.
func (rt *accountRuntime) rememberUnread(globalFolderID int64, count int) {
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	rt.folderUnread[globalFolderID] = count
}

// takeUnreadTotal returns the previous account-wide unread total.
func (rt *accountRuntime) takeUnreadTotal() int {
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	return rt.lastUnreadTotal
}

// setUnreadTotal records the account-wide unread total.
func (rt *accountRuntime) setUnreadTotal(total int) {
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	rt.lastUnreadTotal = total
}

// now is the injected clock.
func (m *Manager) now() time.Time { return m.deps.Now() }

// buildSyncWorker constructs the sync worker for one runtime, routing events
// back into the manager.
func (m *Manager) buildSyncWorker(rt *accountRuntime) (*mailsync.Worker, error) {
	authFunc := m.syncAuthFunc(rt)

	serverFactory, err := m.syncDialFactory()(rt.account, authFunc)
	if err != nil {
		return nil, err
	}

	return mailsync.New(mailsync.Deps{
		AccountID:   accountIDString(rt.entry.ID),
		Account:     rt.account,
		AuthFunc:    authFunc,
		Dial:        serverFactory,
		DB:          rt.store,
		RawBlobs:    m.rawBlobs,
		AttachBlobs: m.attachBlobs,
		Notify: func(ev mailsync.Event) {
			m.handleSyncEvent(rt, ev)
		},
		Logger: m.logger,
	})
}

// syncDialFactory returns the dial factory builder, defaulting to the
// production IMAP dialer wrapped in the erroring signature the Deps field
// declares.
func (m *Manager) syncDialFactory() SyncDialFactory {
	if m.deps.SyncDialFactory != nil {
		return m.deps.SyncDialFactory
	}
	return func(account store.Account, authFunc func(ctx context.Context) (sasl.Client, error)) (mailsync.ServerFactory, error) {
		return mailsync.DialFactory(account, authFunc), nil
	}
}

// defaultMailerFactory is the production SMTP transport builder.
func defaultMailerFactory(account store.Account, blobs send.BlobOpener) send.Mailer {
	return send.NewMailer(send.TransportConfig{
		Host:     account.SMTPHost,
		Port:     account.SMTPPort,
		TLS:      send.TLSMode(account.SMTPTLS),
		Username: account.SMTPUsername,
	}, blobs)
}

// buildSendWorker constructs the send worker for one runtime, routing events
// back into the manager.
func (m *Manager) buildSendWorker(rt *accountRuntime) (*send.Worker, error) {
	mailerFactory := m.deps.MailerFactory
	if mailerFactory == nil {
		mailerFactory = defaultMailerFactory
	}
	return send.New(send.Deps{
		DB:      rt.store,
		Account: rt.account,
		Mailer:  mailerFactory(rt.account, send.NewBlobStore(m.attachBlobs)),
		Auth:    m.sendAuthProvider(rt),
		Notify: func(ev send.Event) {
			m.handleSendEvent(rt, ev)
		},
	})
}
