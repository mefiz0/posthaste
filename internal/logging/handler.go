package logging

import (
	"context"
	"log/slog"
)

// scrubbingHandler wraps another handler and applies capture-time scrubbing:
// attributes whose key is sensitive are dropped, string-shaped values in the
// remaining attributes are redacted, and the message itself is redacted.
// Level, time, source, and group structure pass through unchanged. Because
// scrubbing happens here, at the single point where a record becomes a log
// line, there is no path for an unscrubbed value to reach the file.
type scrubbingHandler struct {
	inner slog.Handler
}

// newScrubbingHandler wraps inner so every record it emits is scrubbed.
func newScrubbingHandler(inner slog.Handler) slog.Handler {
	return scrubbingHandler{inner: inner}
}

// Enabled reports whether the wrapped handler would emit at level l.
func (h scrubbingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle scrubs the record's message and attributes, then forwards it.
func (h scrubbingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, ScrubString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if scrubbed, ok := scrubAttr(a); ok {
			clean.AddAttrs(scrubbed)
		}
		return true
	})
	return h.inner.Handle(ctx, clean)
}

// WithAttrs scrubs the attributes being attached so preformatted fields
// (logger.With) get the same treatment as per-call ones.
func (h scrubbingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if scrubbed, ok := scrubAttr(a); ok {
			cleaned = append(cleaned, scrubbed)
		}
	}
	return scrubbingHandler{inner: h.inner.WithAttrs(cleaned)}
}

// WithGroup forwards group scoping unchanged; key dropping happens per
// attribute, so nesting under groups cannot bypass it.
func (h scrubbingHandler) WithGroup(name string) slog.Handler {
	return scrubbingHandler{inner: h.inner.WithGroup(name)}
}
