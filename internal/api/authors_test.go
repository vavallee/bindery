package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestDeleteAuthor_WithDeleteFiles verifies the ?deleteFiles=true branch
// sweeps every book's on-disk path. The invariant under test is ordering:
// paths must be collected from `books ListByAuthor` *before* the cascade
// wipes the book rows, otherwise we'd have nothing to sweep. A regression
// here re-orphans files (issue #15).
func TestDeleteAuthor_WithDeleteFiles(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL900A", Name: "Jared Diamond", SortName: "Diamond, Jared",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Two audiobook folders + one ebook file, all populated.
	root := t.TempDir()
	mkFolder := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "part1.m4b"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	path1 := mkFolder("Guns Germs and Steel (1997)")
	path2 := mkFolder("Collapse (2005)")
	path3 := filepath.Join(root, "The World Until Yesterday.epub")
	if err := os.WriteFile(path3, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Plus a file-less wanted book — must not trip anything even though
	// FilePath is empty. Note: BookRepo.Create ignores FilePath, so we
	// back-fill via SetFilePath (same path the real importer takes).
	for _, b := range []*models.Book{
		{ForeignID: "OL901W", AuthorID: author.ID, Title: "Guns Germs", SortTitle: "Guns", FilePath: path1, Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "OL902W", AuthorID: author.ID, Title: "Collapse", SortTitle: "Collapse", FilePath: path2, Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "OL903W", AuthorID: author.ID, Title: "World", SortTitle: "World", FilePath: path3, Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "OL904W", AuthorID: author.ID, Title: "Wanted No File", SortTitle: "Wanted", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
	} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if b.FilePath != "" {
			if err := bookRepo.SetFilePath(ctx, b.ID, b.FilePath); err != nil {
				t.Fatal(err)
			}
		}
	}

	h := NewAuthorHandler(authorRepo, bookRepo, nil, nil, profileRepo)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/author/"+strconv.FormatInt(author.ID, 10)+"?deleteFiles=true", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(author.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Delete(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{path1, path2, path3} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err=%v", p, err)
		}
	}
	// And the author row is gone.
	got, _ := authorRepo.GetByID(ctx, author.ID)
	if got != nil {
		t.Error("expected author deleted")
	}
}

// TestDeleteAuthor_WithoutDeleteFiles confirms the default path leaves
// files on disk. Preserves the pre-#15 behaviour for anyone who hits the
// delete button reflexively without opting into a disk sweep.
func TestDeleteAuthor_WithoutDeleteFiles(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL910A", Name: "Keep Files", SortName: "Files, Keep",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "book.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL911W", AuthorID: author.ID, Title: "Book", SortTitle: "Book",
		FilePath: path, Status: "imported", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	h := NewAuthorHandler(authorRepo, bookRepo, nil, nil, profileRepo)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/author/"+strconv.FormatInt(author.ID, 10), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(author.ID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Delete(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should survive default delete, stat err=%v", err)
	}
}
