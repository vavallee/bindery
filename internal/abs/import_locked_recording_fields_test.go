package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_KeepsLockedRecordingFields pins that a narrator, runtime or
// ASIN the user set by hand survives both ABS update paths. Before this the
// import wrote all three unconditionally, so every re-import undid the edit.
func TestImporter_KeepsLockedRecordingFields(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-LRF", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := &models.Book{
		ForeignID: "OL-LRF-ARTEMIS", AuthorID: author.ID, Title: "Artemis", SortTitle: "Artemis",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
		Narrator: "Rosario Dawson", DurationSeconds: 32400, ASIN: "B0USERASIN",
		LockedFields: []string{models.BookFieldNarrator, models.BookFieldDurationSeconds, models.BookFieldASIN},
		Genres:       []string{}, Monitored: true,
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	item := sampleABSItem()
	item.ItemID = "li-artemis-locked"
	item.Title = "Artemis"
	item.Series = nil
	item.Narrators = []string{"Wrong Narrator"}
	item.DurationSeconds = 57600
	item.Authors = []NormalizedAuthor{{ID: "author-weir", Name: "Andy Weir"}}

	// Direct unit of the merge used by the manual-link path.
	merged := *existing
	item.ASIN = "B0ABSASIN1"
	importer.applyABSFormatFields(&merged, item)
	if merged.Narrator != "Rosario Dawson" || merged.DurationSeconds != 32400 || merged.ASIN != "B0USERASIN" {
		t.Fatalf("applyABSFormatFields overwrote locked fields: narrator=%q duration=%d asin=%q", merged.Narrator, merged.DurationSeconds, merged.ASIN)
	}

	// Direct unit of the merge used by every other update path.
	if err := importer.applyBookFields(ctx, existing, author.ID, item); err != nil {
		t.Fatal(err)
	}
	got, err := bookRepo.GetByID(ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Narrator != "Rosario Dawson" || got.DurationSeconds != 32400 || got.ASIN != "B0USERASIN" {
		t.Fatalf("applyBookFields overwrote locked fields: narrator=%q duration=%d asin=%q", got.Narrator, got.DurationSeconds, got.ASIN)
	}
	if got.Title != "Artemis" {
		t.Fatalf("unlocked title should still follow ABS: %q", got.Title)
	}

	// End to end through a run, matched by title, with the item's ASIN empty so
	// the fixture's nil metadata aggregator is never asked.
	item.ASIN = ""
	runSingleABSImport(t, importer, item)
	got, _ = bookRepo.GetByID(ctx, existing.ID)
	if got.Narrator != "Rosario Dawson" || got.DurationSeconds != 32400 {
		t.Fatalf("re-import overwrote locked fields: narrator=%q duration=%d", got.Narrator, got.DurationSeconds)
	}
	if countBooksForAuthor(t, bookRepo, author.ID) != 1 {
		t.Fatal("import should have matched the existing book, not created one")
	}

	// An unlocked book still takes ABS's values, so the guard is the lock, not the path.
	unlocked := &models.Book{
		ForeignID: "OL-LRF-HAIL", AuthorID: author.ID, Title: "Project Hail Mary", SortTitle: "Project Hail Mary",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
		Narrator: "Stale", DurationSeconds: 1, Genres: []string{}, Monitored: true,
	}
	if err := bookRepo.Create(ctx, unlocked); err != nil {
		t.Fatal(err)
	}
	if err := importer.applyBookFields(ctx, unlocked, author.ID, sampleABSItem()); err != nil {
		t.Fatal(err)
	}
	got, _ = bookRepo.GetByID(ctx, unlocked.ID)
	if got.Narrator != "Ray Porter" || got.DurationSeconds != 57600 {
		t.Fatalf("unlocked fields should follow ABS: narrator=%q duration=%d", got.Narrator, got.DurationSeconds)
	}
}

// TestImporter_ManualBookKeepsLockedRecordingFields pins the review queue's
// manual-link path end to end: linking an ABS item to an existing book the
// user locked leaves narrator, runtime and ASIN as the user set them.
func TestImporter_ManualBookKeepsLockedRecordingFields(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-MLRF", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := &models.Book{
		ForeignID: "OL-MLRF-W", AuthorID: author.ID, Title: "Artemis", SortTitle: "Artemis",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
		Narrator: "Rosario Dawson", DurationSeconds: 32400, ASIN: "B0USERASIN",
		LockedFields: []string{models.BookFieldNarrator, models.BookFieldDurationSeconds, models.BookFieldASIN},
		Genres:       []string{}, Monitored: true,
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	item := sampleABSItem()
	item.ItemID = "li-manual-locked"
	item.Narrators = []string{"Wrong Narrator"}
	item.DurationSeconds = 57600
	item.ASIN = "B0ABSASIN1"
	item.ResolvedBookForeignID = existing.ForeignID
	item.ResolvedBookTitle = "Artemis"

	result, created, linked, _, err := importer.upsertManualBook(ctx, asinTestConfig(), 0, author, item)
	if err != nil {
		t.Fatalf("upsertManualBook: %v", err)
	}
	if created || !linked || result == nil || result.row.ID != existing.ID {
		t.Fatalf("expected a link to the existing book: created=%v linked=%v result=%+v", created, linked, result)
	}
	got, err := bookRepo.GetByID(ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Narrator != "Rosario Dawson" || got.DurationSeconds != 32400 || got.ASIN != "B0USERASIN" {
		t.Fatalf("manual link overwrote locked fields: narrator=%q duration=%d asin=%q", got.Narrator, got.DurationSeconds, got.ASIN)
	}
}

// TestRestoreBookFromSnapshot_KeepsLockedFields pins that rolling back an ABS
// run leaves a locked recording field alone, even when its value still equals
// what the run wrote, while every other field rolls back as before, locked or
// not: the work fields can still be overwritten by the upstream merge, and
// rollback is the way back from that.
func TestRestoreBookFromSnapshot_KeepsLockedFields(t *testing.T) {
	before := &bookRollbackSnapshot{Title: "Old Title", SortTitle: "Old Title", Narrator: "Old Narrator", DurationSeconds: 100, ASIN: "B0OLDASIN1", Language: "eng"}
	after := &bookRollbackSnapshot{Title: "ABS Title", SortTitle: "ABS Title", Narrator: "ABS Narrator", DurationSeconds: 200, ASIN: "B0ABSASIN1", Language: "ger"}
	book := &models.Book{
		Title: "ABS Title", SortTitle: "ABS Title", Narrator: "ABS Narrator", DurationSeconds: 200, ASIN: "B0ABSASIN1", Language: "ger",
		LockedFields: []string{models.BookFieldTitle, models.BookFieldNarrator, models.BookFieldASIN},
	}

	if !restoreBookFromSnapshot(book, before, after) {
		t.Fatal("unlocked fields should still roll back")
	}
	if book.Narrator != "ABS Narrator" || book.ASIN != "B0ABSASIN1" {
		t.Fatalf("rollback reverted locked recording fields: narrator=%q asin=%q", book.Narrator, book.ASIN)
	}
	if book.Title != "Old Title" || book.SortTitle != "Old Title" || book.DurationSeconds != 100 || book.Language != "eng" {
		t.Fatalf("fields outside the guard not rolled back: title=%q sort=%q duration=%d language=%q", book.Title, book.SortTitle, book.DurationSeconds, book.Language)
	}
}

// TestImporter_ApplyBookFieldsKeepsLockSetDuringImport pins the write, not
// the merge: the lock check runs on the importer's snapshot, so a lock the
// user sets between that read and the write must still win. The write is
// refused by the updated_at guard, the row is reloaded, the ABS fields are
// applied again against the current locks, and the retry lands.
func TestImporter_ApplyBookFieldsKeepsLockSetDuringImport(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-RACE", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-RACE-W", AuthorID: author.ID, Title: "Artemis", SortTitle: "Artemis",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
		Genres: []string{}, Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	stale, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil || stale == nil {
		t.Fatalf("snapshot: %v", err)
	}

	// The user edits while the import holds its snapshot.
	fresh, _ := bookRepo.GetByID(ctx, book.ID)
	fresh.Narrator = "Rosario Dawson"
	fresh.LockField(models.BookFieldNarrator)
	if err := bookRepo.Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}

	item := sampleABSItem()
	item.ASIN = ""
	if err := importer.applyBookFields(ctx, stale, author.ID, item); err != nil {
		t.Fatal(err)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.Narrator != "Rosario Dawson" || !got.IsFieldLocked(models.BookFieldNarrator) {
		t.Fatalf("stale import write undid the user's lock: narrator=%q locked=%v", got.Narrator, got.LockedFields)
	}
	if got.DurationSeconds != 57600 {
		t.Fatalf("retry did not land the unlocked ABS fields: duration=%d", got.DurationSeconds)
	}
}

// An import write that never lands is a skip for that item, not a failed
// import: the next run retries it.
func TestImporter_ApplyBookFieldsSkipsWhenBookKeepsChanging(t *testing.T) {
	env := covImpNewEnv(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-SKIP", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := env.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-SKIP-W", AuthorID: author.ID, Title: "Artemis", SortTitle: "Artemis",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
		Genres: []string{}, Monitored: true,
	}
	if err := env.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	stale, err := env.books.GetByID(ctx, book.ID)
	if err != nil || stale == nil {
		t.Fatalf("snapshot: %v", err)
	}
	env.exec(t, `CREATE TRIGGER keep_changing BEFORE UPDATE ON books BEGIN SELECT RAISE(IGNORE); END`)

	if err := env.importer.applyBookFields(ctx, stale, author.ID, sampleABSItem()); err != nil {
		t.Fatalf("a lost write must be a skip, got error: %v", err)
	}
	got, _ := env.books.GetByID(ctx, book.ID)
	if got.Narrator != "" || got.DurationSeconds != 0 {
		t.Fatalf("fields landed although every write was swallowed: narrator=%q duration=%d", got.Narrator, got.DurationSeconds)
	}
}

// A book with no updated_at to compare against, such as one this run just
// created, is written directly.
func TestImporter_ApplyBookFieldsWritesDirectlyWithoutUpdatedAt(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-DIRECT", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-DIRECT-W", AuthorID: author.ID, Title: "Artemis", SortTitle: "Artemis",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
		Genres: []string{}, Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	book.UpdatedAtRaw = ""

	if err := importer.applyBookFields(ctx, book, author.ID, sampleABSItem()); err != nil {
		t.Fatal(err)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.DurationSeconds != 57600 {
		t.Fatalf("direct write did not land: duration=%d", got.DurationSeconds)
	}
}
