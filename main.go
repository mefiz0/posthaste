// Posthaste is a fast, reliable, offline-first desktop email client for
// Linux. This package is the Wails app shell: it wires the core mail engine
// (internal/...) to the webview UI. The shell builds against GTK4 and
// WebKitGTK 6.0, the default stack of the pinned Wails release; the core
// engine in internal/ is pure Go and needs no system libraries.
package main

import (
	"log/slog"
	"os"
)

func main() {
	if err := run(); err != nil {
		slog.Error("posthaste: startup failed", "err", err)
		os.Exit(1)
	}
}
