package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/bookorbit"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type bookOrbitLinkFixture struct {
	h        *BookHandler
	settings *db.SettingsRepo
	book     *models.Book
	other    *models.Book // owned by another user
	alice    int64
	bob      int64
}

func newBookOrbitLinkFixture(t *testing.T, bookOrbitBookID string) *bookOrbitLinkFixture {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, true)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	provenance := db.NewABSProvenanceRepo(database)
	settings := db.NewSettingsRepo(database)

	users := db.NewUserRepo(database)
	var uid []int64
	for _, name := range []string{"alice", "bob"} {
		u, err := users.Create(ctx, name, "h-"+name)
		if err != nil {
			t.Fatal(err)
		}
		uid = append(uid, u.ID)
	}
	author := &models.Author{ForeignID: "OL-1927-A", Name: "Link Author", SortName: "Author, Link", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	mk := func(fid string, owner int64) *models.Book {
		b := &models.Book{
			ForeignID: fid, AuthorID: author.ID, OwnerUserID: owner, Title: "Link Book " + fid, SortTitle: fid,
			Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
			MetadataProvider: "openlibrary", Genres: []string{},
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	f := &bookOrbitLinkFixture{settings: settings, book: mk("OL-1927-W1", uid[0]), other: mk("OL-1927-W2", uid[1]), alice: uid[0], bob: uid[1]}
	for idx, b := range []*models.Book{f.book, f.other} {
		if bookOrbitBookID == "" {
			break
		}
		extID := bookOrbitBookID
		if idx == 1 {
			extID += "9"
		}
		if err := provenance.Upsert(ctx, &models.ABSProvenance{
			SourceID: bookorbit.SourceID, LibraryID: "bookorbit-2", EntityType: bookorbit.ProvenanceBookEntity,
			ExternalID: extID, LocalID: b.ID, ItemID: extID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.h = NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).WithSettings(settings).WithBookOrbitLinks(provenance)
	return f
}

func (f *bookOrbitLinkFixture) set(t *testing.T, key, value string) {
	t.Helper()
	if err := f.settings.Set(context.Background(), key, value); err != nil {
		t.Fatal(err)
	}
}

// get returns the raw body and the decoded bookOrbitUrl of GET /book/{id}.
func (f *bookOrbitLinkFixture) get(t *testing.T, id int64, role string, userID int64) (int, string, string) {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/book/"+sid, nil)
	ctx := auth.WithUserRole(req.Context(), role)
	if userID != 0 {
		ctx = auth.WithUserID(ctx, userID)
	}
	rec := httptest.NewRecorder()
	f.h.Get(rec, withURLParam(req.WithContext(ctx), "id", sid))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	link, _ := body["bookOrbitUrl"].(string)
	return rec.Code, rec.Body.String(), link
}

const linkTestKey = "bok_link_test_key_must_never_appear_0000000"

func TestBookDetailCarriesBookOrbitLinkForAdmin(t *testing.T) {
	f := newBookOrbitLinkFixture(t, "61")
	f.set(t, SettingBookOrbitBaseURL, "http://bookorbit:3000")
	f.set(t, SettingBookOrbitAPIKey, linkTestKey)

	code, raw, link := f.get(t, f.book.ID, "admin", f.alice)
	if code != http.StatusOK || link != "http://bookorbit:3000/book/61" {
		t.Fatalf("fallback to the server URL: %d link=%q %s", code, link, raw)
	}
	if strings.Contains(raw, linkTestKey) || strings.Contains(raw, "bookorbit.api_key") {
		t.Fatal("book response carries the API key")
	}

	f.set(t, SettingBookOrbitPublicURL, "https://books.example.com/bookorbit/")
	_, raw, link = f.get(t, f.book.ID, "admin", f.alice)
	if link != "https://books.example.com/bookorbit/book/61" {
		t.Fatalf("public URL not used: %q", link)
	}
	if strings.Contains(raw, "bookorbit:3000") {
		t.Fatal("book response carries the server URL")
	}
}

func TestBookDetailOmitsBookOrbitLinkForNonAdmin(t *testing.T) {
	f := newBookOrbitLinkFixture(t, "61")
	f.set(t, SettingBookOrbitBaseURL, "http://bookorbit:3000")
	_, raw, link := f.get(t, f.book.ID, "user", f.alice)
	if link != "" || strings.Contains(raw, "bookOrbitUrl") || strings.Contains(raw, "bookorbit:3000") {
		t.Fatalf("non admin saw the link: %s", raw)
	}
	_, raw, _ = f.get(t, f.book.ID, "", 0)
	if strings.Contains(raw, "bookOrbitUrl") {
		t.Fatalf("anonymous saw the link: %s", raw)
	}
}

func TestBookDetailBookOrbitLinkRespectsOwnership(t *testing.T) {
	f := newBookOrbitLinkFixture(t, "61")
	f.set(t, SettingBookOrbitBaseURL, "http://bookorbit:3000")
	// A non admin user does not own the other user's book, and is told so.
	code, raw, _ := f.get(t, f.other.ID, "user", f.alice)
	if code != http.StatusNotFound || strings.Contains(raw, "bookorbit") {
		t.Fatalf("cross user read: %d %s", code, raw)
	}
	// The owner, not an admin, still sees no link.
	code, raw, link := f.get(t, f.other.ID, "user", f.bob)
	if code != http.StatusOK || link != "" {
		t.Fatalf("owner without admin: %d %q", code, link)
	}
	_ = raw
}

func TestBookDetailOmitsBookOrbitLinkWhenThereIsNothingToLinkTo(t *testing.T) {
	t.Run("book never imported", func(t *testing.T) {
		f := newBookOrbitLinkFixture(t, "")
		f.set(t, SettingBookOrbitBaseURL, "http://bookorbit:3000")
		if _, raw, _ := f.get(t, f.book.ID, "admin", f.alice); strings.Contains(raw, "bookOrbitUrl") {
			t.Fatalf("link for a book with no stored id: %s", raw)
		}
	})
	t.Run("no url configured", func(t *testing.T) {
		f := newBookOrbitLinkFixture(t, "61")
		if _, raw, _ := f.get(t, f.book.ID, "admin", f.alice); strings.Contains(raw, "bookOrbitUrl") {
			t.Fatalf("link without any URL: %s", raw)
		}
	})
	t.Run("unsafe public url written around the form", func(t *testing.T) {
		f := newBookOrbitLinkFixture(t, "61")
		f.set(t, SettingBookOrbitBaseURL, "http://bookorbit:3000")
		f.set(t, SettingBookOrbitPublicURL, "javascript:alert(1)")
		if _, raw, _ := f.get(t, f.book.ID, "admin", f.alice); strings.Contains(raw, "bookOrbitUrl") {
			t.Fatalf("unsafe URL became a link: %s", raw)
		}
	})
}

func TestBookOrbitConfigPublicURL(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.save(t)
	ctx := context.Background()

	rec := f.put(`{"publicUrl":" https://books.example.com/bo// "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	var cfg bookOrbitConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://books.example.com/bo" {
		t.Errorf("publicUrl = %q", cfg.PublicURL)
	}
	if strings.Contains(rec.Body.String(), bookOrbitTestKey) {
		t.Error("response carries the API key")
	}
	assertSettingValue(t, f.settings, ctx, SettingBookOrbitPublicURL, "https://books.example.com/bo")

	// Leaving it out keeps it; an empty string clears it.
	if rec := f.put(`{"pathRemap":"/books:/srv"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	assertSettingValue(t, f.settings, ctx, SettingBookOrbitPublicURL, "https://books.example.com/bo")
	if rec := f.put(`{"publicUrl":""}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	assertSettingValue(t, f.settings, ctx, SettingBookOrbitPublicURL, "")

	for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "file:///etc/passwd", "books.example.com", "https://u:p@b.example.com"} {
		f.put(`{"publicUrl":"https://keep.example.com"}`)
		body, _ := json.Marshal(map[string]string{"publicUrl": bad})
		if rec := f.put(string(body)); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", bad, rec.Code)
		}
		assertSettingValue(t, f.settings, ctx, SettingBookOrbitPublicURL, "https://keep.example.com")
	}
}

func TestBookOrbitPublicURLSettingIsAdminOnly(t *testing.T) {
	if !isAdminOnlySetting(SettingBookOrbitPublicURL) {
		t.Fatal("bookorbit.public_url is readable by non admins")
	}
}
