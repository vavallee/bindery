package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestEditionRepo_GetByID(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-GET-ED-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-GET-ED-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)
	ed := &models.Edition{
		ForeignID: "calibre:42:EPUB", BookID: b.ID, Title: "Named Edition",
		Format: "EPUB", Language: "eng", IsEbook: true, Monitored: true,
	}
	if err := repo.Upsert(ctx, ed); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetByID(ctx, ed.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected edition, got nil")
	}
	if got.ID != ed.ID || got.BookID != b.ID || got.Title != "Named Edition" || got.Format != "EPUB" {
		t.Fatalf("unexpected edition: %+v", got)
	}

	missing, err := repo.GetByID(ctx, ed.ID+1000)
	if err != nil {
		t.Fatalf("GetByID missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for missing edition, got %+v", missing)
	}
}

func TestEditionRepo_UpsertInsertAndUpdate(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-ED-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-ED-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)

	isbn13 := "9780000000001"
	ed := &models.Edition{
		ForeignID: "calibre:1:EPUB",
		BookID:    b.ID,
		Title:     "First Edition",
		ISBN13:    &isbn13,
		Format:    "EPUB",
		Language:  "eng",
		ImageURL:  "http://img/1.jpg",
		IsEbook:   true,
		Monitored: true,
	}
	if err := repo.Upsert(ctx, ed); err != nil {
		t.Fatalf("insert upsert: %v", err)
	}
	if ed.ID == 0 {
		t.Fatal("expected non-zero ID after insert")
	}
	firstID := ed.ID

	got, err := repo.GetByForeignID(ctx, "calibre:1:EPUB")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if got == nil {
		t.Fatal("expected edition, got nil")
		return
	}
	if got.Title != "First Edition" || !got.IsEbook || !got.Monitored {
		t.Errorf("unexpected round-trip: %+v", got)
	}
	if got.ISBN13 == nil || *got.ISBN13 != isbn13 {
		t.Errorf("ISBN13 mismatch: %v", got.ISBN13)
	}

	// Second upsert updates the row in place; ImageURL empty should preserve
	// the original via COALESCE(NULLIF(...)), ISBN13 omitted should survive
	// via COALESCE.
	ed2 := &models.Edition{
		ForeignID: "calibre:1:EPUB",
		BookID:    b.ID,
		Title:     "Updated Edition",
		Format:    "MOBI",
		Language:  "fra",
		ImageURL:  "",
		IsEbook:   true,
	}
	if err := repo.Upsert(ctx, ed2); err != nil {
		t.Fatalf("update upsert: %v", err)
	}
	if ed2.ID != firstID {
		t.Errorf("upsert must preserve id: want %d, got %d", firstID, ed2.ID)
	}

	got, _ = repo.GetByForeignID(ctx, "calibre:1:EPUB")
	if got.Title != "Updated Edition" {
		t.Errorf("title not updated: %q", got.Title)
	}
	if got.Format != "MOBI" {
		t.Errorf("format not updated: %q", got.Format)
	}
	if got.ImageURL != "http://img/1.jpg" {
		t.Errorf("image_url should be preserved via COALESCE(NULLIF), got %q", got.ImageURL)
	}
	if got.ISBN13 == nil || *got.ISBN13 != isbn13 {
		t.Errorf("ISBN13 should survive update, got %v", got.ISBN13)
	}
}

func TestEditionRepo_UpsertMetadataFillsMissingFields(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-META-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "hc:meta-book", AuthorID: a.ID, Title: "Book", SortTitle: "Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "hardcover", Monitored: true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)
	asin := "B000000001"
	pages := 400
	ok, err := repo.UpsertMetadata(ctx, &models.Edition{
		ForeignID: "hc:ed1",
		BookID:    b.ID,
		Title:     "Hardcover Edition",
		ASIN:      &asin,
		Publisher: "Tor",
		Format:    "Audiobook",
		NumPages:  &pages,
		Language:  "eng",
		ImageURL:  "https://img/ed1.jpg",
		Monitored: true,
	}, b.ForeignID, b.MetadataProvider)
	if err != nil {
		t.Fatalf("metadata upsert: %v", err)
	}
	if !ok {
		t.Fatal("metadata upsert skipped unexpectedly")
	}

	got, err := repo.GetByForeignID(ctx, "hc:ed1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected edition")
		return
	}
	if got.ASIN == nil || *got.ASIN != asin || got.Publisher != "Tor" || got.NumPages == nil || *got.NumPages != pages || got.ImageURL == "" {
		t.Fatalf("metadata fields were not inserted: %+v", got)
	}

	isbn13 := "9780000000001"
	replacementASIN := "B999999999"
	incoming := &models.Edition{
		ForeignID: "hc:ed1",
		BookID:    b.ID,
		Title:     "Updated Title",
		ISBN13:    &isbn13,
		ASIN:      &replacementASIN,
		Publisher: "Should Not Replace",
		Format:    "Kindle",
		Language:  "ger",
		ImageURL:  "https://img/new.jpg",
		Monitored: true,
	}
	ok, err = repo.UpsertMetadata(ctx, incoming, b.ForeignID, b.MetadataProvider)
	if err != nil {
		t.Fatalf("metadata conflict upsert: %v", err)
	}
	if !ok {
		t.Fatal("metadata conflict upsert skipped unexpectedly")
	}
	if incoming.ISBN13 == nil || *incoming.ISBN13 != isbn13 {
		t.Fatalf("incoming edition was not hydrated with filled ISBN13: %+v", incoming)
	}
	if incoming.ASIN == nil || *incoming.ASIN != asin || incoming.Format != "Audiobook" || incoming.ImageURL != "https://img/ed1.jpg" {
		t.Fatalf("incoming edition was not hydrated with preserved stored fields: %+v", incoming)
	}
	got, err = repo.GetByForeignID(ctx, "hc:ed1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ISBN13 == nil || *got.ISBN13 != isbn13 {
		t.Fatalf("missing ISBN13 was not filled: %+v", got)
	}
	if got.Title != "Hardcover Edition" || got.ASIN == nil || *got.ASIN != asin || got.Publisher != "Tor" || got.Format != "Audiobook" || got.Language != "eng" || got.ImageURL != "https://img/ed1.jpg" {
		t.Fatalf("existing non-empty metadata was overwritten: %+v", got)
	}
}

func TestEditionRepo_UpsertMetadataNormalizesBlankText(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-ASIN-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "hc:asin-book", AuthorID: author.ID, Title: "Book", SortTitle: "Book", Status: "wanted", Genres: []string{}, MetadataProvider: "hardcover", Monitored: true}
	if err := NewBookRepo(database).Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)
	textEdition := func(foreignID, value string) *models.Edition {
		return &models.Edition{
			ForeignID: foreignID, BookID: book.ID, Title: value,
			ISBN13: &value, ISBN10: &value, ASIN: &value,
			Publisher: value, Format: value, Language: value, ImageURL: value, EditionInfo: value,
		}
	}
	assertText := func(t *testing.T, got *models.Edition, want string) {
		t.Helper()
		wantTitle := want
		if want == "" {
			wantTitle = "Unknown Edition"
		}
		if got.Title != wantTitle {
			t.Errorf("title = %q, want %q", got.Title, wantTitle)
		}
		for name, value := range map[string]string{"publisher": got.Publisher, "format": got.Format, "language": got.Language, "image_url": got.ImageURL, "edition_info": got.EditionInfo} {
			if value != want {
				t.Errorf("%s = %q, want %q", name, value, want)
			}
		}
		for name, value := range map[string]*string{"isbn_13": got.ISBN13, "isbn_10": got.ISBN10, "asin": got.ASIN} {
			if want == "" {
				if value != nil {
					t.Errorf("%s = %q, want nil", name, *value)
				}
			} else if value == nil || *value != want {
				t.Errorf("%s = %v, want %q", name, value, want)
			}
		}
	}
	for _, tc := range []struct {
		name, stored string
		want         string
	}{
		{name: "empty", stored: "", want: "replacement"},
		{name: "ASCII whitespace", stored: " \t\n\v\f\r", want: "replacement"},
		{name: "Unicode whitespace", stored: "\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000", want: "replacement"},
		{name: "meaningful stored text", stored: " \u2003curated\t ", want: " \u2003curated\t "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edition := textEdition("hc:text-"+tc.name, tc.stored)
			if ok, err := repo.UpsertMetadata(ctx, edition, book.ForeignID, book.MetadataProvider); err != nil || !ok {
				t.Fatalf("insert: ok=%v err=%v", ok, err)
			}
			assertText(t, edition, strings.TrimSpace(tc.stored))
			// Simulate legacy blank fields and curated text whose spacing must survive.
			if _, err := database.ExecContext(ctx, `UPDATE editions SET title=?1, isbn_13=?1, isbn_10=?1, asin=?1,
				publisher=?1, format=?1, language=?1, image_url=?1, edition_info=?1 WHERE id=?2`, tc.stored, edition.ID); err != nil {
				t.Fatal(err)
			}
			incoming := textEdition(edition.ForeignID, " \u2003replacement\t ")
			if ok, err := repo.UpsertMetadata(ctx, incoming, book.ForeignID, book.MetadataProvider); err != nil || !ok {
				t.Fatalf("update: ok=%v err=%v", ok, err)
			}
			assertText(t, incoming, tc.want)
			stored, err := repo.GetByForeignID(ctx, edition.ForeignID)
			if err != nil {
				t.Fatal(err)
			}
			if stored == nil {
				t.Fatal("edition not persisted")
			}
			assertText(t, stored, tc.want)
		})
	}
}

func TestEditionRepo_UpsertMetadataSkipsDifferentBook(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-META-SKIP-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	first := &models.Book{ForeignID: "hc:first", AuthorID: a.ID, Title: "First", SortTitle: "First", Status: "wanted", Genres: []string{}, MetadataProvider: "hardcover", Monitored: true}
	second := &models.Book{ForeignID: "hc:second", AuthorID: a.ID, Title: "Second", SortTitle: "Second", Status: "wanted", Genres: []string{}, MetadataProvider: "hardcover", Monitored: true}
	if err := bookRepo.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.Create(ctx, second); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)
	if ok, err := repo.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:shared-ed", BookID: first.ID, Title: "First Edition"}, first.ForeignID, first.MetadataProvider); err != nil || !ok {
		t.Fatalf("seed metadata edition ok=%v err=%v", ok, err)
	}
	ok, err := repo.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:shared-ed", BookID: second.ID, Title: "Second Edition"}, second.ForeignID, second.MetadataProvider)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected conflicting edition to be skipped")
	}
	got, err := repo.GetByForeignID(ctx, "hc:shared-ed")
	if err != nil {
		t.Fatal(err)
	}
	if got.BookID != first.ID || got.Title != "First Edition" {
		t.Fatalf("edition moved or changed unexpectedly: %+v", got)
	}
}

func TestEditionRepo_UpsertMetadataSkipsReboundParent(t *testing.T) {
	for _, changeProvider := range []bool{false, true} {
		name := "foreign ID"
		if changeProvider {
			name = "provider"
		}
		t.Run(name, func(t *testing.T) {
			database, err := OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			ctx := context.Background()
			books := NewBookRepo(database)
			author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-GUARD-A")
			book := mkBook(t, books, ctx, author.ID, "hc:original", "Book", models.BookStatusWanted)
			before := *book
			repo := NewEditionRepo(database)
			seed := &models.Edition{ForeignID: "hc:existing", BookID: book.ID, Title: "Existing"}
			if ok, err := repo.UpsertMetadata(ctx, seed, before.ForeignID, before.MetadataProvider); err != nil || !ok {
				t.Fatalf("seed: ok=%v err=%v", ok, err)
			}
			if changeProvider {
				book.MetadataProvider = "other-provider"
			} else {
				book.ForeignID = "hc:rebound"
			}
			if err := books.Update(ctx, book); err != nil {
				t.Fatal(err)
			}
			isbn := "9781234567890"
			for _, foreignID := range []string{"hc:new", "hc:existing"} {
				incoming := &models.Edition{ForeignID: foreignID, BookID: book.ID, Title: "Fetched", ISBN13: &isbn}
				if ok, err := repo.UpsertMetadata(ctx, incoming, before.ForeignID, before.MetadataProvider); err != nil || ok {
					t.Fatalf("stale %s write: ok=%v err=%v", foreignID, ok, err)
				}
			}
			attached, err := repo.ListByBook(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(attached) != 1 || attached[0].ID != seed.ID || attached[0].ISBN13 != nil || !attached[0].UpdatedAt.Equal(seed.UpdatedAt) {
				t.Fatalf("stale insert/update changed editions: %+v", attached)
			}
			incoming := &models.Edition{ForeignID: seed.ForeignID, BookID: book.ID, ISBN13: &isbn}
			if ok, err := repo.UpsertMetadata(ctx, incoming, book.ForeignID, book.MetadataProvider); err != nil || !ok || incoming.ISBN13 == nil || *incoming.ISBN13 != isbn {
				t.Fatalf("current identity write: ok=%v err=%v edition=%+v", ok, err, incoming)
			}
		})
	}
}

func TestEditionRepo_GetByForeignIDNotFound(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewEditionRepo(database)

	got, err := repo.GetByForeignID(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil edition, got %+v", got)
	}
}

func TestEditionRepo_ListByBook(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-LB-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-LB-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)

	list, err := repo.ListByBook(ctx, b.ID)
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("want 0, got %d", len(list))
	}

	for _, fid := range []string{"calibre:2:EPUB", "calibre:2:PDF"} {
		if err := repo.Upsert(ctx, &models.Edition{
			ForeignID: fid, BookID: b.ID, Title: "t", Format: "x", Language: "eng", IsEbook: true,
		}); err != nil {
			t.Fatalf("upsert %s: %v", fid, err)
		}
	}

	list, err = repo.ListByBook(ctx, b.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("want 2 editions, got %d", len(list))
	}

	other, err := repo.ListByBook(ctx, 99999)
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("unrelated book should have 0 editions, got %d", len(other))
	}
}

// TestEditionRepo_UpdateFieldsWritesLocksAndRefusesOtherBook pins the user
// edit path: UpdateFields writes the five publication columns and the lock set,
// scopes the write to the edition's book, and a row the importer created with
// no lock set scans as an empty set.
func TestEditionRepo_UpdateFieldsWritesLocksAndRefusesOtherBook(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-UF-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "OL-UF-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	other := &models.Book{ForeignID: "OL-UF-C", AuthorID: a.ID, Title: "Other", SortTitle: "Other", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	if err := bookRepo.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)
	ed := &models.Edition{ForeignID: "abs:edition:lib:item:audiobook", BookID: b.ID, Title: "Recording", Format: "AUDIOBOOK", Monitored: true}
	if err := repo.Upsert(ctx, ed); err != nil {
		t.Fatal(err)
	}

	// A row the importer wrote has no lock set; it must scan as empty, never nil-panic.
	stored, err := repo.GetByID(ctx, ed.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	if len(stored.LockedFields) != 0 {
		t.Fatalf("fresh edition locked=%v, want none", stored.LockedFields)
	}

	isbn13 := "9780306406157"
	when := time.Date(2024, 6, 18, 0, 0, 0, 0, time.UTC)
	stored.Publisher = "Example Audio"
	stored.PublishDate = &when
	stored.ISBN13 = &isbn13
	stored.LockField(models.EditionFieldPublisher)
	stored.LockField(models.EditionFieldPublishDate)
	stored.LockField(models.EditionFieldISBN13)
	updated, err := repo.UpdateFields(ctx, stored, b.ID)
	if err != nil {
		t.Fatalf("UpdateFields: %v", err)
	}
	if !updated {
		t.Fatal("UpdateFields reported no row for the edition's own book")
	}

	for _, got := range mustEditionsByEveryReader(t, repo, ctx, ed.ID, ed.ForeignID, b.ID) {
		if got.Publisher != "Example Audio" || got.ISBN13 == nil || *got.ISBN13 != isbn13 || got.PublishDate == nil || !got.PublishDate.Equal(when) {
			t.Fatalf("fields not persisted: %+v", got)
		}
		if !got.IsFieldLocked(models.EditionFieldPublisher) || !got.IsFieldLocked(models.EditionFieldPublishDate) || !got.IsFieldLocked(models.EditionFieldISBN13) || got.IsFieldLocked(models.EditionFieldASIN) {
			t.Fatalf("lock set not persisted: %v", got.LockedFields)
		}
	}

	// Scoped to the book the caller names, not the one on the struct: a
	// handler that resolved the edition under the URL's book cannot write a
	// row that belongs to another book, whatever the struct says.
	stolen, _ := repo.GetByID(ctx, ed.ID)
	stolen.Publisher = "Thief"
	updated, err = repo.UpdateFields(ctx, stolen, other.ID)
	if err != nil {
		t.Fatalf("UpdateFields other book: %v", err)
	}
	if updated {
		t.Fatal("UpdateFields matched a row under a different book_id")
	}
	again, _ := repo.GetByID(ctx, ed.ID)
	if again.Publisher != "Example Audio" || again.BookID != b.ID {
		t.Fatalf("edition changed through the wrong book: %+v", again)
	}

	// Compare-and-swap on updated_at: a snapshot older than the row loses,
	// so a concurrent import or a second user edit is not reverted.
	stale, _ := repo.GetByID(ctx, ed.ID)
	fresh, _ := repo.GetByID(ctx, ed.ID)
	fresh.Publisher = "Fresh Publisher"
	if ok, err := repo.UpdateFields(ctx, fresh, b.ID); err != nil || !ok {
		t.Fatalf("fresh write: ok=%v err=%v", ok, err)
	}
	stale.Publisher = "Stale Publisher"
	ok, err := repo.UpdateFields(ctx, stale, b.ID)
	if err != nil {
		t.Fatalf("stale write: %v", err)
	}
	if ok {
		t.Fatal("stale snapshot overwrote a newer row")
	}
	final, _ := repo.GetByID(ctx, ed.ID)
	if final.Publisher != "Fresh Publisher" {
		t.Fatalf("publisher = %q, want the fresh write to stand", final.Publisher)
	}
	if final.UpdatedAtRaw == "" || final.UpdatedAtRaw == stale.UpdatedAtRaw {
		t.Fatalf("UpdatedAtRaw not scanned or not advanced: %q vs %q", final.UpdatedAtRaw, stale.UpdatedAtRaw)
	}
}

// mustEditionsByEveryReader returns the same edition through the three
// readers a handler uses (GetByID, GetByForeignID, ListByBook), so a column
// added to one SELECT list and missed in another fails here.
func mustEditionsByEveryReader(t *testing.T, repo *EditionRepo, ctx context.Context, id int64, foreignID string, bookID int64) []models.Edition {
	t.Helper()
	byID, err := repo.GetByID(ctx, id)
	if err != nil || byID == nil {
		t.Fatalf("GetByID: %v", err)
	}
	byForeign, err := repo.GetByForeignID(ctx, foreignID)
	if err != nil || byForeign == nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	list, err := repo.ListByBook(ctx, bookID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByBook: %v (%d rows)", err, len(list))
	}
	return []models.Edition{*byID, *byForeign, list[0]}
}

// seedLockedEditionBook creates an author and book for the upsert lock tests.
func seedLockedEditionBook(t *testing.T, database *sql.DB, foreignID, provider string) *models.Book {
	t.Helper()
	ctx := context.Background()
	a := &models.Author{ForeignID: foreignID + "-A", Name: "A", SortName: "A", MetadataProvider: provider, Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: foreignID, AuthorID: a.ID, Title: "Book", SortTitle: "Book", Status: "wanted", Genres: []string{}, MetadataProvider: provider, Monitored: true}
	if err := NewBookRepo(database).Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEditionRepo_UpsertKeepsLockedColumns pins that the import upsert keeps a
// locked isbn_13 and publish_date on conflict, writes the unlocked columns it
// always wrote, and still inserts a brand new row.
func TestEditionRepo_UpsertKeepsLockedColumns(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	b := seedLockedEditionBook(t, database, "abs:book:lib:lock", "audiobookshelf")
	repo := NewEditionRepo(database)

	userISBN := "9780306406157"
	userDate := time.Date(1998, 1, 1, 0, 0, 0, 0, time.UTC)
	ed := &models.Edition{ForeignID: "abs:edition:lib:lock:audiobook", BookID: b.ID, Title: "Recording", ISBN13: &userISBN, PublishDate: &userDate, Format: "AUDIOBOOK", Language: "eng", Monitored: true}
	if err := repo.Upsert(ctx, ed); err != nil {
		t.Fatalf("insert: %v", err)
	}
	stored, _ := repo.GetByID(ctx, ed.ID)
	stored.LockField(models.EditionFieldISBN13)
	stored.LockField(models.EditionFieldPublishDate)
	if _, err := repo.UpdateFields(ctx, stored, b.ID); err != nil {
		t.Fatal(err)
	}

	absISBN := "9790000000001"
	absDate := time.Date(2024, 6, 18, 0, 0, 0, 0, time.UTC)
	reimport := &models.Edition{ForeignID: ed.ForeignID, BookID: b.ID, Title: "Recording (Unabridged)", ISBN13: &absISBN, PublishDate: &absDate, Format: "AUDIOBOOK", Language: "ger", Monitored: true}
	if err := repo.Upsert(ctx, reimport); err != nil {
		t.Fatalf("re-import upsert: %v", err)
	}
	got, _ := repo.GetByID(ctx, ed.ID)
	if got.ISBN13 == nil || *got.ISBN13 != userISBN {
		t.Fatalf("locked isbn_13 overwritten: %v", got.ISBN13)
	}
	if got.PublishDate == nil || !got.PublishDate.Equal(userDate) {
		t.Fatalf("locked publish_date overwritten: %v", got.PublishDate)
	}
	if got.Title != "Recording (Unabridged)" || got.Language != "ger" {
		t.Fatalf("unlocked columns must still follow the import: %+v", got)
	}
	if !got.IsFieldLocked(models.EditionFieldISBN13) || !got.IsFieldLocked(models.EditionFieldPublishDate) {
		t.Fatalf("re-import dropped the lock set: %v", got.LockedFields)
	}
}

// TestEditionRepo_UpsertMetadataLockBlocksFill pins the #2757 rule on
// editions: a locked, empty publisher, ASIN or publish date is a decision, not
// a gap, so provider hydration must not fill it, while an unlocked empty
// ISBN-10 is filled. publish_date has its own hand-written guard in the
// statement, so it is locked here on purpose.
func TestEditionRepo_UpsertMetadataLockBlocksFill(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	b := seedLockedEditionBook(t, database, "hc:lock-book", "hardcover")
	repo := NewEditionRepo(database)

	ok, err := repo.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:lock-ed", BookID: b.ID, Title: "Audio", Format: "Audiobook", Monitored: true}, b.ForeignID, b.MetadataProvider)
	if err != nil || !ok {
		t.Fatalf("first metadata upsert: ok=%v err=%v", ok, err)
	}
	stored, _ := repo.GetByForeignID(ctx, "hc:lock-ed")
	stored.Publisher = ""
	stored.LockField(models.EditionFieldPublisher)
	stored.LockField(models.EditionFieldASIN)
	stored.LockField(models.EditionFieldPublishDate)
	stored.LockField(models.EditionFieldISBN13)
	if _, err := repo.UpdateFields(ctx, stored, b.ID); err != nil {
		t.Fatal(err)
	}

	asin := "B000000001"
	isbn13 := "9780306406157"
	isbn10 := "0306406152"
	date := time.Date(2020, 2, 2, 0, 0, 0, 0, time.UTC)
	ok, err = repo.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:lock-ed", BookID: b.ID, Title: "Audio", Publisher: "Tor", ASIN: &asin, ISBN13: &isbn13, ISBN10: &isbn10, PublishDate: &date, Format: "Audiobook", Monitored: true}, b.ForeignID, b.MetadataProvider)
	if err != nil || !ok {
		t.Fatalf("second metadata upsert: ok=%v err=%v", ok, err)
	}
	got, _ := repo.GetByForeignID(ctx, "hc:lock-ed")
	if got.Publisher != "" {
		t.Fatalf("locked empty publisher was filled: %q", got.Publisher)
	}
	if got.ASIN != nil {
		t.Fatalf("locked empty asin was filled: %v", *got.ASIN)
	}
	if got.PublishDate != nil {
		t.Fatalf("locked empty publish_date was filled: %v", got.PublishDate)
	}
	if got.ISBN13 != nil {
		t.Fatalf("locked empty isbn_13 was filled: %v", *got.ISBN13)
	}
	if got.ISBN10 == nil || *got.ISBN10 != isbn10 {
		t.Fatalf("unlocked isbn_10 should be filled: %v", got.ISBN10)
	}

	// The struct UpsertMetadata hands back is the re-read row, lock set
	// included, so a caller that goes on to CanWrite sees the real locks.
	hydrated := &models.Edition{ForeignID: "hc:lock-ed", BookID: b.ID, Title: "Audio", Format: "Audiobook", Monitored: true}
	if ok, err := repo.UpsertMetadata(ctx, hydrated, b.ForeignID, b.MetadataProvider); err != nil || !ok {
		t.Fatalf("fourth metadata upsert: ok=%v err=%v", ok, err)
	}
	for _, f := range []string{models.EditionFieldPublisher, models.EditionFieldASIN, models.EditionFieldPublishDate, models.EditionFieldISBN13} {
		if !hydrated.IsFieldLocked(f) {
			t.Fatalf("UpsertMetadata's re-read dropped lock %q: %v", f, hydrated.LockedFields)
		}
	}
	if hydrated.UpdatedAtRaw == "" {
		t.Fatal("UpsertMetadata's re-read carries no UpdatedAtRaw")
	}
}

// TestEditionRepo_UpsertMetadataLockGuardsISBNs pins the isbn_13 and isbn_10
// guards in UpsertMetadata: a locked, empty ISBN is not filled by hydration,
// while an unlocked empty publisher still is.
func TestEditionRepo_UpsertMetadataLockGuardsISBNs(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	b := seedLockedEditionBook(t, database, "hc:isbn-lock-book", "hardcover")
	repo := NewEditionRepo(database)

	ok, err := repo.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:isbn-lock-ed", BookID: b.ID, Title: "Print", Format: "Hardcover", Monitored: true}, b.ForeignID, b.MetadataProvider)
	if err != nil || !ok {
		t.Fatalf("first metadata upsert: ok=%v err=%v", ok, err)
	}
	stored, _ := repo.GetByForeignID(ctx, "hc:isbn-lock-ed")
	stored.LockField(models.EditionFieldISBN13)
	stored.LockField(models.EditionFieldISBN10)
	if _, err := repo.UpdateFields(ctx, stored, b.ID); err != nil {
		t.Fatal(err)
	}

	isbn13, isbn10 := "9780306406157", "0306406152"
	ok, err = repo.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:isbn-lock-ed", BookID: b.ID, Title: "Print", Publisher: "Tor", ISBN13: &isbn13, ISBN10: &isbn10, Format: "Hardcover", Monitored: true}, b.ForeignID, b.MetadataProvider)
	if err != nil || !ok {
		t.Fatalf("second metadata upsert: ok=%v err=%v", ok, err)
	}
	got, _ := repo.GetByForeignID(ctx, "hc:isbn-lock-ed")
	if got.ISBN13 != nil || got.ISBN10 != nil {
		t.Fatalf("locked empty ISBNs were filled: isbn13=%v isbn10=%v", got.ISBN13, got.ISBN10)
	}
	if got.Publisher != "Tor" {
		t.Fatalf("unlocked publisher should be filled: %q", got.Publisher)
	}
}

// TestEditionRepo_UpdateFieldsKeepsConcurrentFill pins that a stale user
// edit cannot undo a fill: hydration filling another column between the
// handler's read and its write moves updated_at, so the write is refused
// and the handler re-reads before answering 409.
func TestEditionRepo_UpdateFieldsKeepsConcurrentFill(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	b := seedLockedEditionBook(t, database, "hc:stale-book", "hardcover")
	repo := NewEditionRepo(database)

	ok, err := repo.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:stale-ed", BookID: b.ID, Title: "Print", Format: "Hardcover", Monitored: true}, b.ForeignID, b.MetadataProvider)
	if err != nil || !ok {
		t.Fatalf("seed: ok=%v err=%v", ok, err)
	}
	stale, _ := repo.GetByForeignID(ctx, "hc:stale-ed")

	isbn10 := "0306406152"
	ok, err = repo.UpsertMetadata(ctx, &models.Edition{ForeignID: "hc:stale-ed", BookID: b.ID, Title: "Print", Publisher: "Tor", ISBN10: &isbn10, Format: "Hardcover", Monitored: true}, b.ForeignID, b.MetadataProvider)
	if err != nil || !ok {
		t.Fatalf("concurrent fill: ok=%v err=%v", ok, err)
	}

	isbn13 := "9780306406157"
	stale.ISBN13 = &isbn13
	stale.LockField(models.EditionFieldISBN13)
	if updated, err := repo.UpdateFields(ctx, stale, b.ID); err != nil || updated {
		t.Fatalf("stale UpdateFields: updated=%v err=%v, want refused", updated, err)
	}

	got, _ := repo.GetByForeignID(ctx, "hc:stale-ed")
	if got.ISBN13 != nil || got.IsFieldLocked(models.EditionFieldISBN13) {
		t.Fatalf("stale edit landed: isbn13=%v locked=%v", got.ISBN13, got.LockedFields)
	}
	if got.Publisher != "Tor" || got.ISBN10 == nil || *got.ISBN10 != isbn10 {
		t.Fatalf("stale copy undid the concurrent fill: publisher=%q isbn10=%v", got.Publisher, got.ISBN10)
	}

	// A fresh read carries the new updated_at and the same edit goes through.
	fresh, _ := repo.GetByForeignID(ctx, "hc:stale-ed")
	fresh.ISBN13 = &isbn13
	fresh.LockField(models.EditionFieldISBN13)
	if updated, err := repo.UpdateFields(ctx, fresh, b.ID); err != nil || !updated {
		t.Fatalf("fresh UpdateFields: updated=%v err=%v", updated, err)
	}
	got, _ = repo.GetByForeignID(ctx, "hc:stale-ed")
	if got.ISBN13 == nil || *got.ISBN13 != isbn13 || !got.IsFieldLocked(models.EditionFieldISBN13) || got.Publisher != "Tor" {
		t.Fatalf("fresh edit: isbn13=%v locked=%v publisher=%q", got.ISBN13, got.LockedFields, got.Publisher)
	}
}

// TestEditionRepo_DeleteProviderEditionsAfterRebindKeepsLockedEditions pins
// that a re-bind drops the old record's provider editions except one the user
// edited: a lock on any column keeps the row, the way a selected edition or
// one with a download does.
func TestEditionRepo_DeleteProviderEditionsAfterRebindKeepsLockedEditions(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	b := seedLockedEditionBook(t, database, "hc:rebind-book", "hardcover")
	repo := NewEditionRepo(database)
	for _, fid := range []string{"hc:rebind-locked", "hc:rebind-plain"} {
		if err := repo.Upsert(ctx, &models.Edition{ForeignID: fid, BookID: b.ID, Title: "Print", Format: "Hardcover", Monitored: true}); err != nil {
			t.Fatal(err)
		}
	}
	locked, _ := repo.GetByForeignID(ctx, "hc:rebind-locked")
	locked.Publisher = "Mine"
	locked.LockField(models.EditionFieldPublisher)
	if ok, err := repo.UpdateFields(ctx, locked, b.ID); err != nil || !ok {
		t.Fatalf("lock: ok=%v err=%v", ok, err)
	}

	n, err := repo.DeleteProviderEditionsAfterRebind(ctx, b.ID, b.ForeignID, b.MetadataProvider)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d editions, want 1 (the unlocked one)", n)
	}
	if kept, _ := repo.GetByForeignID(ctx, "hc:rebind-locked"); kept == nil || kept.Publisher != "Mine" || !kept.IsFieldLocked(models.EditionFieldPublisher) {
		t.Fatalf("locked edition lost on re-bind: %+v", kept)
	}
	if gone, _ := repo.GetByForeignID(ctx, "hc:rebind-plain"); gone != nil {
		t.Fatal("unlocked provider edition survived the re-bind")
	}
}
