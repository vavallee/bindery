package calibre

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPluginClient_AddHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/books" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42,"duplicate":false}`))
	}))
	defer srv.Close()

	c := NewPluginClient(srv.URL, "test-key")
	id, err := c.Add(context.Background(), "/library/book.epub")
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
}

func TestPluginClient_Add503RetrySucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"library swap"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7}`))
	}))
	defer srv.Close()

	c := NewPluginClient(srv.URL, "k")
	// Shrink retry sleep for test speed by using a tight context deadline,
	// but long enough to allow the 2s sleep + second request.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := c.Add(ctx, "/a.epub")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls, got %d", got)
	}
}

func TestPluginClient_Add401ReturnsDescriptiveError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := NewPluginClient(srv.URL, "bad")
	_, err := c.Add(context.Background(), "/a.epub")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("error = %v, want it to mention authentication failure", err)
	}
}

func TestPluginClient_Health(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"plugin_version":"0.1.0","calibre_version":"9.7","library":"/books"}`))
	}))
	defer srv.Close()

	c := NewPluginClient(srv.URL, "k")
	got, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !strings.Contains(got, "0.1.0") || !strings.Contains(got, "9.7") {
		t.Errorf("version string = %q", got)
	}
}

func TestPluginClient_TrimsTrailingSlash(t *testing.T) {
	c := NewPluginClient("http://example.com/", "k")
	if strings.HasSuffix(c.baseURL, "/") {
		t.Errorf("baseURL should be trimmed: %q", c.baseURL)
	}
}
