// Posthaste is a fast, reliable, offline-first desktop email client for
// Linux. This package is the Wails app shell: it wires the core mail engine
// (internal/...) to the webview UI. The shell builds against GTK3 and
// WebKit2GTK-4.1, so every build of this package must pass -tags gtk3; the
// core engine in internal/ needs no tag.
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
