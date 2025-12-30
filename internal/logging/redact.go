package logging

import (
	"context"
	"log/slog"
	"strings"
)

const redactedValue = "[REDACTED]"

var sensitiveKeys = map[string]struct{}{
	"args":          {},
	"authorization": {},
	"body":          {},
	"errors_json":   {},
	"headers":       {},
	"kwargs":        {},
	"payload":       {},
	"payload_json":  {},
	"return_json":   {},
	"spec_json":     {},
	"stderr":        {},
	"stdout":        {},
	"traceback":     {},
}

var sensitiveFragments = []string{
	"secret",
	"token",
	"password",
	"apikey",
	"api_key",
	"authorization",
}

type redactingHandler struct {
	handler slog.Handler
}

func newRedactingHandler(handler slog.Handler) slog.Handler {
	return &redactingHandler{handler: handler}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.NumAttrs() == 0 {
		return h.handler.Handle(ctx, r)
	}
	redacted := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		redacted.AddAttrs(redactAttr(a))
		return true
	})
	return h.handler.Handle(ctx, redacted)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return &redactingHandler{handler: h.handler.WithAttrs(attrs)}
	}
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		redacted = append(redacted, redactAttr(attr))
	}
	return &redactingHandler{handler: h.handler.WithAttrs(redacted)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{handler: h.handler.WithGroup(name)}
}

func redactAttr(attr slog.Attr) slog.Attr {
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		redacted := make([]slog.Attr, 0, len(group))
		for _, item := range group {
			redacted = append(redacted, redactAttr(item))
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(redacted...)}
	}

	if shouldRedactKey(attr.Key) {
		return slog.String(attr.Key, redactedValue)
	}

	return attr
}

func shouldRedactKey(key string) bool {
	if key == "" {
		return false
	}
	lower := strings.ToLower(key)
	if _, ok := sensitiveKeys[lower]; ok {
		return true
	}
	for _, fragment := range sensitiveFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}
