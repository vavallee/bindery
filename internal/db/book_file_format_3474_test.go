package db

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func fileFormat(t *testing.T, repo *BookRepo, bookID int64, path string) string {
	t.Helper()
	files, err := repo.ListFiles(context.Background(), bookID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return f.Format
		}
	}
	t.Fatalf("no book_files row for %s", path)
	return ""
}

// TestSetFilePath_AudioFileOnBothBookIsAudiobook is #3474: a "both" book fell
// back to ebook, so the library finder's audio hit was stored as an ebook file.
func TestSetFilePath_AudioFileOnBothBookIsAudiobook(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)
	if _, err := database.Exec("UPDATE books SET media_type = ? WHERE id = ?", models.MediaTypeBoth, book.ID); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"/audio/a.m4b", "/audio/B.MP3"} {
		if err := repo.SetFilePath(ctx, book.ID, p); err != nil {
			t.Fatal(err)
		}
		if got := fileFormat(t, repo, book.ID, p); got != models.MediaTypeAudiobook {
			t.Errorf("%s stored as %q, want audiobook", p, got)
		}
	}
	if err := repo.SetFilePath(ctx, book.ID, "/lib/a.epub"); err != nil {
		t.Fatal(err)
	}
	if got := fileFormat(t, repo, book.ID, "/lib/a.epub"); got != models.MediaTypeEbook {
		t.Errorf("epub stored as %q, want ebook", got)
	}
}

// An audio extension is audiobook whatever format the caller names, and an
// extensionless audiobook folder keeps the caller's format.
func TestTrack_AudioExtensionOverridesCallerFormat(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	repo := NewBookRepo(database)

	if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/x/a.flac"); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, "/x/folder"); err != nil {
		t.Fatal(err)
	}
	if got := fileFormat(t, repo, book.ID, "/x/a.flac"); got != models.MediaTypeAudiobook {
		t.Errorf("flac stored as %q", got)
	}
	if got := fileFormat(t, repo, book.ID, "/x/folder"); got != models.MediaTypeAudiobook {
		t.Errorf("folder stored as %q", got)
	}
}

// TestBackfillBookFileFormats_RepairsMislabelledRows covers rows already
// written wrong, gated on their own marker.
func TestBackfillBookFileFormats_RepairsMislabelledRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()
	database := openFileDB(t, path)
	author := &models.Author{ForeignID: "OL-BF-A", Name: "Bryan Davis", SortName: "Davis, Bryan"}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	book := &models.Book{ForeignID: "OL-BF-B", AuthorID: author.ID, Title: "Circles of Seven", Status: models.BookStatusWanted, MediaType: models.MediaTypeBoth}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct{ format, p string }{
		{"ebook", "/m/a.epub"}, {"ebook", "/m/Chapter 1.MP3"}, {"ebook", "/m/b.m4b"}, {"audiobook", "/m/dir"},
	} {
		if _, err := database.Exec(`INSERT INTO book_files (book_id, format, path) VALUES (?, ?, ?)`, book.ID, f.format, f.p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeyBookFileFmt); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	books = NewBookRepo(database)
	want := map[string]string{
		"/m/a.epub": "ebook", "/m/Chapter 1.MP3": "audiobook", "/m/b.m4b": "audiobook", "/m/dir": "audiobook",
	}
	for p, w := range want {
		if got := fileFormat(t, books, book.ID, p); got != w {
			t.Errorf("%s = %q, want %q", p, got, w)
		}
	}
	if v, ok := settingValue(t, database, backfillRevKeyBookFileFmt); !ok || v != strconv.Itoa(bookFileFormatRev) {
		t.Errorf("marker = %q (present %v)", v, ok)
	}

	// A current marker means a later open does not touch rows again.
	if _, err := database.Exec(`UPDATE book_files SET format='ebook' WHERE path='/m/b.m4b'`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	database = openFileDB(t, path)
	defer database.Close()
	if got := fileFormat(t, NewBookRepo(database), book.ID, "/m/b.m4b"); got != "ebook" {
		t.Errorf("backfill re-ran despite a current marker")
	}
}
