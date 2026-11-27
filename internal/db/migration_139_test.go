package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate139UpgradeAndDown runs migration 139 on a database that looks
// like one from before it (books present, no last_auto_search_at) and checks
// that every existing book reads as never searched, which is what makes the
// first sweep after an upgrade run in the old title order. Then the Down
// section is run and the books survive it.
func TestMigrate139UpgradeAndDown(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	const file = "139_books_last_auto_search_at.sql"
	content, err := migrationsFS.ReadFile("migrations/" + file)
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	idx := strings.Index(s, "-- +migrate Down")
	if idx < 0 {
		t.Fatal("migration 139 has no Down section")
	}
	down := func() {
		for _, stmt := range splitSQLStatements(s[idx+len("-- +migrate Down"):]) {
			mustExec(t, database, stmt)
		}
	}
	columnCount := func() int {
		var n int
		if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('books') WHERE name = 'last_auto_search_at'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Down on the fresh schema removes the column.
	down()
	if columnCount() != 0 {
		t.Fatal("Down left books.last_auto_search_at in place")
	}

	// A pre-139 database: wanted and imported books, no column.
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-M139", Name: "M139", SortName: "M139", MetadataProvider: "ol", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for title, status := range map[string]string{
		"Wanted One": models.BookStatusWanted,
		"Wanted Two": models.BookStatusWanted,
		"On Shelf":   models.BookStatusImported,
	} {
		b := models.Book{ForeignID: "OL-" + title, AuthorID: author.ID, Title: title, SortTitle: title, Status: status,
			Genres: []string{}, MetadataProvider: "ol", Monitored: true, MediaType: models.MediaTypeEbook}
		if err := books.Create(ctx, &b); err != nil {
			t.Fatal(err)
		}
		ids[title] = b.ID
	}

	// Upgrade: forget 139 and run the migrator.
	mustExec(t, database, `DELETE FROM schema_migrations WHERE version = ?`, migrationVersionForTest(t, file))
	if err := migrate(database); err != nil {
		t.Fatalf("migrate 139: %v", err)
	}
	if columnCount() != 1 {
		t.Fatal("Up did not add books.last_auto_search_at")
	}
	for title, id := range ids {
		at, err := books.LastAutoSearchedAt(ctx, id)
		if err != nil || at != nil {
			t.Errorf("%s: stamp after upgrade = %v (%v), want never searched", title, at, err)
		}
	}
	// The column is usable straight away.
	now := time.Now()
	if err := books.MarkAutoSearched(ctx, ids["Wanted One"], now); err != nil {
		t.Fatal(err)
	}
	if at, err := books.LastAutoSearchedAt(ctx, ids["Wanted One"]); err != nil || at == nil || !at.Equal(now) {
		t.Fatalf("stamp read back = %v (%v), want %v", at, err, now.UTC())
	}

	// Down again keeps every row.
	down()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("books after Down = %d (%v), want 3", n, err)
	}
}

// The sweep's list: never searched first in title order, then by stamp,
// oldest first. ListByStatus, which every other reader of the wanted and
// imported lists uses, keeps its title order whatever the stamps say.
func TestListByStatusLeastRecentlySearched(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-LRS", Name: "LRS", SortName: "LRS", MetadataProvider: "ol", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	create := func(title, status string, monitored, excluded bool) {
		t.Helper()
		b := models.Book{ForeignID: "OL-" + title, AuthorID: author.ID, Title: title, SortTitle: title, Status: status,
			Genres: []string{}, MetadataProvider: "ol", Monitored: monitored, Excluded: excluded, MediaType: models.MediaTypeEbook}
		if err := books.Create(ctx, &b); err != nil {
			t.Fatal(err)
		}
		ids[title] = b.ID
	}
	for _, title := range []string{"Delta", "Alpha", "Echo", "Charlie", "Bravo", "Foxtrot"} {
		create(title, models.BookStatusWanted, true, false)
	}
	create("Imported", models.BookStatusImported, true, false)
	create("Unmonitored", models.BookStatusWanted, false, false)
	create("Excluded", models.BookStatusWanted, true, false)
	if err := books.SetExcluded(ctx, ids["Excluded"], true); err != nil {
		t.Fatal(err)
	}

	titlesOf := func(list []models.Book, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(list))
		for i, b := range list {
			out[i] = b.Title
		}
		return strings.Join(out, " ")
	}
	const byTitle = "Alpha Bravo Charlie Delta Echo Foxtrot"
	if got := titlesOf(books.ListByStatusLeastRecentlySearched(ctx, models.BookStatusWanted)); got != byTitle {
		t.Fatalf("nothing searched yet: %q, want title order %q", got, byTitle)
	}

	// Stamps chosen so text order and time order disagree unless the stored
	// form is fixed width: a whole second, then fractions of the same second.
	base := time.Date(2026, 11, 26, 10, 0, 5, 0, time.UTC)
	stamps := []struct {
		title string
		at    time.Time
	}{
		{"Echo", base}, // 10:00:05 exactly
		{"Alpha", base.Add(500 * time.Millisecond)},   // 10:00:05.5
		{"Delta", base.Add(500*time.Millisecond + 1)}, // one nanosecond later
		{"Bravo", base.Add(-time.Hour).In(time.FixedZone("x", 3600))},
	}
	for _, st := range stamps {
		if err := books.MarkAutoSearched(ctx, ids[st.title], st.at); err != nil {
			t.Fatal(err)
		}
	}
	// A stamp on a book that is not on the list changes nothing.
	if err := books.MarkAutoSearched(ctx, ids["Imported"], base.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	const fair = "Charlie Foxtrot Bravo Echo Alpha Delta"
	if got := titlesOf(books.ListByStatusLeastRecentlySearched(ctx, models.BookStatusWanted)); got != fair {
		t.Fatalf("sweep order = %q, want never searched by title then oldest stamp first %q", got, fair)
	}
	if got := titlesOf(books.ListByStatus(ctx, models.BookStatusWanted)); got != byTitle {
		t.Fatalf("ListByStatus = %q, want it left in title order %q", got, byTitle)
	}
	if got := titlesOf(books.ListByStatusAndUser(ctx, models.BookStatusWanted, 0)); got != byTitle {
		t.Fatalf("ListByStatusAndUser = %q, want it left in title order %q", got, byTitle)
	}

	// A full row update, which is how a metadata refresh or an edit writes a
	// book, must not clear the stamp.
	b, err := books.GetByID(ctx, ids["Echo"])
	if err != nil || b == nil {
		t.Fatalf("get: %v", err)
	}
	b.Description = "edited"
	if err := books.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	if at, err := books.LastAutoSearchedAt(ctx, ids["Echo"]); err != nil || at == nil || !at.Equal(base) {
		t.Fatalf("stamp after Update = %v (%v), want %v", at, err, base)
	}
	if _, err := books.LastAutoSearchedAt(ctx, 999999); err == nil {
		t.Fatal("reading the stamp of a book that does not exist returned no error")
	}
}
