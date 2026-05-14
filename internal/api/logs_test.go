package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/logbuf"
)

func logHandlerFixture(t *testing.T) (*LogHandler, *logbuf.Ring) {
	t.Helper()
	ring := logbuf.New(100)
	return NewLogHandler(ring), ring
}

func logHandlerWithDB(t *testing.T) (*LogHandler, *logbuf.Ring, *db.LogRepo) {
	t.Helper()
	ring := logbuf.New(100)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewLogRepo(database)
	h := NewLogHandler(ring).WithLogRepo(repo)
	return h, ring, repo
}

func seedRecord(ring *logbuf.Ring, level slog.Level, msg string, attrs ...slog.Attr) {
	rec := slog.NewRecord(time.Now(), level, msg, 0)
	rec.AddAttrs(attrs...)
	_ = ring.Handle(context.Background(), rec)
}

func insertDBEntry(t *testing.T, repo *db.LogRepo, ts time.Time, level, component, msg string) {
	t.Helper()
	if err := repo.Insert(context.Background(), db.LogEntry{
		TS:        ts,
		Level:     level,
		Component: component,
		Message:   msg,
	}); err != nil {
		t.Fatalf("insert db log: %v", err)
	}
}

// --- Ring buffer tests (no DB) ---

func TestLogList_Empty(t *testing.T) {
	h, _ := logHandlerFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/system/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []logbuf.Entry
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(out))
	}
}

func TestLogList_ReturnsEntries(t *testing.T) {
	h, ring := logHandlerFixture(t)
	for _, msg := range []string{"alpha", "beta", "gamma"} {
		seedRecord(ring, slog.LevelInfo, msg)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/system/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []logbuf.Entry
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(out))
	}
}

func TestLogList_LevelFilter(t *testing.T) {
	h, ring := logHandlerFixture(t)
	ring.SetLevel(slog.LevelDebug)
	seedRecord(ring, slog.LevelDebug, "debug-msg")
	seedRecord(ring, slog.LevelInfo, "info-msg")
	seedRecord(ring, slog.LevelWarn, "warn-msg")
	seedRecord(ring, slog.LevelError, "error-msg")

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/system/logs?level=warn", nil))
	var out []logbuf.Entry
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries >= WARN, got %d", len(out))
	}
	for _, e := range out {
		if e.Level != "WARN" && e.Level != "ERROR" {
			t.Errorf("unexpected level %q in filtered result", e.Level)
		}
	}
}

func TestLogList_LimitParam(t *testing.T) {
	h, ring := logHandlerFixture(t)
	for range 50 {
		seedRecord(ring, slog.LevelInfo, "x")
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/system/logs?limit=5", nil))
	var out []logbuf.Entry
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 5 {
		t.Fatalf("expected 5, got %d", len(out))
	}
}

func TestLogList_LimitCappedAt1000(t *testing.T) {
	h, ring := logHandlerFixture(t)
	for range 100 {
		seedRecord(ring, slog.LevelInfo, "x")
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/system/logs?limit=9999", nil))
	var out []logbuf.Entry
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) > 1000 {
		t.Fatalf("limit not capped: got %d", len(out))
	}
}

// --- DB-backed handler tests ---

func TestLogList_DB_ByComponent(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	now := time.Now().UTC()
	insertDBEntry(t, repo, now.Add(-time.Minute), "INFO", "scheduler", "job started")
	insertDBEntry(t, repo, now, "INFO", "downloader", "download ok")

	req := httptest.NewRequest(http.MethodGet, "/system/logs?component=scheduler", nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []db.LogEntry
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 {
		t.Fatalf("expected 1 scheduler entry, got %d", len(out))
	}
	if out[0].Component != "scheduler" {
		t.Errorf("unexpected component %q", out[0].Component)
	}
}

func TestLogList_DB_DateRange(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	base := time.Now().UTC()
	insertDBEntry(t, repo, base.Add(-10*time.Minute), "INFO", "", "old")
	insertDBEntry(t, repo, base.Add(-5*time.Minute), "INFO", "", "mid")
	insertDBEntry(t, repo, base, "INFO", "", "new")

	from := base.Add(-6 * time.Minute).Format(time.RFC3339)
	to := base.Add(-4 * time.Minute).Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet, "/system/logs?from="+from+"&to="+to, nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []db.LogEntry
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 {
		t.Fatalf("expected 1 entry in range, got %d", len(out))
	}
	if out[0].Message != "mid" {
		t.Errorf("unexpected message %q", out[0].Message)
	}
}

func TestLogList_DB_Search(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	now := time.Now().UTC()
	insertDBEntry(t, repo, now.Add(-2*time.Second), "INFO", "", "book download started")
	insertDBEntry(t, repo, now, "ERROR", "", "book download failed")

	req := httptest.NewRequest(http.MethodGet, "/system/logs?q=download&from="+now.Add(-time.Hour).Format(time.RFC3339), nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []db.LogEntry
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries matching 'download', got %d", len(out))
	}
}

// --- Level / SetLevel tests ---

func TestLogSetLevel_Valid(t *testing.T) {
	h, ring := logHandlerFixture(t)
	body := bytes.NewBufferString(`{"level":"debug"}`)
	rec := httptest.NewRecorder()
	h.SetLevel(rec, httptest.NewRequest(http.MethodPut, "/system/loglevel", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ring.Level() != slog.LevelDebug {
		t.Errorf("expected ring level=DEBUG, got %s", ring.Level())
	}
}

func TestLogSetLevel_Invalid(t *testing.T) {
	h, _ := logHandlerFixture(t)
	body := bytes.NewBufferString(`{}`)
	rec := httptest.NewRecorder()
	h.SetLevel(rec, httptest.NewRequest(http.MethodPut, "/system/loglevel", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestLogGetLevel(t *testing.T) {
	h, ring := logHandlerFixture(t)
	ring.SetLevel(slog.LevelWarn)
	rec := httptest.NewRecorder()
	h.GetLevel(rec, httptest.NewRequest(http.MethodGet, "/system/loglevel", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out map[string]string
	json.NewDecoder(rec.Body).Decode(&out)
	if out["level"] != "WARN" {
		t.Errorf("expected WARN, got %q", out["level"])
	}
}

func TestLogSetLevel_PropagatestoDBHandler(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ring := logbuf.New(100)
	dbHandler := db.NewLogSlogHandler(db.NewLogRepo(database), slog.LevelInfo)
	t.Cleanup(func() { dbHandler.Stop(context.Background()) })

	h := NewLogHandler(ring).WithDBLogHandler(dbHandler)

	body := bytes.NewBufferString(`{"level":"debug"}`)
	rec := httptest.NewRecorder()
	h.SetLevel(rec, httptest.NewRequest(http.MethodPut, "/system/loglevel", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ring.Level() != slog.LevelDebug {
		t.Errorf("ring level: want DEBUG, got %s", ring.Level())
	}
	if dbHandler.Level() != slog.LevelDebug {
		t.Errorf("dbHandler level: want DEBUG, got %s", dbHandler.Level())
	}
}
