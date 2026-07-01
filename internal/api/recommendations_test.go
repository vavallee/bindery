package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
