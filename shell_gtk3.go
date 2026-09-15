//go:build gtk3

package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/mefiz0/posthaste/internal/app"
	"github.com/mefiz0/posthaste/internal/auth"
	"github.com/mefiz0/posthaste/internal/logging"
	"github.com/mefiz0/posthaste/internal/settings"
)

// The webview is served from the Vite build output. A minimal frontend/dist
// must exist for the embed; `task build` produces the real one.
//
//go:embed all:frontend/dist
var distFS embed.FS

//go:embed build/appicon.png
var appIcon []byte

// fileSettingsStore adapts the settings package's file functions to the
// manager's SettingsStore interface.
type fileSettingsStore struct {
	path string
}

func (s fileSettingsStore) Load() (settings.Settings, error) {
	return settings.Load(s.path)
}

func (s fileSettingsStore) Save(cfg settings.Settings) error {
	return settings.Save(s.path, cfg)
}

// desktopNotifier posts native desktop notifications through the Wails
// notifications service (freedesktop.org notifications on Linux).
type desktopNotifier struct {
	service *notifications.NotificationService
}

// Notify sends one notification. Failures (for example no notification
// daemon) are logged and otherwise ignored: a missed notification must never
// take the mail engine down with it.
func (d desktopNotifier) Notify(title, body string) {
	if d.service == nil {
		return
	}
	err := d.service.SendNotification(notifications.NotificationOptions{
		ID:    fmt.Sprintf("posthaste-%d", time.Now().UnixNano()),
		Title: title,
		Body:  body,
	})
	if err != nil {
		slog.Warn("app: notification failed", "err", err)
	}
}

// shellState bridges the manager's event stream to Wails and owns the tray
// unread badge. It is the single place the app shell reacts to engine events.
type shellState struct {
	app  atomic.Pointer[application.App]
	tray atomic.Pointer[application.SystemTray]

	mu              sync.Mutex
	unreadByAccount map[int64]int
	minimizeToTray  bool
}

func (s *shellState) setMinimizeToTray(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.minimizeToTray = enabled
}

func (s *shellState) minimize() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.minimizeToTray
}

// Emit forwards every engine event to the Wails event bus and maintains the
// tray badge from unread-count events. Payloads carry their own type
// discriminator, so the frontend routes them through one union.
func (s *shellState) Emit(name string, data any) {
	if instance := s.app.Load(); instance != nil {
		instance.Event.Emit(name, data)
	}
	if event, ok := data.(app.UnreadCountEvent); ok && name == app.EventUnreadCount {
		s.updateTrayBadge(event.AccountID, event.UnreadCount)
	}
}

// updateTrayBadge tracks per-account unread totals and reflects the sum in
// the tray label. The tooltip is a no-op on Linux, so the label carries the
// badge; a zero total collapses back to the plain name.
func (s *shellState) updateTrayBadge(accountID int64, count int) {
	s.mu.Lock()
	s.unreadByAccount[accountID] = count
	total := 0
	for _, c := range s.unreadByAccount {
		total += c
	}
	tray := s.tray.Load()
	s.mu.Unlock()

	if tray == nil {
		return
	}
	label := "Posthaste"
	if total > 0 {
		label = fmt.Sprintf("Posthaste (%d)", total)
	}
	tray.SetLabel(label)
}

// setTray stores the active tray (or nil) so badge updates no-op while the
// tray setting is off.
func (s *shellState) setTray(tray *application.SystemTray) {
	s.tray.Store(tray)
}

func run() error {
	paths := settings.ResolvePaths()
	if err := settings.EnsureDirs(paths); err != nil {
		return err
	}

	cfg, err := settings.Load(paths.SettingsFile())
	if err != nil {
		return err
	}
	logger, closeLogs, err := logging.New(logging.Options{
		Dir:     paths.LogDir,
		Verbose: cfg.VerboseLogging,
	})
	if err != nil {
		return err
	}
	slog.SetDefault(logger)

	state := &shellState{
		unreadByAccount: make(map[int64]int),
		minimizeToTray:  cfg.MinimizeToTray,
	}

	// The notifications service is registered with the application below and
	// its lifecycle is Wails-managed; the adapter only calls into it.
	notifier := desktopNotifier{service: notifications.New()}

	manager := app.NewManager(app.Deps{
		Paths:       paths,
		Settings:    fileSettingsStore{path: paths.SettingsFile()},
		Logger:      logger,
		Emitter:     state,
		Credentials: auth.NewKeyring(),
		Notifier:    notifier,
		QuitFunc: func() {
			if instance := state.app.Load(); instance != nil {
				instance.Quit()
			}
		},
		BrowserOpenFunc: func(raw string) error {
			instance := state.app.Load()
			if instance == nil {
				return errors.New("application is not running")
			}
			return instance.Browser.OpenURL(raw)
		},
		FilePicker: func() ([]string, error) {
			instance := state.app.Load()
			if instance == nil {
				return nil, errors.New("application is not running")
			}
			dialog := instance.Dialog.OpenFile()
			if window, ok := instance.Window.GetByName("main"); ok {
				dialog.AttachToWindow(window)
			}
			dialog.CanChooseFiles(true).SetTitle("Attach files")
			return dialog.PromptForMultipleSelection()
		},
		OnSettingsChanged: func(updated settings.Settings) {
			state.setMinimizeToTray(updated.MinimizeToTray)
			if instance := state.app.Load(); instance != nil {
				applyTraySetting(instance, state, updated.MinimizeToTray)
			}
		},
	})
	manager.Start(context.Background())

	instance := application.New(application.Options{
		Name:        "Posthaste",
		Description: "A fast, reliable, offline-first email client",
		Icon:        appIcon,
		Services: []application.Service{
			// The notifications service is first so it is started before any
			// service that may raise a notification during startup.
			application.NewService(notifier.service),
			application.NewService(app.NewAccountService(manager)),
			application.NewService(app.NewMailService(manager)),
			application.NewService(app.NewComposeService(manager)),
			application.NewService(app.NewAttachmentService(manager)),
			application.NewService(app.NewSettingsService(manager)),
			application.NewService(app.NewAppService(manager)),
		},
		Assets: application.AssetOptions{
			Handler:        application.AssetFileServerFS(distFS),
			DisableLogging: true,
		},
		Linux: application.LinuxOptions{
			ProgramName: "posthaste",
		},
		OnShutdown: func() {
			manager.Shutdown()
			_ = closeLogs()
		},
	})
	state.app.Store(instance)

	window := instance.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "Posthaste",
		Width:     1280,
		Height:    800,
		MinWidth:  900,
		MinHeight: 600,
		URL:       "/",
		// A transparent window lets the frosted sidebar actually sample the
		// desktop behind it; the UI paints opaque surfaces everywhere it does
		// not want the wallpaper to show through.
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Linux: application.LinuxWindow{
			Icon: appIcon,
		},
	})

	// With minimize-to-tray enabled, closing the window hides it instead.
	// The setting is read at close time so toggling it applies at once; the
	// delete-event handler blocks the GTK destroy already, so cancelling the
	// event leaves a live window to show again.
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if !state.minimize() {
			return
		}
		e.Cancel()
		window.Hide()
	})

	applyTraySetting(instance, state, cfg.MinimizeToTray)

	return instance.Run()
}

// applyTraySetting creates or destroys the system tray to match the setting.
// GNOME has no StatusNotifier support without a user-installed extension;
// Wails registers via StatusNotifier regardless, so on GNOME the icon may
// simply not appear. That degrade is logged rather than hidden: the setting
// stays on and the tray shows up once the extension is present.
func applyTraySetting(instance *application.App, state *shellState, enabled bool) {
	if existing := state.tray.Swap(nil); existing != nil {
		existing.Destroy()
	}
	if !enabled {
		return
	}

	tray := instance.SystemTray.New()
	tray.SetIcon(appIcon)
	tray.SetLabel("Posthaste")

	menu := instance.NewMenu()
	menu.Add("Show Posthaste").OnClick(func(*application.Context) {
		if window, ok := instance.Window.GetByName("main"); ok {
			window.Show()
		}
	})
	menu.Add("Compose New").OnClick(func(*application.Context) {
		instance.Event.Emit(app.EventUICompose)
	})
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) {
		instance.Quit()
	})
	tray.SetMenu(menu)

	state.setTray(tray)

	if isGNOME() {
		slog.Warn("app: GNOME needs the AppIndicator/KStatusNotifierItem extension for tray icons; " +
			"the icon may not appear until it is installed")
	}
}

// isGNOME reports whether the session looks like GNOME, where tray support
// depends on an extension.
func isGNOME() bool {
	return strings.Contains(strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP")), "gnome")
}
