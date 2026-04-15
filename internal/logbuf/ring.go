// Package logbuf provides an in-process ring buffer for slog records so the
// UI can surface recent log entries without requiring shell access.
package logbuf

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultCapacity = 1000

// Entry is a single log record stored in the ring buffer.
type Entry struct {
	Time  time.Time         `json:"time"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Ring is a fixed-capacity circular buffer of log entries. It implements
// slog.Handler so it can be attached directly to the slog pipeline.
// Oldest entries are silently dropped when the buffer is full.
type Ring struct {
	mu       sync.RWMutex
	entries  []Entry
	head     int // index of the next write position
	count    int // number of valid entries (≤ cap)
	capacity int

	// levelVar gates which records are stored. Changing it at runtime
	// allows the UI to switch between info/debug without restarting.
	levelVar atomic.Int64

	// preAttrs are attributes inherited via WithAttrs — stored so that
	// sub-handlers created by slog can be wrapped cheaply.
	preAttrs []slog.Attr
}

// New returns a Ring with the given capacity.
func New(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	r := &Ring{
		entries:  make([]Entry, capacity),
		capacity: capacity,
	}
	r.levelVar.Store(int64(slog.LevelInfo))
	return r
}

// SetLevel changes the minimum level that is stored in the ring.
// Levels below this threshold are passed through to the tee target but
// not buffered.
func (r *Ring) SetLevel(l slog.Level) {
	r.levelVar.Store(int64(l))
}

// Level returns the current minimum level.
func (r *Ring) Level() slog.Level {
	return slog.Level(r.levelVar.Load())
}

// Snapshot returns a copy of all buffered entries in chronological order
// (oldest first). The optional minLevel filter excludes entries below the
// given level; pass slog.LevelDebug-1 to include everything.
func (r *Ring) Snapshot(minLevel slog.Level, limit int) []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	total := r.count
	if total == 0 {
		return nil
	}

	// Walk entries from oldest to newest.
	start := 0
	if r.count == r.capacity {
		start = r.head // head points at the oldest when buffer is full
	}

	var out []Entry
	for i := range total {
		idx := (start + i) % r.capacity
		e := r.entries[idx]
		if slog.Level(levelValue(e.Level)) < minLevel {
			continue
		}
		out = append(out, e)
	}

	// Apply limit (take the most recent `limit` entries).
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// slog.Handler implementation ------------------------------------------------

func (r *Ring) Enabled(_ context.Context, level slog.Level) bool {
	return level >= r.Level()
}

func (r *Ring) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level < r.Level() {
		return nil
	}

	e := Entry{
		Time:  rec.Time,
		Level: rec.Level.String(),
		Msg:   rec.Message,
	}

	// Collect pre-attrs + record attrs.
	var attrs map[string]string
	addAttr := func(a slog.Attr) bool {
		if a.Key == "" {
			return true
		}
		if attrs == nil {
			attrs = make(map[string]string)
		}
		attrs[a.Key] = fmt.Sprintf("%v", a.Value.Any())
		return true
	}
	for _, a := range r.preAttrs {
		addAttr(a)
	}
	rec.Attrs(addAttr)
	e.Attrs = attrs

	r.mu.Lock()
	r.entries[r.head] = e
	r.head = (r.head + 1) % r.capacity
	if r.count < r.capacity {
		r.count++
	}
	r.mu.Unlock()
	return nil
}

func (r *Ring) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &attrHandler{ring: r, extra: append(append([]slog.Attr{}, r.preAttrs...), attrs...)}
}

func (r *Ring) WithGroup(name string) slog.Handler {
	return &attrHandler{ring: r, group: name, extra: r.preAttrs}
}

// attrHandler wraps a Ring to inject additional attrs from WithAttrs /
// WithGroup without allocating a new ring.
type attrHandler struct {
	ring  *Ring
	extra []slog.Attr
	group string
}

func (h *attrHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.ring.Enabled(ctx, l)
}

func (h *attrHandler) Handle(ctx context.Context, rec slog.Record) error {
	// Prepend our extra attrs.
	r2 := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	for _, a := range h.extra {
		r2.AddAttrs(a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		r2.AddAttrs(a)
		return true
	})
	return h.ring.Handle(ctx, r2)
}

func (h *attrHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &attrHandler{
		ring:  h.ring,
		extra: append(append([]slog.Attr{}, h.extra...), attrs...),
		group: h.group,
	}
}

func (h *attrHandler) WithGroup(name string) slog.Handler {
	return &attrHandler{ring: h.ring, extra: h.extra, group: name}
}

// Tee -----------------------------------------------------------------------

// Tee is a slog.Handler that writes to two handlers simultaneously —
// typically the stdout JSONHandler and the Ring buffer.
type Tee struct {
	a, b slog.Handler
}

// NewTee returns a Handler that forwards each record to both a and b.
func NewTee(a, b slog.Handler) *Tee {
	return &Tee{a: a, b: b}
}

func (t *Tee) Enabled(ctx context.Context, level slog.Level) bool {
	return t.a.Enabled(ctx, level) || t.b.Enabled(ctx, level)
}

func (t *Tee) Handle(ctx context.Context, rec slog.Record) error {
	var errA, errB error
	if t.a.Enabled(ctx, rec.Level) {
		errA = t.a.Handle(ctx, rec.Clone())
	}
	if t.b.Enabled(ctx, rec.Level) {
		errB = t.b.Handle(ctx, rec.Clone())
	}
	if errA != nil {
		return errA
	}
	return errB
}

func (t *Tee) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Tee{a: t.a.WithAttrs(attrs), b: t.b.WithAttrs(attrs)}
}

func (t *Tee) WithGroup(name string) slog.Handler {
	return &Tee{a: t.a.WithGroup(name), b: t.b.WithGroup(name)}
}

// levelValue converts a level string back to a numeric value for filtering.
func levelValue(s string) slog.Level {
	switch s {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
