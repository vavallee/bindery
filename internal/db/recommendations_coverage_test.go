package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestRecommendationRepo_Coverage exercises batch replacement, the list
// filters, dismissal persistence and author exclusions, all scoped per user.
func TestRecommendationRepo_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewRecommendationRepo(database)
	const alice, bob = int64(1), int64(2)

	cands := []models.RecommendationCandidate{
		{ForeignID: "f-low", RecType: "author", Title: "Low", Score: 1, Genres: []string{"sf"}},
		{ForeignID: "f-high", RecType: "series", Title: "High", Score: 9},
		{ForeignID: "f-mid", RecType: "author", Title: "Mid", Score: 5, Genres: []string{"a", "b"}},
	}
	if err := repo.ReplaceBatch(ctx, alice, cands); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceBatch(ctx, bob, cands[:1]); err != nil {
		t.Fatal(err)
	}

	titles := func(recs []models.Recommendation) []string {
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.Title)
		}
		return out
	}
	eq := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range want {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	list, err := repo.List(ctx, alice, "", 0, 0)
	if err != nil || !eq(titles(list), "High", "Mid", "Low") {
		t.Fatalf("List(alice) = %v, %v; want score DESC", titles(list), err)
	}
	if list[1].Genres[1] != "b" || len(list[0].Genres) != 0 || list[0].UserID != alice {
		t.Fatalf("genres/user decode wrong: %+v", list)
	}
	byType, err := repo.List(ctx, alice, "author", 0, 0)
	if err != nil || !eq(titles(byType), "Mid", "Low") {
		t.Fatalf("List(alice, author) = %v, %v", titles(byType), err)
	}
	paged, err := repo.List(ctx, alice, "", 1, 1)
	if err != nil || !eq(titles(paged), "Mid") {
		t.Fatalf("List(alice, limit 1 offset 1) = %v, %v", titles(paged), err)
	}
	bobList, err := repo.List(ctx, bob, "", 0, 0)
	if err != nil || !eq(titles(bobList), "Low") {
		t.Fatalf("List(bob) = %v, %v", titles(bobList), err)
	}

	// ReplaceBatch swaps the whole set for that user only.
	if err := repo.ReplaceBatch(ctx, bob, []models.RecommendationCandidate{{ForeignID: "f-new", RecType: "author", Title: "New", Score: 2}}); err != nil {
		t.Fatal(err)
	}
	if bobList, _ := repo.List(ctx, bob, "", 0, 0); !eq(titles(bobList), "New") {
		t.Fatalf("bob after replace = %v, want [New]", titles(bobList))
	}
	if aliceList, _ := repo.List(ctx, alice, "", 0, 0); len(aliceList) != 3 {
		t.Fatalf("alice disturbed by bob's replace: %v", titles(aliceList))
	}

	// Dismiss hides the rec and records the foreign id for this user only.
	high := list[0]
	if err := repo.Dismiss(ctx, alice, high.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Dismiss(ctx, alice, 999999); err == nil {
		t.Error("Dismiss of a missing rec should fail")
	}
	got, err := repo.GetByID(ctx, high.ID)
	if err != nil || got == nil || !got.Dismissed {
		t.Fatalf("GetByID after Dismiss = %+v, %v", got, err)
	}
	if after, _ := repo.List(ctx, alice, "", 0, 0); !eq(titles(after), "Mid", "Low") {
		t.Fatalf("List after Dismiss = %v", titles(after))
	}
	if ok, err := repo.IsDismissed(ctx, alice, "f-high"); err != nil || !ok {
		t.Fatalf("IsDismissed(alice, f-high) = %v, %v", ok, err)
	}
	if ok, err := repo.IsDismissed(ctx, bob, "f-high"); err != nil || ok {
		t.Fatalf("IsDismissed(bob, f-high) = %v, %v; dismissals are per user", ok, err)
	}
	// Dismissing twice is idempotent (INSERT OR IGNORE).
	if err := repo.Dismiss(ctx, alice, high.ID); err != nil {
		t.Fatal(err)
	}
	ids, err := repo.ListDismissedIDs(ctx, alice)
	if err != nil || len(ids) != 1 || !ids["f-high"] {
		t.Fatalf("ListDismissedIDs = %v, %v", ids, err)
	}
	// Dismissals survive regeneration of the batch.
	if err := repo.ReplaceBatch(ctx, alice, cands); err != nil {
		t.Fatal(err)
	}
	if ok, _ := repo.IsDismissed(ctx, alice, "f-high"); !ok {
		t.Fatal("dismissal lost on ReplaceBatch")
	}
	if err := repo.ClearDismissals(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if ids, _ := repo.ListDismissedIDs(ctx, alice); len(ids) != 0 {
		t.Fatalf("ListDismissedIDs after clear = %v", ids)
	}

	// Author exclusions: sorted, idempotent, per user.
	for _, name := range []string{"Zed", "Amy", "Zed"} {
		if err := repo.AddAuthorExclusion(ctx, alice, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.AddAuthorExclusion(ctx, bob, "Bob Only"); err != nil {
		t.Fatal(err)
	}
	ex, err := repo.ListAuthorExclusions(ctx, alice)
	if err != nil || !eq(ex, "Amy", "Zed") {
		t.Fatalf("ListAuthorExclusions(alice) = %v, %v", ex, err)
	}
	if err := repo.RemoveAuthorExclusion(ctx, alice, "Zed"); err != nil {
		t.Fatal(err)
	}
	if ex, _ := repo.ListAuthorExclusions(ctx, alice); !eq(ex, "Amy") {
		t.Fatalf("after remove = %v", ex)
	}
	if ex, _ := repo.ListAuthorExclusions(ctx, bob); !eq(ex, "Bob Only") {
		t.Fatalf("bob exclusions = %v", ex)
	}
}
