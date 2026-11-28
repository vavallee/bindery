package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type fakeRecommendationEngine struct{}

func (fakeRecommendationEngine) Run(context.Context, int64) error { return nil }

// TestRecommendationListScopedToCaller proves the feed is per-user: a request
// authenticated as one user must never see another user's recommendations.
// Before the fix the handler hardcoded user id 1, so every caller shared one
// feed — this test fails against that code (alice's request returned user 1's
// rows, not her own).
func TestRecommendationListScopedToCaller(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	recRepo := db.NewRecommendationRepo(database)

	const alice, bob int64 = 10, 20
	if err := recRepo.ReplaceBatch(ctx, alice, []models.RecommendationCandidate{{
		ForeignID: "hc:alice-book", RecType: models.RecTypeListCross,
		Title: "Alice Book", Genres: []string{}, Score: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recRepo.ReplaceBatch(ctx, bob, []models.RecommendationCandidate{{
		ForeignID: "hc:bob-book", RecType: models.RecTypeListCross,
		Title: "Bob Book", Genres: []string{}, Score: 1,
	}}); err != nil {
		t.Fatal(err)
	}

	handler := NewRecommendationHandler(recRepo, fakeRecommendationEngine{}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil)
	req = req.WithContext(auth.WithUserID(req.Context(), alice))
	rec := httptest.NewRecorder()
	handler.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []models.Recommendation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("alice should see exactly her 1 recommendation, got %d: %+v", len(got), got)
	}
	if got[0].ForeignID != "hc:alice-book" {
		t.Fatalf("cross-user leak: alice saw %q, want hc:alice-book", got[0].ForeignID)
	}
}

func TestRecommendationAddHydratesHardcoverEditions(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	recRepo := db.NewRecommendationRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	editionRepo := db.NewEditionRepo(database)

	author := &models.Author{
		ForeignID:        "hc:rec-author",
		Name:             "Rec Author",
		SortName:         "Author, Rec",
		MetadataProvider: "hardcover",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	if err := recRepo.ReplaceBatch(ctx, 1, []models.RecommendationCandidate{{
		ForeignID:  "hc:rec-book",
		RecType:    models.RecTypeListCross,
		Title:      "Recommended Book",
		AuthorName: author.Name,
		AuthorID:   &author.ID,
		MediaType:  models.MediaTypeAudiobook,
		Genres:     []string{},
		Score:      1,
	}}); err != nil {
		t.Fatal(err)
	}
	audioASIN := "B123REC000"
	provider := &stubMetaProvider{
		name: "hardcover",
		editionsByBook: map[string][]models.Edition{
			"hc:rec-book": {{
				ForeignID: "hc:rec-book-audio",
				Title:     "Recommended Book",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}},
		},
	}
	searcher := newMockBookSearcher()
	handler := NewRecommendationHandler(recRepo, fakeRecommendationEngine{}, authorRepo, bookRepo, searcher).
		WithFinder(seriesRepo, nil).
		WithEditionHydration(editionRepo, metadata.NewAggregator(provider).WithAudnexClient(nil)).
		WithAppContext(ctx)

	rec := httptest.NewRecorder()
	handler.Add(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/1/add", nil), "id", "1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.ASIN != audioASIN {
		t.Fatalf("queued ASIN = %q, want %q", queued.ASIN, audioASIN)
	}
	book, err := bookRepo.GetByForeignID(ctx, "hc:rec-book")
	if err != nil {
		t.Fatal(err)
	}
	if book == nil || book.ASIN != audioASIN {
		t.Fatalf("book was not hydrated: %+v", book)
	}
	editions, err := editionRepo.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:rec-book-audio" {
		t.Fatalf("expected hydrated edition, got %+v", editions)
	}
}

// TestRecommendationAddWidensUnpinnedMediaType pins the #2768 rule on the
// recommendation path. A recommendation's format is filled by the recommender
// from the provider, or defaulted to ebook; the user never chose it, and the
// add request carries no format. Under the AddBook rule that is not a pin, so
// hydration must still widen an ebook recommendation to "both" and take the
// audiobook's ASIN when Hardcover lists an audio edition.
func TestRecommendationAddWidensUnpinnedMediaType(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	recRepo := db.NewRecommendationRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	editionRepo := db.NewEditionRepo(database)

	author := &models.Author{
		ForeignID:        "hc:rec-ebook-author",
		Name:             "Rec Ebook Author",
		SortName:         "Author, Rec Ebook",
		MetadataProvider: "hardcover",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	if err := recRepo.ReplaceBatch(ctx, 1, []models.RecommendationCandidate{{
		ForeignID:  "hc:rec-ebook",
		RecType:    models.RecTypeListCross,
		Title:      "Recommended Ebook",
		AuthorName: author.Name,
		AuthorID:   &author.ID,
		MediaType:  models.MediaTypeEbook,
		Genres:     []string{},
		Score:      1,
	}}); err != nil {
		t.Fatal(err)
	}
	audioASIN := "B2768REC000"
	provider := &stubMetaProvider{
		name: "hardcover",
		editionsByBook: map[string][]models.Edition{
			"hc:rec-ebook": {{
				ForeignID: "hc:rec-ebook-audio",
				Title:     "Recommended Ebook",
				ASIN:      &audioASIN,
				Format:    "Audiobook",
				Monitored: true,
			}},
		},
	}
	handler := NewRecommendationHandler(recRepo, fakeRecommendationEngine{}, authorRepo, bookRepo, nil).
		WithFinder(seriesRepo, nil).
		WithEditionHydration(editionRepo, metadata.NewAggregator(provider).WithAudnexClient(nil)).
		WithAppContext(ctx)

	rec := httptest.NewRecorder()
	handler.Add(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/1/add", nil), "id", "1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	book, err := bookRepo.GetByForeignID(ctx, "hc:rec-ebook")
	if err != nil {
		t.Fatal(err)
	}
	if book == nil {
		t.Fatal("recommended book was not created")
	}
	if book.MediaType != models.MediaTypeBoth {
		t.Fatalf("MediaType = %q, want both (a recommendation's format is not a pin, so hydration may widen it)", book.MediaType)
	}
	if book.ASIN != audioASIN {
		t.Fatalf("ASIN = %q, want %q from the audio edition", book.ASIN, audioASIN)
	}
	// Hydration ran and stored the audio edition.
	editions, err := editionRepo.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:rec-ebook-audio" {
		t.Fatalf("expected hydrated edition, got %+v", editions)
	}
}

// recommendationAuthorFixture seeds an author owned by alice (or by nobody
// when shared is set) and one recommendation for bob that names that author,
// by id when byID is set and by name otherwise, and returns the handler plus
// the ids a test needs.
func recommendationAuthorFixture(t *testing.T, tenancy, byID, shared bool) (h *RecommendationHandler, books *db.BookRepo, recID, aliceAuthorID, alice, bob int64) {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, tenancy)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	users := db.NewUserRepo(database)
	a, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}
	recRepo := db.NewRecommendationRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)

	author := &models.Author{
		ForeignID: "hc:alice-author", Name: "Alice Writer", SortName: "Writer, Alice",
		MetadataProvider: "hardcover", Monitored: true,
	}
	owner := a.ID
	if shared {
		owner = 0
	}
	if err := authorRepo.CreateForUser(ctx, author, owner); err != nil {
		t.Fatal(err)
	}
	cand := models.RecommendationCandidate{
		ForeignID: "hc:rec-cross", RecType: models.RecTypeListCross, Title: "Cross Book",
		AuthorName: author.Name, MediaType: models.MediaTypeEbook, Genres: []string{}, Score: 1,
	}
	if byID {
		cand.AuthorID = &author.ID
	}
	if err := recRepo.ReplaceBatch(ctx, b.ID, []models.RecommendationCandidate{cand}); err != nil {
		t.Fatal(err)
	}
	recs, err := recRepo.List(ctx, b.ID, "", 10, 0)
	if err != nil || len(recs) != 1 {
		t.Fatalf("bob's recommendations = %+v err=%v", recs, err)
	}
	h = NewRecommendationHandler(recRepo, fakeRecommendationEngine{}, authorRepo, bookRepo, nil).WithAppContext(ctx)
	return h, bookRepo, recs[0].ID, author.ID, a.ID, b.ID
}

func addRecommendationAs(h *RecommendationHandler, ctx context.Context, recID int64) *httptest.ResponseRecorder {
	id := strconv.FormatInt(recID, 10)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/"+id+"/add", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.Add(rec, withURLParam(req, "id", id))
	return rec
}

// With tenancy on, adding a recommendation must not file the new book under
// another user's author. Before the fix the author was resolved from every
// user's authors, so bob's add created a wanted, monitored book inside
// alice's author, and the search it queued downloaded into her library.
func TestRecommendationAdd_TenancyDoesNotUseAnotherUsersAuthor(t *testing.T) {
	for _, byID := range []bool{false, true} {
		t.Run(map[bool]string{false: "by name", true: "by id"}[byID], func(t *testing.T) {
			h, books, recID, aliceAuthorID, _, bob := recommendationAuthorFixture(t, true, byID, false)
			rec := addRecommendationAs(h, auth.WithUserID(context.Background(), bob), recID)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "Alice") {
				t.Fatalf("response names alice's author: %s", rec.Body.String())
			}
			under, err := books.ListByAuthor(context.Background(), aliceAuthorID)
			if err != nil {
				t.Fatal(err)
			}
			if len(under) != 0 {
				t.Fatalf("bob's add filed %d book(s) under alice's author: %+v", len(under), under)
			}
		})
	}
}

// The admin manages every library, and tenancy off is one shared library, so
// both still resolve another account's author. The book takes that author's
// owner, as Add Book gives it (#4090), so it lands in alice's library rather
// than in everyone's.
func TestRecommendationAdd_AdminAndTenancyOffStillResolveAuthor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
	}{
		{"admin under tenancy", true},
		{"tenancy off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, books, recID, aliceAuthorID, alice, bob := recommendationAuthorFixture(t, tc.tenancy, false, false)
			ctx := auth.WithUserID(context.Background(), bob)
			if tc.tenancy {
				ctx = auth.WithUserRole(ctx, "admin")
			}
			rec := addRecommendationAs(h, ctx, recID)
			if rec.Code != http.StatusCreated {
				t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
			}
			book, err := books.GetByForeignID(context.Background(), "hc:rec-cross")
			if err != nil || book == nil {
				t.Fatalf("book = %+v err=%v", book, err)
			}
			if book.AuthorID != aliceAuthorID || book.OwnerUserID != alice {
				t.Fatalf("book author=%d owner=%d, want author %d owner alice (%d)", book.AuthorID, book.OwnerUserID, aliceAuthorID, alice)
			}
		})
	}
}

// ownerAfterAddBothWays adds the work hc:rec-cross under one author twice, on
// two fresh databases: once through Add Book and once from a recommendation,
// both as the same caller. authorOwner picks who owns the author: "caller",
// "other" (another account) or "shared" (nobody). It returns the owner each
// path stamped on the book, and the ids of the caller and the other account.
func ownerAfterAddBothWays(t *testing.T, tenancy bool, role, authorOwner string) (viaAddBook, viaRecommendation, caller, other int64) {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, tenancy)
	seed := func() (*sql.DB, *db.AuthorRepo, *db.BookRepo, *models.Author, int64, int64) {
		database, err := db.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Close() })
		database.SetMaxOpenConns(1)
		ctx := context.Background()
		users := db.NewUserRepo(database)
		c, err := users.Create(ctx, "caller", "h1")
		if err != nil {
			t.Fatal(err)
		}
		o, err := users.Create(ctx, "other", "h2")
		if err != nil {
			t.Fatal(err)
		}
		owner := map[string]int64{"caller": c.ID, "other": o.ID, "shared": 0}[authorOwner]
		authorRepo := db.NewAuthorRepo(database)
		author := &models.Author{
			ForeignID: "hc:rec-author", Name: "Rec Writer", SortName: "Writer, Rec",
			MetadataProvider: "hardcover", Monitored: true,
		}
		if err := authorRepo.CreateForUser(ctx, author, owner); err != nil {
			t.Fatal(err)
		}
		return database, authorRepo, db.NewBookRepo(database), author, c.ID, o.ID
	}
	callerCtx := func(id int64) context.Context {
		return auth.WithUserRole(auth.WithUserID(context.Background(), id), role)
	}

	// Add Book.
	database, authorRepo, bookRepo, author, c, o := seed()
	provider := &stubMetaProvider{
		name: "hardcover",
		getBookByID: map[string]*models.Book{"hc:rec-cross": {
			ForeignID: "hc:rec-cross", Title: "Cross Book", SortTitle: "Cross Book",
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover",
		}},
	}
	ah := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, db.NewMetadataProfileRepo(database), nil)
	body, _ := json.Marshal(map[string]any{
		"foreignBookId": "hc:rec-cross", "foreignAuthorId": author.ForeignID, "authorName": author.Name,
	})
	parent, cancel := context.WithTimeout(callerCtx(c), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)).WithContext(parent)
	rec := httptest.NewRecorder()
	ah.AddBook(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Add Book: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := bookRepo.GetByForeignID(context.Background(), "hc:rec-cross")
	if err != nil || got == nil {
		t.Fatalf("Add Book: book = %+v err=%v", got, err)
	}
	viaAddBook = got.OwnerUserID

	// Recommendation, on a fresh database with the same seed.
	database, authorRepo, bookRepo, author, c2, _ := seed()
	if c2 != c {
		t.Fatalf("seed gave caller %d then %d", c, c2)
	}
	recRepo := db.NewRecommendationRepo(database)
	if err := recRepo.ReplaceBatch(context.Background(), c, []models.RecommendationCandidate{{
		ForeignID: "hc:rec-cross", RecType: models.RecTypeListCross, Title: "Cross Book",
		AuthorID: &author.ID, AuthorName: author.Name, MediaType: models.MediaTypeEbook, Genres: []string{}, Score: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	recs, err := recRepo.List(context.Background(), c, "", 10, 0)
	if err != nil || len(recs) != 1 {
		t.Fatalf("caller's recommendations = %+v err=%v", recs, err)
	}
	rh := NewRecommendationHandler(recRepo, fakeRecommendationEngine{}, authorRepo, bookRepo, nil).WithAppContext(context.Background())
	if rec := addRecommendationAs(rh, callerCtx(c), recs[0].ID); rec.Code != http.StatusCreated {
		t.Fatalf("recommendation: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err = bookRepo.GetByForeignID(context.Background(), "hc:rec-cross")
	if err != nil || got == nil {
		t.Fatalf("recommendation: book = %+v err=%v", got, err)
	}
	return viaAddBook, got.OwnerUserID, c, o
}

// Adding the same work under the same author gives the book the same owner
// whether it comes from Add Book or from a recommendation (#4090). Before the
// fix a recommendation stamped auth.ListScopeUserID, which is 0 for an admin,
// so an admin's recommendation under their own author made a book with no
// owner that every user saw and none but an admin could change, where Add
// Book filed it in the admin's library.
func TestRecommendationAdd_OwnerMatchesAddBook(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tenancy     bool
		role        string
		authorOwner string
		want        string // "caller", "other" or "none"
	}{
		{"admin under own author", true, "admin", "caller", "caller"},
		{"admin under another account's author", true, "admin", "other", "other"},
		{"admin under shared author", true, "admin", "shared", "none"},
		{"user under own author", true, "user", "caller", "caller"},
		{"user under shared author", true, "user", "shared", "caller"},
		{"tenancy off under own author", false, "admin", "caller", "caller"},
		{"tenancy off under shared author", false, "user", "shared", "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viaAddBook, viaRec, caller, other := ownerAfterAddBothWays(t, tc.tenancy, tc.role, tc.authorOwner)
			want := map[string]int64{"caller": caller, "other": other, "none": 0}[tc.want]
			if viaAddBook != want {
				t.Fatalf("Add Book owner = %d, want %s (%d)", viaAddBook, tc.want, want)
			}
			if viaRec != viaAddBook {
				t.Fatalf("recommendation owner = %d, Add Book owner = %d: the two add paths disagree", viaRec, viaAddBook)
			}
		})
	}
}

// Under tenancy, a recommendation filed under a shared (unowned) author is
// still the caller's book. With no owner it would appear in every user's
// library, alice's included, as a wanted book she never asked for.
func TestRecommendationAdd_TenancySharedAuthorBookIsCallers(t *testing.T) {
	h, books, recID, sharedAuthorID, _, bob := recommendationAuthorFixture(t, true, false, true)
	rec := addRecommendationAs(h, auth.WithUserID(context.Background(), bob), recID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	book, err := books.GetByForeignID(context.Background(), "hc:rec-cross")
	if err != nil || book == nil {
		t.Fatalf("book = %+v err=%v", book, err)
	}
	if book.AuthorID != sharedAuthorID || book.OwnerUserID != bob {
		t.Fatalf("book author=%d owner=%d, want author %d owner bob (%d)", book.AuthorID, book.OwnerUserID, sharedAuthorID, bob)
	}
}

// An admin's recommendation that carries only an author name, when several
// accounts hold an author with that name: a shared author wins, and with
// none the book is left unowned rather than put in whichever account's
// author sorted first (#4090).
func TestRecommendationAdd_AdminNameMatchAcrossAccounts(t *testing.T) {
	for _, withShared := range []bool{false, true} {
		t.Run(map[bool]string{false: "no shared author", true: "shared author"}[withShared], func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			ctx := context.Background()
			users := db.NewUserRepo(database)
			admin, _ := users.Create(ctx, "admin", "h0")
			alice, _ := users.Create(ctx, "alice", "h1")
			bob, _ := users.Create(ctx, "bob", "h2")
			authorRepo := db.NewAuthorRepo(database)
			bookRepo := db.NewBookRepo(database)
			recRepo := db.NewRecommendationRepo(database)
			mk := func(fid string, owner int64) *models.Author {
				a := &models.Author{ForeignID: fid, Name: "Same Name", SortName: "Name, Same", MetadataProvider: "hardcover"}
				if err := authorRepo.CreateForUser(ctx, a, owner); err != nil {
					t.Fatal(err)
				}
				return a
			}
			mk("hc:same-alice", alice.ID)
			mk("hc:same-bob", bob.ID)
			var sharedAuthor *models.Author
			if withShared {
				sharedAuthor = mk("hc:same-shared", 0)
			}
			if err := recRepo.ReplaceBatch(ctx, admin.ID, []models.RecommendationCandidate{{
				ForeignID: "hc:same-book", RecType: models.RecTypeListCross, Title: "Same Book",
				AuthorName: "Same Name", MediaType: models.MediaTypeEbook, Genres: []string{}, Score: 1,
			}}); err != nil {
				t.Fatal(err)
			}
			recs, err := recRepo.List(ctx, admin.ID, "", 10, 0)
			if err != nil || len(recs) != 1 {
				t.Fatalf("recommendations = %+v err=%v", recs, err)
			}
			h := NewRecommendationHandler(recRepo, fakeRecommendationEngine{}, authorRepo, bookRepo, nil).WithAppContext(ctx)
			adminCtx := auth.WithUserRole(auth.WithUserID(ctx, admin.ID), "admin")
			if rec := addRecommendationAs(h, adminCtx, recs[0].ID); rec.Code != http.StatusCreated {
				t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
			}
			book, err := bookRepo.GetByForeignID(ctx, "hc:same-book")
			if err != nil || book == nil {
				t.Fatalf("book = %+v err=%v", book, err)
			}
			if book.OwnerUserID != 0 {
				t.Fatalf("book owner = %d, want none (alice %d, bob %d)", book.OwnerUserID, alice.ID, bob.ID)
			}
			if withShared && book.AuthorID != sharedAuthor.ID {
				t.Fatalf("book filed under author %d, want the shared author %d", book.AuthorID, sharedAuthor.ID)
			}
		})
	}
}

func TestPickAuthorByName(t *testing.T) {
	a := func(id, owner int64, name string) models.Author {
		return models.Author{ID: id, OwnerUserID: owner, Name: name}
	}
	for _, tc := range []struct {
		name          string
		authors       []models.Author
		wantID        int64
		wantAmbiguous bool
	}{
		{"no match", []models.Author{a(1, 5, "Other")}, 0, false},
		{"one match", []models.Author{a(1, 5, "N")}, 1, false},
		{"same owner twice keeps the first", []models.Author{a(1, 5, "N"), a(2, 5, "N")}, 1, false},
		{"two accounts, no shared", []models.Author{a(1, 5, "N"), a(2, 6, "N")}, 1, true},
		{"two accounts and a shared one", []models.Author{a(1, 5, "N"), a(2, 6, "N"), a(3, 0, "N")}, 3, false},
		{"own and shared", []models.Author{a(1, 5, "N"), a(3, 0, "N")}, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, amb := pickAuthorByName(tc.authors, "N")
			var id int64
			if got != nil {
				id = got.ID
			}
			if id != tc.wantID || amb != tc.wantAmbiguous {
				t.Fatalf("picked %d ambiguous %v, want %d %v", id, amb, tc.wantID, tc.wantAmbiguous)
			}
		})
	}
}
