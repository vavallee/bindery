package db

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
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

// sendPaced feeds n numbered records through Handle without ever overflowing
// the 512 slot channel, so a test can reason about exact row counts.
func sendPaced(t *testing.T, h *LogHandler, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		for len(h.ch) > logHandlerBufSize/2 {
			time.Sleep(time.Millisecond)
		}
		rec := slog.NewRecord(time.Now(), slog.LevelInfo, fmt.Sprintf("m-%04d", i), 0)
		if err := h.Handle(context.Background(), rec); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}
}

func stopHandler(t *testing.T, h *LogHandler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func openLogTestDB(t *testing.T) *LogRepo {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewLogRepo(database)
}

func countLogs(t *testing.T, repo *LogRepo, where string) int {
	t.Helper()
	var n int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM logs ` + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 1,000 records must cost a bounded number of transactions, not 1,000.
func TestLogHandler_BatchesIntoFewTransactions(t *testing.T) {
	repo := openLogTestDB(t)
	// maxWait is long so only the row limit closes batches: the count is exact.
	h := newLogHandler(repo, slog.LevelDebug, 100, time.Minute)

	sendPaced(t, h, 1000)
	stopHandler(t, h)

	if n := countLogs(t, repo, ""); n != 1000 {
		t.Fatalf("persisted %d rows, want 1000", n)
	}
	// One transaction per 100 rows; allow slack for how the tail splits, but it
	// must stay far below one per record.
	if got := h.batches.Load(); got < 10 || got > 20 {
		t.Errorf("1000 records took %d transactions, want 10 to 20", got)
	}
}

// Rows land in the order they were logged.
func TestLogHandler_PreservesOrder(t *testing.T) {
	repo := openLogTestDB(t)
	h := newLogHandler(repo, slog.LevelDebug, 7, time.Minute)

	sendPaced(t, h, 250)
	stopHandler(t, h)

	rows, err := repo.db.Query(`SELECT message FROM logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("m-%04d", i); m != want {
			t.Fatalf("row %d is %q, want %q", i, m, want)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i != 250 {
		t.Fatalf("got %d rows, want 250", i)
	}
}

// A partial batch is flushed by Stop even though neither limit was reached.
func TestLogHandler_StopFlushesPartialBatch(t *testing.T) {
	repo := openLogTestDB(t)
	h := newLogHandler(repo, slog.LevelDebug, 100, time.Hour)

	sendPaced(t, h, 5)
	stopHandler(t, h)

	if n := countLogs(t, repo, ""); n != 5 {
		t.Errorf("Stop left %d rows, want 5", n)
	}
}

// The wait limit writes a small batch without Stop or a full buffer.
func TestLogHandler_FlushesAfterMaxWait(t *testing.T) {
	repo := openLogTestDB(t)
	h := newLogHandler(repo, slog.LevelDebug, 100, 40*time.Millisecond)
	defer stopHandler(t, h)

	sendPaced(t, h, 3)
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := countLogs(t, repo, "")
		if n == 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 3 rows written after 3s with a 40ms wait limit", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// While a batch is waiting to fill, the single pooled connection must stay
// free for everyone else.
func TestLogHandler_DoesNotHoldConnectionWhileWaiting(t *testing.T) {
	repo := openLogTestDB(t)
	h := newLogHandler(repo, slog.LevelDebug, 100, time.Hour)
	defer stopHandler(t, h)

	sendPaced(t, h, 1)
	time.Sleep(50 * time.Millisecond) // let drain take the entry and start waiting

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var one int
	if err := repo.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("connection busy while the batch waits: %v", err)
	}
}

// A row the database rejects must not take the rest of its batch with it, and
// the failure is reported once on stderr.
func TestLogHandler_FailedBatchIsSalvagedAndReportedOnce(t *testing.T) {
	repo := openLogTestDB(t)
	if _, err := repo.db.Exec(`CREATE TRIGGER reject_bad BEFORE INSERT ON logs
		WHEN NEW.message LIKE 'bad%' BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	var buf lockedBuffer
	old := logStderr
	logStderr = &buf
	defer func() { logStderr = old }()

	// Two batches of five, each with one rejected row: eight rows must be
	// salvaged, and the two failures are one run, so one report.
	h := newLogHandler(repo, slog.LevelDebug, 5, time.Minute)
	for _, m := range []string{"ok-1", "bad-1", "ok-2", "ok-3", "ok-4", "bad-2", "ok-5", "ok-6", "ok-7", "ok-8"} {
		rec := slog.NewRecord(time.Now(), slog.LevelInfo, m, 0)
		_ = h.Handle(context.Background(), rec)
	}
	stopHandler(t, h)

	if n := countLogs(t, repo, `WHERE message LIKE 'ok-%'`); n != 8 {
		t.Errorf("salvaged %d good rows, want 8", n)
	}
	out := buf.String()
	if strings.Count(out, "log writer dropped") != 1 {
		t.Errorf("want exactly one stderr report, got %q", out)
	}
	if !strings.Contains(out, "dropped 1 of 5") {
		t.Errorf("report should say 1 of 5 was lost: %q", out)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// BenchmarkLogWrite compares the old write path (one autocommit per record)
// with the batched one (InsertBatch, 100 per transaction) on a file backed
// database with the production pragmas. ns/op is per record.
func BenchmarkLogWrite(b *testing.B) {
	entry := LogEntry{
		TS: time.Now(), Level: "INFO", Component: "bench", Message: "a representative log line",
		Fields: map[string]string{"method": "GET", "path": "/api/v1/books", "status": "200"},
	}
	open := func(b *testing.B) *LogRepo {
		b.Helper()
		database, err := Open(filepath.Join(b.TempDir(), "bench.db"))
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = database.Close() })
		return NewLogRepo(database)
	}
	b.Run("autocommit_per_row", func(b *testing.B) {
		repo := open(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := repo.Insert(context.Background(), entry); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("batched_100", func(b *testing.B) {
		repo := open(b)
		batch := make([]LogEntry, 100)
		for i := range batch {
			batch[i] = entry
		}
		b.ResetTimer()
		for i := 0; i < b.N; i += len(batch) {
			if err := repo.InsertBatch(context.Background(), batch); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// The logs table is part of every backup, so a logged URL must not reach it
// with an indexer apikey, a token or a password in it.
func TestLogSlogHandler_RedactsSecretsBeforeWrite(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()
	repo := NewLogRepo(database)
	h := NewLogSlogHandler(repo, slog.LevelDebug)
	logger := slog.New(h)

	logger.Info("fetch failed: https://idx.example/api?t=search&apikey=s3cr3tvalue",
		"url", "https://idx.example/dl?token=anothersecret", "client", "http://user:hunter2@host/x")

	deadline := time.Now().Add(3 * time.Second)
	var entries []LogEntry
	for time.Now().Before(deadline) {
		entries, _ = repo.Query(context.Background(), LogFilter{Limit: 10})
		if len(entries) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	all := entries[0].Message
	for _, v := range entries[0].Fields {
		all += " " + v
	}
	for _, secret := range []string{"s3cr3tvalue", "anothersecret", "hunter2"} {
		if strings.Contains(all, secret) {
			t.Errorf("stored log row contains %q: %s", secret, all)
		}
	}
}
