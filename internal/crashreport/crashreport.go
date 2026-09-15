// Package crashreport captures Go panics at the process boundary and turns
// them into strictly-scrubbed crash reports.
//
// Scrubbing happens at capture: only the panic value and the goroutine stack
// are ever read, and both pass through the same address/token redaction the
// local logger uses. No message content, addresses, subjects, attachment
// names, or credentials are collected, so none can reach a report even by
// accident. Reports are written locally by default; nothing is ever sent
// unless the shell wires a Sink and the user has opted in.
package crashreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/mefiz0/posthaste/internal/logging"
)

// Report is what a crash handler persists. It deliberately carries only the
// panic, its stack, and build/runtime facts.
type Report struct {
	Time    string `json:"time"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Panic   string `json:"panic"`
	Stack   string `json:"stack"`
}

// Sink receives a captured report. It is only ever called after the user has
// opted in. A nil Sink makes the handler write a file under Dir instead.
type Sink func(ctx context.Context, report Report) error

// Options configures Install.
type Options struct {
	// Enabled reports whether the user has opted in. It is read at crash time
	// so toggling the setting applies without reinstalling the handler.
	Enabled func() bool
	// Dir receives report files when no Sink is set.
	Dir string
	// Version is the application version recorded in the report.
	Version string
	// Logger receives the scrubbed panic line, whether or not reporting is
	// enabled. The error is always logged for the user's own troubleshooting.
	Logger *slog.Logger
	// Sink overrides where an enabled report goes. Nil writes a file in Dir.
	Sink Sink
	// Exit terminates the process after a crash; nil uses os.Exit. Injectable
	// for tests.
	Exit func(int)
}

// Handler turns panics into scrubbed reports. Install one at the process
// entry point and call Handle from a defer.
type Handler struct {
	opts Options
	now  func() time.Time
	env  envFacts
}

// envFacts is the build/runtime data copied into every report, resolved once
// at install time.
type envFacts struct {
	os   string
	arch string
}

// Install returns a handler for the given options.
func Install(opts Options) *Handler {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Exit == nil {
		opts.Exit = os.Exit
	}
	return &Handler{
		opts: opts,
		now:  time.Now,
		env:  envFacts{os: runtime.GOOS, arch: runtime.GOARCH},
	}
}

// Capture builds the scrubbed report for a recovered panic value.
func (h *Handler) Capture(recovered any) Report {
	return Report{
		Time:    h.now().UTC().Format(time.RFC3339),
		Version: h.opts.Version,
		OS:      h.env.os,
		Arch:    h.env.arch,
		Panic:   logging.ScrubString(fmt.Sprint(recovered)),
		Stack:   logging.ScrubString(string(debug.Stack())),
	}
}

// Handle recovers a panic, logs it scrubbed, and — only when the user opted
// in — persists a report before exiting. It is meant to be deferred at the
// top of the process entry point.
func (h *Handler) Handle() {
	recovered := recover()
	if recovered == nil {
		return
	}

	report := h.Capture(recovered)
	h.opts.Logger.Error("crash: recovered panic",
		"version", report.Version, "os", report.OS, "arch", report.Arch,
		"panic", report.Panic)

	if h.opts.Enabled != nil && h.opts.Enabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var err error
		if h.opts.Sink != nil {
			err = h.opts.Sink(ctx, report)
		} else {
			err = WriteFile(h.opts.Dir, report)
		}
		if err != nil {
			h.opts.Logger.Error("crash: write report", "err", err)
		}
	}
	h.opts.Exit(1)
}

// WriteFile persists one report as JSON under dir. The file is written
// user-only; the report already contains no message content.
func WriteFile(dir string, report Report) error {
	if dir == "" {
		return errors.New("crashreport: no report directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("crashreport: create dir: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	name := "crash-" + stamp + ".json"
	path := filepath.Join(dir, name)

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("crashreport: encode: %w", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("crashreport: write %s: %w", name, err)
	}
	return nil
}
