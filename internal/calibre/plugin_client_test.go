package calibre

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPluginClient_AddHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"plugin_version":"0.5.0","calibre_version":"9.8","library":"/books","capabilities":["book_metadata"]}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/books" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Path     string   `json:"path"`
			Metadata Metadata `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Path != "/library/book.epub" || body.Metadata.Title != "Dune" {
			t.Fatalf("body = %+v, want path and metadata", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42,"duplicate":false}`))
	}))
	defer srv.Close()

	c := NewPluginClient(srv.URL, "test-key")
	id, err := c.Add(context.Background(), "/library/book.epub", Metadata{Title: "Dune"})
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
}

func TestPluginClient_AddOmitsMetadataWhenCapabilityMissing(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"plugin_version":"0.4.0","calibre_version":"9.7","library":"/books"}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/books" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42,"duplicate":false}`))
	}))
	defer srv.Close()

	c := NewPluginClient(srv.URL, "test-key")
	id, err := c.Add(context.Background(), "/library/book.epub", Metadata{Title: "Dune"})
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
	if posted["path"] != "/library/book.epub" {
		t.Fatalf("posted path = %v", posted["path"])
	}
	if _, ok := posted["metadata"]; ok {
		t.Fatalf("metadata should be omitted for old plugin health response: %+v", posted)
	}
}

func TestPluginClient_AddSendsMetadataWhenCapabilityProbeFails(t *testing.T) {
	var posted struct {
		Path     string   `json:"path"`
		Metadata Metadata `json:"metadata"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"library swap"}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/books" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42,"duplicate":false}`))
	}))
	defer srv.Close()

	c := NewPluginClient(srv.URL, "test-key")
	id, err := c.Add(context.Background(), "/library/book.epub", Metadata{Title: "Dune"})
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
	if posted.Path != "/library/book.epub" {
		t.Fatalf("posted path = %q", posted.Path)
	}
	if posted.Metadata.Title != "Dune" {
		t.Fatalf("posted metadata title = %q, want Dune", posted.Metadata.Title)
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
	id, err := c.Add(ctx, "/a.epub", Metadata{})
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
	_, err := c.Add(context.Background(), "/a.epub", Metadata{})
	if err == nil {
		t.Fatal("expected error, got nil")
		return
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

func TestPluginClient_AddRetriesLegacyPayloadWhenMetadataRejected(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"plugin_version":"0.5.0","calibre_version":"9.8","library":"/books","capabilities":["book_metadata"]}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unknown field metadata"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":99}`))
	}))
	defer srv.Close()

	c := NewPluginClient(srv.URL, "k")
	id, err := c.Add(context.Background(), "/a.epub", Metadata{Title: "Dune"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id != 99 {
		t.Fatalf("id = %d, want 99", id)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d, want 2", len(bodies))
	}
	if _, ok := bodies[0]["metadata"]; !ok {
		t.Fatalf("first body missing metadata: %+v", bodies[0])
	}
	if _, ok := bodies[1]["metadata"]; ok {
		t.Fatalf("legacy retry should omit metadata: %+v", bodies[1])
	}
}

func TestPluginClient_TrimsTrailingSlash(t *testing.T) {
	c := NewPluginClient("http://example.com/", "k")
	if strings.HasSuffix(c.baseURL, "/") {
		t.Errorf("baseURL should be trimmed: %q", c.baseURL)
	}
}
