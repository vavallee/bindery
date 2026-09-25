package db

import (
	"context"
	"strings"
	"testing"

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
