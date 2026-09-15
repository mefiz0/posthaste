//go:build !gtk3

package main

import "errors"

// run is a placeholder for builds without the gtk3 tag: the app shell links
// against GTK3 and WebKit2GTK-4.1, so building it without the tag is never
// meaningful. The core engine in internal/ needs no tag.
func run() error {
	return errors.New("posthaste: build the app shell with -tags gtk3 (GTK3 + WebKit2GTK-4.1)")
}
