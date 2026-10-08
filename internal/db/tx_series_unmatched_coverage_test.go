package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestWithTxRoutesWritesThroughTransaction checks the WithTx clones that
// calibre.Rollback composes: writes made through them land only when the
// transaction commits, and a rollback leaves every table as it was.
func TestWithTxRoutesWritesThroughTransaction(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	series := NewSeriesRepo(database)
	editions := NewEditionRepo(database)
	runs := NewCalibreImportRunRepo(database)
	prov := NewCalibreProvenanceRepo(database)

	author := mkAuthor(t, authors, ctx, "OL-TX-A")
	book := mkBook(t, books, ctx, author.ID, "OL-TX-1", "Tx Book", models.BookStatusWanted)
	s := &models.Series{ForeignID: "OL-TX-S", Title: "Tx Series"}
	if err := series.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := series.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	ed := &models.Edition{ForeignID: "OL-TX-E", BookID: book.ID, Title: "Tx Edition", Format: "epub"}
	if err := editions.Upsert(ctx, ed); err != nil {
		t.Fatal(err)
	}
	run := &models.CalibreImportRun{LibraryPath: "/cal", Status: "completed"}
	if err := runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*models.CalibreProvenance{
		{EntityType: "book", ExternalID: "cal-b1", LocalID: book.ID, ImportRunID: &run.ID},
		{EntityType: "author", ExternalID: "cal-a1", LocalID: author.ID, ImportRunID: &run.ID},
	} {
		if err := prov.Upsert(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	mutate := func(txAuthors *AuthorRepo, txSeries *SeriesRepo, txEditions *EditionRepo, txRuns *CalibreImportRunRepo, txProv *CalibreProvenanceRepo) {
		t.Helper()
		if err := txRuns.UpdateStatus(ctx, run.ID, "rolled_back"); err != nil {
			t.Fatal(err)
		}
		got, err := txProv.ListByLocal(ctx, "book", book.ID)
		if err != nil || len(got) != 1 {
			t.Fatalf("ListByLocal inside tx = %+v, %v", got, err)
		}
		if p, err := txProv.GetByExternal(ctx, "default", "author", "cal-a1"); err != nil || p == nil {
			t.Fatalf("GetByExternal inside tx = %+v, %v", p, err)
		}
		if n, err := txProv.DeleteByLocal(ctx, "book", book.ID); err != nil || n != 1 {
			t.Fatalf("DeleteByLocal inside tx = %d, %v", n, err)
		}
		if err := txProv.DeleteByExternal(ctx, "default", "author", "cal-a1"); err != nil {
			t.Fatal(err)
		}
		if err := txSeries.UnlinkBook(ctx, s.ID, book.ID); err != nil {
			t.Fatal(err)
		}
		if err := txEditions.Delete(ctx, ed.ID); err != nil {
			t.Fatal(err)
		}
		a := *author
		a.Name = "Renamed In Tx"
		if err := txAuthors.Update(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}

	// Rollback: nothing sticks.
	tx, err := runs.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	mutate(authors.WithTx(tx), series.WithTx(tx), editions.WithTx(tx), runs.WithTx(tx), prov.WithTx(tx))
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if r, _ := runs.GetByID(ctx, run.ID); r == nil || r.Status != "completed" {
		t.Fatalf("run after rollback = %+v; want completed", r)
	}
	if got, _ := prov.ListByLocal(ctx, "book", book.ID); len(got) != 1 {
		t.Fatalf("book provenance after rollback = %+v", got)
	}
	if ids, _ := series.GetSeriesIDsForBook(ctx, book.ID); len(ids) != 1 || ids[0] != s.ID {
		t.Fatalf("series link after rollback = %v", ids)
	}
	if e, _ := editions.GetByID(ctx, ed.ID); e == nil {
		t.Fatal("edition gone after rollback")
	}
	if a, _ := authors.GetByID(ctx, author.ID); a == nil || a.Name != author.Name {
		t.Fatalf("author after rollback = %+v", a)
	}

	// Commit: everything sticks.
	tx, err = runs.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	mutate(authors.WithTx(tx), series.WithTx(tx), editions.WithTx(tx), runs.WithTx(tx), prov.WithTx(tx))
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if r, _ := runs.GetByID(ctx, run.ID); r == nil || r.Status != "rolled_back" {
		t.Fatalf("run after commit = %+v; want rolled_back", r)
	}
	if got, _ := prov.ListByLocal(ctx, "book", book.ID); len(got) != 0 {
		t.Fatalf("book provenance after commit = %+v", got)
	}
	if ids, _ := series.GetSeriesIDsForBook(ctx, book.ID); len(ids) != 0 {
		t.Fatalf("series link after commit = %v", ids)
	}
	if e, _ := editions.GetByID(ctx, ed.ID); e != nil {
		t.Fatalf("edition after commit = %+v", e)
	}
	if a, _ := authors.GetByID(ctx, author.ID); a == nil || a.Name != "Renamed In Tx" {
		t.Fatalf("author after commit = %+v", a)
	}

	// Author delete through a tx also removes its identifier rows.
	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatal(err)
	}
	tx, err = runs.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := authors.WithTx(tx).Delete(ctx, author.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if a, _ := authors.GetByID(ctx, author.ID); a != nil {
		t.Fatalf("author after tx delete = %+v", a)
	}
	var idents int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM author_identifiers WHERE author_id = ?`, author.ID).Scan(&idents); err != nil {
		t.Fatal(err)
	}
	if idents != 0 {
		t.Fatalf("author_identifiers left after delete: %d", idents)
	}
}

// TestSeriesLookupsAndSearch_Coverage covers SearchTitles ranking and owner
// scoping, the per-book membership readers, ClearGenreOverride and
// UpdateForeignID.
func TestSeriesLookupsAndSearch_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	alice := seedUser(t, ctx, database, "series-alice")
	bob := seedUser(t, ctx, database, "series-bob")
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	repo := NewSeriesRepo(database)
	author := mkAuthor(t, authors, ctx, "OL-SS-A")

	mkOwned := func(fid string, owner int64) *models.Book {
		t.Helper()
		b := &models.Book{ForeignID: fid, AuthorID: author.ID, Title: fid, SortTitle: fid, Status: models.BookStatusWanted,
			Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: owner}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	aliceBook := mkOwned("OL-SS-1", alice)
	bobBook := mkOwned("OL-SS-2", bob)

	mkSeries := func(fid, title string) *models.Series {
		t.Helper()
		s := &models.Series{ForeignID: fid, Title: title}
		if err := repo.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	exact := mkSeries("S1", "Dune")
	lead := mkSeries("S2", "Dune Chronicles")
	mid := mkSeries("S3", "The Dune Saga")
	prefix := mkSeries("S4", "Dunes of Arrakis")
	frag := mkSeries("S5", "Sand Dunesque")
	inner := mkSeries("S6", "Redunes")
	words := mkSeries("S7", "Saga of the Dune")
	bobOnly := mkSeries("S8", "Dune Bob Edition")
	for _, s := range []*models.Series{exact, lead, mid, prefix, frag, inner, words} {
		if err := repo.LinkBook(ctx, s.ID, aliceBook.ID, "1", s.ID == exact.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.LinkBook(ctx, bobOnly.ID, bobBook.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	names := func(ss []models.Series) []string {
		out := []string{}
		for _, s := range ss {
			out = append(out, s.Title)
		}
		return out
	}
	got, err := repo.SearchTitles(ctx, "dune", alice, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Dune", "Dune Chronicles", "The Dune Saga", "Saga of the Dune", "Dunes of Arrakis", "Sand Dunesque", "Redunes"}
	if len(got) != len(want) {
		t.Fatalf("SearchTitles(dune, alice) = %v, want %v", names(got), want)
	}
	for i := range want {
		if got[i].Title != want[i] {
			t.Fatalf("SearchTitles(dune, alice) = %v, want %v", names(got), want)
		}
	}
	if got, _ := repo.SearchTitles(ctx, "dune", alice, 2); len(got) != 2 || got[0].ID != exact.ID {
		t.Fatalf("SearchTitles limit 2 = %v", names(got))
	}
	if got, _ := repo.SearchTitles(ctx, "bob edition", bob, 10); len(got) != 1 || got[0].ID != bobOnly.ID {
		t.Fatalf("SearchTitles(bob) = %v", names(got))
	}
	if got, _ := repo.SearchTitles(ctx, "bob edition", alice, 10); len(got) != 0 {
		t.Fatalf("SearchTitles leaked bob's series to alice: %v", names(got))
	}
	if got, _ := repo.SearchTitles(ctx, "bob edition", 0, 10); len(got) != 1 {
		t.Fatalf("SearchTitles unscoped = %v", names(got))
	}
	// Word-by-word match: every token present, but not as one phrase.
	if got, _ := repo.SearchTitles(ctx, "saga dune", alice, 10); len(got) != 2 {
		t.Fatalf("SearchTitles(saga dune) = %v; want both saga titles", names(got))
	}
	for _, q := range []string{"", "???"} {
		if got, err := repo.SearchTitles(ctx, q, alice, 10); err != nil || len(got) != 0 {
			t.Fatalf("SearchTitles(%q) = %v, %v", q, names(got), err)
		}
	}
	if got, _ := repo.SearchTitles(ctx, "dune", alice, 0); len(got) != 0 {
		t.Fatalf("SearchTitles limit 0 = %v", names(got))
	}

	ids, err := repo.GetSeriesIDsForBook(ctx, aliceBook.ID)
	if err != nil || len(ids) != 7 {
		t.Fatalf("GetSeriesIDsForBook = %v, %v", ids, err)
	}
	mems, err := repo.ListBookSeriesMembershipsForBook(ctx, aliceBook.ID)
	if err != nil || len(mems) != 7 {
		t.Fatalf("ListBookSeriesMembershipsForBook = %+v, %v", mems, err)
	}
	primaries := 0
	for _, m := range mems {
		if m.BookID != aliceBook.ID || m.Position != "1" {
			t.Fatalf("membership = %+v", m)
		}
		if m.Primary {
			primaries++
			if m.SeriesID != exact.ID || m.SeriesTitle != "Dune" || m.SeriesForeignID != "S1" {
				t.Fatalf("primary membership = %+v", m)
			}
		}
	}
	if primaries != 1 {
		t.Fatalf("primary memberships = %d, want 1", primaries)
	}
	if mems, err := repo.ListBookSeriesMembershipsForBook(ctx, 999999); err != nil || len(mems) != 0 {
		t.Fatalf("memberships for a missing book = %+v, %v", mems, err)
	}

	if err := repo.SetGenreOverride(ctx, exact.ID, []string{"sf"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClearGenreOverride(ctx, exact.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.GetByID(ctx, exact.ID); s == nil || s.GenreOverrideSet {
		t.Fatalf("after ClearGenreOverride = %+v", s)
	}

	if err := repo.UpdateForeignID(ctx, 0, "ignored"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateForeignID(ctx, lead.ID, "S2-new"); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.GetByForeignID(ctx, "S2-new"); s == nil || s.ID != lead.ID {
		t.Fatalf("GetByForeignID after UpdateForeignID = %+v", s)
	}
	if err := repo.UpdateForeignID(ctx, mid.ID, "S2-new"); err == nil {
		t.Error("UpdateForeignID onto an existing foreign id should fail the UNIQUE constraint")
	}
}

// TestUnmatchedUnitHelpers_Coverage covers the fingerprint, ignore and
// cross-reference helpers Undo relies on.
func TestUnmatchedUnitHelpers_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewUnmatchedUnitRepo(database)
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := mkAuthor(t, authors, ctx, "OL-UU-A")
	book := mkBook(t, books, ctx, author.ID, "OL-UU-1", "Unit Book", models.BookStatusWanted)

	if got, err := repo.Get(ctx, 999999); err != nil || got != nil {
		t.Fatalf("Get(missing) = %+v, %v", got, err)
	}

	unit := func(path, folder string) UnmatchedUnitScan {
		return UnmatchedUnitScan{
			UnitPath: path, UnitKind: UnmatchedKindFile, Format: "ebook", FileCount: 1, SizeBytes: 1,
			RootPath: "/uu", RelPath: path[len("/uu/"):], AuthorFolder: folder, ParsedTitle: path,
			Reason: "no_title_match", MemberPaths: []string{path},
		}
	}
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{
		unit("/uu/A/1.epub", "A"), unit("/uu/A/2.epub", "A"), unit("/uu/B/3.epub", "B"), unit("/uu/B/4.epub", "B"),
	}, ReconcileScanOptions{SkipDeletion: true}); err != nil {
		t.Fatal(err)
	}
	idOf := func(path string) int64 {
		t.Helper()
		var id int64
		if err := database.QueryRowContext(ctx, `SELECT id FROM unmatched_units WHERE unit_path = ?`, path).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	stateOf := func(path string) string {
		t.Helper()
		u, err := repo.Get(ctx, idOf(path))
		if err != nil || u == nil {
			t.Fatalf("Get(%s) = %+v, %v", path, u, err)
		}
		return u.State
	}

	// IgnorePending: no selector is a no-op; by id; by author folder.
	if n, err := repo.IgnorePending(ctx, nil, ""); err != nil || n != 0 {
		t.Fatalf("IgnorePending(no selector) = %d, %v", n, err)
	}
	if n, err := repo.IgnorePending(ctx, []int64{idOf("/uu/A/1.epub"), 999999}, ""); err != nil || n != 1 {
		t.Fatalf("IgnorePending(ids) = %d, %v; want 1", n, err)
	}
	if stateOf("/uu/A/1.epub") != UnmatchedStateIgnored || stateOf("/uu/A/2.epub") != UnmatchedStatePending {
		t.Fatal("IgnorePending(ids) touched the wrong rows")
	}
	if n, err := repo.IgnorePending(ctx, nil, "B"); err != nil || n != 2 {
		t.Fatalf("IgnorePending(folder B) = %d, %v; want 2", n, err)
	}
	// An already ignored row is not counted again.
	if n, err := repo.IgnorePending(ctx, []int64{idOf("/uu/A/1.epub")}, ""); err != nil || n != 0 {
		t.Fatalf("IgnorePending(already ignored) = %d, %v; want 0", n, err)
	}

	// Cross references.
	u1, u2 := idOf("/uu/A/1.epub"), idOf("/uu/A/2.epub")
	if _, err := database.ExecContext(ctx, `UPDATE unmatched_units SET book_id = ?, created_author_id = ? WHERE id = ?`, book.ID, author.ID, u1); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.BookReferencedElsewhere(ctx, book.ID, u1); err != nil || ok {
		t.Fatalf("BookReferencedElsewhere(except holder) = %v, %v; want false", ok, err)
	}
	if ok, err := repo.BookReferencedElsewhere(ctx, book.ID, u2); err != nil || !ok {
		t.Fatalf("BookReferencedElsewhere(except other) = %v, %v; want true", ok, err)
	}
	if ok, err := repo.AuthorReferencedElsewhere(ctx, author.ID, u1); err != nil || ok {
		t.Fatalf("AuthorReferencedElsewhere(except holder) = %v, %v; want false", ok, err)
	}
	if ok, err := repo.AuthorReferencedElsewhere(ctx, author.ID, u2); err != nil || !ok {
		t.Fatalf("AuthorReferencedElsewhere(except other) = %v, %v; want true", ok, err)
	}

	// BookFingerprint: empty for a missing book, stable while untouched, and
	// moves when something hangs off the book.
	if fp, err := repo.BookFingerprint(ctx, 999999); err != nil || fp != "" {
		t.Fatalf("BookFingerprint(missing) = %q, %v", fp, err)
	}
	fp1, err := repo.BookFingerprint(ctx, book.ID)
	if err != nil || fp1 == "" {
		t.Fatalf("BookFingerprint = %q, %v", fp1, err)
	}
	if again, _ := repo.BookFingerprint(ctx, book.ID); again != fp1 {
		t.Fatalf("fingerprint not stable: %q vs %q", fp1, again)
	}
	bid := book.ID
	if err := NewHistoryRepo(database).Create(ctx, &models.HistoryEvent{BookID: &bid, EventType: models.HistoryEventGrabbed, SourceTitle: "x"}); err != nil {
		t.Fatal(err)
	}
	fp2, err := repo.BookFingerprint(ctx, book.ID)
	if err != nil || fp2 == fp1 {
		t.Fatalf("fingerprint after a history event = %q (was %q), %v; want it to change", fp2, fp1, err)
	}
}
