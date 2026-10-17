package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// photoFixture mirrors coverFixture: an author with a photo from its provider
// and a handler with a cover store, plus a helper posting an upload (#3287).
func photoFixture(t *testing.T) (*AuthorHandler, *db.AuthorRepo, *models.Author, func(body []byte) *httptest.ResponseRecorder) {
	t.Helper()
	h, authors, ctx := authorListFixture(t)
	h.WithCoverStore(covers.NewStore(t.TempDir()))
	author := &models.Author{ForeignID: "nb:author:1", Name: "Kari Nordmann", SortName: "Nordmann, Kari",
		MetadataProvider: "nb", ImageURL: "https://example.invalid/old.jpg"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	post := func(body []byte) *httptest.ResponseRecorder {
		id := strconv.FormatInt(author.ID, 10)
		req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/author/"+id+"/photo", bytes.NewReader(body)), "id", id)
		rec := httptest.NewRecorder()
		h.UploadPhoto(rec, req)
		return rec
	}
	return h, authors, author, post
}

func TestUploadPhoto_StoresAndMarksItTheUsers(t *testing.T) {
	h, authors, author, post := photoFixture(t)
	rec := post(pngBytes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := authors.GetByID(t.Context(), author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !covers.IsRef(got.ImageURL) {
		t.Fatalf("image_url = %q, want a stored-cover reference", got.ImageURL)
	}
	if _, ct, ok := h.coverStore.Resolve(got.ImageURL); !ok || ct != "image/png" {
		t.Errorf("stored photo resolves = %v, content type %q", ok, ct)
	}
	if got.ImageSource == nil || got.ImageSource.Provider != models.ProfileSourceUser {
		t.Errorf("image source = %+v, want the user's", got.ImageSource)
	}
	// The response carries a URL the browser can load, not the reference.
	var resp models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if want := ProxyImageURL(got.ImageURL); resp.ImageURL != want {
		t.Errorf("response imageUrl = %q, want %q", resp.ImageURL, want)
	}

	// No refresh replaces it: neither the provider's own photo nor an
	// enricher's.
	for _, upstream := range []*models.Author{
		{ImageURL: "https://provider.example/k.jpg"},
		{ImageURL: "https://commons.example/k.jpg", ImageSource: &models.ProfileSource{Provider: "wikimedia-commons"}},
	} {
		before := got.ImageURL
		if mergeAuthorProfileFields(got, upstream); got.ImageURL != before || got.ImageSource.Provider != models.ProfileSourceUser {
			t.Errorf("a refresh replaced the uploaded photo with %q (%+v)", got.ImageURL, got.ImageSource)
		}
	}
}

// Anything that is not an image, or too large, is refused and the author keeps
// its photo.
func TestUploadPhoto_RejectsBadUploads(t *testing.T) {
	for name, tc := range map[string]struct {
		body []byte
		want int
	}{
		"not an image":  {[]byte("<html>hello</html>"), http.StatusUnsupportedMediaType},
		"empty":         {nil, http.StatusUnsupportedMediaType},
		"too large":     {append(append([]byte{}, pngBytes...), make([]byte, covers.MaxBytes)...), http.StatusRequestEntityTooLarge},
		"one byte over": {append(append([]byte{}, pngBytes...), make([]byte, covers.MaxBytes+1-len(pngBytes))...), http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			_, authors, author, post := photoFixture(t)
			if rec := post(tc.body); rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			got, _ := authors.GetByID(t.Context(), author.ID)
			if got.ImageURL != author.ImageURL || got.ImageSource != nil {
				t.Errorf("photo changed to %q (%+v)", got.ImageURL, got.ImageSource)
			}
		})
	}
}

func TestUploadPhoto_NoStoreIsUnavailable(t *testing.T) {
	h, _, _, post := photoFixture(t)
	h.coverStore = nil
	if rec := post(pngBytes); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

// Another user's author is not found, as for every per-author route.
func TestUploadPhoto_OtherUsersAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserAuthors(t)
	h := NewAuthorHandler(f.authors, nil, f.books, nil, nil, nil, f.profiles, nil).WithCoverStore(covers.NewStore(t.TempDir()))
	id := strconv.FormatInt(f.a1.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/author/"+id+"/photo", bytes.NewReader(pngBytes)), "id", id)
	req = req.WithContext(withAuthCtx(req.Context(), f.u2, "user"))
	rec := httptest.NewRecorder()
	h.UploadPhoto(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if got, _ := f.authors.GetByID(t.Context(), f.a1.ID); got.ImageURL != "" {
		t.Errorf("another user's author got a photo: %q", got.ImageURL)
	}
}

func TestUploadPhoto_UnreadableBody(t *testing.T) {
	h, authors, author, _ := photoFixture(t)
	id := strconv.FormatInt(author.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/author/"+id+"/photo", errReader{}), "id", id)
	rec := httptest.NewRecorder()
	h.UploadPhoto(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got, _ := authors.GetByID(t.Context(), author.ID); got.ImageURL != author.ImageURL {
		t.Errorf("image_url changed to %q", got.ImageURL)
	}
}

func TestUploadPhoto_StoreWriteFails(t *testing.T) {
	h, authors, author, post := photoFixture(t)
	notADir := filepath.Join(t.TempDir(), "covers")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.WithCoverStore(covers.NewStore(notADir))
	if rec := post(pngBytes); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if got, _ := authors.GetByID(t.Context(), author.ID); got.ImageURL != author.ImageURL {
		t.Errorf("image_url changed to %q", got.ImageURL)
	}
}

// A bio typed in Edit Author is the user's own; clearing it hands the field
// back to the provider and the enrichers (#3287).
func TestUpdateAuthorDescription(t *testing.T) {
	h, authors, author, _ := photoFixture(t)
	put := func(body string) *httptest.ResponseRecorder {
		id := strconv.FormatInt(author.ID, 10)
		req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/author/"+id, strings.NewReader(body)), "id", id)
		rec := httptest.NewRecorder()
		h.Update(rec, req)
		return rec
	}
	get := func() *models.Author {
		got, err := authors.GetByID(t.Context(), author.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if rec := put(`{"description":"  Kari Nordmann writes about the fjells.  "}`); rec.Code != http.StatusOK {
		t.Fatalf("set: status %d: %s", rec.Code, rec.Body.String())
	}
	got := get()
	if got.Description != "Kari Nordmann writes about the fjells." || got.DescriptionSource == nil || got.DescriptionSource.Provider != models.ProfileSourceUser {
		t.Fatalf("after setting: %q from %+v", got.Description, got.DescriptionSource)
	}
	if mergeAuthorProfileFields(got, &models.Author{Description: "The provider's bio."}); got.Description != "Kari Nordmann writes about the fjells." {
		t.Errorf("a refresh replaced the user's bio with %q", got.Description)
	}

	// A save that leaves the bio as it is keeps it the user's.
	if rec := put(`{"monitored":true}`); rec.Code != http.StatusOK {
		t.Fatalf("unrelated save: status %d", rec.Code)
	}
	if got := get(); got.DescriptionSource == nil {
		t.Error("an unrelated save dropped the user's mark")
	}

	if rec := put(`{"description":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d", rec.Code)
	}
	got = get()
	if got.Description != "" || got.DescriptionSource != nil {
		t.Errorf("after clearing: %q from %+v", got.Description, got.DescriptionSource)
	}
	if mergeAuthorProfileFields(got, &models.Author{Description: "The provider's bio."}); got.Description != "The provider's bio." {
		t.Errorf("after clearing, a refresh should fill the bio again, got %q", got.Description)
	}

	if rec := put(`{"description":"` + strings.Repeat("x", maxAuthorDescriptionBytes+1) + `"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an over-long bio: status %d, want 400", rec.Code)
	}
}

// A save that fails after the photo is stored is a server error, not a
// success the page would show.
func TestUploadPhoto_SaveFails(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authors := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "nb:author:1", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "nb"}
	if err := authors.Create(t.Context(), author); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TRIGGER refuse_author_update BEFORE UPDATE ON authors BEGIN SELECT RAISE(ABORT, 'read only'); END`); err != nil {
		t.Fatal(err)
	}
	h := NewAuthorHandler(authors, nil, db.NewBookRepo(database), nil, nil, nil, db.NewMetadataProfileRepo(database), nil).
		WithCoverStore(covers.NewStore(t.TempDir()))
	id := strconv.FormatInt(author.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/author/"+id+"/photo", bytes.NewReader(pngBytes)), "id", id)
	rec := httptest.NewRecorder()
	h.UploadPhoto(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", rec.Code, rec.Body.String())
	}
}
