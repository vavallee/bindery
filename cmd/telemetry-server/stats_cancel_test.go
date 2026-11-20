package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// writeSpy is an http.ResponseWriter that records whether a handler wrote
// anything at all, status or body. httptest.ResponseRecorder reports 200 for
// a handler that never wrote, which cannot tell "wrote nothing" from "wrote
// a 200".
type writeSpy struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *writeSpy) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *writeSpy) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *writeSpy) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

// captureSlog routes the default logger into a buffer at Debug level for the
// rest of the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &logs
}

// statsHandlers are the two endpoints that run computeStats per request.
func statsHandlers(s *server) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/stats":     s.handleStatsPage,
		"/api/stats": s.handleStats,
	}
}

// A visitor who closes the tab (or a request whose deadline passes) while
// the dashboard queries run is not a server fault. The handler must stay
// quiet at ERROR and must not write a 500 nobody will read.
func TestStatsHandlers_ClientGoneIsNotAnError(t *testing.T) {
	contexts := map[string]func() (context.Context, context.CancelFunc){
		"canceled": func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		},
		"deadline exceeded": func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		},
	}
	for ctxName, mkCtx := range contexts {
		s := newTestServer(t, "v1.9.5")
		s.statsToken = "s3cret"
		for path, h := range statsHandlers(s) {
			t.Run(ctxName+" "+path, func(t *testing.T) {
				logs := captureSlog(t)
				ctx, cancel := mkCtx()
				defer cancel()
				req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer s3cret")
				w := &writeSpy{}
				h(w, req)

				if w.status != 0 || w.body.Len() != 0 {
					t.Errorf("handler wrote status %d with %d body bytes, want nothing written", w.status, w.body.Len())
				}
				out := logs.String()
				if strings.Contains(out, "level=ERROR") {
					t.Errorf("client gone logged at ERROR:\n%s", out)
				}
				if !strings.Contains(out, "level=DEBUG") {
					t.Errorf("client gone not logged at DEBUG:\n%s", out)
				}
			})
		}
	}
}

// A real query failure on a live request is still a server fault: ERROR log
// and a 500.
func TestStatsHandlers_DBErrorStillLogsError(t *testing.T) {
	s := newTestServer(t, "v1.9.5")
	s.statsToken = "s3cret"
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	for path, h := range statsHandlers(s) {
		t.Run(path, func(t *testing.T) {
			logs := captureSlog(t)
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer s3cret")
			w := &writeSpy{}
			h(w, req)

			if w.status != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.status)
			}
			if !strings.Contains(w.body.String(), "internal error") {
				t.Errorf("body = %q, want the internal error message", w.body.String())
			}
			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("DB error not logged at ERROR:\n%s", logs.String())
			}
		})
	}
}
