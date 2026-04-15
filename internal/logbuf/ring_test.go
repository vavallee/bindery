package logbuf

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func newRecord(level slog.Level, msg string, attrs ...slog.Attr) slog.Record {
	rec := slog.NewRecord(time.Now(), level, msg, 0)
	rec.AddAttrs(attrs...)
	return rec
}

// TestRing_BasicHandle verifies that Handle stores an entry and Snapshot returns it.
func TestRing_BasicHandle(t *testing.T) {
	r := New(10)
	_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, "hello"))

	entries := r.Snapshot(slog.LevelDebug, 0)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Msg != "hello" {
		t.Errorf("want msg %q, got %q", "hello", entries[0].Msg)
	}
	if entries[0].Level != "INFO" {
		t.Errorf("want level INFO, got %q", entries[0].Level)
	}
}

// TestRing_Capacity verifies that writing more entries than capacity keeps only
// the most recent ones (oldest are evicted).
func TestRing_Capacity(t *testing.T) {
	r := New(3)
	for i := range 5 {
		msg := []string{"a", "b", "c", "d", "e"}[i]
		_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, msg))
	}

	entries := r.Snapshot(slog.LevelDebug, 0)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries (capacity), got %d", len(entries))
	}
	// Should be c, d, e in order
	want := []string{"c", "d", "e"}
	for i, e := range entries {
		if e.Msg != want[i] {
			t.Errorf("entry[%d]: want %q, got %q", i, want[i], e.Msg)
		}
	}
}

// TestRing_LevelFilter verifies Snapshot filters by minLevel.
func TestRing_LevelFilter(t *testing.T) {
	r := New(10)
	_ = r.Handle(context.Background(), newRecord(slog.LevelDebug, "dbg"))
	_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, "inf"))
	_ = r.Handle(context.Background(), newRecord(slog.LevelWarn, "wrn"))
	_ = r.Handle(context.Background(), newRecord(slog.LevelError, "err"))

	warnAndAbove := r.Snapshot(slog.LevelWarn, 0)
	if len(warnAndAbove) != 2 {
		t.Fatalf("want 2 entries >= WARN, got %d", len(warnAndAbove))
	}
}

// TestRing_Limit verifies that Snapshot respects the limit parameter.
func TestRing_Limit(t *testing.T) {
	r := New(100)
	for range 50 {
		_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, "x"))
	}
	entries := r.Snapshot(slog.LevelDebug, 10)
	if len(entries) != 10 {
		t.Fatalf("want 10, got %d", len(entries))
	}
}

// TestRing_Attrs verifies that record attributes are captured.
func TestRing_Attrs(t *testing.T) {
	r := New(10)
	_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, "msg",
		slog.String("key", "val"),
	))
	entries := r.Snapshot(slog.LevelDebug, 0)
	if entries[0].Attrs["key"] != "val" {
		t.Errorf("want attrs[key]=val, got %q", entries[0].Attrs["key"])
	}
}

// TestRing_Enabled verifies the level gate.
func TestRing_Enabled(t *testing.T) {
	r := New(10)
	r.SetLevel(slog.LevelWarn)

	if r.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("INFO should not be enabled when level=WARN")
	}
	if !r.Enabled(context.Background(), slog.LevelError) {
		t.Error("ERROR should be enabled when level=WARN")
	}

	// Below-threshold records must not be stored.
	_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, "should not store"))
	if len(r.Snapshot(slog.LevelDebug, 0)) != 0 {
		t.Error("INFO record should not be stored when ring level=WARN")
	}
}

// TestRing_SetLevel verifies that SetLevel takes effect immediately.
func TestRing_SetLevel(t *testing.T) {
	r := New(10)
	r.SetLevel(slog.LevelWarn)
	_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, "before"))
	r.SetLevel(slog.LevelInfo)
	_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, "after"))

	entries := r.Snapshot(slog.LevelDebug, 0)
	if len(entries) != 1 || entries[0].Msg != "after" {
		t.Errorf("want 1 entry 'after', got %v", entries)
	}
}

// TestRing_Concurrent verifies there are no data races under concurrent writes.
func TestRing_Concurrent(t *testing.T) {
	r := New(50)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = r.Handle(context.Background(), newRecord(slog.LevelInfo, "concurrent"))
			}
		}()
	}
	wg.Wait()
	entries := r.Snapshot(slog.LevelDebug, 0)
	if len(entries) > 50 {
		t.Errorf("capacity exceeded: got %d entries", len(entries))
	}
}

// TestTee_BothHandlersReceiveRecords verifies Tee fans out to both targets.
func TestTee_BothHandlersReceiveRecords(t *testing.T) {
	a := New(10)
	b := New(10)
	tee := NewTee(a, b)

	rec := newRecord(slog.LevelInfo, "broadcast")
	if err := tee.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	for _, r := range []*Ring{a, b} {
		entries := r.Snapshot(slog.LevelDebug, 0)
		if len(entries) != 1 || entries[0].Msg != "broadcast" {
			t.Errorf("ring missing entry: got %v", entries)
		}
	}
}

// TestTee_Enabled is true when either handler is enabled.
func TestTee_Enabled(t *testing.T) {
	a := New(10)
	a.SetLevel(slog.LevelError)
	b := New(10)
	b.SetLevel(slog.LevelDebug)
	tee := NewTee(a, b)

	if !tee.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Tee should be enabled for INFO when b accepts DEBUG+")
	}
}

// TestRing_WithAttrs verifies attrs injected via WithAttrs appear on entries.
func TestRing_WithAttrs(t *testing.T) {
	r := New(10)
	child := r.WithAttrs([]slog.Attr{slog.String("service", "bindery")})
	_ = child.Handle(context.Background(), newRecord(slog.LevelInfo, "msg"))

	entries := r.Snapshot(slog.LevelDebug, 0)
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	if entries[0].Attrs["service"] != "bindery" {
		t.Errorf("want service=bindery, got %q", entries[0].Attrs["service"])
	}
}

// TestRing_EmptySnapshot verifies Snapshot returns nil (not a panic) when empty.
func TestRing_EmptySnapshot(t *testing.T) {
	r := New(10)
	entries := r.Snapshot(slog.LevelDebug, 0)
	if entries != nil {
		t.Errorf("want nil for empty ring, got %v", entries)
	}
}
