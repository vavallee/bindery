package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestRecommendationRepoStoresNilGenresAsEmptyArray(t *testing.T) {
	ctx := context.Background()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	repo := NewRecommendationRepo(database)
	if err := repo.ReplaceBatch(ctx, 1, []models.RecommendationCandidate{{
		ForeignID: "ol:nil-genres",
		RecType:   models.RecTypeSeries,
		Title:     "Nil Genres",
	}}); err != nil {
		t.Fatalf("replace batch: %v", err)
	}

	var storedGenres string
	if err := database.QueryRowContext(ctx, "SELECT genres FROM recommendations WHERE foreign_id = ?", "ol:nil-genres").Scan(&storedGenres); err != nil {
		t.Fatalf("query stored genres: %v", err)
	}
	if storedGenres != "[]" {
		t.Fatalf("stored genres = %q, want []", storedGenres)
	}

	recs, err := repo.List(ctx, 1, "", 10, 0)
	if err != nil {
		t.Fatalf("list recommendations: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len(recs) = %d, want 1", len(recs))
	}
	if recs[0].Genres == nil {
		t.Fatal("listed genres is nil, want empty slice")
	}
	if len(recs[0].Genres) != 0 {
		t.Fatalf("len(genres) = %d, want 0", len(recs[0].Genres))
	}
}

func TestRecommendationRepoReadsLegacyNullGenresAsEmptyArray(t *testing.T) {
	ctx := context.Background()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()

	_, err = database.ExecContext(ctx, `
		INSERT INTO recommendations (user_id, foreign_id, rec_type, title, genres)
		VALUES (1, 'ol:legacy-null-genres', 'series', 'Legacy Null Genres', 'null')`)
	if err != nil {
		t.Fatalf("insert legacy recommendation: %v", err)
	}

	repo := NewRecommendationRepo(database)
	recs, err := repo.List(ctx, 1, "", 10, 0)
	if err != nil {
		t.Fatalf("list recommendations: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len(recs) = %d, want 1", len(recs))
	}
	if recs[0].Genres == nil {
		t.Fatal("listed legacy genres is nil, want empty slice")
	}
	if len(recs[0].Genres) != 0 {
		t.Fatalf("len(legacy genres) = %d, want 0", len(recs[0].Genres))
	}

	rec, err := repo.GetByID(ctx, recs[0].ID)
	if err != nil {
		t.Fatalf("get recommendation: %v", err)
	}
	if rec == nil {
		t.Fatal("get recommendation returned nil")
	}
	if rec.Genres == nil {
		t.Fatal("loaded legacy genres is nil, want empty slice")
	}
	if len(rec.Genres) != 0 {
		t.Fatalf("len(loaded legacy genres) = %d, want 0", len(rec.Genres))
	}
}
