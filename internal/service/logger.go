package service

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"

	ksvc "github.com/kardianos/service"
)

// loggerHandler is a slog.Handler that writes to a service.Logger (the Windows
// event log): one entry per record, the level picking Error, Warning or Info.
type loggerHandler struct {
	out   ksvc.Logger
	level slog.Leveler

	mu   *sync.Mutex
	buf  *bytes.Buffer
	text slog.Handler // formats into buf
}

func newLoggerHandler(out ksvc.Logger, level slog.Leveler) *loggerHandler {
	buf := new(bytes.Buffer)
	return &loggerHandler{
		out:   out,
		level: level,
		mu:    new(sync.Mutex),
		buf:   buf,
		text: slog.NewTextHandler(buf, &slog.HandlerOptions{
			Level: slog.LevelDebug,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				// The event log stamps the time and level itself.
				if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
					return slog.Attr{}
				}
				return a
			},
		}),
	}
}

func (h *loggerHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *loggerHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	h.buf.Reset()
	err := h.text.Handle(ctx, r)
	line := strings.TrimSuffix(h.buf.String(), "\n")
	h.mu.Unlock()
	if err != nil {
		return err
	}
	switch {
	case r.Level >= slog.LevelError:
		return h.out.Error(line)
	case r.Level >= slog.LevelWarn:
		return h.out.Warning(line)
	}
	return h.out.Info(line)
}

func (h *loggerHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.text = h.text.WithAttrs(as)
	return &c
}

func (h *loggerHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.text = h.text.WithGroup(name)
	return &c
}
