package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Regression suite for the Tier-1 cross-user IDOR fix (D1). Each resource gets
// three scenarios:
//
//   1. Gate on + non-owner caller          -> 404 (no leak)
//   2. Gate on + admin caller              -> succeeds
//   3. Gate OFF (default)                  -> succeeds even cross-user, so the
//      env-gate promise (existing tests + single-user installs unchanged) is
//      asserted in the same file as the new behavior.
//
// To keep the test surface compact we exercise one Get and one Delete per
// resource. The other handler shapes share the exact same CheckOwnership call
// site; adding 18 near-identical tests would dilute signal without catching
// new bugs.

// withAuthCtx attaches a userID and role to ctx — mirrors what the auth
// middleware would do at runtime, but constructed by hand for tests.
func withAuthCtx(ctx context.Context, userID int64, role string) context.Context {
	ctx = auth.WithUserID(ctx, userID)
	ctx = auth.WithUserRole(ctx, role)
	return ctx
}

// newRequestForID builds a chi-aware request with `{id}` populated. Tests want
// to hit Get/Delete handlers directly without spinning up the whole router.
func newRequestForID(method, path string, id int64, ctx context.Context) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
	return req
}

// setOwner stamps owner_user_id directly on a row. The Create paths for some
// repos (books, profiles, root folders) do not yet write that column, so the
// tests force it by hand to mirror what migration 025's backfill would do for
// existing rows in a production install.
func setOwner(t *testing.T, database *sql.DB, table string, id, owner int64) {
	t.Helper()
	if _, err := database.Exec("UPDATE "+table+" SET owner_user_id=? WHERE id=?", owner, id); err != nil {
		t.Fatalf("set owner on %s: %v", table, err)
	}
}

// --- Author ---------------------------------------------------------------

type authzAuthorFixture struct {
	database *sql.DB
	authors  *db.AuthorRepo
	books    *db.BookRepo
	profiles *db.MetadataProfileRepo
	u1, u2   int64
	a1, a2   *models.Author
}

func seedTwoUserAuthors(t *testing.T) authzAuthorFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	users := db.NewUserRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	profiles := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	u1, err := users.Create(ctx, "alice", "hash1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	u2, err := users.Create(ctx, "bob", "hash2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	a1 := &models.Author{ForeignID: "OL-A1", Name: "Author One", SortName: "One, Author", MetadataProvider: "openlibrary"}
	if err := authors.CreateForUser(ctx, a1, u1.ID); err != nil {
		t.Fatalf("create author1: %v", err)
	}
	a2 := &models.Author{ForeignID: "OL-A2", Name: "Author Two", SortName: "Two, Author", MetadataProvider: "openlibrary"}
	if err := authors.CreateForUser(ctx, a2, u2.ID); err != nil {
		t.Fatalf("create author2: %v", err)
	}
	// GetByID re-reads through the scan path so the owner column lands on the
	// struct, which the handler will read via author.OwnerUserID.
	a1, _ = authors.GetByID(ctx, a1.ID)
	a2, _ = authors.GetByID(ctx, a2.ID)
	return authzAuthorFixture{
		database: database, authors: authors, books: books, profiles: profiles,
		u1: u1.ID, u2: u2.ID, a1: a1, a2: a2,
	}
}

func TestAuthor_Get_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserAuthors(t)
	h := NewAuthorHandler(f.authors, nil, f.books, nil, nil, nil, f.profiles, nil)

	// Bob (u2) tries to read Alice's author (a1).
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodGet, "/api/v1/author/"+strconv.FormatInt(f.a1.ID, 10), f.a1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user Get must 404 with gate on; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthor_Get_AdminCanSeeAllWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserAuthors(t)
	h := NewAuthorHandler(f.authors, nil, f.books, nil, nil, nil, f.profiles, nil)

	ctx := withAuthCtx(context.Background(), 99, "admin")
	req := newRequestForID(http.MethodGet, "/api/v1/author/"+strconv.FormatInt(f.a1.ID, 10), f.a1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin Get must succeed; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthor_Get_GateOffAllowsCrossUser(t *testing.T) {
	// Default state (the env-gate canary): existing tests and single-user
	// installs must keep seeing the pre-fix behavior, namely no per-user
	// isolation, when BINDERY_ENFORCE_TENANCY is unset.
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserAuthors(t)
	h := NewAuthorHandler(f.authors, nil, f.books, nil, nil, nil, f.profiles, nil)

	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodGet, "/api/v1/author/"+strconv.FormatInt(f.a1.ID, 10), f.a1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gate off must preserve pre-fix cross-user access; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthor_Delete_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserAuthors(t)
	h := NewAuthorHandler(f.authors, nil, f.books, nil, nil, nil, f.profiles, nil)

	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodDelete, "/api/v1/author/"+strconv.FormatInt(f.a1.ID, 10), f.a1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user Delete must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := f.authors.GetByID(context.Background(), f.a1.ID); got == nil {
		t.Error("cross-user Delete must not actually remove the row")
	}
}

// --- Book -----------------------------------------------------------------

type authzBookFixture struct {
	authzAuthorFixture
	b1, b2 *models.Book
}

func seedTwoUserBooks(t *testing.T) authzBookFixture {
	t.Helper()
	af := seedTwoUserAuthors(t)
	ctx := context.Background()

	b1 := &models.Book{ForeignID: "B-1", AuthorID: af.a1.ID, Title: "Alice Book", SortTitle: "Alice Book", Status: "wanted", MetadataProvider: "openlibrary"}
	if err := af.books.Create(ctx, b1); err != nil {
		t.Fatalf("create b1: %v", err)
	}
	b2 := &models.Book{ForeignID: "B-2", AuthorID: af.a2.ID, Title: "Bob Book", SortTitle: "Bob Book", Status: "wanted", MetadataProvider: "openlibrary"}
	if err := af.books.Create(ctx, b2); err != nil {
		t.Fatalf("create b2: %v", err)
	}
	// BookRepo.Create does not yet set owner_user_id (Tier-2 work); stamp it
	// manually so the handler-level check has something to verify.
	setOwner(t, af.database, "books", b1.ID, af.u1)
	setOwner(t, af.database, "books", b2.ID, af.u2)
	b1, _ = af.books.GetByID(ctx, b1.ID)
	b2, _ = af.books.GetByID(ctx, b2.ID)
	return authzBookFixture{authzAuthorFixture: af, b1: b1, b2: b2}
}

func TestBook_Get_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBooks(t)

	h := &BookHandler{books: f.books}
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodGet, "/api/v1/book/"+strconv.FormatInt(f.b1.ID, 10), f.b1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user book Get must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBook_Get_AdminCanSeeAllWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBooks(t)

	h := &BookHandler{books: f.books}
	ctx := withAuthCtx(context.Background(), 99, "admin")
	req := newRequestForID(http.MethodGet, "/api/v1/book/"+strconv.FormatInt(f.b1.ID, 10), f.b1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin book Get must succeed; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBook_Get_GateOffAllowsCrossUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserBooks(t)

	h := &BookHandler{books: f.books}
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodGet, "/api/v1/book/"+strconv.FormatInt(f.b1.ID, 10), f.b1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gate off must preserve cross-user book access; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBook_Delete_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserBooks(t)

	h := &BookHandler{books: f.books}
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(f.b1.ID, 10), f.b1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user book Delete must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := f.books.GetByID(context.Background(), f.b1.ID); got == nil {
		t.Error("cross-user Delete must not actually remove the row")
	}
}

// --- MetadataProfile -------------------------------------------------------

type authzMetadataProfileFixture struct {
	database *sql.DB
	repo     *db.MetadataProfileRepo
	u1, u2   int64
	p1, p2   *models.MetadataProfile
}

func seedTwoUserMetadataProfiles(t *testing.T) authzMetadataProfileFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	users := db.NewUserRepo(database)
	repo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	u1, _ := users.Create(ctx, "alice", "h1")
	u2, _ := users.Create(ctx, "bob", "h2")

	p1 := &models.MetadataProfile{Name: "Alice Profile"}
	if err := repo.Create(ctx, p1); err != nil {
		t.Fatalf("create p1: %v", err)
	}
	p2 := &models.MetadataProfile{Name: "Bob Profile"}
	if err := repo.Create(ctx, p2); err != nil {
		t.Fatalf("create p2: %v", err)
	}
	setOwner(t, database, "metadata_profiles", p1.ID, u1.ID)
	setOwner(t, database, "metadata_profiles", p2.ID, u2.ID)
	p1, _ = repo.GetByID(ctx, p1.ID)
	p2, _ = repo.GetByID(ctx, p2.ID)
	return authzMetadataProfileFixture{database: database, repo: repo, u1: u1.ID, u2: u2.ID, p1: p1, p2: p2}
}

func TestMetadataProfile_Get_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserMetadataProfiles(t)

	h := NewMetadataProfileHandler(f.repo)
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodGet, "/api/v1/metadataprofile/"+strconv.FormatInt(f.p1.ID, 10), f.p1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user metadata profile Get must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMetadataProfile_Get_AdminCanSeeAllWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserMetadataProfiles(t)

	h := NewMetadataProfileHandler(f.repo)
	ctx := withAuthCtx(context.Background(), 99, "admin")
	req := newRequestForID(http.MethodGet, "/api/v1/metadataprofile/"+strconv.FormatInt(f.p1.ID, 10), f.p1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin Get must succeed; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMetadataProfile_Get_GateOffAllowsCrossUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserMetadataProfiles(t)

	h := NewMetadataProfileHandler(f.repo)
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodGet, "/api/v1/metadataprofile/"+strconv.FormatInt(f.p1.ID, 10), f.p1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gate off must preserve cross-user access; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMetadataProfile_Delete_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserMetadataProfiles(t)

	h := NewMetadataProfileHandler(f.repo)
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodDelete, "/api/v1/metadataprofile/"+strconv.FormatInt(f.p1.ID, 10), f.p1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user metadata profile Delete must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := f.repo.GetByID(context.Background(), f.p1.ID); got == nil {
		t.Error("Delete must not remove the row when blocked")
	}
}

// --- QualityProfile --------------------------------------------------------

type authzQualityProfileFixture struct {
	database *sql.DB
	repo     *db.QualityProfileRepo
	u1, u2   int64
	p1       *models.QualityProfile
}

func seedTwoUserQualityProfile(t *testing.T) authzQualityProfileFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	users := db.NewUserRepo(database)
	repo := db.NewQualityProfileRepo(database)
	ctx := context.Background()

	u1, _ := users.Create(ctx, "alice", "h1")
	u2, _ := users.Create(ctx, "bob", "h2")

	p1 := &models.QualityProfile{
		Name:           "Alice Quality",
		Cutoff:         "epub",
		UpgradeAllowed: true,
		Items:          []models.QualityItem{{Quality: "epub", Allowed: true}},
	}
	if err := repo.Create(ctx, p1); err != nil {
		t.Fatalf("create p1: %v", err)
	}
	setOwner(t, database, "quality_profiles", p1.ID, u1.ID)
	p1, _ = repo.GetByID(ctx, p1.ID)
	return authzQualityProfileFixture{database: database, repo: repo, u1: u1.ID, u2: u2.ID, p1: p1}
}

func TestQualityProfile_Get_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserQualityProfile(t)

	h := NewQualityProfileHandler(f.repo)
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodGet, "/api/v1/qualityprofile/"+strconv.FormatInt(f.p1.ID, 10), f.p1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user quality profile Get must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestQualityProfile_Get_GateOffAllowsCrossUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserQualityProfile(t)

	h := NewQualityProfileHandler(f.repo)
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodGet, "/api/v1/qualityprofile/"+strconv.FormatInt(f.p1.ID, 10), f.p1.ID, ctx)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gate off must preserve cross-user access; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// --- RootFolder ------------------------------------------------------------

type authzRootFolderFixture struct {
	database *sql.DB
	repo     *db.RootFolderRepo
	u1, u2   int64
	rf       *models.RootFolder
}

func seedTwoUserRootFolder(t *testing.T) authzRootFolderFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	users := db.NewUserRepo(database)
	repo := db.NewRootFolderRepo(database)
	ctx := context.Background()

	u1, _ := users.Create(ctx, "alice", "h1")
	u2, _ := users.Create(ctx, "bob", "h2")

	rf, err := repo.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("create rf: %v", err)
	}
	setOwner(t, database, "root_folders", rf.ID, u1.ID)
	rf, _ = repo.GetByID(ctx, rf.ID)
	return authzRootFolderFixture{database: database, repo: repo, u1: u1.ID, u2: u2.ID, rf: rf}
}

func TestRootFolder_Delete_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserRootFolder(t)

	h := NewRootFolderHandler(f.repo)
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodDelete, "/api/v1/rootfolder/"+strconv.FormatInt(f.rf.ID, 10), f.rf.ID, ctx)
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user root folder Delete must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := f.repo.GetByID(context.Background(), f.rf.ID); got == nil {
		t.Error("Delete must not remove the row when blocked")
	}
}

func TestRootFolder_Delete_GateOffAllowsCrossUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserRootFolder(t)

	h := NewRootFolderHandler(f.repo)
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodDelete, "/api/v1/rootfolder/"+strconv.FormatInt(f.rf.ID, 10), f.rf.ID, ctx)
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("gate off must preserve pre-fix cross-user delete; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// --- Recommendation --------------------------------------------------------

type authzRecommendationFixture struct {
	database *sql.DB
	repo     *db.RecommendationRepo
	u1, u2   int64
	recID    int64
}

func seedTwoUserRecommendation(t *testing.T) authzRecommendationFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	users := db.NewUserRepo(database)
	repo := db.NewRecommendationRepo(database)
	ctx := context.Background()

	u1, _ := users.Create(ctx, "alice", "h1")
	u2, _ := users.Create(ctx, "bob", "h2")

	// Insert one recommendation for u1 directly so we control the user_id.
	res, err := database.Exec(`
		INSERT INTO recommendations (
			user_id, foreign_id, rec_type, title, author_name, author_id,
			image_url, description, genres, rating, ratings_count, release_date,
			language, media_type, score, reason, series_id, series_pos,
			dismissed, batch_id, created_at
		) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, '[]', 0, 0, NULL, '', '', 0, '', NULL, '', 0, 'batch', CURRENT_TIMESTAMP)`,
		u1.ID, "rec-foreign-1", "series", "Alice Rec", "Some Author", "", "")
	if err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}
	recID, _ := res.LastInsertId()
	return authzRecommendationFixture{database: database, repo: repo, u1: u1.ID, u2: u2.ID, recID: recID}
}

func TestRecommendation_Dismiss_CrossUserBlockedWhenGateOn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserRecommendation(t)

	h := &RecommendationHandler{recs: f.repo, appCtx: context.Background()}
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodPost, "/api/v1/recommendations/"+strconv.FormatInt(f.recID, 10)+"/dismiss", f.recID, ctx)
	rec := httptest.NewRecorder()
	h.Dismiss(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user recommendation Dismiss must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecommendation_Dismiss_GateOffAllowsCrossUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := seedTwoUserRecommendation(t)

	h := &RecommendationHandler{recs: f.repo, appCtx: context.Background()}
	ctx := withAuthCtx(context.Background(), f.u2, "user")
	req := newRequestForID(http.MethodPost, "/api/v1/recommendations/"+strconv.FormatInt(f.recID, 10)+"/dismiss", f.recID, ctx)
	rec := httptest.NewRecorder()
	h.Dismiss(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("gate off must preserve pre-fix cross-user Dismiss; got %d body=%s", rec.Code, rec.Body.String())
	}
}
