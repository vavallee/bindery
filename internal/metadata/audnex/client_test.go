package audnex

import (
	"context"
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
