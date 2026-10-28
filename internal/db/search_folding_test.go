package db

import (
	"context"
	"path/filepath"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/models"
)

// seedSearchLibrary builds a small multilingual library: the authors and titles
// whose spellings have actually been reported as unfindable.
func seedSearchLibrary(t *testing.T) (*BookRepo, *AuthorRepo, *AuthorAliasRepo, context.Context) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)
	ctx := context.Background()

	authors := map[string]*models.Author{}
	mkAuthor := func(id, name, sortName string) {
		a := &models.Author{ForeignID: id, Name: name, SortName: sortName, Monitored: true}
		if err := authorRepo.Create(ctx, a); err != nil {
			t.Fatalf("seed author %s: %v", name, err)
		}
		authors[name] = a
	}
	mkAuthor("OL-NESBO", "Jo Nesbø", "Nesbø, Jo")
	mkAuthor("OL-ROWLING", "J.K. Rowling", "Rowling, J.K.")
	mkAuthor("OL-LIU", "刘慈欣", "刘慈欣")
	mkAuthor("OL-OSTER", "Anne Østergaard", "Østergaard, Anne")
	mkAuthor("OL-BLACK", "Holly Black", "Black, Holly")
	mkAuthor("OL-CUSSLER", "Clive Cussler", "Cussler, Clive")
	mkAuthor("OL-ASIMOV", "Isaac Asimov", "Asimov, Isaac")
	mkAuthor("OL-TOLKIEN", "J.R.R. Tolkien", "Tolkien, J.R.R.")

	mkBook := func(author, title string) {
		b := &models.Book{
			ForeignID: "OL-B" + title, AuthorID: authors[author].ID,
			Title: title, SortTitle: title, MediaType: models.MediaTypeEbook,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatalf("seed book %s: %v", title, err)
		}
	}
	mkBook("Jo Nesbø", "Snømannen")
	mkBook("J.K. Rowling", "Harry Potter und der Orden des Phönix")
	mkBook("J.K. Rowling", "ハリー・ポッター")
	mkBook("刘慈欣", "三体")
	mkBook("Anne Østergaard", "Køge")
	mkBook("Clive Cussler", "Poseidon's Arrow")
	mkBook("Isaac Asimov", "Foundation & Empire")
	mkBook("J.R.R. Tolkien", "The Hobbit")
	mkBook("J.R.R. Tolkien", "The Hobbit: Illustrated Edition")

	// A pen name, so alias search keeps working through the folded column (#1176).
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: authors["Holly Black"].ID, Name: "Cassandra Clare"}); err != nil {
		t.Fatalf("seed alias: %v", err)
	}
	return bookRepo, authorRepo, aliasRepo, ctx
}

// TestBookSearchFoldsQueryAndRow is the regression for #1660: the search box
// matched `LIKE ? COLLATE NOCASE` against the raw title, and SQLite folds ASCII
// and nothing else, so none of these queries returned their book.
func TestBookSearchFoldsQueryAndRow(t *testing.T) {
	bookRepo, _, _, ctx := seedSearchLibrary(t)

	cases := []struct {
		query string
		want  string
		why   string
	}{
		{"phonix", "Harry Potter und der Orden des Phönix", "ö typed as o (#1610)"},
		{"Phönix", "Harry Potter und der Orden des Phönix", "the accented spelling still works"},
		{"PHÖNIX", "Harry Potter und der Orden des Phönix", "non-ASCII case folds"},
		{"snomannen", "Snømannen", "ø has no decomposition (#1642)"},
		{"koge", "Køge", "ø again, this time the whole title"},
		{"nesbo", "Snømannen", "author name folded, matched through the join"},
		{"三体", "三体", "a two code point CJK query"},
		{"刘慈欣", "三体", "CJK author"},
		{"ハリーポッター", "ハリー・ポッター", "the middle dot is optional (#1645)"},
		{"poseidons arrow", "Poseidon's Arrow", "apostrophe deleted, not separated (#2042)"},
		{"poseidon's arrow", "Poseidon's Arrow", "and the apostrophised spelling too"},
		{"foundation and empire", "Foundation & Empire", "& spelled out"},
		{"foundation & empire", "Foundation & Empire", "& as typed"},
		{"hobbit tolkien", "The Hobbit", "two tokens spread across title and author"},
	}
	for _, tc := range cases {
		got, total, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Search: tc.query}, 50, 0)
		if err != nil {
			t.Fatalf("search %q: %v", tc.query, err)
		}
		if total == 0 {
			t.Errorf("search %q found nothing, want %q (%s)", tc.query, tc.want, tc.why)
			continue
		}
		if got[0].Title != tc.want {
			t.Errorf("search %q ranked %q first, want %q (%s)", tc.query, got[0].Title, tc.want, tc.why)
		}
	}
}

// TestBookSearchIsUnicodeFormInvariant covers the half of #1646 that reaches the
// search box: the row is stored composed (every provider sends NFC) while a
// query typed or pasted on macOS arrives decomposed.
func TestBookSearchIsUnicodeFormInvariant(t *testing.T) {
	bookRepo, _, _, ctx := seedSearchLibrary(t)

	for _, q := range []string{"Phönix", "Snømannen", "Køge"} {
		composed, total, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Search: norm.NFC.String(q)}, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		if total == 0 {
			t.Fatalf("composed query %q found nothing", q)
		}
		decomposed, dtotal, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Search: norm.NFD.String(q)}, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		if dtotal != total || len(decomposed) == 0 || decomposed[0].Title != composed[0].Title {
			t.Errorf("query %q: NFC found %d (%q), NFD found %d — the two spellings must agree",
				q, total, composed[0].Title, dtotal)
		}
	}
}

// TestBookSearchRanksExactBeforePrefix pins the tier order from searchrank.go.
// Without it the "best" hit was whichever matching row sorted first
// alphabetically, so a query that names a book exactly could still be listed
// under a longer title that merely contains it.
func TestBookSearchRanksExactBeforePrefix(t *testing.T) {
	bookRepo, _, _, ctx := seedSearchLibrary(t)

	got, total, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Search: "the hobbit"}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("search 'the hobbit' total=%d, want 2", total)
	}
	if got[0].Title != "The Hobbit" {
		t.Errorf("search 'the hobbit' ranked %q first, want the exact title %q", got[0].Title, "The Hobbit")
	}
	if got[1].Title != "The Hobbit: Illustrated Edition" {
		t.Errorf("search 'the hobbit' ranked %q second, want the longer title", got[1].Title)
	}
}

// TestAuthorSearchFoldsQueryAndRow is the same regression on the Authors list,
// including the alias path (#1176) which now matches on its own folded column.
func TestAuthorSearchFoldsQueryAndRow(t *testing.T) {
	_, authorRepo, _, ctx := seedSearchLibrary(t)

	cases := []struct {
		query string
		want  string
		why   string
	}{
		{"ostergaard", "Anne Østergaard", "ø folded (#1347's sibling on the search side)"},
		{"Østergaard", "Anne Østergaard", "the accented spelling"},
		{"ØSTERGAARD", "Anne Østergaard", "non-ASCII case folding"},
		{"nesbo", "Jo Nesbø", "ø again"},
		{"刘慈欣", "刘慈欣", "CJK name"},
		{"cassandra clare", "Holly Black", "found through a pen name (#1176)"},
		{"rowling", "J.K. Rowling", "plain surname still works"},
	}
	for _, tc := range cases {
		got, total, err := authorRepo.ListPageFiltered(ctx, AuthorListFilter{Search: tc.query}, 50, 0)
		if err != nil {
			t.Fatalf("author search %q: %v", tc.query, err)
		}
		if total == 0 {
			t.Errorf("author search %q found nothing, want %q (%s)", tc.query, tc.want, tc.why)
			continue
		}
		if got[0].Name != tc.want {
			t.Errorf("author search %q ranked %q first, want %q (%s)", tc.query, got[0].Name, tc.want, tc.why)
		}
	}
}

// TestBookListOrdersAccentedTitlesInPlace is #1347 for the Books list. Ordering
// on the raw sort_title put every title with a non-ASCII first letter after "Z",
// which reads as the A–Z list being broken.
func TestBookListOrdersAccentedTitlesInPlace(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-SORT", Name: "Sort Author", SortName: "Author, Sort", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Deliberately seeded out of order, and deliberately interleaving accented
	// initials with ASCII ones.
	for _, title := range []string{"Zebra", "Ångström", "Middlemarch", "Ödland", "Łódź", "Adventure"} {
		b := &models.Book{ForeignID: "OL-S" + title, AuthorID: author.ID, Title: title, SortTitle: title, MediaType: models.MediaTypeEbook}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatalf("seed %s: %v", title, err)
		}
	}

	got, _, err := bookRepo.ListPageFiltered(ctx, BookListFilter{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Adventure", "Ångström", "Łódź", "Middlemarch", "Ödland", "Zebra"}
	if len(got) != len(want) {
		t.Fatalf("got %d books, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Title != want[i] {
			titles := make([]string, len(got))
			for j, b := range got {
				titles[j] = b.Title
			}
			t.Fatalf("A–Z order = %v, want %v", titles, want)
		}
	}
}

// TestSearchWorksAfterUpgradingAnExistingLibrary is the upgrade path, which the
// tests above do not cover: they create rows through the repos, which write the
// keys, whereas a real user's rows predate migration 083 and arrive with the
// columns empty.
//
// That state is worth asserting end to end rather than one backfill at a time,
// because the failure it guards against is silent. If the backfill did not run,
// or ran but the search did not read what it wrote, the library would simply
// stop being searchable — with no error anywhere, and no way for the user to
// tell it apart from "I own nothing matching that".
func TestSearchWorksAfterUpgradingAnExistingLibrary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-UP-A", Name: "Jo Nesbø", SortName: "Nesbø, Jo"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Snømannen", "Ödland"} {
		if err := books.Create(ctx, &models.Book{
			ForeignID: "OL-UP-" + title, AuthorID: author.ID,
			Title: title, SortTitle: title, MediaType: models.MediaTypeEbook,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Rewind to the state migration 083 leaves an existing database in: the
	// columns exist and are empty, and no backfill has claimed a revision.
	for _, stmt := range []string{
		"UPDATE books SET search_key = '', sort_key = ''",
		"UPDATE authors SET search_key = ''",
		"UPDATE author_aliases SET search_key = ''",
		"DELETE FROM settings WHERE key IN ('backfill.search_key_rev', 'backfill.book_sort_key_rev')",
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatalf("rewind %q: %v", stmt, err)
		}
	}
	database.Close()

	// Restart, as an upgrade does.
	database = openFileDB(t, path)
	defer database.Close()
	books = NewBookRepo(database)
	authors = NewAuthorRepo(database)

	got, total, err := books.ListPageFiltered(ctx, BookListFilter{Search: "snomannen"}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].Title != "Snømannen" {
		t.Errorf("after upgrade, search 'snomannen' returned %d rows, want the pre-existing Snømannen", total)
	}
	if _, atotal, err := authors.ListPageFiltered(ctx, AuthorListFilter{Search: "nesbo"}, 50, 0); err != nil || atotal != 1 {
		t.Errorf("after upgrade, author search 'nesbo' returned %d rows (err %v), want 1", atotal, err)
	}
	ordered, _, err := books.ListPageFiltered(ctx, BookListFilter{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0].Title != "Ödland" {
		t.Errorf("after upgrade, A–Z order = %v, want Ödland first (its folded key is 'odland')", ordered)
	}
}

// TestBookSearchMatchesSeriesName is the regression for #3462: each word had
// to be in the title or the author, so naming the series a book belongs to
// found nothing, and adding it to an author search emptied the result.
func TestBookSearchMatchesSeriesName(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	todd := &models.Author{ForeignID: "OL-TODD", Name: "Charles Todd", SortName: "Todd, Charles"}
	other := &models.Author{ForeignID: "OL-OTHER", Name: "Jane Doe", SortName: "Doe, Jane"}
	for _, a := range []*models.Author{todd, other} {
		if err := authorRepo.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	mkBook := func(author *models.Author, title string) *models.Book {
		b := &models.Book{ForeignID: "OL-B" + title, AuthorID: author.ID, Title: title, SortTitle: title, MediaType: models.MediaTypeEbook}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	inSeries := mkBook(todd, "A Test of Wills")
	mkBook(todd, "A Duty to the Dead")
	titled := mkBook(other, "Rutledge Remembered")

	series := &models.Series{ForeignID: "OL-S-RUTLEDGE", Title: "Inspector Ian Rutledge"}
	if err := seriesRepo.CreateOrGet(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, inSeries.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		query string
		want  []string
	}{
		// The title match ranks ahead of the series-only match.
		{"Rutledge", []string{titled.Title, inSeries.Title}},
		{"Ian Rutledge", []string{inSeries.Title}},
		{"Charles Todd Ian Rutledge", []string{inSeries.Title}},
		{"inspector rutledge todd", []string{inSeries.Title}},
	}
	for _, tc := range cases {
		got, total, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Search: tc.query}, 50, 0)
		if err != nil {
			t.Fatalf("search %q: %v", tc.query, err)
		}
		if total != len(tc.want) || len(got) != len(tc.want) {
			t.Errorf("search %q: total %d, %d rows, want %v", tc.query, total, len(got), tc.want)
			continue
		}
		for i, w := range tc.want {
			if got[i].Title != w {
				t.Errorf("search %q: row %d = %q, want %q", tc.query, i, got[i].Title, w)
			}
		}
	}

	// A rename carries the key with it.
	if err := seriesRepo.UpdateTitle(ctx, series.ID, "Inspector Rutledge Mysteries"); err != nil {
		t.Fatal(err)
	}
	if _, total, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Search: "Ian Rutledge"}, 50, 0); err != nil || total != 0 {
		t.Errorf("search after rename: total %d, err %v, want the old series name to stop matching", total, err)
	}
	if got, total, err := bookRepo.ListPageFiltered(ctx, BookListFilter{Search: "mysteries"}, 50, 0); err != nil || total != 1 || got[0].Title != inSeries.Title {
		t.Errorf("search for the new series name: total %d, err %v, want %q", total, err, inSeries.Title)
	}

	// A series row from before migration 114 has an empty key until the
	// startup backfill folds it.
	if _, err := database.Exec("UPDATE series SET search_key = ''"); err != nil {
		t.Fatal(err)
	}
	if err := backfillSearchKeys(database); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := database.QueryRow("SELECT search_key FROM series WHERE id = ?", series.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "inspector rutledge mysteries" {
		t.Errorf("series.search_key after backfill = %q, want %q", key, "inspector rutledge mysteries")
	}
}

// Migration 114 adds series.search_key and drops the backfill marker. An
// install that already ran the search key backfill at the current revision
// would otherwise never fold its existing series, and series name search would
// silently find nothing for every series made before the upgrade (#3462).
func TestMigrate114BackfillsSeriesSearchKeysOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	author := &models.Author{ForeignID: "OL-114-A", Name: "Charles Todd", SortName: "Todd, Charles"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-114-B", AuthorID: author.ID, Title: "A Test of Wills", SortTitle: "A Test of Wills", MediaType: models.MediaTypeEbook}
	if err := NewBookRepo(database).Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{ForeignID: "OL-114-S", Title: "Inspector Ian Rutledge"}
	if err := NewSeriesRepo(database).CreateOrGet(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := NewSeriesRepo(database).LinkBook(ctx, series.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := settingValue(t, database, "backfill.search_key_rev"); !ok {
		t.Fatal("precondition: a migrated database should have the search key backfill marker, as an upgraded install does")
	}

	// Rewind to the previous release: no series.search_key column, migration
	// 114 not recorded, and the backfill marker present at the current revision.
	for _, stmt := range []string{
		"ALTER TABLE series DROP COLUMN search_key",
		"DELETE FROM schema_migrations WHERE version = 114",
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatalf("rewind %q: %v", stmt, err)
		}
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()

	var key string
	if err := database.QueryRow("SELECT search_key FROM series WHERE id = ?", series.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "inspector ian rutledge" {
		t.Errorf("series.search_key after upgrading = %q, want the existing series backfilled", key)
	}
	got, total, err := NewBookRepo(database).ListPageFiltered(ctx, BookListFilter{Search: "ian rutledge"}, 50, 0)
	if err != nil || total != 1 || got[0].Title != book.Title {
		t.Errorf("series name search after upgrading: total %d, err %v, want the existing book", total, err)
	}
}
