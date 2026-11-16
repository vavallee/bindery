package bookorbit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(baseURL, fakeKey)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestNormalizeBaseURL(t *testing.T) {
	good := map[string]string{
		"http://bookorbit:3000":             "http://bookorbit:3000",
		" https://books.example.com/ ":      "https://books.example.com",
		"http://192.168.1.20:3000/api/v1/":  "http://192.168.1.20:3000",
		"https://example.com/bookorbit/api": "https://example.com/bookorbit",
		"http://host/sub?x=1#frag":          "http://host/sub",
	}
	for in, want := range good {
		got, err := NormalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "bookorbit:3000", "ftp://host", "http://", "http://user:pw@host"} {
		if got, err := NormalizeBaseURL(bad); err == nil {
			t.Errorf("NormalizeBaseURL(%q) = %q, want an error", bad, got)
		}
	}
}

// A LAN or loopback server is the normal case and must pass, exactly as it
// does for Audiobookshelf. A cloud metadata address must not.
func TestValidateBaseURLAddressPolicy(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:3000", "http://192.168.1.20:3000", "http://10.0.0.5"} {
		if _, err := ValidateBaseURL(ok); err != nil {
			t.Errorf("ValidateBaseURL(%q) refused a LAN address: %v", ok, err)
		}
	}
	if _, err := ValidateBaseURL("http://169.254.169.254/latest"); err == nil {
		t.Error("ValidateBaseURL accepted the cloud metadata address")
	}
}

func TestNormalizeAPIKey(t *testing.T) {
	if _, err := NormalizeAPIKey("  "); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := NormalizeAPIKey("bok_abc\r\nX-Evil: 1"); err == nil {
		t.Error("key with a line break accepted")
	}
	if got, err := NormalizeAPIKey(" bok_abc "); err != nil || got != "bok_abc" {
		t.Errorf("NormalizeAPIKey = %q, %v", got, err)
	}
}

// The key travels in X-Api-Key and nowhere else: not in the URL, where a
// proxy would log it, and not as a second credential, which BookOrbit refuses.
func TestClientSendsKeyOnlyInHeader(t *testing.T) {
	fake := newFakeServer(t)
	var rawQuery string
	inner := fake.Config.Handler
	fake.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		inner.ServeHTTP(w, r)
	})
	client := newTestClient(t, fake.URL)
	me, err := client.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if me.AuthenticationMethod != "api_key" || me.Username != "reader" {
		t.Errorf("Me = %+v", me)
	}
	if fake.lastKey != fakeKey {
		t.Errorf("X-Api-Key = %q", fake.lastKey)
	}
	if fake.lastAuth != "" {
		t.Errorf("Authorization header was also sent: %q", fake.lastAuth)
	}
	if rawQuery != "" {
		t.Errorf("request carried a query string: %q", rawQuery)
	}
}

func TestClientListLibraries(t *testing.T) {
	fake := newFakeServer(t)
	fake.addLibrary(2, "Fantasy", "/books/Fantasy")
	fake.addLibrary(17, "Romantasy", "/books/Romantasy")
	libs, err := newTestClient(t, fake.URL).ListLibraries(context.Background())
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libs) != 2 || libs[1].ID != 17 || libs[1].Name != "Romantasy" || len(libs[1].Folders) != 1 || libs[1].Folders[0] != "/books/Romantasy" {
		t.Fatalf("libraries = %+v", libs)
	}
}

// The listing asks for flat, oldest first pages of a fixed size.
func TestClientListBookIDsQuery(t *testing.T) {
	fake := newFakeServer(t)
	fake.addLibrary(2, "Fantasy", "/books")
	for id := int64(1); id <= 3; id++ {
		fake.addBook(2, fakeBook(id, "Book", []string{"A"}))
	}
	page, err := newTestClient(t, fake.URL).ListBookIDs(context.Background(), 2, 0)
	if err != nil {
		t.Fatalf("ListBookIDs: %v", err)
	}
	if page.Total != 3 || len(page.IDs) != 3 || page.IDs[2] != 3 {
		t.Fatalf("page = %+v", page)
	}
	query := fake.queries[0]
	if query["collapseSeries"] != false || query["collapseLinked"] != false {
		t.Errorf("collapse flags not sent as false: %v", query)
	}
	pagination := query["pagination"].(map[string]any)
	if pagination["page"] != float64(0) || pagination["size"] != float64(PageSize) {
		t.Errorf("pagination = %v", pagination)
	}
	sortSpec := query["sort"].([]any)[0].(map[string]any)
	if sortSpec["field"] != "addedAt" || sortSpec["dir"] != "asc" {
		t.Errorf("sort = %v", sortSpec)
	}
}

func TestClientGetBookReadsWhatTheImporterNeeds(t *testing.T) {
	fake := newFakeServer(t)
	book := withSeries(fakeBook(41, "Foxy Tales", []string{"Caroline Peckham", "Susanne Valenti"},
		fakeFile(1, "/books/Books/Romantasy/Caroline Peckham & Susanne Valenti/Zodiac Academy/4.5 - Foxy Tales/Foxy Tales.epub", "primary"),
		fakeFile(2, "/books/Books/Romantasy/Caroline Peckham & Susanne Valenti/Zodiac Academy/4.5 - Foxy Tales/cover.jpg", "cover"),
	), 9, "Zodiac Academy", "4.5")
	book["isbn13"] = "9781234567897"
	book["providerIds"] = map[string]any{"audible": "B0TESTASIN", "amazon": "B0KINDLE00"}
	book["audioMetadata"] = map[string]any{"durationSeconds": 3600, "narrators": []map[string]any{{"id": 1, "name": "Some Reader"}}}
	// A field no version of this importer has heard of.
	book["holographicEdition"] = map[string]any{"layers": 3}
	fake.addBook(17, book)

	got, err := newTestClient(t, fake.URL).GetBook(context.Background(), 41)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.ID != 41 || got.LibraryID != 17 || got.Title != "Foxy Tales" {
		t.Errorf("identity = %+v", got)
	}
	if len(got.Authors) != 2 || got.Authors[0].Name != "Caroline Peckham" || got.Authors[1].Name != "Susanne Valenti" {
		t.Errorf("authors = %+v", got.Authors)
	}
	if len(got.Series) != 1 || got.Series[0].Name != "Zodiac Academy" || got.Series[0].Index != "4.5" || got.Series[0].ID != 9 {
		t.Errorf("series = %+v", got.Series)
	}
	if got.ISBN13 != "9781234567897" || got.ASIN != "B0TESTASIN" {
		t.Errorf("identifiers: isbn13 %q asin %q", got.ISBN13, got.ASIN)
	}
	if len(got.Files) != 2 || got.Files[0].Role != "primary" || got.Files[0].Format != "epub" || !strings.HasSuffix(got.Files[0].Path, "Foxy Tales.epub") {
		t.Errorf("files = %+v", got.Files)
	}
	if got.DurationSeconds != 3600 || len(got.Narrators) != 1 {
		t.Errorf("audio = %v %v", got.DurationSeconds, got.Narrators)
	}
}

// A series position is text on the servers seen so far. If one sends a number
// instead, the digits must come through unchanged: 4.5 stays 4.5 and 2 stays
// 2, not 2.0.
func TestSeriesIndexSurvivesAsWritten(t *testing.T) {
	for index, want := range map[any]string{"4.5": "4.5", 4.5: "4.5", 2: "2", "10": "10", "0.1": "0.1"} {
		fake := newFakeServer(t)
		fake.addBook(1, withSeries(fakeBook(7, "T", []string{"A"}, fakeFile(1, "/b/t.epub", "primary")), 3, "S", index))
		got, err := newTestClient(t, fake.URL).GetBook(context.Background(), 7)
		if err != nil {
			t.Fatalf("GetBook: %v", err)
		}
		if len(got.Series) != 1 || got.Series[0].Index != want {
			t.Errorf("index %#v read as %+v, want %q", index, got.Series, want)
		}
	}
}

// An older server has no seriesMemberships list, only the three flat fields.
func TestSeriesFallsBackToFlatFields(t *testing.T) {
	fake := newFakeServer(t)
	book := withSeries(fakeBook(7, "T", []string{"A"}, fakeFile(1, "/b/t.epub", "primary")), 3, "Old Style", "2")
	delete(book, "seriesMemberships")
	fake.addBook(1, book)
	got, err := newTestClient(t, fake.URL).GetBook(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if len(got.Series) != 1 || got.Series[0].Name != "Old Style" || got.Series[0].Index != "2" {
		t.Errorf("series = %+v", got.Series)
	}
}

// A record that lost a field the import depends on is an error, never a book
// with no files or no author. Each case removes or breaks one field.
func TestGetBookMissingRequiredFieldIsShapeError(t *testing.T) {
	cases := map[string]func(map[string]any){
		"files missing":         func(b map[string]any) { delete(b, "files") },
		"files null":            func(b map[string]any) { b["files"] = nil },
		"files renamed":         func(b map[string]any) { b["bookFiles"] = b["files"]; delete(b, "files") },
		"authors missing":       func(b map[string]any) { delete(b, "authors") },
		"authors are strings":   func(b map[string]any) { b["authors"] = []string{"A"} },
		"title missing":         func(b map[string]any) { delete(b, "title") },
		"id missing":            func(b map[string]any) { delete(b, "id") },
		"libraryId is a string": func(b map[string]any) { b["libraryId"] = "two" },
		"file path renamed": func(b map[string]any) {
			f := b["files"].([]map[string]any)[0]
			f["path"] = f["absolutePath"]
			delete(f, "absolutePath")
		},
		"file role missing": func(b map[string]any) { delete(b["files"].([]map[string]any)[0], "role") },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			book := fakeBook(7, "T", []string{"A"}, fakeFile(1, "/b/t.epub", "primary"))
			book["libraryId"] = int64(1)
			breakIt(book)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeFake(w, http.StatusOK, book)
			}))
			defer srv.Close()
			_, err := newTestClient(t, srv.URL).GetBook(context.Background(), 7)
			var shape *ShapeError
			if !errors.As(err, &shape) {
				t.Fatalf("err = %v, want a ShapeError", err)
			}
			if !strings.Contains(shape.Error(), "GET /api/v1/books/7") || !strings.Contains(shape.Error(), "nothing was imported") {
				t.Errorf("message does not say where and what happened: %s", shape)
			}
		})
	}
}

// A null title is a book without a title, which BookOrbit allows. It is data.
func TestGetBookNullTitleIsNotAShapeError(t *testing.T) {
	fake := newFakeServer(t)
	book := fakeBook(7, "", []string{"A"}, fakeFile(1, "/b/t.epub", "primary"))
	book["title"] = nil
	fake.addBook(1, book)
	got, err := newTestClient(t, fake.URL).GetBook(context.Background(), 7)
	if err != nil || got.Title != "" {
		t.Fatalf("GetBook = %+v, %v", got, err)
	}
}

// An Amazon id can be a Kindle one. Bindery's ASIN slot means the Audible
// audiobook, so only the Audible id may land there.
func TestGetBookTakesNoASINFromAnAmazonOnlyRecord(t *testing.T) {
	fake := newFakeServer(t)
	fake.addLibrary(17, "Romantasy", "/books")
	book := fakeBook(43, "Kindle Only", []string{"Some Author"}, fakeFile(1, "/books/Kindle Only.epub", "primary"))
	book["providerIds"] = map[string]any{"audible": nil, "amazon": "B0KINDLE00"}
	fake.addBook(17, book)

	got, err := newTestClient(t, fake.URL).GetBook(context.Background(), 43)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.ASIN != "" {
		t.Errorf("ASIN = %q, want none from an Amazon only record", got.ASIN)
	}
}

func TestListBookIDsShapeErrors(t *testing.T) {
	for name, body := range map[string]any{
		"items missing":    map[string]any{"total": 1},
		"total missing":    map[string]any{"items": []any{}},
		"items not a list": map[string]any{"items": "x", "total": 1},
		"item without id":  map[string]any{"items": []any{map[string]any{"title": "x"}}, "total": 1},
		"array, not page":  []any{},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeFake(w, http.StatusOK, body)
			}))
			defer srv.Close()
			_, err := newTestClient(t, srv.URL).ListBookIDs(context.Background(), 2, 0)
			var shape *ShapeError
			if !errors.As(err, &shape) {
				t.Fatalf("err = %v, want a ShapeError", err)
			}
		})
	}
}

func TestClientAPIErrorCarriesCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeFake(w, http.StatusForbidden, map[string]any{"statusCode": 403, "errorCode": "API_KEY_READ_ONLY", "message": "This API key is read only"})
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv.URL).ListLibraries(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 403 || apiErr.Code != "API_KEY_READ_ONLY" {
		t.Fatalf("err = %#v", err)
	}
}

// The key must never appear in an error, whatever the server echoes back.
func TestClientErrorNeverContainsKey(t *testing.T) {
	fake := newFakeServer(t)
	fake.apiKey = "a-different-key"
	_, err := newTestClient(t, fake.URL).ListLibraries(context.Background())
	if err == nil {
		t.Fatal("want an error for a rejected key")
	}
	if strings.Contains(err.Error(), fakeKey) {
		t.Errorf("error leaks the API key: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("err = %v, want a 401 APIError", err)
	}
}

func TestClientRefusesOversizedBody(t *testing.T) {
	old := maxResponseBytes
	maxResponseBytes = 1024
	defer func() { maxResponseBytes = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"name":"` + strings.Repeat("x", 4096) + `"}]`))
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv.URL).ListLibraries(context.Background())
	if err == nil || !strings.Contains(err.Error(), "more than 1024 bytes") {
		t.Fatalf("err = %v, want the size refusal", err)
	}
}

func TestClientRetriesServerErrorsButNotClientErrors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeFake(w, http.StatusOK, []any{})
	}))
	defer srv.Close()
	if _, err := newTestClient(t, srv.URL).ListLibraries(context.Background()); err != nil {
		t.Fatalf("ListLibraries after two 502s: %v", err)
	}
	if hits.Load() != 3 {
		t.Errorf("attempts = %d, want 3", hits.Load())
	}

	hits.Store(0)
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer denied.Close()
	if _, err := newTestClient(t, denied.URL).ListLibraries(context.Background()); err == nil {
		t.Fatal("want an error for 401")
	}
	// A rejected key is counted by BookOrbit's limiter, so it is not retried.
	if hits.Load() != 1 {
		t.Errorf("a 401 was tried %d times, want 1", hits.Load())
	}
}

// net/http strips Authorization when a redirect leaves the host but forwards
// every other header. The key is in X-Api-Key, so the client has to drop it
// itself or a redirect hands the key to whoever it points at.
func TestClientDoesNotSendKeyAcrossARedirectToAnotherHost(t *testing.T) {
	var leaked atomic.Value
	leaked.Store("")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("X-Api-Key"))
		writeFake(w, http.StatusOK, []any{})
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/v1/libraries", http.StatusFound)
	}))
	defer origin.Close()

	if _, err := newTestClient(t, origin.URL).ListLibraries(context.Background()); err != nil {
		t.Fatalf("ListLibraries through a redirect: %v", err)
	}
	if got := leaked.Load().(string); got != "" {
		t.Fatalf("the API key followed a redirect to another host")
	}
}

// A redirect that stays on the same host (http to a path with a slash, say)
// keeps the key, or a server behind such a redirect could never be read.
func TestClientKeepsKeyOnSameHostRedirect(t *testing.T) {
	var got atomic.Value
	got.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/libraries" {
			http.Redirect(w, r, "/api/v1/libraries/", http.StatusTemporaryRedirect)
			return
		}
		got.Store(r.Header.Get("X-Api-Key"))
		writeFake(w, http.StatusOK, []any{})
	}))
	defer srv.Close()
	if _, err := newTestClient(t, srv.URL).ListLibraries(context.Background()); err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if got.Load().(string) != fakeKey {
		t.Error("the key was dropped on a redirect within the same host")
	}
}
