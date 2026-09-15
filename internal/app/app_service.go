package app

import (
	"context"
)

// AppService is the bound surface for whole-application actions the frontend
// cannot reach otherwise: manual sync, opening external links, and quitting.
type AppService struct {
	manager *Manager
}

// NewAppService returns the app service bound to the manager.
func NewAppService(manager *Manager) *AppService {
	return &AppService{manager: manager}
}

// SyncNow nudges one account's sync worker, or every account when the ID is
// zero. An explicit trigger also resumes accounts parked offline.
func (s *AppService) SyncNow(ctx context.Context, accountID int64) {
	s.manager.TriggerSync(accountID)
}

// SyncActivity returns the engine's recent sync activity log, so the UI can
// show what happened even when it subscribed after a pass had already run.
func (s *AppService) SyncActivity(ctx context.Context) []SyncActivityEntry {
	return s.manager.SyncActivity()
}

// Quit terminates the application through the shell-provided hook, running
// the graceful shutdown path.
func (s *AppService) Quit(ctx context.Context) {
	s.manager.Quit()
}

// OpenExternal opens an http(s) URL in the user's default browser. URLs
// arrive from message content, so every other scheme is refused.
func (s *AppService) OpenExternal(ctx context.Context, raw string) error {
	return s.manager.OpenURL(raw)
}
