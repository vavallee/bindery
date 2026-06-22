package audnex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetBook(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/books/B0036S4B2G", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("region"); q != "us" {
			t.Errorf("region query = %q, want us", q)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"asin": "B0036S4B2G",
			"title": "Dune",
			"authors": [{"name":"Frank Herbert"}],
			"narrators": [{"name":"Scott Brick"},{"name":"Simon Vance"}],
			"runtimeLengthMin": 1290,
			"image": "https://example.com/dune.jpg",
			"summary": "Desert planet epic."
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New("us")
	c.baseURL = srv.URL

	b, err := c.GetBook(context.Background(), "B0036S4B2G")
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("expected book, got nil")
		return
	}
	if b.NarratorList() != "Scott Brick, Simon Vance" {
		t.Errorf("NarratorList = %q", b.NarratorList())
	}
	if b.DurationSeconds() != 1290*60 {
		t.Errorf("DurationSeconds = %d", b.DurationSeconds())
	}
}

func TestGetBook404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/books/BNONE", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New("us")
	c.baseURL = srv.URL

	b, err := c.GetBook(context.Background(), "BNONE")
	if err != nil {
		t.Fatal(err)
	}
	if b != nil {
		t.Errorf("expected nil on 404, got %+v", b)
	}
}

// TestGetBook_MalformedBody verifies that a 200 carrying invalid JSON surfaces
// a decode error rather than panicking or silently returning an empty Book.
// audnex parses an untrusted upstream response, so a truncated/garbage body
// must fail loudly.
func TestGetBook_MalformedBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/books/B0BAD", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Truncated JSON object — a decoder must reject this.
		w.Write([]byte(`{`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New("us")
	c.baseURL = srv.URL

	b, err := c.GetBook(context.Background(), "B0BAD")
	if err == nil {
		t.Fatal("expected decode error on malformed JSON body, got nil")
	}
	if b != nil {
		t.Errorf("expected nil book on decode error, got %+v", b)
	}
}

// TestGetBook_ContextCanceled verifies that an already-cancelled context makes
// GetBook return promptly with a context error instead of hanging or returning
// a nil error. No metadata client had a context-cancel test before this.
func TestGetBook_ContextCanceled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/books/B0036S4B2G", func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be reached when the context is already cancelled")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New("us")
	c.baseURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	b, err := c.GetBook(ctx, "B0036S4B2G")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if b != nil {
		t.Errorf("expected nil book on cancellation, got %+v", b)
	}
}
