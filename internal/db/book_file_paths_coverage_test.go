package db

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestBookFilePaths_Coverage covers the BookRepo wrappers around book_files
// and the BookFileRepo methods they delegate to: tracking, moving, untracking,
// path rewrites and the aggregate status refresh each one triggers. The paths
// do not exist on disk, which derivedFormatPath treats as "keep the first row".
func TestBookFilePaths_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	files := NewBookFileRepo(database)
	author := mkAuthor(t, authors, ctx, "OL-BFP-A")
	one := mkBook(t, books, ctx, author.ID, "OL-BFP-1", "One", models.BookStatusWanted)
	two := mkBook(t, books, ctx, author.ID, "OL-BFP-2", "Two", models.BookStatusWanted)
	three := mkBook(t, books, ctx, author.ID, "OL-BFP-3", "Three", models.BookStatusWanted)

	status := func(id int64) *models.Book {
		t.Helper()
		b, err := books.GetByID(ctx, id)
		if err != nil || b == nil {
			t.Fatalf("GetByID(%d) = %+v, %v", id, b, err)
		}
		return b
	}

	// Empty state.
	if paths, err := books.ListAllBookFilePaths(ctx); err != nil || len(paths) != 0 {
		t.Fatalf("ListAllBookFilePaths(empty) = %v, %v", paths, err)
	}
	if n, max, err := books.BookFilesFingerprint(ctx); err != nil || n != 0 || max != 0 {
		t.Fatalf("BookFilesFingerprint(empty) = %d,%d,%v", n, max, err)
	}

	// AddIfMissing reports creation once, then false for the same book, and a
	// typed error for another book.
	created, err := files.AddIfMissing(ctx, one.ID, models.MediaTypeEbook, "/lib/one.epub")
	if err != nil || !created {
		t.Fatalf("AddIfMissing first = %v, %v", created, err)
	}
	created, err = files.AddIfMissing(ctx, one.ID, models.MediaTypeEbook, "/lib/one.epub")
	if err != nil || created {
		t.Fatalf("AddIfMissing repeat = %v, %v; want false,nil", created, err)
	}
	_, err = files.AddIfMissing(ctx, two.ID, models.MediaTypeEbook, "/lib/one.epub")
	var owned *PathOwnedError
	if !errors.As(err, &owned) || owned.OwnerBookID != one.ID || owned.OwnerTitle != "One" {
		t.Fatalf("AddIfMissing for another book = %v; want PathOwnedError naming One", err)
	}
	if owned.Error() == "" {
		t.Error("PathOwnedError.Error is empty")
	}

	if err := books.AddBookFile(ctx, one.ID, models.MediaTypeEbook, "/lib/one.epub"); err != nil {
		t.Fatal(err)
	}
	if b := status(one.ID); b.Status != models.BookStatusImported || b.EbookFilePath != "/lib/one.epub" {
		t.Fatalf("after AddBookFile = status %q path %q", b.Status, b.EbookFilePath)
	}

	if owned, err := books.PathOwnedByOtherBook(ctx, "/lib/one.epub", two.ID); err != nil || !owned {
		t.Fatalf("PathOwnedByOtherBook(two) = %v, %v; want true", owned, err)
	}
	if owned, err := books.PathOwnedByOtherBook(ctx, "/lib/one.epub", one.ID); err != nil || owned {
		t.Fatalf("PathOwnedByOtherBook(one) = %v, %v; want false", owned, err)
	}
	if owned, err := books.PathOwnedByOtherBook(ctx, "/lib/none.epub", 0); err != nil || owned {
		t.Fatalf("PathOwnedByOtherBook(untracked) = %v, %v; want false", owned, err)
	}

	// MoveBookFile takes the path for two and refreshes both books.
	prev, err := books.MoveBookFile(ctx, "/lib/one.epub", two.ID, models.MediaTypeEbook)
	if err != nil || prev != one.ID {
		t.Fatalf("MoveBookFile = %d, %v; want previous owner %d", prev, err, one.ID)
	}
	if b := status(one.ID); b.Status != models.BookStatusWanted || b.EbookFilePath != "" {
		t.Fatalf("source after move = status %q path %q; want wanted and empty", b.Status, b.EbookFilePath)
	}
	if b := status(two.ID); b.Status != models.BookStatusImported {
		t.Fatalf("target after move = status %q; want imported", b.Status)
	}
	// Moving onto the current owner is a no-op that reports itself.
	if prev, err := books.MoveBookFile(ctx, "/lib/one.epub", two.ID, models.MediaTypeEbook); err != nil || prev != two.ID {
		t.Fatalf("MoveBookFile onto owner = %d, %v", prev, err)
	}
	// Moving an untracked path inserts it.
	if prev, err := books.MoveBookFile(ctx, "/lib/three.m4b", three.ID, models.MediaTypeAudiobook); err != nil || prev != 0 {
		t.Fatalf("MoveBookFile(untracked) = %d, %v; want 0", prev, err)
	}

	if err := books.AddBookFile(ctx, three.ID, models.MediaTypeEbook, "/lib/three.epub"); err != nil {
		t.Fatal(err)
	}
	paths, err := books.ListAllBookFilePaths(ctx)
	if err != nil || len(paths) != 3 {
		t.Fatalf("ListAllBookFilePaths = %v, %v", paths, err)
	}
	n, maxID, err := books.BookFilesFingerprint(ctx)
	if err != nil || n != 3 || maxID == 0 {
		t.Fatalf("BookFilesFingerprint = %d,%d,%v", n, maxID, err)
	}

	byBook, err := books.ListFilesForBooks(ctx, []int64{two.ID, three.ID, one.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byBook[two.ID]) != 1 || len(byBook[three.ID]) != 2 || len(byBook[one.ID]) != 0 {
		t.Fatalf("ListFilesForBooks = %v", byBook)
	}
	threeFiles, err := books.ListBookFiles(ctx, three.ID)
	if err != nil || len(threeFiles) != 2 || threeFiles[0].Path != "/lib/three.m4b" {
		t.Fatalf("ListBookFiles(three) = %+v, %v; want id order", threeFiles, err)
	}
	if id, err := books.BookIDForFile(ctx, threeFiles[1].ID); err != nil || id != three.ID {
		t.Fatalf("BookIDForFile = %d, %v", id, err)
	}
	if id, err := books.BookIDForFile(ctx, 999999); err != nil || id != 0 {
		t.Fatalf("BookIDForFile(missing) = %d, %v; want 0", id, err)
	}

	// UpdateBookFilePath repoints the row and the rendered path follows; a
	// collision with another row's path is a UNIQUE violation, and a missing
	// row id is an error rather than a silent no-op.
	epoch := books.BookFilesPathEpoch()
	if err := books.UpdateBookFilePath(ctx, threeFiles[1].ID, three.ID, "/lib/renamed.epub"); err != nil {
		t.Fatal(err)
	}
	if books.BookFilesPathEpoch() != epoch+1 {
		t.Errorf("path epoch = %d, want %d", books.BookFilesPathEpoch(), epoch+1)
	}
	if b := status(three.ID); b.EbookFilePath != "/lib/renamed.epub" {
		t.Fatalf("EbookFilePath after rename = %q", b.EbookFilePath)
	}
	if err := books.UpdateBookFilePath(ctx, threeFiles[1].ID, three.ID, "/lib/one.epub"); err == nil {
		t.Error("UpdateBookFilePath onto another row's path should fail the UNIQUE constraint")
	}
	if err := books.UpdateBookFilePath(ctx, 999999, three.ID, "/lib/x.epub"); err == nil {
		t.Error("UpdateBookFilePath for a missing row should fail")
	}

	// UntrackFilePathForBook only removes the row while it belongs to bookID.
	if removed, err := books.UntrackFilePathForBook(ctx, "/lib/renamed.epub", two.ID); err != nil || removed {
		t.Fatalf("UntrackFilePathForBook(wrong book) = %v, %v; want false", removed, err)
	}
	if removed, err := books.UntrackFilePathForBook(ctx, "/lib/renamed.epub", three.ID); err != nil || !removed {
		t.Fatalf("UntrackFilePathForBook = %v, %v; want true", removed, err)
	}
	if b := status(three.ID); b.EbookFilePath != "" {
		t.Fatalf("EbookFilePath after untrack = %q, want empty", b.EbookFilePath)
	}

	// UntrackFilePath reports the owner, or 0 for an untracked path.
	if id, err := books.UntrackFilePath(ctx, "/lib/never.epub"); err != nil || id != 0 {
		t.Fatalf("UntrackFilePath(untracked) = %d, %v", id, err)
	}
	if id, err := books.UntrackFilePath(ctx, "/lib/three.m4b"); err != nil || id != three.ID {
		t.Fatalf("UntrackFilePath = %d, %v; want %d", id, err, three.ID)
	}

	// RemoveBookFile returns the refreshed book, or nil when the path was untracked.
	if b, err := books.RemoveBookFile(ctx, "/lib/never.epub"); err != nil || b != nil {
		t.Fatalf("RemoveBookFile(untracked) = %+v, %v", b, err)
	}
	b, err := books.RemoveBookFile(ctx, "/lib/one.epub")
	if err != nil || b == nil || b.ID != two.ID || b.Status != models.BookStatusWanted || b.EbookFilePath != "" {
		t.Fatalf("RemoveBookFile = %+v, %v; want book two back to wanted", b, err)
	}

	// SetCalibreIDIfUnset is write-once.
	if ok, err := books.SetCalibreIDIfUnset(ctx, one.ID, 41); err != nil || !ok {
		t.Fatalf("SetCalibreIDIfUnset first = %v, %v", ok, err)
	}
	if ok, err := books.SetCalibreIDIfUnset(ctx, one.ID, 42); err != nil || ok {
		t.Fatalf("SetCalibreIDIfUnset second = %v, %v; want false", ok, err)
	}
	if got, err := books.GetByCalibreID(ctx, 41); err != nil || got == nil || got.ID != one.ID {
		t.Fatalf("GetByCalibreID(41) = %+v, %v", got, err)
	}
	if ok, err := books.SetCalibreIDIfUnset(ctx, 999999, 1); err != nil || ok {
		t.Fatalf("SetCalibreIDIfUnset(missing) = %v, %v; want false", ok, err)
	}
}

// TestBookListSortOrders drives ListPageFiltered through every whitelisted
// sort key and checks the resulting order, so a typo in bookSortOrder shows
// up as a wrong page rather than only as a coverage gap.
func TestBookListSortOrders(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	zed := &models.Author{ForeignID: "OL-SORT-Z", Name: "Zed Zulu", SortName: "Zulu, Zed", MetadataProvider: "openlibrary"}
	amy := &models.Author{ForeignID: "OL-SORT-A", Name: "Amy Alpha", SortName: "Alpha, Amy", MetadataProvider: "openlibrary"}
	for _, a := range []*models.Author{zed, amy} {
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(authorID int64, fid, title, media, status, release string) {
		t.Helper()
		b := &models.Book{ForeignID: fid, AuthorID: authorID, Title: title, SortTitle: title, Status: status,
			MediaType: media, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `UPDATE books SET release_date = ? WHERE id = ?`, release, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	mk(zed.ID, "OL-SORT-1", "Apple", models.MediaTypeAudiobook, models.BookStatusWanted, "2001-01-01")
	mk(amy.ID, "OL-SORT-2", "Banana", models.MediaTypeEbook, models.BookStatusSkipped, "2003-01-01")
	mk(amy.ID, "OL-SORT-3", "Cherry", models.MediaTypeEbook, models.BookStatusImported, "2002-01-01")

	cases := map[string][]string{
		"":          {"Apple", "Banana", "Cherry"},
		"title-za":  {"Cherry", "Banana", "Apple"},
		"date-new":  {"Banana", "Cherry", "Apple"},
		"date-old":  {"Apple", "Cherry", "Banana"},
		"author-az": {"Banana", "Cherry", "Apple"},
		"author-za": {"Apple", "Banana", "Cherry"},
		"type-az":   {"Apple", "Banana", "Cherry"},
		"type-za":   {"Banana", "Cherry", "Apple"},
		"status-az": {"Cherry", "Banana", "Apple"},
		"status-za": {"Apple", "Banana", "Cherry"},
		"bogus":     {"Apple", "Banana", "Cherry"},
	}
	for sort, want := range cases {
		got, total, err := books.ListPageFiltered(ctx, BookListFilter{Sort: sort}, 10, 0)
		if err != nil || total != 3 {
			t.Fatalf("sort %q: total=%d err=%v", sort, total, err)
		}
		for i := range want {
			if got[i].Title != want[i] {
				var titles []string
				for _, b := range got {
					titles = append(titles, b.Title)
				}
				t.Errorf("sort %q = %v, want %v", sort, titles, want)
				break
			}
		}
	}
}
