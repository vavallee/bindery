package db

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const logHandlerBufSize = 512

// LogHandler is a slog.Handler that persists records to the database via a
// buffered channel. If the channel is full the record is silently dropped —
// the handler must never block the request path. DB errors are also silently
// dropped after logging to stderr once.
type LogHandler struct {
	repo     *LogRepo
	ch       chan LogEntry
	preAttrs []slog.Attr
	level    slog.Level
	// wg is non-nil only on the root handler returned by NewLogSlogHandler.
	// Child handlers from WithAttrs share repo+ch but must not own the wg or
	// the close-channel responsibility.
	wg *sync.WaitGroup
}

// NewLogSlogHandler returns a non-blocking slog.Handler backed by repo.
// Call Stop to flush in-flight entries and shut the drain goroutine down.
func NewLogSlogHandler(repo *LogRepo, minLevel slog.Level) *LogHandler {
	h := &LogHandler{
		repo:  repo,
		ch:    make(chan LogEntry, logHandlerBufSize),
		level: minLevel,
		wg:    &sync.WaitGroup{},
	}
	h.wg.Add(1)
	go h.drain()
	return h
}

func (h *LogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *LogHandler) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level < h.level {
		return nil
	}

	// Send on a closed channel panics — even with the default branch in the
	// select below (default fires when the channel is full, not when closed).
	// Stop closes h.ch on shutdown, after which any late Handle call would
	// otherwise crash the process. Recover and drop instead.
	defer func() { _ = recover() }()

	component := ""
	fields := map[string]string{}

	for _, a := range h.preAttrs {
		if a.Key == "component" {
			component = fmt.Sprintf("%v", a.Value.Any())
		} else if a.Key != "" {
			fields[a.Key] = fmt.Sprintf("%v", a.Value.Any())
		}
	}

	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "component" {
			component = fmt.Sprintf("%v", a.Value.Any())
		} else if a.Key != "" {
			fields[a.Key] = fmt.Sprintf("%v", a.Value.Any())
		}
		return true
	})

	e := LogEntry{
		TS:        rec.Time,
		Level:     rec.Level.String(),
		Component: component,
		Message:   rec.Message,
		Fields:    fields,
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}

	// Non-blocking send — drop if full.
	select {
	case h.ch <- e:
	default:
	}
	return nil
}

func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := append(append([]slog.Attr{}, h.preAttrs...), attrs...)
	// Intentionally do NOT propagate wg: only the root handler owns shutdown.
	return &LogHandler{repo: h.repo, ch: h.ch, preAttrs: combined, level: h.level}
}

func (h *LogHandler) WithGroup(_ string) slog.Handler {
	return h
}

func (h *LogHandler) drain() {
	if h.wg != nil {
		defer h.wg.Done()
	}
	for e := range h.ch {
		_ = h.repo.Insert(context.Background(), e)
	}
}

// Stop closes the log channel and blocks until the drain goroutine has
// flushed any in-flight entries. Safe to call once on the root handler;
// calling on a child returned by WithAttrs is a no-op. ctx bounds the wait;
// if it expires before drain finishes, Stop returns ctx.Err and the
// remaining entries are abandoned.
func (h *LogHandler) Stop(ctx context.Context) error {
	if h.wg == nil {
		// Child handler — no shutdown responsibility.
		return nil
	}
	close(h.ch)
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
