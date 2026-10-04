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

// Profile List must hide what Get hides. Get 404s a profile owned by another
// user under tenancy, but List used the unscoped repo call and returned it
// anyway, so the IDOR guard on Get was decorative.

func listMetadataProfileIDs(t *testing.T, h *MetadataProfileHandler, ctx context.Context) map[int64]bool {
	t.Helper()
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/metadataprofile", nil).WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d body=%s", rec.Code, rec.Body.String())
	}
	var got []models.MetadataProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := make(map[int64]bool, len(got))
	for _, p := range got {
		ids[p.ID] = true
	}
	return ids
}

func TestMetadataProfile_List_ExcludesOtherUsersWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserMetadataProfiles(t)
	h := NewMetadataProfileHandler(f.repo)

	ids := listMetadataProfileIDs(t, h, withAuthCtx(context.Background(), f.u2, "user"))
	if ids[f.p1.ID] {
		t.Fatalf("bob's list includes alice's profile %d; Get 404s it, so List must not return it", f.p1.ID)
	}
	if !ids[f.p2.ID] {
		t.Fatalf("bob's list is missing his own profile %d", f.p2.ID)
	}
	// Unowned rows (the seeded defaults) stay visible, matching CheckOwnership.
	all, err := f.repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.OwnerUserID == 0 && !ids[p.ID] {
			t.Fatalf("unowned profile %d (%s) must stay visible", p.ID, p.Name)
		}
	}
}

func TestMetadataProfile_List_AdminAndGateOffSeeAll(t *testing.T) {
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
			f := seedTwoUserMetadataProfiles(t)
			h := NewMetadataProfileHandler(f.repo)
			ids := listMetadataProfileIDs(t, h, withAuthCtx(context.Background(), f.u2, tc.role))
			if !ids[f.p1.ID] || !ids[f.p2.ID] {
				t.Fatalf("list = %v; want both %d and %d", ids, f.p1.ID, f.p2.ID)
			}
		})
	}
}

func listQualityProfileIDs(t *testing.T, h *QualityProfileHandler, ctx context.Context) map[int64]bool {
	t.Helper()
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/qualityprofile", nil).WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d body=%s", rec.Code, rec.Body.String())
	}
	var got []models.QualityProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := make(map[int64]bool, len(got))
	for _, p := range got {
		ids[p.ID] = true
	}
	return ids
}

func TestQualityProfile_List_ExcludesOtherUsersWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserQualityProfile(t)
	h := NewQualityProfileHandler(f.repo)

	if ids := listQualityProfileIDs(t, h, withAuthCtx(context.Background(), f.u2, "user")); ids[f.p1.ID] {
		t.Fatalf("bob's list includes alice's quality profile %d; Get 404s it, so List must not return it", f.p1.ID)
	}
	if ids := listQualityProfileIDs(t, h, withAuthCtx(context.Background(), f.u1, "user")); !ids[f.p1.ID] {
		t.Fatalf("alice's list is missing her own quality profile %d", f.p1.ID)
	}
	if ids := listQualityProfileIDs(t, h, withAuthCtx(context.Background(), 99, "admin")); !ids[f.p1.ID] {
		t.Fatalf("admin list is missing alice's quality profile %d", f.p1.ID)
	}
}

func TestQualityProfile_List_GateOffSeesAll(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserQualityProfile(t)
	h := NewQualityProfileHandler(f.repo)
	if ids := listQualityProfileIDs(t, h, withAuthCtx(context.Background(), f.u2, "user")); !ids[f.p1.ID] {
		t.Fatalf("gate off must keep cross-user visibility; list = %v", ids)
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
