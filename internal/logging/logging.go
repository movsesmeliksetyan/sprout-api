// Package logging builds the process logger and carries request-scoped
// fields through a context.
package logging

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey struct{}

// New returns a JSON logger writing to w. Records logged with a context
// (InfoContext and friends) also carry the fields attached to it by With.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(&contextHandler{Handler: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

// With returns a context carrying attrs in addition to any already attached.
// An attr whose key is already present replaces the earlier one.
func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	existing := fromContext(ctx)
	merged := make([]slog.Attr, 0, len(existing)+len(attrs))
	merged = append(merged, existing...)
	for _, attr := range attrs {
		replaced := false
		for i := range merged {
			if merged[i].Key == attr.Key {
				merged[i] = attr
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, attr)
		}
	}
	return context.WithValue(ctx, ctxKey{}, merged)
}

func fromContext(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	return attrs
}

type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs := fromContext(ctx); len(attrs) > 0 {
		r = r.Clone()
		r.AddAttrs(attrs...)
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}
