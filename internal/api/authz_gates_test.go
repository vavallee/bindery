package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Quality and metadata profiles are instance wide configuration: only admins
// write them, and migration 025 stamped every existing row (seeded defaults
// included) with owner_user_id = 1. An owner filter on the reads therefore
// hid every profile from every non admin under tenancy, which emptied the
// profile pickers in the author forms. A user must see admin owned profiles.

// seedAdminOwnedProfiles builds the post migration 025 state: an admin (id 1)
// and a user, with every quality and metadata profile owned by the admin.
func seedAdminOwnedProfiles(t *testing.T) (*db.QualityProfileRepo, *db.MetadataProfileRepo, int64, int64) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	users := db.NewUserRepo(database)
	admin, err := users.Create(ctx, "admin", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}
	qp := db.NewQualityProfileRepo(database)
	mp := db.NewMetadataProfileRepo(database)
	if err := qp.CreateForUser(ctx, &models.QualityProfile{
		Name: "Admin Quality", Cutoff: "epub", UpgradeAllowed: true,
		Items: []models.QualityItem{{Quality: "epub", Allowed: true}},
	}, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := mp.CreateForUser(ctx, &models.MetadataProfile{Name: "Admin Metadata", AllowedLanguages: "eng"}, admin.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"quality_profiles", "metadata_profiles"} {
		if _, err := database.Exec("UPDATE "+table+" SET owner_user_id=?", admin.ID); err != nil {
			t.Fatal(err)
		}
	}
	return qp, mp, admin.ID, bob.ID
}

func TestProfiles_UserSeesAdminOwnedProfilesWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	qp, mp, _, bob := seedAdminOwnedProfiles(t)
	ctx := withAuthCtx(context.Background(), bob, "user")

	quality, err := qp.List(context.Background())
	if err != nil || len(quality) == 0 {
		t.Fatalf("seed quality profiles: %v (n=%d)", err, len(quality))
	}
	meta, err := mp.List(context.Background())
	if err != nil || len(meta) == 0 {
		t.Fatalf("seed metadata profiles: %v (n=%d)", err, len(meta))
	}

	qh := NewQualityProfileHandler(qp)
	rec := httptest.NewRecorder()
	qh.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/qualityprofile", nil).WithContext(ctx))
	var gotQ []models.QualityProfile
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &gotQ) != nil || len(gotQ) != len(quality) {
		t.Fatalf("quality List as user: status=%d got %d profiles, want %d; body=%s", rec.Code, len(gotQ), len(quality), rec.Body.String())
	}
	for _, p := range quality {
		rec := httptest.NewRecorder()
		qh.Get(rec, newRequestForID(http.MethodGet, "/api/v1/qualityprofile/"+strconv.FormatInt(p.ID, 10), p.ID, ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("quality Get %d (%s) as user: got %d, want 200", p.ID, p.Name, rec.Code)
		}
	}

	mh := NewMetadataProfileHandler(mp)
	rec = httptest.NewRecorder()
	mh.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/metadataprofile", nil).WithContext(ctx))
	var gotM []models.MetadataProfile
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &gotM) != nil || len(gotM) != len(meta) {
		t.Fatalf("metadata List as user: status=%d got %d profiles, want %d; body=%s", rec.Code, len(gotM), len(meta), rec.Body.String())
	}
	for _, p := range meta {
		rec := httptest.NewRecorder()
		mh.Get(rec, newRequestForID(http.MethodGet, "/api/v1/metadataprofile/"+strconv.FormatInt(p.ID, 10), p.ID, ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("metadata Get %d (%s) as user: got %d, want 200", p.ID, p.Name, rec.Code)
		}
	}
}

// The hardcover diff loaded the series with the unscoped GetByID, so a user's
// diff listed other users' books as Present and LocalOnly, titles and local
// book ids included. Series Get already used the scoped loader.

type hardcoverDiffTenancyFixture struct {
	h                     *SeriesHandler
	seriesID              int64
	alice, bob            int64
	aliceKings            int64
	bobOathbringer, bobPv int64
}

func seedHardcoverDiffTenancy(t *testing.T) hardcoverDiffTenancyFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	catalog := stormlightCatalog()
	catalog.Books = append(catalog.Books, metadata.SeriesCatalogBook{
		ForeignID:  "hc:oathbringer",
		ProviderID: "103",
		Title:      "Oathbringer",
		Position:   "3",
		Book: models.Book{
			ForeignID: "hc:oathbringer",
			Title:     "Oathbringer",
			Author:    catalog.Books[0].Book.Author,
		},
	})
	catalog.BookCount = len(catalog.Books)

	seriesRepo := db.NewSeriesRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	users := db.NewUserRepo(database)
	h := NewSeriesHandler(seriesRepo, bookRepo, authorRepo,
		metadata.NewAggregator(&stubSeriesProvider{catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog}}).WithAudnexClient(nil),
		&mockBookSearcher{})

	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}

	series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	makeBook := func(foreignID, title, position string, owner int64) int64 {
		book := &models.Book{
			ForeignID: foreignID,
			AuthorID:  author.ID,
			Title:     title,
			SortTitle: title,
			Status:    models.BookStatusWanted,
			Genres:    []string{},
		}
		if err := bookRepo.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		setOwner(t, database, "books", book.ID, owner)
		if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, book.ID, position, true); err != nil {
			t.Fatal(err)
		}
		return book.ID
	}
	return hardcoverDiffTenancyFixture{
		h:              h,
		seriesID:       series.ID,
		alice:          alice.ID,
		bob:            bob.ID,
		aliceKings:     makeBook("hc:the-way-of-kings", "The Way of Kings", "1", alice.ID),
		bobOathbringer: makeBook("hc:oathbringer", "Oathbringer", "3", bob.ID),
		bobPv:          makeBook("local:bob-private", "Bob Private Shelf Book", "", bob.ID),
	}
}

func (f hardcoverDiffTenancyFixture) diffAs(t *testing.T, userID int64, role string) seriesHardcoverDiffResponse {
	t.Helper()
	ctx := withAuthCtx(context.Background(), userID, role)
	rec := httptest.NewRecorder()
	f.h.HardcoverDiff(rec, newRequestForID(http.MethodGet, "/api/v1/series/"+strconv.FormatInt(f.seriesID, 10)+"/hardcover-diff", f.seriesID, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("diff: got %d body=%s", rec.Code, rec.Body.String())
	}
	var got seriesHardcoverDiffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func diffLocalBookIDs(d seriesHardcoverDiffResponse) map[int64]bool {
	ids := map[int64]bool{}
	for _, rows := range [][]seriesHardcoverDiffBook{d.Present, d.Missing, d.LocalOnly, d.Uncertain} {
		for _, row := range rows {
			if row.LocalBookID != nil {
				ids[*row.LocalBookID] = true
			}
		}
	}
	return ids
}

func TestSeriesHardcoverDiff_ExcludesOtherUsersBooksWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedHardcoverDiffTenancy(t)

	got := f.diffAs(t, f.alice, "user")
	ids := diffLocalBookIDs(got)
	if ids[f.bobOathbringer] || ids[f.bobPv] {
		t.Fatalf("alice's diff exposes bob's books: local ids %v (bob owns %d and %d)", ids, f.bobOathbringer, f.bobPv)
	}
	if !ids[f.aliceKings] {
		t.Fatalf("alice's diff lost her own book %d: %+v", f.aliceKings, got)
	}
	if len(got.LocalOnly) != 0 {
		t.Fatalf("localOnly = %+v; want none, bob's private book must not appear", got.LocalOnly)
	}
	if len(got.Missing) != 1 || got.Missing[0].ForeignBookID != "hc:oathbringer" || got.Missing[0].LocalBookID != nil {
		t.Fatalf("missing = %+v; want Oathbringer with no local link, since alice does not own it", got.Missing)
	}
	if got.PresentCount != 1 {
		t.Fatalf("presentCount = %d; want 1", got.PresentCount)
	}
}

func TestSeriesHardcoverDiff_AdminAndGateOffSeeWholeSeries(t *testing.T) {
	cases := []struct {
		name string
		gate bool
		role string
	}{
		{name: "admin with gate on", gate: true, role: "admin"},
		{name: "user with gate off", gate: false, role: "user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.gate)
			f := seedHardcoverDiffTenancy(t)
			got := f.diffAs(t, f.alice, tc.role)
			ids := diffLocalBookIDs(got)
			for _, id := range []int64{f.aliceKings, f.bobOathbringer, f.bobPv} {
				if !ids[id] {
					t.Fatalf("diff local ids = %v; want book %d present", ids, id)
				}
			}
		})
	}
}
