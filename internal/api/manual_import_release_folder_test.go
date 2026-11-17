package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// TestManualImportScan_ReleaseFolderNames runs the Import page's "From a
// folder" scan, with the real scanner behind it, over an import source folder
// of release folders. The audiobook folder parsed as the author "H G", so its
// metadata search found nothing, and the ebook's release folder came back
// whole as its author.
func TestManualImportScan_ReleaseFolderNames(t *testing.T) {
	t.Parallel()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	downloads := db.NewDownloadRepo(database)
	author := &models.Author{ForeignID: "OL-WELLS-A", Name: "H. G. Wells", SortName: "Wells, H. G."}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-WELLS-W", AuthorID: author.ID, Title: "The War of the Worlds", Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	library := resolvedTempDir(t)
	src := resolvedTempDir(t)
	audioDir := filepath.Join(src, "The War of the Worlds - H. G. Wells [m4b]")
	writeTestFile(t, filepath.Join(audioDir, "The War of the Worlds.m4b"))
	writeTestFile(t, filepath.Join(src, "TEST-bare [epub]", "TEST-bare.epub"))

	scanner := importer.NewScanner(downloads, db.NewDownloadClientRepo(database), books, authors, db.NewHistoryRepo(database),
		library, library, "", "", "")
	h := NewManualImportHandler(scanner, downloads, books)

	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(src))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var resp ScanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byName := map[string]ScanItem{}
	for _, it := range resp.Items {
		byName[it.Name] = it
	}

	audio, ok := byName["The War of the Worlds - H. G. Wells [m4b]"]
	if !ok {
		t.Fatalf("no unit for the audiobook folder; items = %+v", resp.Items)
	}
	if audio.ParsedTitle != "The War of the Worlds" || audio.ParsedAuthor != "H. G. Wells" {
		t.Errorf("audiobook folder parsed %q / %q, want %q / %q", audio.ParsedTitle, audio.ParsedAuthor, "The War of the Worlds", "H. G. Wells")
	}
	if audio.Match != "confident" || audio.Book == nil || audio.Book.ID != book.ID {
		t.Errorf("audiobook folder match %q book %+v, want confident on book %d", audio.Match, audio.Book, book.ID)
	}

	bare, ok := byName["TEST-bare.epub"]
	if !ok {
		t.Fatalf("no unit for the bare ebook; items = %+v", resp.Items)
	}
	if bare.ParsedTitle != "TEST-bare" || bare.ParsedAuthor != "" {
		t.Errorf("bare release parsed %q / %q, want %q and no author", bare.ParsedTitle, bare.ParsedAuthor, "TEST-bare")
	}
}
