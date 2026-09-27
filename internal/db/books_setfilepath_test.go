package db

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// SetFilePath used to fall back to a bare UPDATE on any GetByID failure,
// which hid the load error, skipped book_files and refreshBookStatus, and
// reported success for a book that does not exist (#2819).
func TestSetFilePathReportsLoadErrors(t *testing.T) {
	ctx := context.Background()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	author := &models.Author{ForeignID: "OL-2819-A", Name: "Load Error Author", SortName: "Author, Load Error", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-2819-W", AuthorID: author.ID, Title: "Load Error Book", SortTitle: "Load Error Book", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, MediaType: models.MediaTypeEbook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	t.Run("unreadable book", func(t *testing.T) {
		// An unscannable calibre_id makes GetByID fail on a row that exists.
		if _, err := database.ExecContext(ctx, "UPDATE books SET calibre_id='corrupt' WHERE id=?", book.ID); err != nil {
			t.Fatal(err)
		}
		_, loadErr := books.GetByID(ctx, book.ID)
		if loadErr == nil {
			t.Fatal("precondition: GetByID should fail on the corrupt row")
		}

		err := books.SetFilePath(ctx, book.ID, "/books/unreadable.epub")
		if err == nil {
			t.Error("SetFilePath returned nil although the book could not be loaded")
		} else if !strings.Contains(err.Error(), loadErr.Error()) {
			t.Errorf("SetFilePath error %q does not carry the load error %q", err, loadErr)
		}

		var status, filePath string
		if err := database.QueryRowContext(ctx, "SELECT status, COALESCE(file_path, '') FROM books WHERE id=?", book.ID).Scan(&status, &filePath); err != nil {
			t.Fatal(err)
		}
		if status != models.BookStatusWanted || filePath != "" {
			t.Errorf("row was written behind book_files: status=%q file_path=%q, want %q and empty", status, filePath, models.BookStatusWanted)
		}
	})

	t.Run("missing book", func(t *testing.T) {
		const missingID = 999999
		err := books.SetFilePath(ctx, missingID, "/books/missing.epub")
		if err == nil {
			t.Fatal("SetFilePath returned nil for a book that does not exist")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error %q should say the book was not found", err)
		}
	})
}
