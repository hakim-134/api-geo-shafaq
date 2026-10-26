package platform

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/getsentry/sentry-go"
)

// Error tracking: every Error-level log line (a panic, a 5xx, a failed job) also goes to
// GlitchTip, which speaks Sentry's protocol. One place, so no handler has to remember to
// report. Off when GLITCHTIP_DSN is empty (local runs, tests).

func setupErrors(dsn, environment, release string) error {
	if dsn == "" {
		return nil
	}
	return sentry.Init(sentry.ClientOptions{Dsn: dsn, Environment: environment, Release: release})
}

// FlushErrors sends what is still queued; binaries call it on the way out.
func FlushErrors() { sentry.Flush(3 * time.Second) }

// errorsHandler passes every record on, and reports the Error ones.
type errorsHandler struct {
	slog.Handler
	attrs []slog.Attr
}

func (h errorsHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		ev := sentry.NewEvent()
		ev.Level = sentry.LevelError
		ev.Message = r.Message
		ev.Timestamp = r.Time
		extra := map[string]any{}
		add := func(a slog.Attr) bool {
			v := a.Value.Resolve().Any()
			if err, ok := v.(error); ok {
				v = err.Error()
			}
			extra[a.Key] = v
			return true
		}
		for _, a := range h.attrs {
			add(a)
		}
		r.Attrs(add)
		// one issue per log message (and route, for failed requests), however the error text
		// varies from game to game
		ev.Fingerprint = []string{r.Message, fmt.Sprint(extra["route"])}
		if route, ok := extra["route"]; ok {
			ev.Message += fmt.Sprintf(" %v → %v", route, extra["status"])
		}
		if err, ok := extra["err"]; ok {
			ev.Message += fmt.Sprintf(": %v", err)
		}
		ev.Contexts = map[string]sentry.Context{"log": extra}
		sentry.CaptureEvent(ev)
	}
	return h.Handler.Handle(ctx, r)
}

func (h errorsHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return errorsHandler{Handler: h.Handler.WithAttrs(as), attrs: append(h.attrs[:len(h.attrs):len(h.attrs)], as...)}
}

func (h errorsHandler) WithGroup(name string) slog.Handler {
	return errorsHandler{Handler: h.Handler.WithGroup(name), attrs: h.attrs}
}
