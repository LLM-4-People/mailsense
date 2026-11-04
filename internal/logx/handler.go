package logx

import (
	"context"
	"log/slog"
)

// handler is the slog.Handler behind every logger; it resolves the active
// pipeline per call so reconfiguration applies to existing loggers.
type handler struct {
	component string
	attrs     []slog.Attr // already group-qualified
	group     string      // "a.b." prefix for attributes added later
}

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= active.Load().levels.levelFor(h.component)
}

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	c := active.Load()
	e := Entry{
		Time:      r.Time.UTC(),
		Level:     r.Level.String(),
		Component: h.component,
		Message:   c.redactor.redact(r.Message),
	}
	if len(h.attrs) > 0 || r.NumAttrs() > 0 {
		e.Attrs = map[string]any{}
		for _, a := range h.attrs {
			addAttr(e.Attrs, "", a, c.redactor)
		}
		r.Attrs(func(a slog.Attr) bool {
			addAttr(e.Attrs, h.group, a, c.redactor)
			return true
		})
	}
	c.write(e)
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr(nil), h.attrs...), qualify(h.group, attrs)...)
	return &next
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.group = h.group + name + "."
	return &next
}

func qualify(group string, attrs []slog.Attr) []slog.Attr {
	if group == "" {
		return attrs
	}
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = slog.Attr{Key: group + a.Key, Value: a.Value}
	}
	return out
}
