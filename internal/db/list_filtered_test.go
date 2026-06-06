package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestAuthorRepo_ListPageFiltered_Pagination is the direct regression test for
// issue #1010 bug 1: a library with more authors than one page must be fully
// reachable via limit/offset, and total must reflect the whole set (not the
// page length).
func TestAuthorRepo_ListPageFiltered_Pagination(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewAuthorRepo(database)
	ctx := context.Background()

	names := []string{"Acevedo", "Brown", "Clarke", "Dahl", "Eco"}
	for i, n := range names {
		if err := repo.Create(ctx, &models.Author{
			ForeignID: "OL-P" + n, Name: n, SortName: n, Monitored: i%2 == 0,
		}); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}

	// Page 2 of size 2 must return the 3rd/4th authors with total=5.
	got, total, err := repo.ListPageFiltered(ctx, AuthorListFilter{Sort: "az"}, 2, 2)
	if err != nil {
		t.Fatalf("ListPageFiltered: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5 (the whole set, not the page length)", total)
	}
	if len(got) != 2 {
		t.Fatalf("page len = %d, want 2", len(got))
	}
	if got[0].Name != "Clarke" || got[1].Name != "Dahl" {
		t.Errorf("page 2 = [%s, %s], want [Clarke, Dahl]", got[0].Name, got[1].Name)
	}

	// The last page (offset 4) must reach the author past the first page —
	// the row that was unreachable before the fix.
	last, _, err := repo.ListPageFiltered(ctx, AuthorListFilter{Sort: "az"}, 2, 4)
	if err != nil {
		t.Fatalf("ListPageFiltered last: %v", err)
	}
	if len(last) != 1 || last[0].Name != "Eco" {
		t.Errorf("last page = %v, want [Eco]", last)
	}
}

func TestAuthorRepo_ListPageFiltered_SearchSortMonitored(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewAuthorRepo(database)
	ctx := context.Background()

	seed := []struct {
		name      string
		monitored bool
	}{
		{"Brad Thor", true},
		{"Douglas Adams", false},
		{"Thornton Wilder", true},
		{"Elizabeth Acevedo", false},
	}
	for _, s := range seed {
		if err := repo.Create(ctx, &models.Author{
			ForeignID: "OL-S" + s.name, Name: s.name, SortName: s.name, Monitored: s.monitored,
		}); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}

	// Case-insensitive substring search on name (#1010 bug 2).
	got, total, err := repo.ListPageFiltered(ctx, AuthorListFilter{Search: "thor", Sort: "az"}, 50, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("search 'thor' total=%d len=%d, want 2/2 (Brad Thor, Thornton Wilder)", total, len(got))
	}
	if got[0].Name != "Brad Thor" || got[1].Name != "Thornton Wilder" {
		t.Errorf("search 'thor' = [%s, %s], want [Brad Thor, Thornton Wilder]", got[0].Name, got[1].Name)
	}

	// Monitored filter.
	mon := true
	monly, total, err := repo.ListPageFiltered(ctx, AuthorListFilter{Monitored: &mon}, 50, 0)
	if err != nil {
		t.Fatalf("monitored filter: %v", err)
	}
	if total != 2 || len(monly) != 2 {
		t.Errorf("monitored=true total=%d len=%d, want 2", total, len(monly))
	}

	// Descending sort.
	za, _, err := repo.ListPageFiltered(ctx, AuthorListFilter{Sort: "za"}, 1, 0)
	if err != nil {
		t.Fatalf("za: %v", err)
	}
	if len(za) != 1 || za[0].Name != "Thornton Wilder" {
		t.Errorf("za first = %v, want [Thornton Wilder]", za)
	}
}

func TestBookRepo_ListPageFiltered(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-BA", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("author: %v", err)
	}

	books := []struct {
		title     string
		status    string
		monitored bool
		media     string
	}{
		{"Mistborn", models.BookStatusImported, true, models.MediaTypeEbook},
		{"Elantris", models.BookStatusWanted, true, models.MediaTypeAudiobook},
		{"Warbreaker", models.BookStatusWanted, false, models.MediaTypeEbook}, // wanted but unmonitored
		{"The Way of Kings", models.BookStatusImported, true, models.MediaTypeBoth},
	}
	for i, b := range books {
		if err := bookRepo.Create(ctx, &models.Book{
			ForeignID: "OL-BW" + b.title, AuthorID: author.ID, Title: b.title, SortTitle: b.title,
			Status: b.status, Monitored: b.monitored, MediaType: b.media,
		}); err != nil {
			t.Fatalf("seed book %d: %v", i, err)
		}
	}

	// Pagination + total (#1010 bug 1).
	page, total, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Sort: "title-az"}, 2, 0)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if total != 4 || len(page) != 2 {
		t.Errorf("page total=%d len=%d, want 4/2", total, len(page))
	}

	// Search matches title.
	_, total, err = bookRepo.ListPageFiltered(ctx, BookListFilter{Search: "mistborn"}, 50, 0)
	if err != nil {
		t.Fatalf("search title: %v", err)
	}
	if total != 1 {
		t.Errorf("search 'mistborn' total=%d, want 1", total)
	}

	// Search matches author name (the cross-table case the old client-side
	// filter could not reach past page 1).
	_, total, err = bookRepo.ListPageFiltered(ctx, BookListFilter{Search: "sanderson"}, 50, 0)
	if err != nil {
		t.Fatalf("search author: %v", err)
	}
	if total != 4 {
		t.Errorf("search 'sanderson' total=%d, want 4 (all books by the author)", total)
	}

	// Status=wanted must exclude the unmonitored "Warbreaker" (only wanted
	// requires monitored=1, mirroring the old Books-page behaviour).
	wanted, total, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Status: models.BookStatusWanted}, 50, 0)
	if err != nil {
		t.Fatalf("status wanted: %v", err)
	}
	if total != 1 || len(wanted) != 1 || wanted[0].Title != "Elantris" {
		t.Errorf("status=wanted = %d rows, want 1 (Elantris only)", total)
	}

	// Status=imported does NOT require monitored, so both imported books show.
	_, total, err = bookRepo.ListPageFiltered(ctx, BookListFilter{Status: models.BookStatusImported}, 50, 0)
	if err != nil {
		t.Fatalf("status imported: %v", err)
	}
	if total != 2 {
		t.Errorf("status=imported total=%d, want 2", total)
	}

	// mediaType=audiobook is both-aware: Elantris (audiobook) + The Way of
	// Kings (both).
	_, total, err = bookRepo.ListPageFiltered(ctx, BookListFilter{MediaType: "audiobook"}, 50, 0)
	if err != nil {
		t.Fatalf("media audiobook: %v", err)
	}
	if total != 2 {
		t.Errorf("mediaType=audiobook total=%d, want 2 (audiobook + both)", total)
	}

	// mediaType=ebook is both-aware: Mistborn + Warbreaker (ebook) + The Way of
	// Kings (both).
	_, total, err = bookRepo.ListPageFiltered(ctx, BookListFilter{MediaType: "ebook"}, 50, 0)
	if err != nil {
		t.Fatalf("media ebook: %v", err)
	}
	if total != 3 {
		t.Errorf("mediaType=ebook total=%d, want 3 (ebook + both)", total)
	}

	// Descending title sort.
	za, _, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Sort: "title-za"}, 1, 0)
	if err != nil {
		t.Fatalf("title-za: %v", err)
	}
	if len(za) != 1 || za[0].Title != "Warbreaker" {
		t.Errorf("title-za first = %v, want [Warbreaker]", za)
	}
}
