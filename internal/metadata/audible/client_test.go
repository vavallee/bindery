package audible

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestSearchBooksByAuthor(t *testing.T) {
	var gotQuery, gotResponseGroups, gotUA string
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0/catalog/products", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("author")
		gotResponseGroups = r.URL.Query().Get("response_groups")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"products": [
				{
					"asin": "B0036S4B2G",
					"title": "Dune",
					"subtitle": "Dune, Book 1",
					"language": "english",
					"runtime_length_min": 1290,
					"release_date": "2007-08-07",
					"product_images": {"500": "https://example.com/dune-500.jpg", "1024": "https://example.com/dune-1024.jpg"},
					"publisher_summary": "Desert planet epic.",
					"narrators": [{"name": "Scott Brick"}, {"name": "Simon Vance"}],
					"format_type": "unabridged"
				},
				{
					"asin": "B01GXP8A",
					"title": "Der Wüstenplanet",
					"language": "german",
					"release_date": "2019-01-15"
				},
				{
					"asin": "",
					"title": "Missing ASIN skipped"
				},
				{
					"asin": "B999",
					"title": ""
				}
			]
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	c.baseURL = srv.URL

	books, err := c.SearchBooksByAuthor(context.Background(), "Frank Herbert")
	if err != nil {
		t.Fatalf("SearchBooksByAuthor: %v", err)
	}
	if gotQuery != "Frank Herbert" {
		t.Errorf("author query = %q, want %q", gotQuery, "Frank Herbert")
	}
	if gotResponseGroups == "" {
		t.Error("response_groups query param missing")
	}
	if gotUA == "" {
		t.Error("User-Agent header missing")
	}
	if len(books) != 2 {
		t.Fatalf("got %d books, want 2 (third/fourth should be filtered)", len(books))
	}

	dune := books[0]
	if dune.ForeignID != "audible:B0036S4B2G" {
		t.Errorf("ForeignID = %q", dune.ForeignID)
	}
	if dune.Title != "Dune: Dune, Book 1" {
		t.Errorf("Title = %q (subtitle should be appended)", dune.Title)
	}
	if dune.ASIN != "B0036S4B2G" {
		t.Errorf("ASIN = %q", dune.ASIN)
	}
	if dune.MediaType != models.MediaTypeAudiobook {
		t.Errorf("MediaType = %q, want audiobook", dune.MediaType)
	}
	if dune.Language != "eng" {
		t.Errorf("Language = %q, want eng (normalized from 'english')", dune.Language)
	}
	if dune.Narrator != "Scott Brick, Simon Vance" {
		t.Errorf("Narrator = %q", dune.Narrator)
	}
	if dune.DurationSeconds != 1290*60 {
		t.Errorf("DurationSeconds = %d", dune.DurationSeconds)
	}
	if dune.ImageURL != "https://example.com/dune-1024.jpg" {
		t.Errorf("ImageURL = %q, want largest-size", dune.ImageURL)
	}
	if dune.ReleaseDate == nil || dune.ReleaseDate.Year() != 2007 {
		t.Errorf("ReleaseDate = %v", dune.ReleaseDate)
	}
	if dune.MetadataProvider != "audible" {
		t.Errorf("MetadataProvider = %q", dune.MetadataProvider)
	}

	german := books[1]
	if german.Language != "ger" {
		t.Errorf("German book language = %q, want ger", german.Language)
	}
}

func TestSearchBooksByAuthor_EmptyAuthor(t *testing.T) {
	c := New()
	// No HTTP server — an empty author must not hit the network.
	books, err := c.SearchBooksByAuthor(context.Background(), "   ")
	if err != nil {
		t.Fatalf("empty author: %v", err)
	}
	if books != nil {
		t.Errorf("want nil, got %v", books)
	}
}

func TestSearchBooksByAuthor_HTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0/catalog/products", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"down"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	c.baseURL = srv.URL

	_, err := c.SearchBooksByAuthor(context.Background(), "Somebody")
	if err == nil {
		t.Fatal("expected error on HTTP 503")
	}
}

// TestSearchBooksByAuthor_MalformedBody verifies that a 200 carrying invalid
// JSON surfaces a decode error rather than panicking or silently returning an
// empty result set. The catalogue response is untrusted upstream input, so a
// truncated/garbage body must fail loudly.
func TestSearchBooksByAuthor_MalformedBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0/catalog/products", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Truncated JSON object — a decoder must reject this.
		_, _ = w.Write([]byte(`{`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	c.baseURL = srv.URL

	books, err := c.SearchBooksByAuthor(context.Background(), "Somebody")
	if err == nil {
		t.Fatal("expected decode error on malformed JSON body, got nil")
	}
	if books != nil {
		t.Errorf("expected nil books on decode error, got %v", books)
	}
}

// TestSearchBooksByAuthor_ContextCanceled verifies that an already-cancelled
// context makes the call return promptly with a context error instead of
// hanging or returning a nil error. No metadata client had a context-cancel
// test before this.
func TestSearchBooksByAuthor_ContextCanceled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0/catalog/products", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server must not be reached when the context is already cancelled")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	c.baseURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	books, err := c.SearchBooksByAuthor(ctx, "Somebody")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if books != nil {
		t.Errorf("expected nil books on cancellation, got %v", books)
	}
}

func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"english":   "eng",
		"English":   "eng",
		"  GERMAN ": "ger",
		"french":    "fre",
		"":          "",
		"zulu":      "zulu", // unmapped falls through
		"eng":       "eng",  // already a code
	}
	for in, want := range cases {
		if got := normalizeLanguage(in); got != want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickLargestCover(t *testing.T) {
	if got := pickLargestCover(nil); got != "" {
		t.Errorf("nil map: got %q", got)
	}
	got := pickLargestCover(map[string]string{
		"300":  "small.jpg",
		"1024": "big.jpg",
		"500":  "med.jpg",
		"":     "empty-key",
		"bad":  "nonnumeric.jpg",
	})
	if got != "big.jpg" {
		t.Errorf("pickLargestCover = %q, want big.jpg", got)
	}
}
