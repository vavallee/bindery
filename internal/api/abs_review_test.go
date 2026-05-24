package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type stubABSReviewImporter struct {
	lastCfg  abs.ImportConfig
	lastItem abs.NormalizedLibraryItem
}

func (s *stubABSReviewImporter) ImportReview(_ context.Context, cfg abs.ImportConfig, item abs.NormalizedLibraryItem) (abs.ImportItemResult, error) {
	s.lastCfg = cfg
	s.lastItem = item
	return abs.ImportItemResult{ItemID: item.ItemID, Outcome: "created"}, nil
}

func (s *stubABSReviewImporter) ReviewFileMapping(context.Context, abs.ImportConfig, abs.NormalizedLibraryItem) abs.ReviewFileMapping {
	return abs.ReviewFileMapping{}
}

func absReviewFixture(t *testing.T) (*sql.DB, *ABSReviewHandler, *db.ABSReviewItemRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)
	reviews := db.NewABSReviewItemRepo(database)
	h := NewABSReviewHandler(reviews, db.NewABSImportRunRepo(database), &stubABSReviewImporter{}, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	return database, h, reviews
}

func seedABSReviewItems(t *testing.T, database *sql.DB, reviews *db.ABSReviewItemRepo, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		item := models.ABSReviewItem{
			SourceID:      abs.DefaultSourceID,
			LibraryID:     "lib-books",
			ItemID:        "item-" + strconv.Itoa(i),
			Title:         "Title " + strconv.Itoa(i),
			PrimaryAuthor: "Author " + strconv.Itoa(i),
			MediaType:     models.MediaTypeAudiobook,
			ReviewReason:  "unmatched_book",
			PayloadJSON:   `{"itemId":"item-` + strconv.Itoa(i) + `"}`,
			Status:        "pending",
		}
		if err := reviews.UpsertPending(context.Background(), &item); err != nil {
			t.Fatal(err)
		}
	}
	ts := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	if _, err := database.ExecContext(context.Background(), `UPDATE abs_review_queue SET updated_at = ?`, ts); err != nil {
		t.Fatalf("normalize review timestamps: %v", err)
	}
}

func TestABSReviewHandler_ListDefaultPagination(t *testing.T) {
	database, h, reviews := absReviewFixture(t)
	seedABSReviewItems(t, database, reviews, 2)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/review", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out absReviewListResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if out.Total != 2 || out.Limit != 50 || out.Offset != 0 || len(out.Items) != 2 {
		t.Fatalf("out = %+v, want default pagination metadata and two items", out)
	}
}

func TestABSReviewHandler_ListCustomLimitOffset(t *testing.T) {
	database, h, reviews := absReviewFixture(t)
	seedABSReviewItems(t, database, reviews, 3)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/review?limit=1&offset=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out absReviewListResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if out.Total != 3 || out.Limit != 1 || out.Offset != 1 || len(out.Items) != 1 || out.Items[0].ItemID != "item-2" {
		t.Fatalf("out = %+v, want second item in stable order", out)
	}
}

func TestABSReviewHandler_ListMaxLimitClamping(t *testing.T) {
	database, h, reviews := absReviewFixture(t)
	seedABSReviewItems(t, database, reviews, 105)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/review?limit=1000", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out absReviewListResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if out.Total != 105 || out.Limit != 100 || out.Offset != 0 || len(out.Items) != 100 {
		t.Fatalf("out = total %d limit %d offset %d len %d, want clamped page", out.Total, out.Limit, out.Offset, len(out.Items))
	}
}

func TestABSReviewHandler_ListStableOrdering(t *testing.T) {
	database, h, reviews := absReviewFixture(t)
	seedABSReviewItems(t, database, reviews, 3)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/abs/review?limit=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out absReviewListResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	got := []string{}
	for _, item := range out.Items {
		got = append(got, item.ItemID)
	}
	want := []string{"item-3", "item-2", "item-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestABSReviewHandler_Approve(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	reviews := db.NewABSReviewItemRepo(database)
	payload, _ := json.Marshal(abs.NormalizedLibraryItem{
		ItemID:    "item-123",
		LibraryID: "lib-books",
		Title:     "Harry Potter and the Deathly Hallows",
		Authors:   []abs.NormalizedAuthor{{ID: "author-1", Name: "J.K. Rowling"}},
	})
	if err := reviews.UpsertPending(context.Background(), &models.ABSReviewItem{
		SourceID:      abs.DefaultSourceID,
		LibraryID:     "lib-books",
		ItemID:        "item-123",
		Title:         "Harry Potter and the Deathly Hallows",
		PrimaryAuthor: "J.K. Rowling",
		MediaType:     models.MediaTypeAudiobook,
		ReviewReason:  "unmatched_book",
		PayloadJSON:   string(payload),
		Status:        "pending",
	}); err != nil {
		t.Fatal(err)
	}
	items, err := reviews.ListByStatus(context.Background(), "pending")
	if err != nil || len(items) != 1 {
		t.Fatalf("pending items = %d err=%v, want 1", len(items), err)
	}

	importer := &stubABSReviewImporter{}
	type ctxKey struct{}
	key := ctxKey{}
	h := NewABSReviewHandler(reviews, db.NewABSImportRunRepo(database), importer, func(ctx context.Context) ABSStoredConfig {
		if ctx.Value(key) != "request" {
			t.Fatalf("load config context value = %v, want request", ctx.Value(key))
		}
		return ABSStoredConfig{
			BaseURL:   "https://abs.example.com",
			APIKey:    "secret",
			Label:     "Shelf",
			LibraryID: "lib-books",
			Enabled:   true,
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/review/1/approve", bytes.NewReader(nil))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), key, "request"))
	rec := httptest.NewRecorder()

	h.Approve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if importer.lastCfg.BaseURL != "https://abs.example.com" || importer.lastItem.ItemID != "item-123" {
		t.Fatalf("importer cfg=%+v item=%+v", importer.lastCfg, importer.lastItem)
	}
	updated, err := reviews.GetByID(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.Status != "approved" {
		t.Fatalf("updated review = %+v, want approved", updated)
	}
}

func TestABSReviewHandler_ResolveAuthorGroupsByPrimaryAuthor(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	reviews := db.NewABSReviewItemRepo(database)
	for _, item := range []models.ABSReviewItem{
		{SourceID: abs.DefaultSourceID, LibraryID: "lib-books", ItemID: "item-1", Title: "The Bands of Mourning", PrimaryAuthor: "Brandon Sanderson", MediaType: models.MediaTypeAudiobook, ReviewReason: "unmatched_author", PayloadJSON: `{"itemId":"item-1"}`, Status: "pending"},
		{SourceID: abs.DefaultSourceID, LibraryID: "lib-books", ItemID: "item-2", Title: "Mistborn", PrimaryAuthor: "brandon sanderson", MediaType: models.MediaTypeAudiobook, ReviewReason: "unmatched_author", PayloadJSON: `{"itemId":"item-2"}`, Status: "pending"},
		{SourceID: abs.DefaultSourceID, LibraryID: "lib-books", ItemID: "item-3", Title: "Onyx Storm", PrimaryAuthor: "Rebecca Yarros", MediaType: models.MediaTypeAudiobook, ReviewReason: "unmatched_author", PayloadJSON: `{"itemId":"item-3"}`, Status: "pending"},
	} {
		item := item
		if err := reviews.UpsertPending(context.Background(), &item); err != nil {
			t.Fatal(err)
		}
	}

	h := NewABSReviewHandler(reviews, db.NewABSImportRunRepo(database), &stubABSReviewImporter{}, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	body := bytes.NewBufferString(`{"foreignAuthorId":"OL123A","authorName":"Brandon Sanderson","applyTo":"same_author"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/review/1/resolve-author", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.ResolveAuthor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	items, err := reviews.ListByStatus(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	resolved := 0
	unrelatedUntouched := false
	for _, item := range items {
		if item.PrimaryAuthor == "Rebecca Yarros" {
			unrelatedUntouched = item.ResolvedAuthorForeignID == ""
			continue
		}
		if item.ResolvedAuthorForeignID == "OL123A" && item.ResolvedAuthorName == "Brandon Sanderson" {
			resolved++
		}
		if item.Title == "" {
			t.Fatalf("title unexpectedly changed for %+v", item)
		}
	}
	if resolved != 2 || !unrelatedUntouched {
		t.Fatalf("items = %+v, want two Brandon rows resolved and Rebecca untouched", items)
	}
}

func TestABSReviewHandler_ResolveBookStoresEditedTitle(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	reviews := db.NewABSReviewItemRepo(database)
	if err := reviews.UpsertPending(context.Background(), &models.ABSReviewItem{
		SourceID:      abs.DefaultSourceID,
		LibraryID:     "lib-books",
		ItemID:        "item-1",
		Title:         "The Bands of Mourning (2 of 2)",
		PrimaryAuthor: "Brandon Sanderson",
		MediaType:     models.MediaTypeAudiobook,
		ReviewReason:  "unmatched_book",
		PayloadJSON:   `{"itemId":"item-1"}`,
		Status:        "pending",
	}); err != nil {
		t.Fatal(err)
	}
	h := NewABSReviewHandler(reviews, db.NewABSImportRunRepo(database), &stubABSReviewImporter{}, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	body := bytes.NewBufferString(`{"foreignBookId":"OL456W","title":"The Bands of Mourning","editedTitle":"The Bands of Mourning"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/review/1/resolve-book", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.ResolveBook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	item, err := reviews.GetByID(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if item.ResolvedBookForeignID != "OL456W" || item.ResolvedBookTitle != "The Bands of Mourning" || item.EditedTitle != "The Bands of Mourning" {
		t.Fatalf("item = %+v, want resolved book fields", item)
	}
}

func TestABSReviewHandler_Dismiss(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	reviews := db.NewABSReviewItemRepo(database)
	if err := reviews.UpsertPending(context.Background(), &models.ABSReviewItem{
		SourceID:      abs.DefaultSourceID,
		LibraryID:     "lib-books",
		ItemID:        "item-456",
		Title:         "Unknown Title",
		PrimaryAuthor: "Unknown Author",
		MediaType:     models.MediaTypeEbook,
		ReviewReason:  "unmatched_author",
		PayloadJSON:   `{"itemId":"item-456","libraryId":"lib-books","title":"Unknown Title"}`,
		Status:        "pending",
	}); err != nil {
		t.Fatal(err)
	}

	h := NewABSReviewHandler(reviews, db.NewABSImportRunRepo(database), &stubABSReviewImporter{}, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/review/1/dismiss", bytes.NewReader(nil))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Dismiss(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := reviews.GetByID(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.Status != "dismissed" {
		t.Fatalf("updated review = %+v, want dismissed", updated)
	}
}

func TestABSReviewHandler_DismissRunHappyPath(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	runs := db.NewABSImportRunRepo(database)
	run := &models.ABSImportRun{SourceID: abs.DefaultSourceID, Status: "completed"}
	if err := runs.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	otherRun := &models.ABSImportRun{SourceID: abs.DefaultSourceID, Status: "completed"}
	if err := runs.Create(context.Background(), otherRun); err != nil {
		t.Fatal(err)
	}

	reviews := db.NewABSReviewItemRepo(database)
	for i, runID := range []int64{run.ID, run.ID, otherRun.ID} {
		item := &models.ABSReviewItem{
			SourceID:      abs.DefaultSourceID,
			LibraryID:     "lib-books",
			ItemID:        "item-" + strconv.Itoa(i+1),
			Title:         "Title",
			PrimaryAuthor: "Author",
			MediaType:     models.MediaTypeAudiobook,
			ReviewReason:  "unmatched_book",
			PayloadJSON:   `{}`,
			Status:        "pending",
			LatestRunID:   &runID,
		}
		if err := reviews.UpsertPending(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		// UpsertPending only honours LatestRunID via INSERT, so reapply it
		// for the second-and-later rows that conflict.
		if _, err := database.ExecContext(context.Background(),
			`UPDATE abs_review_queue SET latest_run_id = ? WHERE item_id = ?`, runID, item.ItemID); err != nil {
			t.Fatal(err)
		}
	}

	h := NewABSReviewHandler(reviews, runs, &stubABSReviewImporter{}, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/review/dismiss-run/"+strconv.FormatInt(run.ID, 10), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("runID", strconv.FormatInt(run.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.DismissRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]int64
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["dismissed"] != 2 {
		t.Fatalf("dismissed = %d, want 2", body["dismissed"])
	}
	pending, err := reviews.ListByStatus(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ItemID != "item-3" {
		t.Fatalf("pending = %+v, want only item-3 (other run) left pending", pending)
	}
}

func TestABSReviewHandler_DismissRunUnknownRun404(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	reviews := db.NewABSReviewItemRepo(database)
	runs := db.NewABSImportRunRepo(database)
	h := NewABSReviewHandler(reviews, runs, &stubABSReviewImporter{}, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/review/dismiss-run/9999", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("runID", "9999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.DismissRun(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestABSReviewHandler_DismissRunInvalidID(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	reviews := db.NewABSReviewItemRepo(database)
	runs := db.NewABSImportRunRepo(database)
	h := NewABSReviewHandler(reviews, runs, &stubABSReviewImporter{}, func(context.Context) ABSStoredConfig { return ABSStoredConfig{} })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/review/dismiss-run/abc", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("runID", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.DismissRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
