package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// #4132: an author whose metadata profile id names a profile that was deleted
// used to mean something different on every path. These tests build the
// reported library (profile 1 deleted, "Deutsch" and "Deutsch + English"
// left) and hold each consumer to the same answer.

type missingProfileFixture struct {
	database *sql.DB
	profiles *db.MetadataProfileRepo
	authors  *db.AuthorRepo
	settings *db.SettingsRepo
	deutsch  *models.MetadataProfile
	both     *models.MetadataProfile
	orphan   *models.Author
}

func newMissingProfileFixture(t *testing.T) missingProfileFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	f := missingProfileFixture{
		database: database,
		profiles: db.NewMetadataProfileRepo(database),
		authors:  db.NewAuthorRepo(database),
		settings: db.NewSettingsRepo(database),
		deutsch:  &models.MetadataProfile{Name: "Deutsch", AllowedLanguages: "ger", UnknownLanguageBehavior: models.UnknownLanguageFail, SkipPartBooks: true, MinPages: 40},
		both:     &models.MetadataProfile{Name: "Deutsch + English", AllowedLanguages: "ger,eng", UnknownLanguageBehavior: models.UnknownLanguagePass},
	}
	for _, p := range []*models.MetadataProfile{f.deutsch, f.both} {
		if err := f.profiles.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM metadata_profiles WHERE id = ?", models.DefaultMetadataProfileID); err != nil {
		t.Fatal(err)
	}
	gone := models.DefaultMetadataProfileID
	f.orphan = &models.Author{ForeignID: "ol:orphan", Name: "Orphan", SortName: "Orphan", MetadataProfileID: &gone}
	if err := f.authors.Create(ctx, f.orphan); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMissingMetadataProfile_EveryConsumerUsesTheSameProfile(t *testing.T) {
	f := newMissingProfileFixture(t)
	ctx := context.Background()

	t.Run("search", func(t *testing.T) {
		h := &IndexerHandler{profiles: f.profiles}
		got := h.resolveAllowedLanguages(ctx, f.orphan)
		if len(got) != 1 || got[0] != "ger" {
			t.Fatalf("search languages = %v, want [ger], the first existing profile", got)
		}
	})

	t.Run("catalogue sync", func(t *testing.T) {
		h := &AuthorHandler{profiles: f.profiles}
		langs, unknownFail := h.resolveAllowedLanguages(ctx, f.orphan)
		if len(langs) != 1 || langs[0] != "ger" || !unknownFail {
			t.Fatalf("sync languages = %v unknownFail=%v, want [ger] true", langs, unknownFail)
		}
		if !h.resolveSkipPartBooks(ctx, f.orphan) {
			t.Error("sync ignored the substitute profile's skipPartBooks")
		}
		if got, _ := h.resolveEditionFilters(ctx, f.orphan); got != 40 {
			t.Errorf("sync minPages = %d, want 40", got)
		}
	})

	t.Run("reconcile catalogue", func(t *testing.T) {
		h := &AuthorHandler{profiles: f.profiles}
		p, err := h.reconciliationProfile(ctx, f.orphan)
		if err != nil {
			t.Fatalf("reconcile aborted: %v", err)
		}
		if p.name != "Deutsch" {
			t.Fatalf("reconcile profile = %q, want Deutsch", p.name)
		}
	})

	t.Run("series fill", func(t *testing.T) {
		p := effectiveMetadataProfile(ctx, f.profiles, f.orphan)
		if p == nil || p.Name != "Deutsch" {
			t.Fatalf("series fill profile = %+v, want Deutsch", p)
		}
	})

	t.Run("install default setting wins over the first profile", func(t *testing.T) {
		if err := f.settings.Set(ctx, SettingAuthorDefaultMetadataProfileID, strconv.FormatInt(f.both.ID, 10)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.settings.Delete(ctx, SettingAuthorDefaultMetadataProfileID) })
		h := &IndexerHandler{profiles: f.profiles}
		got := h.resolveAllowedLanguages(ctx, f.orphan)
		if len(got) != 2 {
			t.Fatalf("search languages = %v, want [ger eng]", got)
		}
		a := &AuthorHandler{profiles: f.profiles}
		if p, err := a.reconciliationProfile(ctx, f.orphan); err != nil || p.name != "Deutsch + English" {
			t.Fatalf("reconcile profile = %q err=%v, want Deutsch + English", p.name, err)
		}
	})
}

func TestMissingMetadataProfile_AuthorGetSaysSo(t *testing.T) {
	f := newMissingProfileFixture(t)
	h := NewAuthorHandler(f.authors, nil, db.NewBookRepo(f.database), db.NewSeriesRepo(f.database), nil, nil, f.profiles, nil)
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/author/1", nil), "id", strconv.FormatInt(f.orphan.ID, 10))
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	fb := got.MetadataProfileFallback
	if fb == nil || fb.MissingProfileID != models.DefaultMetadataProfileID || fb.UsedProfileID != f.deutsch.ID || fb.UsedProfileName != "Deutsch" {
		t.Fatalf("metadataProfileFallback = %+v", fb)
	}
}

// Adding an author or a book with no explicit profile must not stamp the
// deleted id 1 again.
func TestMissingMetadataProfile_NewAuthorsGetAnExistingProfile(t *testing.T) {
	f := newMissingProfileFixture(t)
	ctx := context.Background()

	a := &models.Author{ForeignID: "ol:new", Name: "New", SortName: "New"}
	applyAuthorCreateOptions(a, true, models.AuthorMonitorModeAll, 0, nil, nil, nil, nil)
	if err := f.authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if a.MetadataProfileID == nil || *a.MetadataProfileID != f.deutsch.ID {
		t.Fatalf("new author profile = %v, want %d (first existing)", a.MetadataProfileID, f.deutsch.ID)
	}

	if err := f.settings.Set(ctx, SettingAuthorDefaultMetadataProfileID, strconv.FormatInt(f.both.ID, 10)); err != nil {
		t.Fatal(err)
	}
	b := &models.Author{ForeignID: "ol:new2", Name: "New2", SortName: "New2"}
	applyAuthorCreateOptions(b, true, models.AuthorMonitorModeAll, 0, nil, nil, nil, nil)
	if err := f.authors.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if b.MetadataProfileID == nil || *b.MetadataProfileID != f.both.ID {
		t.Fatalf("new author profile = %v, want the configured default %d", b.MetadataProfileID, f.both.ID)
	}

	explicit := f.both.ID
	c := &models.Author{ForeignID: "ol:new3", Name: "New3", SortName: "New3"}
	applyAuthorCreateOptions(c, true, models.AuthorMonitorModeAll, 0, nil, &explicit, nil, nil)
	if c.MetadataProfileID == nil || *c.MetadataProfileID != explicit {
		t.Fatal("an explicit profile id was overwritten")
	}
}

func TestMetadataProfileDelete_RefusedWhileAuthorsUseIt(t *testing.T) {
	f := newMissingProfileFixture(t)
	ctx := context.Background()
	a := &models.Author{ForeignID: "ol:user", Name: "Uses Deutsch", SortName: "Uses Deutsch", MetadataProfileID: &f.deutsch.ID}
	if err := f.authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	h := NewMetadataProfileHandler(f.profiles)
	del := func(url string, id int64) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, url, nil), "id", strconv.FormatInt(id, 10)))
		return rec
	}

	rec := del("/api/v1/metadataprofile/x", f.deutsch.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error       string   `json:"error"`
		AuthorCount int      `json:"authorCount"`
		AuthorNames []string `json:"authorNames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AuthorCount != 1 || len(body.AuthorNames) != 1 || body.AuthorNames[0] != "Uses Deutsch" || body.Error == "" {
		t.Fatalf("409 body = %+v", body)
	}
	if p, _ := f.profiles.GetByID(ctx, f.deutsch.ID); p == nil {
		t.Fatal("a refused delete removed the profile")
	}

	// Moving the authors deletes it and re-points them.
	if rec := del("/api/v1/metadataprofile/x?moveAuthorsTo=999", f.deutsch.ID); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown target: status = %d, want 400", rec.Code)
	}
	if rec := del("/api/v1/metadataprofile/x?moveAuthorsTo="+strconv.FormatInt(f.deutsch.ID, 10), f.deutsch.ID); rec.Code != http.StatusBadRequest {
		t.Fatalf("self target: status = %d, want 400", rec.Code)
	}
	rec = del("/api/v1/metadataprofile/x?moveAuthorsTo="+strconv.FormatInt(f.both.ID, 10), f.deutsch.ID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("move delete: status = %d body=%s", rec.Code, rec.Body.String())
	}
	moved, err := f.authors.GetByID(ctx, a.ID)
	if err != nil || moved == nil || moved.MetadataProfileID == nil || *moved.MetadataProfileID != f.both.ID {
		t.Fatalf("author after move = %+v err=%v", moved, err)
	}
	if p, _ := f.profiles.GetByID(ctx, f.deutsch.ID); p != nil {
		t.Fatal("the profile survived a move delete")
	}

	// Unused profiles still delete plainly. The orphan author points at the
	// long gone id 1, not at this one.
	unused := &models.MetadataProfile{Name: "Unused", AllowedLanguages: "eng"}
	if err := f.profiles.Create(ctx, unused); err != nil {
		t.Fatal(err)
	}
	if rec := del("/api/v1/metadataprofile/x", unused.ID); rec.Code != http.StatusNoContent {
		t.Fatalf("unused delete: status = %d", rec.Code)
	}
}

func TestSettingDefaultMetadataProfile_RefusesAProfileThatDoesNotExist(t *testing.T) {
	f := newMissingProfileFixture(t)
	ctx := context.Background()
	if err := db.ValidateDefaultAuthorMetadataProfileID(ctx, f.settings, "999"); err == nil {
		t.Error("a missing profile id was accepted")
	}
	if err := db.ValidateDefaultAuthorMetadataProfileID(ctx, f.settings, "abc"); err == nil {
		t.Error("garbage was accepted")
	}
	for _, ok := range []string{"", strconv.FormatInt(f.both.ID, 10)} {
		if err := db.ValidateDefaultAuthorMetadataProfileID(ctx, f.settings, ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}
