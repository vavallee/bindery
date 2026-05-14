package db

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/logbuf"
)

func TestLogSlogHandler_MirrorsToDBAndRing(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	repo := NewLogRepo(database)
	ring := logbuf.New(100)
	ring.SetLevel(slog.LevelDebug)

	dbHandler := NewLogSlogHandler(repo, slog.LevelDebug)
	logger := slog.New(logbuf.NewTee(ring, dbHandler))

	logger.Info("test message", "component", "api", "key", "value")

	// Give the async drain goroutine time to flush.
	deadline := time.Now().Add(2 * time.Second)
	var entries []LogEntry
	for time.Now().Before(deadline) {
		entries, err = repo.Query(context.Background(), LogFilter{Limit: 10})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(entries) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(entries) == 0 {
		t.Fatal("expected at least 1 DB entry, got 0")
	}
	if entries[0].Message != "test message" {
		t.Errorf("unexpected message %q", entries[0].Message)
	}
	if entries[0].Component != "api" {
		t.Errorf("unexpected component %q", entries[0].Component)
	}
	if entries[0].Fields["key"] != "value" {
		t.Errorf("unexpected Fields[key] %q", entries[0].Fields["key"])
	}

	// Ring buffer should also have the entry.
	snap := ring.Snapshot(slog.LevelDebug, 10)
	if len(snap) == 0 {
		t.Error("expected ring buffer to also have the entry")
	}
}

func TestLogHandlerStop(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	repo := NewLogRepo(database)
	h := NewLogSlogHandler(repo, slog.LevelDebug)

	const n = 25
	for i := 0; i < n; i++ {
		rec := slog.NewRecord(time.Now(), slog.LevelInfo, "stop-test", 0)
		rec.AddAttrs(slog.String("component", "stoptest"))
		if err := h.Handle(context.Background(), rec); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Stop(ctx); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	// Verify all entries were persisted.
	entries, err := repo.Query(context.Background(), LogFilter{Limit: n + 5})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(entries) != n {
		t.Errorf("expected %d entries persisted, got %d", n, len(entries))
	}

	// Calling Handle after Stop must not panic — record is dropped.
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "after-stop", 0)
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Errorf("Handle after Stop: unexpected error %v", err)
	}

	// Stop on a child handler returned by WithAttrs is a no-op (does not
	// re-close the channel).
	child := h.WithAttrs([]slog.Attr{slog.String("k", "v")}).(*LogHandler)
	if err := child.Stop(context.Background()); err != nil {
		t.Errorf("child.Stop returned error: %v", err)
	}
}

func TestLogHandler_SetLevelAndLevel(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	h := NewLogSlogHandler(NewLogRepo(database), slog.LevelInfo)
	defer h.Stop(context.Background())

	if h.Level() != slog.LevelInfo {
		t.Errorf("initial level: want INFO, got %s", h.Level())
	}

	h.SetLevel(slog.LevelDebug)

	if h.Level() != slog.LevelDebug {
		t.Errorf("after SetLevel: want DEBUG, got %s", h.Level())
	}
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("Enabled(DEBUG) should be true after SetLevel(DEBUG)")
	}
}

func TestLogHandler_SetLevelPropagatestoWithAttrsChild(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	h := NewLogSlogHandler(NewLogRepo(database), slog.LevelInfo)
	defer h.Stop(context.Background())

	child := h.WithAttrs([]slog.Attr{slog.String("component", "test")})

	// Child starts at parent's level.
	if child.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("child should not be enabled for DEBUG before SetLevel")
	}

	// Changing parent level propagates to child via shared atomic.
	h.SetLevel(slog.LevelDebug)
	if !child.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("child should be enabled for DEBUG after parent SetLevel(DEBUG)")
	}

	h.SetLevel(slog.LevelWarn)
	if child.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("child should not be enabled for INFO after parent SetLevel(WARN)")
	}
}

func TestLogSlogHandler_DropsOnFullChannel(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	repo := NewLogRepo(database)
	h := NewLogSlogHandler(repo, slog.LevelInfo)

	// Flood the handler — should never block even when channel is full.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 2000 {
			rec := slog.NewRecord(time.Now(), slog.LevelInfo, "flood", 0)
			_ = i
			_ = h.Handle(context.Background(), rec)
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler blocked — should be non-blocking")
	}
}
