package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// coverRouter mounts the cover route through the same body middleware main
// puts at the router root, so a body cap that the route forgets to raise shows
// up here. The handler's own unit tests call UploadCover directly and never
// see the router wide 1 MiB cap.
func coverRouter(t *testing.T) (http.Handler, int64) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	h := api.NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).WithCoverStore(covers.NewStore(t.TempDir()))
	r := chi.NewRouter()
	r.Use(api.PreserveRawBody)
	r.Use(api.MaxRequestBody)
	registerBookCoverRoutes(r, h)
	return r, book.ID
}

func postCover(r http.Handler, id int64, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/book/"+strconv.FormatInt(id, 10)+"/cover", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func pngOfSize(n int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n-8)...)
}

func TestCoverRoute_BodyCaps(t *testing.T) {
	r, id := coverRouter(t)

	if rec := postCover(r, id, pngOfSize(500<<10)); rec.Code != http.StatusOK {
		t.Fatalf("500 KiB png: status %d: %s", rec.Code, rec.Body.String())
	}
	// Real covers are a few MB, well past the router default of 1 MiB.
	if rec := postCover(r, id, pngOfSize(2<<20)); rec.Code != http.StatusOK {
		t.Fatalf("2 MiB png: status %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postCover(r, id, pngOfSize(covers.MaxBytes)); rec.Code != http.StatusOK {
		t.Fatalf("png at the cover limit: status %d: %s", rec.Code, rec.Body.String())
	}

	rec := postCover(r, id, pngOfSize(covers.MaxBytes+1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the limit: status %d, want 413", rec.Code)
	}
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e["error"], "10 MB") {
		t.Errorf("413 message %q should name the 10 MB cover limit", e["error"])
	}
	// Far over the limit is cut off by the route cap, with the same message.
	rec = postCover(r, id, pngOfSize(covers.MaxBytes+(1<<20)))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "10 MB") {
		t.Fatalf("far over the limit: status %d body %s", rec.Code, rec.Body.String())
	}

	// A large body that is not an image is still a 415.
	if rec := postCover(r, id, bytes.Repeat([]byte("x"), 2<<20)); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("2 MiB non-image: status %d, want 415", rec.Code)
	}
}

// TestAuthorPhotoRoute_BodyCaps holds the author photo route (#3287) to the
// same body caps as the cover route, through the router wide middleware.
func TestAuthorPhotoRoute_BodyCaps(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authors := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "nb:author:1", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "nb"}
	if err := authors.Create(context.Background(), author); err != nil {
		t.Fatal(err)
	}
	h := api.NewAuthorHandler(authors, nil, db.NewBookRepo(database), nil, nil, nil, db.NewMetadataProfileRepo(database), nil).
		WithCoverStore(covers.NewStore(t.TempDir()))
	r := chi.NewRouter()
	r.Use(api.PreserveRawBody)
	r.Use(api.MaxRequestBody)
	registerAuthorPhotoRoutes(r, h)
	post := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/author/"+strconv.FormatInt(author.ID, 10)+"/photo", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(pngOfSize(2 << 20)); rec.Code != http.StatusOK {
		t.Fatalf("2 MiB png: status %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(pngOfSize(covers.MaxBytes)); rec.Code != http.StatusOK {
		t.Fatalf("png at the limit: status %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(pngOfSize(covers.MaxBytes + (1 << 20))); rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "10 MB") {
		t.Fatalf("far over the limit: status %d body %s", rec.Code, rec.Body.String())
	}
	// The uploaded photo can be cleared through the same route.
	req := httptest.NewRequest(http.MethodDelete, "/author/"+strconv.FormatInt(author.ID, 10)+"/photo", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := authors.GetByID(context.Background(), author.ID); got.ImageURL != "" || got.ImageSource != nil {
		t.Errorf("after clearing: %q from %+v", got.ImageURL, got.ImageSource)
	}
}
