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

	"github.com/vavallee/bindery/internal/logbuf"
)

func logHandlerFixture(t *testing.T) (*LogHandler, *logbuf.Ring) {
	t.Helper()
	ring := logbuf.New(100)
	return NewLogHandler(ring), ring
}

func seedRecord(ring *logbuf.Ring, level slog.Level, msg string, attrs ...slog.Attr) {
	rec := slog.NewRecord(time.Now(), level, msg, 0)
	rec.AddAttrs(attrs...)
	_ = ring.Handle(context.Background(), rec)
}

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
