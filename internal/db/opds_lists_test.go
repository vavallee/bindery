package db

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The OPDS "Recently Added" query must read the top of the partial index and
// stop. Without INDEXED BY the planner takes idx_books_status_sort_title for
// the status equality and sorts every imported book in a temp b-tree.
func TestListRecentDownloadable_PlanReadsThePartialIndex(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, userID := range []int64{0, 7} {
		q, args := recentDownloadableQuery(userID, 50)
		plan := strings.Join(explainPlan(t, database, q, args...), "\n")
		if got := strings.Count(plan, "USING INDEX idx_books_recently_imported"); got != 2 {
			t.Errorf("user %d: %d of the 2 reads of books use idx_books_recently_imported:\n%s", userID, got, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("user %d: the plan sorts instead of reading the index in order:\n%s", userID, plan)
		}
	}
}

// The authors with a downloadable book come from one pass over authors with a
// lookup by author id, not from a scan of books.
func TestListWithDownloadableBooks_PlanSearchesBooksByAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, userID := range []int64{0, 7} {
		q, args := authorsWithDownloadableBooksQuery(userID)
		plan := strings.Join(explainPlan(t, database, q, args...), "\n")
		if !strings.Contains(plan, "USING INDEX idx_books_author_id (author_id=?)") {
			t.Errorf("user %d: books are not searched by author:\n%s", userID, plan)
		}
		if strings.Contains(plan, "SCAN b") {
			t.Errorf("user %d: the plan scans books:\n%s", userID, plan)
		}
	}
}

// Stored updated_at text orders correctly only to the second, so the order
// inside a second has to come from the parsed time, and the id settles a tie.
func TestListRecentDownloadable_OrdersInsideOneSecondByTime(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	authors, books := NewAuthorRepo(database), NewBookRepo(database)
	a := &models.Author{ForeignID: "a", Name: "A", SortName: "A"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	// As text, ".5Z" sorts above ".55Z" and "00Z" above "00.9Z".
	stamps := []string{
		"2026-11-20T10:00:00.5Z",
		"2026-11-20T10:00:00.55Z",
		"2026-11-20T10:00:00Z",
		"2026-11-20T10:00:00.9Z",
		"2026-11-20T10:00:00.9Z",
		"2026-11-20T09:59:59.999Z",
	}
	ids := make([]int64, len(stamps))
	for i, stamp := range stamps {
		b := &models.Book{ForeignID: fmt.Sprint("b", i), AuthorID: a.ID, Title: fmt.Sprint("B", i), Status: models.BookStatusWanted}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		ids[i] = b.ID
		if _, err := database.Exec(`UPDATE books SET status = 'imported', monitored = 1, file_path = '/x.epub', updated_at = ? WHERE id = ?`, stamp, b.ID); err != nil {
			t.Fatal(err)
		}
	}

	got, err := books.ListRecentDownloadable(ctx, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{ids[4], ids[3], ids[1]}
	if len(got) != len(want) {
		t.Fatalf("got %d books, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Errorf("position %d: book %d (%s), want book %d", i, got[i].ID, got[i].UpdatedAtRaw, want[i])
		}
	}

	// A limit past the end returns everything, and no limit returns nothing.
	if got, err = books.ListRecentDownloadable(ctx, 0, 50); err != nil || len(got) != len(stamps) {
		t.Errorf("limit 50: %d books, err %v, want %d", len(got), err, len(stamps))
	}
	if got, err = books.ListRecentDownloadable(ctx, 0, 0); err != nil || got != nil {
		t.Errorf("limit 0: %d books, err %v, want none", len(got), err)
	}
}

// A series counts for a user when it links a book they own or nobody owns.
func TestSeriesIDsWithBooksVisibleTo(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	authors, books, series, users := NewAuthorRepo(database), NewBookRepo(database), NewSeriesRepo(database), NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "hash")
	if err != nil {
		t.Fatal(err)
	}
	a := &models.Author{ForeignID: "a", Name: "A", SortName: "A"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	// One series per owner: alice, bob, nobody. A fourth links no book.
	seriesIDs := map[string]int64{}
	for name, owner := range map[string]any{"alice": alice.ID, "bob": bob.ID, "nobody": nil, "empty": nil} {
		s := &models.Series{ForeignID: "s:" + name, Title: name}
		if err := series.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		seriesIDs[name] = s.ID
		if name == "empty" {
			continue
		}
		b := &models.Book{ForeignID: "b:" + name, AuthorID: a.ID, Title: name, Status: models.BookStatusWanted}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("UPDATE books SET owner_user_id = ? WHERE id = ?", owner, b.ID); err != nil {
			t.Fatal(err)
		}
		if err := series.LinkBook(ctx, s.ID, b.ID, "1", true); err != nil {
			t.Fatal(err)
		}
	}

	got, err := series.IDsWithBooksVisibleTo(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"alice": true, "nobody": true, "bob": false, "empty": false} {
		if _, ok := got[seriesIDs[name]]; ok != want {
			t.Errorf("series %q visible to alice = %v, want %v", name, ok, want)
		}
	}
}
