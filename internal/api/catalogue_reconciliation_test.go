package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type partialSnapshotProvider struct {
	stubMetaProvider
	complete bool
}

type failingSnapshotProvider struct {
	stubMetaProvider
	err error
}

func (p *failingSnapshotProvider) GetAuthorWorksSnapshot(_ context.Context, _ string) ([]models.Book, bool, error) {
	return nil, false, p.err
}

type editionResultsProvider struct {
	partialSnapshotProvider
	editions map[string][]models.Edition
	errors   map[string]error
}

func (p *editionResultsProvider) GetEditions(_ context.Context, foreignID string) ([]models.Edition, error) {
	if err := p.errors[foreignID]; err != nil {
		return nil, err
	}
	return p.editions[foreignID], nil
}

func (p *partialSnapshotProvider) GetAuthorWorksSnapshot(_ context.Context, _ string) ([]models.Book, bool, error) {
	return p.works, p.complete, nil
}

type reconciliationFixture struct {
	handler  *AuthorHandler
	author   *models.Author
	books    *db.BookRepo
	profiles *db.MetadataProfileRepo
	database *sql.DB
}

func newReconciliationFixture(t *testing.T, provider metadata.Provider) reconciliationFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	profiles := db.NewMetadataProfileRepo(database)
	profile, err := profiles.GetByID(ctx, models.DefaultMetadataProfileID)
	if err != nil || profile == nil {
		t.Fatalf("load default metadata profile: profile=%+v err=%v", profile, err)
	}
	profile.AllowedLanguages = "eng"
	profile.UnknownLanguageBehavior = models.UnknownLanguageFail
	if err := profiles.Update(ctx, profile); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID: "hc:test-author", Name: "Test Author", SortName: "Author, Test",
		MetadataProvider: "hardcover", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	return reconciliationFixture{
		handler:  NewAuthorHandler(authors, nil, books, nil, metadata.NewAggregator(provider), nil, profiles, nil),
		author:   author,
		books:    books,
		profiles: profiles,
		database: database,
	}
}

func (f reconciliationFixture) createBook(t *testing.T, foreignID, title, provider, status string) *models.Book {
	t.Helper()
	book := &models.Book{
		ForeignID: foreignID, AuthorID: f.author.ID, Title: title, SortTitle: title,
		MetadataProvider: provider, Monitored: true, Status: status, MediaType: models.MediaTypeEbook,
	}
	if err := f.books.Create(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	return book
}

func reconciliationRequest(method, target string, authorID int64, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(authorID, 10))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestPreviewCatalogueReconciliation_ReportsOnlySafeCandidates(t *testing.T) {
	provider := &stubMetaProvider{name: "hardcover", works: []models.Book{
		{ForeignID: "hc:keep", Title: "Keep", Language: "eng", MetadataProvider: "hardcover"},
		{ForeignID: "hc:spanish", Title: "Spanish", Language: "spa", MetadataProvider: "hardcover"},
		{ForeignID: "hc:unknown", Title: "Unknown", Language: "", MetadataProvider: "hardcover"},
	}}
	f := newReconciliationFixture(t, provider)
	keep := f.createBook(t, "hc:keep", "Keep", "hardcover", models.BookStatusWanted)
	spanish := f.createBook(t, "hc:spanish", "Spanish", "hardcover", models.BookStatusWanted)
	staleProvider := f.createBook(t, "OL-stale", "Old OpenLibrary Work", "openlibrary", models.BookStatusWanted)
	missing := f.createBook(t, "hc:missing", "Removed Upstream", "hardcover", models.BookStatusWanted)
	unknown := f.createBook(t, "hc:unknown", "Unknown", "hardcover", models.BookStatusWanted)
	imported := f.createBook(t, "hc:owned", "Owned", "hardcover", models.BookStatusImported)
	withFile := f.createBook(t, "hc:partial-owned", "Partially Owned", "hardcover", models.BookStatusWanted)
	withFile.MediaType = models.MediaTypeBoth
	if err := f.books.Update(context.Background(), withFile); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(context.Background(), withFile.ID, models.MediaTypeEbook, "/library/owned.epub"); err != nil {
		t.Fatal(err)
	}
	excluded := f.createBook(t, "hc:excluded", "Excluded", "hardcover", models.BookStatusWanted)
	if err := f.books.SetExcluded(context.Background(), excluded.ID, true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	f.handler.PreviewCatalogueReconciliation(rec, reconciliationRequest(http.MethodGet, "/api/v1/author/1/catalogue-reconciliation", f.author.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got CatalogueReconciliation
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.ProviderComplete || got.Provider != "hardcover" {
		t.Errorf("provider status = %q complete=%v", got.Provider, got.ProviderComplete)
	}
	wantReasons := map[int64]string{
		spanish.ID:       reconcileReasonLanguage,
		staleProvider.ID: reconcileReasonProviderChanged,
		missing.ID:       reconcileReasonNotInCatalogue,
	}
	if len(got.Candidates) != len(wantReasons) {
		t.Fatalf("candidates = %+v, want %d", got.Candidates, len(wantReasons))
	}
	for _, candidate := range got.Candidates {
		if want := wantReasons[candidate.BookID]; want != candidate.Reason {
			t.Errorf("candidate %d reason = %q, want %q", candidate.BookID, candidate.Reason, want)
		}
	}
	if got.Summary.ProtectedFiles != 1 || got.Summary.ProtectedImported != 1 || got.Summary.ProtectedExcluded != 1 {
		t.Errorf("protection summary = %+v", got.Summary)
	}
	if got.Summary.Indeterminate != 1 {
		t.Errorf("indeterminate = %d, want 1 for unknown language", got.Summary.Indeterminate)
	}
	if got.Summary.Kept != 2 {
		t.Errorf("kept = %d, want 2 (%q and %q)", got.Summary.Kept, keep.Title, unknown.Title)
	}
	_ = imported
}

func TestApplyCatalogueReconciliation_RecomputesAndProtectsNewFiles(t *testing.T) {
	provider := &stubMetaProvider{name: "hardcover", works: []models.Book{
		{ForeignID: "hc:keep", Title: "Keep", Language: "eng", MetadataProvider: "hardcover"},
	}}
	f := newReconciliationFixture(t, provider)
	deleteMe := f.createBook(t, "hc:gone", "Gone", "hardcover", models.BookStatusWanted)
	becameOwned := f.createBook(t, "hc:owned-after-preview", "Owned after preview", "hardcover", models.BookStatusWanted)
	becameOwned.MediaType = models.MediaTypeBoth
	if err := f.books.Update(context.Background(), becameOwned); err != nil {
		t.Fatal(err)
	}

	preview, err := f.handler.buildCatalogueReconciliation(context.Background(), f.author)
	if err != nil || len(preview.Candidates) != 2 {
		t.Fatalf("preview candidates=%+v err=%v", preview.Candidates, err)
	}
	if err := f.books.AddBookFile(context.Background(), becameOwned.ID, models.MediaTypeEbook, "/library/new.epub"); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(applyCatalogueReconciliationRequest{BookIDs: []int64{deleteMe.ID, becameOwned.ID}})
	rec := httptest.NewRecorder()
	f.handler.ApplyCatalogueReconciliation(rec, reconciliationRequest(http.MethodPost, "/api/v1/author/1/catalogue-reconciliation", f.author.ID, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got CatalogueReconciliation
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Applied == nil || got.Applied.Deleted != 1 || got.Applied.Skipped != 1 {
		t.Fatalf("apply summary = %+v", got.Applied)
	}
	if book, _ := f.books.GetByID(context.Background(), deleteMe.ID); book != nil {
		t.Errorf("eligible metadata-only row survived: %+v", book)
	}
	if book, _ := f.books.GetByID(context.Background(), becameOwned.ID); book == nil {
		t.Error("file-bearing row was deleted after preview")
	}
}

func TestApplyCatalogueReconciliation_UsesOnlyUniqueCurrentCandidates(t *testing.T) {
	provider := &stubMetaProvider{name: "hardcover"}
	f := newReconciliationFixture(t, provider)
	deleteMe := f.createBook(t, "hc:gone", "Gone", "hardcover", models.BookStatusWanted)
	body, err := json.Marshal(applyCatalogueReconciliationRequest{
		BookIDs: []int64{0, -1, deleteMe.ID, deleteMe.ID, deleteMe.ID + 999},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.handler.ApplyCatalogueReconciliation(rec, reconciliationRequest(
		http.MethodPost,
		"/api/v1/author/1/catalogue-reconciliation",
		f.author.ID,
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got CatalogueReconciliation
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Applied == nil || got.Applied.Requested != 2 || got.Applied.Deleted != 1 || got.Applied.Skipped != 1 {
		t.Fatalf("apply summary = %+v, want requested=2 deleted=1 skipped=1", got.Applied)
	}
}

func TestPreviewCatalogueReconciliation_PartialProviderNeverUsesAbsence(t *testing.T) {
	provider := &partialSnapshotProvider{
		stubMetaProvider: stubMetaProvider{name: "hardcover", works: []models.Book{
			{ForeignID: "hc:spanish", Title: "Spanish", Language: "spa", MetadataProvider: "hardcover"},
		}},
		complete: false,
	}
	f := newReconciliationFixture(t, provider)
	missing := f.createBook(t, "hc:not-returned", "Not returned", "hardcover", models.BookStatusWanted)
	spanish := f.createBook(t, "hc:spanish", "Spanish", "hardcover", models.BookStatusWanted)

	got, err := f.handler.buildCatalogueReconciliation(context.Background(), f.author)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderComplete || got.Warning == "" {
		t.Fatalf("partial snapshot not reported: %+v", got)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].BookID != spanish.ID {
		t.Fatalf("partial candidates = %+v, want only explicit language rejection", got.Candidates)
	}
	if got.Summary.Indeterminate != 1 {
		t.Fatalf("indeterminate = %d, want the missing local row", got.Summary.Indeterminate)
	}
	if book, _ := f.books.GetByID(context.Background(), missing.ID); book == nil {
		t.Fatal("preview mutated the database")
	}
}

func TestPreviewCatalogueReconciliation_RejectedEmptyIDDoesNotMatchLocalBook(t *testing.T) {
	provider := &partialSnapshotProvider{
		stubMetaProvider: stubMetaProvider{name: "hardcover", works: []models.Book{
			{ForeignID: "", Title: "Rejected Provider Work", Language: "spa", MetadataProvider: "hardcover"},
		}},
		complete: false,
	}
	f := newReconciliationFixture(t, provider)
	local := f.createBook(t, "", "Unrelated Local Book", "hardcover", models.BookStatusWanted)

	got, err := f.handler.buildCatalogueReconciliation(context.Background(), f.author)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want empty-id local book protected by partial snapshot", got.Candidates)
	}
	if got.Summary.Kept != 1 || got.Summary.Indeterminate != 1 {
		t.Fatalf("summary = %+v, want unrelated local book kept as indeterminate", got.Summary)
	}
	if book, err := f.books.GetByID(context.Background(), local.ID); err != nil || book == nil {
		t.Fatalf("local book missing after preview: book=%+v err=%v", book, err)
	}
}

func TestPreviewCatalogueReconciliation_AcceptedEmptyIDDoesNotKeepLocalBook(t *testing.T) {
	provider := &partialSnapshotProvider{
		stubMetaProvider: stubMetaProvider{name: "hardcover", works: []models.Book{
			{ForeignID: "", Title: "Accepted Provider Work", Language: "eng", MetadataProvider: "hardcover"},
		}},
		complete: true,
	}
	f := newReconciliationFixture(t, provider)
	local := f.createBook(t, "", "Unrelated Local Book", "hardcover", models.BookStatusWanted)

	got, err := f.handler.buildCatalogueReconciliation(context.Background(), f.author)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].BookID != local.ID ||
		got.Candidates[0].Reason != reconcileReasonNotInCatalogue {
		t.Fatalf("candidates = %+v, want unrelated local book missing from current catalogue", got.Candidates)
	}
	if got.Summary.Kept != 0 {
		t.Fatalf("kept = %d, want 0", got.Summary.Kept)
	}
}

func TestPreviewCatalogueReconciliation_InvalidID(t *testing.T) {
	rec := httptest.NewRecorder()
	h := &AuthorHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/author/nope/catalogue-reconciliation", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nope")
	h.PreviewCatalogueReconciliation(rec, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestApplyCatalogueReconciliation_RejectsInvalidRequests(t *testing.T) {
	provider := &stubMetaProvider{name: "hardcover"}
	f := newReconciliationFixture(t, provider)
	tests := []struct {
		name string
		body []byte
	}{
		{name: "invalid json", body: []byte(`{`)},
		{name: "missing book ids", body: []byte(`{}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			f.handler.ApplyCatalogueReconciliation(rec, reconciliationRequest(
				http.MethodPost,
				"/api/v1/author/1/catalogue-reconciliation",
				f.author.ID,
				tt.body,
			))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}

	rec := httptest.NewRecorder()
	f.handler.ApplyCatalogueReconciliation(rec, reconciliationRequest(
		http.MethodPost, "/api/v1/author/0/catalogue-reconciliation", 0, []byte(`{"bookIds":[1]}`),
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid author status = %d, want 400", rec.Code)
	}
}

func TestCatalogueReconciliation_ReportsDependencyAndStorageErrors(t *testing.T) {
	t.Run("preview without metadata aggregator", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		f.handler.meta = nil
		rec := httptest.NewRecorder()
		f.handler.PreviewCatalogueReconciliation(rec, reconciliationRequest(
			http.MethodGet, "/api/v1/author/1/catalogue-reconciliation", f.author.ID, nil,
		))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("apply without metadata aggregator", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		f.handler.meta = nil
		rec := httptest.NewRecorder()
		f.handler.ApplyCatalogueReconciliation(rec, reconciliationRequest(
			http.MethodPost, "/api/v1/author/1/catalogue-reconciliation", f.author.ID, []byte(`{"bookIds":[1]}`),
		))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("profile configuration failure", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		f.handler.profiles = nil
		_, err := f.handler.buildCatalogueReconciliation(context.Background(), f.author)
		if err == nil || !strings.Contains(err.Error(), "metadata profiles") {
			t.Fatalf("error = %v, want metadata profile configuration error", err)
		}
	})

	t.Run("provider failure", func(t *testing.T) {
		providerErr := errors.New("provider unavailable")
		f := newReconciliationFixture(t, &failingSnapshotProvider{
			stubMetaProvider: stubMetaProvider{name: "hardcover"},
			err:              providerErr,
		})
		_, err := f.handler.buildCatalogueReconciliation(context.Background(), f.author)
		if !errors.Is(err, providerErr) {
			t.Fatalf("error = %v, want wrapped provider error", err)
		}
	})

	t.Run("local catalogue query failure", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		if _, err := f.database.ExecContext(context.Background(), "DROP TABLE books"); err != nil {
			t.Fatal(err)
		}
		_, err := f.handler.buildCatalogueReconciliation(context.Background(), f.author)
		if err == nil || !strings.Contains(err.Error(), "list local author catalogue") {
			t.Fatalf("error = %v, want local catalogue context", err)
		}
	})

	t.Run("identifier query failure", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		f.createBook(t, "hc:gone", "Gone", "hardcover", models.BookStatusWanted)
		if _, err := f.database.ExecContext(context.Background(), "DROP TABLE book_identifiers"); err != nil {
			t.Fatal(err)
		}
		_, err := f.handler.buildCatalogueReconciliation(context.Background(), f.author)
		if err == nil || !strings.Contains(err.Error(), "list identifiers for author "+strconv.FormatInt(f.author.ID, 10)) {
			t.Fatalf("error = %v, want identifier context", err)
		}
	})

	t.Run("guarded delete failure", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		candidate := f.createBook(t, "hc:gone", "Gone", "hardcover", models.BookStatusWanted)
		if _, err := f.database.ExecContext(context.Background(), `
			CREATE TRIGGER fail_reconciliation_delete
			BEFORE DELETE ON books
			BEGIN
				SELECT RAISE(ABORT, 'blocked delete');
			END`); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(applyCatalogueReconciliationRequest{BookIDs: []int64{candidate.ID}})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		f.handler.ApplyCatalogueReconciliation(rec, reconciliationRequest(
			http.MethodPost, "/api/v1/author/1/catalogue-reconciliation", f.author.ID, body,
		))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
		}
		if book, err := f.books.GetByID(context.Background(), candidate.ID); err != nil || book == nil {
			t.Fatalf("candidate missing after failed delete: book=%+v err=%v", book, err)
		}
	})
}

func TestOwnedAuthorFromRequest_RejectsMissingUnownedAndUnavailableAuthors(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		rec := httptest.NewRecorder()
		f.handler.PreviewCatalogueReconciliation(rec, reconciliationRequest(
			http.MethodGet, "/api/v1/author/999/catalogue-reconciliation", f.author.ID+999, nil,
		))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("owned by another user", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		owner, err := db.NewUserRepo(f.database).Create(context.Background(), "author-owner", "password-hash")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.database.ExecContext(context.Background(), "UPDATE authors SET owner_user_id = ? WHERE id = ?", owner.ID, f.author.ID); err != nil {
			t.Fatal(err)
		}
		t.Setenv(auth.EnforceTenancyEnv, "true")
		req := reconciliationRequest(http.MethodGet, "/api/v1/author/1/catalogue-reconciliation", f.author.ID, nil)
		ctx := auth.WithUserRole(auth.WithUserID(req.Context(), 7), "user")
		rec := httptest.NewRecorder()
		f.handler.PreviewCatalogueReconciliation(rec, req.WithContext(ctx))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("repository unavailable", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		if err := f.database.Close(); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		f.handler.PreviewCatalogueReconciliation(rec, reconciliationRequest(
			http.MethodGet, "/api/v1/author/1/catalogue-reconciliation", f.author.ID, nil,
		))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})
}

func TestReconciliationProfile_ReportsConfigurationErrors(t *testing.T) {
	if _, err := (&AuthorHandler{}).reconciliationProfile(context.Background(), &models.Author{}); err == nil {
		t.Fatal("missing profile repository returned nil error")
	}

	t.Run("profile not found", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		missingID := int64(999)
		author := *f.author
		author.MetadataProfileID = &missingID
		if _, err := f.handler.reconciliationProfile(context.Background(), &author); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("error = %v, want not found", err)
		}
	})

	t.Run("profile query failure", func(t *testing.T) {
		f := newReconciliationFixture(t, &stubMetaProvider{name: "hardcover"})
		if _, err := f.database.ExecContext(context.Background(), "DROP TABLE metadata_profiles"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.handler.reconciliationProfile(context.Background(), f.author); err == nil || !strings.Contains(err.Error(), "load metadata profile") {
			t.Fatalf("error = %v, want load context", err)
		}
	})
}

func TestBuildCatalogueReconciliation_UsesConservativeEditionAndIdentityEvidence(t *testing.T) {
	pages100 := 100
	pages300 := 300
	editionErr := errors.New("edition lookup failed")
	provider := &editionResultsProvider{
		partialSnapshotProvider: partialSnapshotProvider{
			stubMetaProvider: stubMetaProvider{name: "hardcover", works: []models.Book{
				{ForeignID: "", Title: "No ID", Language: "eng", MetadataProvider: "hardcover"},
				{ForeignID: "dnb:123", Title: "DNB Work", Language: "eng", MetadataProvider: "dnb"},
				{ForeignID: "hc:short", Title: "Short Work", Language: "eng", MetadataProvider: "hardcover"},
				{ForeignID: "hc:error", Title: "Edition Error", Language: "eng", MetadataProvider: "hardcover"},
				{ForeignID: "hc:accepted", Title: "Accepted Work", Language: "eng", MetadataProvider: "hardcover"},
				{ForeignID: "hc:spanish", Title: "Matched Spanish", Language: "spa", MetadataProvider: "hardcover"},
			}},
			complete: true,
		},
		editions: map[string][]models.Edition{
			"hc:short":    {{NumPages: &pages100}},
			"hc:accepted": {{NumPages: &pages300}},
		},
		errors: map[string]error{"hc:error": editionErr},
	}
	f := newReconciliationFixture(t, provider)
	profile, err := f.profiles.GetByID(context.Background(), models.DefaultMetadataProfileID)
	if err != nil || profile == nil {
		t.Fatalf("load profile: profile=%+v err=%v", profile, err)
	}
	profile.MinPages = 200
	if err := f.profiles.Update(context.Background(), profile); err != nil {
		t.Fatal(err)
	}

	noID := f.createBook(t, "local:no-id", "No ID", "hardcover", models.BookStatusWanted)
	dnb := f.createBook(t, "dnb:123", "DNB Work", "dnb", models.BookStatusWanted)
	short := f.createBook(t, "hc:short", "Short Work", "hardcover", models.BookStatusWanted)
	editionUnknown := f.createBook(t, "hc:error", "Edition Error", "hardcover", models.BookStatusWanted)
	alias := f.createBook(t, "OL-alias", "Different Local Title", "openlibrary", models.BookStatusWanted)
	if err := f.books.UpsertBookIdentifier(context.Background(), alias.ID, "hc:accepted"); err != nil {
		t.Fatal(err)
	}
	titleMatch := f.createBook(t, "OL-title", "Accepted Work", "openlibrary", models.BookStatusWanted)
	rejectedTitle := f.createBook(t, "OL-spanish", "Matched Spanish", "openlibrary", models.BookStatusWanted)
	fallbackProvider := f.createBook(t, "OL-stale", "Stale Work", "", models.BookStatusWanted)
	downloading := f.createBook(t, "hc:downloading", "Downloading", "hardcover", models.BookStatusDownloading)
	otherOwner, err := db.NewUserRepo(f.database).Create(context.Background(), "other-owner", "password-hash")
	if err != nil {
		t.Fatal(err)
	}
	nonOwner := &models.Book{
		ForeignID: "hc:other-owner", AuthorID: f.author.ID, Title: "Other Owner", SortTitle: "Other Owner",
		MetadataProvider: "hardcover", Monitored: true, Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, OwnerUserID: otherOwner.ID,
	}
	if err := f.books.Create(context.Background(), nonOwner); err != nil {
		t.Fatal(err)
	}

	t.Setenv(auth.EnforceTenancyEnv, "true")
	ctx := auth.WithUserRole(auth.WithUserID(context.Background(), 7), "user")
	got, err := f.handler.buildCatalogueReconciliation(ctx, f.author)
	if err != nil {
		t.Fatal(err)
	}
	wantCandidates := map[int64]string{
		short.ID:            reconcileReasonPages,
		rejectedTitle.ID:    reconcileReasonLanguage,
		fallbackProvider.ID: reconcileReasonProviderChanged,
	}
	if len(got.Candidates) != len(wantCandidates) {
		t.Fatalf("candidates = %+v, want %d", got.Candidates, len(wantCandidates))
	}
	for _, candidate := range got.Candidates {
		if want := wantCandidates[candidate.BookID]; candidate.Reason != want {
			t.Errorf("candidate %d reason = %q, want %q", candidate.BookID, candidate.Reason, want)
		}
	}
	if got.Summary.Indeterminate < 3 {
		t.Errorf("indeterminate = %d, want no-ID, DNB, and edition-error rows protected", got.Summary.Indeterminate)
	}
	if got.Summary.ProtectedStatus < 2 {
		t.Errorf("protected status = %d, want downloading and other-owner rows", got.Summary.ProtectedStatus)
	}
	for _, protected := range []*models.Book{noID, dnb, editionUnknown, alias, titleMatch, downloading, nonOwner} {
		if book, err := f.books.GetByID(context.Background(), protected.ID); err != nil || book == nil {
			t.Errorf("protected book %q missing: book=%+v err=%v", protected.Title, book, err)
		}
	}
}

func TestReconciliationRejectReason_ProfileReasonsAndIndeterminateEvidence(t *testing.T) {
	pages100 := 100
	pages250 := 250
	tests := []struct {
		name          string
		work          models.Book
		profile       reconciliationProfile
		evidence      editionEvidence
		wantReason    string
		indeterminate bool
	}{
		{name: "accepted", work: models.Book{Title: "Dune"}},
		{name: "empty title", work: models.Book{}, wantReason: reconcileReasonCatalogueFilter},
		{name: "author self reference", work: models.Book{Title: "Test Author"}, wantReason: reconcileReasonCatalogueFilter},
		{name: "provider compilation", work: models.Book{Title: "Stories", IsCompilation: true}, wantReason: reconcileReasonCatalogueFilter},
		{name: "unambiguous bundle", work: models.Book{Title: "Dune Box Set"}, wantReason: reconcileReasonCatalogueFilter},
		{
			name: "known language rejected", work: models.Book{Title: "Libro", Language: "spa"},
			profile: reconciliationProfile{allowedLangs: []string{"eng"}}, wantReason: reconcileReasonLanguage,
		},
		{
			name: "unknown language kept", work: models.Book{Title: "Unknown"},
			profile: reconciliationProfile{allowedLangs: []string{"eng"}, unknownFail: true}, indeterminate: true,
		},
		{
			name: "part book", work: models.Book{Title: "The New Turing Omnibus"},
			profile: reconciliationProfile{skipPartBooks: true}, wantReason: reconcileReasonPartBook,
		},
		{
			name: "missing date", work: models.Book{Title: "Undated"},
			profile: reconciliationProfile{skipMissingDate: true}, wantReason: reconcileReasonMissingDate,
		},
		{
			name: "below popularity", work: models.Book{Title: "Unpopular", RatingsCount: 1, AverageRating: 2},
			profile: reconciliationProfile{minPopularity: 10}, wantReason: reconcileReasonPopularity,
		},
		{
			name: "edition lookup unavailable", work: models.Book{Title: "Unresolved"},
			profile: reconciliationProfile{minPages: 200}, indeterminate: true,
		},
		{
			name: "missing isbn", work: models.Book{Title: "No ISBN"},
			profile: reconciliationProfile{skipMissingISBN: true}, evidence: editionEvidence{known: true}, wantReason: reconcileReasonISBN,
		},
		{
			name: "below page floor", work: models.Book{Title: "Short"},
			profile:    reconciliationProfile{minPages: 200},
			evidence:   editionEvidence{known: true, editions: []models.Edition{{NumPages: &pages100}}},
			wantReason: reconcileReasonPages,
		},
		{
			name: "page floor accepted", work: models.Book{Title: "Long"},
			profile:  reconciliationProfile{minPages: 200},
			evidence: editionEvidence{known: true, editions: []models.Edition{{NumPages: &pages250}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, indeterminate := reconciliationRejectReason(tt.work, "test author", tt.profile, tt.evidence)
			if reason != tt.wantReason || indeterminate != tt.indeterminate {
				t.Fatalf("reason=%q indeterminate=%v, want %q/%v", reason, indeterminate, tt.wantReason, tt.indeterminate)
			}
		})
	}
}
