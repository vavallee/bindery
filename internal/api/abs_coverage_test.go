package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// covSyChiReq builds a request carrying chi URL params, for the ABS handlers
// that read {id} or {runID}.
func covSyChiReq(method, target, body string, params map[string]string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func covSyErrorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	s, _ := out["error"].(string)
	return s
}

// --- ABS conflicts -----------------------------------------------------------

func covSySeedConflict(t *testing.T, conflicts *db.ABSMetadataConflictRepo, c *models.ABSMetadataConflict) *models.ABSMetadataConflict {
	t.Helper()
	if c.SourceID == "" {
		c.SourceID = "default"
	}
	if c.LibraryID == "" {
		c.LibraryID = "lib-books"
	}
	if c.AppliedSource == "" {
		c.AppliedSource = abs.MetadataSourceUpstream
	}
	if c.ResolutionStatus == "" {
		c.ResolutionStatus = "unresolved"
	}
	if err := conflicts.Upsert(context.Background(), c); err != nil {
		t.Fatalf("Upsert conflict: %v", err)
	}
	return c
}

func TestABSConflictCoverage_ResolveRejectsBadInput(t *testing.T) {
	_, h, conflicts, _, _ := absConflictFixture(t)
	c := covSySeedConflict(t, conflicts, &models.ABSMetadataConflict{
		ItemID: "li-1", EntityType: "book", LocalID: 999, FieldName: "description",
		ABSValue: "abs", UpstreamValue: "up",
	})
	cases := []struct {
		name, id, body string
		want           int
		wantErr        string
	}{
		{"non-numeric id", "abc", `{"source":"abs"}`, http.StatusBadRequest, "invalid id"},
		{"unknown id", "4242", `{"source":"abs"}`, http.StatusNotFound, "conflict not found"},
		{"malformed body", covSyID(c.ID), `{`, http.StatusBadRequest, "invalid request body"},
		{"unknown source", covSyID(c.ID), `{"source":"calibre"}`, http.StatusBadRequest, "source must be 'abs' or 'upstream'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.Resolve(rec, covSyChiReq(http.MethodPost, "/api/v1/abs/conflicts/x/resolve", tc.body, map[string]string{"id": tc.id}))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if got := covSyErrorBody(t, rec); got != tc.wantErr {
				t.Fatalf("error = %q, want %q", got, tc.wantErr)
			}
		})
	}
	// None of the rejected requests may have claimed the conflict.
	stored, err := conflicts.GetByID(context.Background(), c.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.ResolutionStatus != "unresolved" {
		t.Fatalf("status = %q after rejected requests, want unresolved", stored.ResolutionStatus)
	}
}

func covSyID(n int64) string { return strconv.FormatInt(n, 10) }

func TestABSConflictCoverage_ResolveWithoutStoreIs404(t *testing.T) {
	h := NewABSConflictHandler(nil, nil, nil)
	rec := httptest.NewRecorder()
	h.Resolve(rec, covSyChiReq(http.MethodPost, "/api/v1/abs/conflicts/1/resolve", `{"source":"abs"}`, map[string]string{"id": "1"}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/conflicts?limit=7&offset=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List status = %d, want 200", rec.Code)
	}
	var out absConflictListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Items == nil || len(out.Items) != 0 || out.Total != 0 || out.Limit != 7 || out.Offset != 3 {
		t.Fatalf("List without store = %+v, want empty page echoing limit/offset", out)
	}
}

// A conflict another caller already resolved must not be applied again: the
// handler returns the stored state and leaves the entity alone.
func TestABSConflictCoverage_ResolveAlreadyResolvedReturnsCurrentState(t *testing.T) {
	_, h, conflicts, authors, books := absConflictFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-COV-A", Name: "Cov Author", SortName: "Author, Cov", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-COV-B", AuthorID: author.ID, Title: "Cov Book", SortTitle: "Cov Book", Description: "kept", Status: models.BookStatusWanted, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	c := covSySeedConflict(t, conflicts, &models.ABSMetadataConflict{
		ItemID: "li-res", EntityType: "book", LocalID: book.ID, FieldName: "description",
		ABSValue: "abs text", UpstreamValue: "kept",
		AppliedSource: abs.MetadataSourceUpstream, PreferredSource: abs.MetadataSourceUpstream,
		ResolutionStatus: "resolved",
	})

	rec := httptest.NewRecorder()
	h.Resolve(rec, covSyChiReq(http.MethodPost, "/x", `{"source":"abs"}`, map[string]string{"id": covSyID(c.ID)}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got absConflictResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ResolutionStatus != "resolved" || got.AppliedSource != abs.MetadataSourceUpstream || got.AppliedValue != "kept" || got.EntityName != "Cov Book" {
		t.Fatalf("response = %+v, want the existing upstream resolution", got)
	}
	reloaded, _ := books.GetByID(ctx, book.ID)
	if reloaded.Description != "kept" {
		t.Fatalf("description = %q, the losing resolve must not apply its value", reloaded.Description)
	}
}

// An apply failure (an unsupported field) must release the claim so the
// conflict can be resolved again, rather than stranding it in "resolving".
func TestABSConflictCoverage_ResolveApplyFailureUnclaims(t *testing.T) {
	_, h, conflicts, _, books := absConflictFixture(t)
	ctx := context.Background()
	authorRepo := h.authors
	author := &models.Author{ForeignID: "OL-COV-A2", Name: "Fail Author", SortName: "Author, Fail", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-COV-B2", AuthorID: author.ID, Title: "Fail Book", SortTitle: "Fail Book", Status: models.BookStatusWanted, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	c := covSySeedConflict(t, conflicts, &models.ABSMetadataConflict{
		ItemID: "li-bad", EntityType: "book", LocalID: book.ID, FieldName: "no_such_field",
		ABSValue: "a", UpstreamValue: "b",
	})

	rec := httptest.NewRecorder()
	h.Resolve(rec, covSyChiReq(http.MethodPost, "/x", `{"source":"upstream"}`, map[string]string{"id": covSyID(c.ID)}))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	stored, err := conflicts.GetByID(ctx, c.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.ResolutionStatus != "unresolved" {
		t.Fatalf("status = %q, want unresolved after a failed apply", stored.ResolutionStatus)
	}
}

func TestABSConflictCoverage_ResolveAuthorConflictAppliesValue(t *testing.T) {
	_, h, conflicts, authors, _ := absConflictFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-COV-A3", Name: "Sorted Author", SortName: "old sort", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	c := covSySeedConflict(t, conflicts, &models.ABSMetadataConflict{
		ItemID: "li-auth", EntityType: "author", LocalID: author.ID, FieldName: "sort_name",
		ABSValue: "  Author, Sorted  ", UpstreamValue: "old sort",
	})

	rec := httptest.NewRecorder()
	h.Resolve(rec, covSyChiReq(http.MethodPost, "/x", `{"source":"abs"}`, map[string]string{"id": covSyID(c.ID)}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got absConflictResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.EntityName != "Sorted Author" || got.AppliedSource != abs.MetadataSourceABS || got.ResolutionStatus != "resolved" {
		t.Fatalf("response = %+v", got)
	}
	reloaded, _ := authors.GetByID(ctx, author.ID)
	if reloaded.SortName != "Author, Sorted" {
		t.Fatalf("sort name = %q, want trimmed ABS value", reloaded.SortName)
	}
	if reloaded.LastMetadataRefreshAt == nil {
		t.Fatal("LastMetadataRefreshAt not stamped")
	}
}

// A conflict whose local entity is gone resolves
// without writing anything, and the response falls back to the ABS item id.
func TestABSConflictCoverage_ResolveMissingEntityFallsBackToItemID(t *testing.T) {
	_, h, conflicts, _, _ := absConflictFixture(t)
	for _, entity := range []string{"author", "book"} {
		c := covSySeedConflict(t, conflicts, &models.ABSMetadataConflict{
			ItemID: "li-gone-" + entity, EntityType: entity, LocalID: 9999, FieldName: "description",
			ABSValue: "a", UpstreamValue: "b",
		})
		rec := httptest.NewRecorder()
		h.Resolve(rec, covSyChiReq(http.MethodPost, "/x", `{"source":"abs"}`, map[string]string{"id": covSyID(c.ID)}))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s", entity, rec.Code, rec.Body.String())
		}
		var got absConflictResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.EntityName != "li-gone-"+entity || got.ResolutionStatus != "resolved" || got.AppliedValue != "a" {
			t.Fatalf("%s: response = %+v", entity, got)
		}
	}
}

// --- ABS import --------------------------------------------------------------

type covSyABSImporter struct {
	runsErr     error
	runs        []models.ABSImportRun
	rollbackErr error
	gotRunIDs   []int64
}

func (s *covSyABSImporter) Start(context.Context, abs.ImportConfig) error { return nil }
func (s *covSyABSImporter) Progress() abs.ImportProgress                  { return abs.ImportProgress{} }
func (s *covSyABSImporter) RecentRuns(context.Context, int) ([]models.ABSImportRun, error) {
	return s.runs, s.runsErr
}
func (s *covSyABSImporter) RollbackPreview(_ context.Context, id int64) (*abs.RollbackResult, error) {
	s.gotRunIDs = append(s.gotRunIDs, id)
	if s.rollbackErr != nil {
		return nil, s.rollbackErr
	}
	return &abs.RollbackResult{RunID: id, Preview: true, Status: "preview"}, nil
}
func (s *covSyABSImporter) Rollback(_ context.Context, id int64) (*abs.RollbackResult, error) {
	s.gotRunIDs = append(s.gotRunIDs, id)
	if s.rollbackErr != nil {
		return nil, s.rollbackErr
	}
	return &abs.RollbackResult{RunID: id, Status: "rolled_back"}, nil
}

func TestABSImportCoverage_StartRejectsMalformedBody(t *testing.T) {
	stub := &stubABSImporter{}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	rec := httptest.NewRecorder()
	h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", strings.NewReader(`{"dryRun":`)))
	if rec.Code != http.StatusBadRequest || covSyErrorBody(t, rec) != "invalid request body" {
		t.Fatalf("status = %d body=%s, want 400 invalid request body", rec.Code, rec.Body.String())
	}
	if stub.lastCfg.SourceID != "" {
		t.Fatal("importer started despite a malformed body")
	}
}

func TestABSImportCoverage_RunsHydratesAndReportsErrors(t *testing.T) {
	started := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	stub := &covSyABSImporter{runs: []models.ABSImportRun{{ID: 3, SourceID: "default", Status: "completed", StartedAt: started}}}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })

	rec := httptest.NewRecorder()
	h.Runs(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/import/runs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var runs []abs.PersistedImportRun
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != 3 || runs[0].Status != "completed" {
		t.Fatalf("runs = %+v", runs)
	}

	// No runs serialises as [] rather than null.
	stub.runs = nil
	rec = httptest.NewRecorder()
	h.Runs(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/import/runs", nil))
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty runs body = %q, want []", rec.Body.String())
	}

	stub.runsErr = errors.New("db down")
	rec = httptest.NewRecorder()
	h.Runs(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/import/runs", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestABSImportCoverage_RollbackValidatesRunID(t *testing.T) {
	stub := &covSyABSImporter{}
	h := NewABSImportHandler(stub, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	handlers := map[string]http.HandlerFunc{"preview": h.RollbackPreview, "rollback": h.Rollback}

	for name, fn := range handlers {
		for _, tc := range []struct{ raw, wantErr string }{
			{"", "run id is required"},
			{"  ", "run id is required"},
			{"abc", "invalid run id"},
			{"0", "invalid run id"},
			{"-4", "invalid run id"},
		} {
			rec := httptest.NewRecorder()
			fn(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"runID": tc.raw}))
			if rec.Code != http.StatusBadRequest || covSyErrorBody(t, rec) != tc.wantErr {
				t.Fatalf("%s(%q): status=%d body=%s, want 400 %q", name, tc.raw, rec.Code, rec.Body.String(), tc.wantErr)
			}
		}
	}
	if len(stub.gotRunIDs) != 0 {
		t.Fatalf("importer called with %v for invalid ids", stub.gotRunIDs)
	}

	rec := httptest.NewRecorder()
	h.RollbackPreview(rec, covSyChiReq(http.MethodGet, "/x", "", map[string]string{"runID": "12"}))
	var res abs.RollbackResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.RunID != 12 || !res.Preview {
		t.Fatalf("preview status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.Rollback(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"runID": "12"}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "rolled_back") {
		t.Fatalf("rollback status=%d body=%s", rec.Code, rec.Body.String())
	}

	stub.rollbackErr = errors.New("run 12 is still running")
	for name, fn := range handlers {
		rec := httptest.NewRecorder()
		fn(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"runID": "12"}))
		if rec.Code != http.StatusBadRequest || covSyErrorBody(t, rec) != "run 12 is still running" {
			t.Fatalf("%s: status=%d body=%s, want 400 with importer error", name, rec.Code, rec.Body.String())
		}
	}
}

// --- ABS review --------------------------------------------------------------

type covSyABSReviewImporter struct {
	err   error
	calls int
}

func (s *covSyABSReviewImporter) ImportReview(context.Context, abs.ImportConfig, abs.NormalizedLibraryItem) (abs.ImportItemResult, error) {
	s.calls++
	return abs.ImportItemResult{}, s.err
}

func (s *covSyABSReviewImporter) ReviewFileMapping(context.Context, abs.ImportConfig, abs.NormalizedLibraryItem) abs.ReviewFileMapping {
	return abs.ReviewFileMapping{}
}

func covSyValidABSConfig(context.Context) ABSStoredConfig {
	return ABSStoredConfig{BaseURL: "https://abs.example.com", APIKey: "secret", LibraryID: "lib-books", Enabled: true}
}

func covSySeedReview(t *testing.T, reviews *db.ABSReviewItemRepo, itemID, payload string) *models.ABSReviewItem {
	t.Helper()
	item := &models.ABSReviewItem{
		SourceID: abs.DefaultSourceID, LibraryID: "lib-books", ItemID: itemID,
		Title: "Title " + itemID, PrimaryAuthor: "Author " + itemID,
		MediaType: models.MediaTypeAudiobook, ReviewReason: "unmatched_book",
		PayloadJSON: payload, Status: "pending",
	}
	if err := reviews.UpsertPending(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	items, err := reviews.ListByStatus(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		if items[i].ItemID == itemID {
			return &items[i]
		}
	}
	t.Fatalf("seeded review %s not found", itemID)
	return nil
}

func covSyReviewStatus(t *testing.T, reviews *db.ABSReviewItemRepo, id int64) string {
	t.Helper()
	got, err := reviews.GetByID(context.Background(), id)
	if err != nil || got == nil {
		t.Fatalf("GetByID(%d): %v", id, err)
	}
	return got.Status
}

func TestABSReviewCoverage_ItemHandlersRejectBadIDs(t *testing.T) {
	_, h, _ := absReviewFixture(t)
	handlers := map[string]http.HandlerFunc{
		"approve":        h.Approve,
		"resolve-author": h.ResolveAuthor,
		"resolve-book":   h.ResolveBook,
		"dismiss":        h.Dismiss,
	}
	for name, fn := range handlers {
		for _, tc := range []struct {
			raw     string
			want    int
			wantErr string
		}{
			{"", http.StatusBadRequest, "review item id is required"},
			{"x", http.StatusBadRequest, "invalid review item id"},
			{"0", http.StatusBadRequest, "invalid review item id"},
			{"777", http.StatusNotFound, "review item not found"},
		} {
			rec := httptest.NewRecorder()
			fn(rec, covSyChiReq(http.MethodPost, "/x", `{}`, map[string]string{"id": tc.raw}))
			if rec.Code != tc.want || covSyErrorBody(t, rec) != tc.wantErr {
				t.Fatalf("%s(%q): status=%d body=%s, want %d %q", name, tc.raw, rec.Code, rec.Body.String(), tc.want, tc.wantErr)
			}
		}
	}
}

func TestABSReviewCoverage_ApproveGuards(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)
	reviews := db.NewABSReviewItemRepo(database)
	runs := db.NewABSImportRunRepo(database)

	valid := `{"itemId":"ok","libraryId":"lib-books","title":"T"}`

	t.Run("not pending is 409 and never imports", func(t *testing.T) {
		imp := &covSyABSReviewImporter{}
		h := NewABSReviewHandler(reviews, runs, imp, covSyValidABSConfig)
		item := covSySeedReview(t, reviews, "done", valid)
		if err := reviews.UpdateStatus(context.Background(), item.ID, "approved"); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.Approve(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"id": covSyID(item.ID)}))
		if rec.Code != http.StatusConflict || !strings.Contains(covSyErrorBody(t, rec), "status: approved") {
			t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
		}
		if imp.calls != 0 {
			t.Fatal("ImportReview ran for a non-pending item")
		}
	})

	t.Run("corrupt payload is 400", func(t *testing.T) {
		imp := &covSyABSReviewImporter{}
		h := NewABSReviewHandler(reviews, runs, imp, covSyValidABSConfig)
		item := covSySeedReview(t, reviews, "corrupt", `{not json`)
		rec := httptest.NewRecorder()
		h.Approve(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"id": covSyID(item.ID)}))
		if rec.Code != http.StatusBadRequest || covSyErrorBody(t, rec) != "stored review payload is invalid" {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := covSyReviewStatus(t, reviews, item.ID); got != "pending" {
			t.Fatalf("status = %q, want pending", got)
		}
	})

	t.Run("disabled source is 400 and stays pending", func(t *testing.T) {
		imp := &covSyABSReviewImporter{}
		h := NewABSReviewHandler(reviews, runs, imp, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
		item := covSySeedReview(t, reviews, "disabled", valid)
		rec := httptest.NewRecorder()
		h.Approve(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"id": covSyID(item.ID)}))
		if rec.Code != http.StatusBadRequest || covSyErrorBody(t, rec) != "abs source is disabled" {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if imp.calls != 0 || covSyReviewStatus(t, reviews, item.ID) != "pending" {
			t.Fatalf("calls=%d, item should remain pending and unimported", imp.calls)
		}
	})

	t.Run("import failure reverts to pending", func(t *testing.T) {
		imp := &covSyABSReviewImporter{err: errors.New("no matching author upstream")}
		h := NewABSReviewHandler(reviews, runs, imp, covSyValidABSConfig)
		item := covSySeedReview(t, reviews, "fails", valid)
		rec := httptest.NewRecorder()
		h.Approve(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"id": covSyID(item.ID)}))
		if rec.Code != http.StatusBadRequest || covSyErrorBody(t, rec) != "no matching author upstream" {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if imp.calls != 1 {
			t.Fatalf("ImportReview calls = %d, want 1", imp.calls)
		}
		if got := covSyReviewStatus(t, reviews, item.ID); got != "pending" {
			t.Fatalf("status = %q, want reverted to pending", got)
		}
	})
}

func TestABSReviewCoverage_ResolveValidation(t *testing.T) {
	_, h, reviews := absReviewFixture(t)
	item := covSySeedReview(t, reviews, "resolve", `{"itemId":"resolve"}`)
	id := covSyID(item.ID)

	cases := []struct {
		name    string
		fn      http.HandlerFunc
		body    string
		wantErr string
	}{
		{"author malformed body", h.ResolveAuthor, `{`, "invalid request body"},
		{"author bad applyTo", h.ResolveAuthor, `{"foreignAuthorId":"OL1A","authorName":"A","applyTo":"everyone"}`, "applyTo must be same_author"},
		{"author missing fields", h.ResolveAuthor, `{"applyTo":"same_author"}`, "foreignAuthorId and authorName required"},
		{"book malformed body", h.ResolveBook, `[`, "invalid request body"},
		{"book missing fields", h.ResolveBook, `{"title":"only a title"}`, "foreignBookId and title required"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		tc.fn(rec, covSyChiReq(http.MethodPost, "/x", tc.body, map[string]string{"id": id}))
		if rec.Code != http.StatusBadRequest || covSyErrorBody(t, rec) != tc.wantErr {
			t.Fatalf("%s: status=%d body=%s, want 400 %q", tc.name, rec.Code, rec.Body.String(), tc.wantErr)
		}
	}
	stored, _ := reviews.GetByID(context.Background(), item.ID)
	if stored.ResolvedAuthorForeignID != "" || stored.ResolvedBookForeignID != "" {
		t.Fatalf("rejected resolves wrote data: %+v", stored)
	}
}

func TestABSReviewCoverage_DismissRunValidation(t *testing.T) {
	_, h, reviews := absReviewFixture(t)
	for _, tc := range []struct{ raw, wantErr string }{
		{"", "run id is required"},
		{"nope", "invalid run id"},
		{"-1", "invalid run id"},
	} {
		rec := httptest.NewRecorder()
		h.DismissRun(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"runID": tc.raw}))
		if rec.Code != http.StatusBadRequest || covSyErrorBody(t, rec) != tc.wantErr {
			t.Fatalf("DismissRun(%q): status=%d body=%s", tc.raw, rec.Code, rec.Body.String())
		}
	}

	// Without a run repo the existence check is skipped and the dismissal is
	// scoped to the run id alone: an unknown run dismisses nothing.
	item := covSySeedReview(t, reviews, "keep", `{}`)
	noRuns := NewABSReviewHandler(reviews, nil, &covSyABSReviewImporter{}, covSyValidABSConfig)
	rec := httptest.NewRecorder()
	noRuns.DismissRun(rec, covSyChiReq(http.MethodPost, "/x", "", map[string]string{"runID": "55"}))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"dismissed":0}` {
		t.Fatalf("status=%d body=%s, want 200 dismissed 0", rec.Code, rec.Body.String())
	}
	if got := covSyReviewStatus(t, reviews, item.ID); got != "pending" {
		t.Fatalf("unrelated item status = %q, want pending", got)
	}
}
