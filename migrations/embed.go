// Package migrations embeds the goose SQL migrations into the binary so the app
// ships as a single artifact with no loose files to install alongside it.
package migrations

import "embed"

// FS holds every goose migration, ordered by filename.
//
//go:embed *.sql
var FS embed.FS
