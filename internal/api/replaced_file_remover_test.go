package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type staticRoots []string

func (r staticRoots) List(context.Context) ([]models.RootFolder, error) {
	out := make([]models.RootFolder, len(r))
	for i, p := range r {
		out[i] = models.RootFolder{Path: p}
	}
	return out, nil
}

func replacedRemoverFixture(t *testing.T) (*db.BookRepo, *models.Book, *models.Book, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	a := &models.Author{ForeignID: "OL-RR-A", Name: "A", SortName: "A", MetadataProvider: "ol", Monitored: true}
	if err := db.NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	books := db.NewBookRepo(database)
	mk := func(fid string) *models.Book {
		b := &models.Book{ForeignID: fid, AuthorID: a.ID, Title: fid, SortTitle: fid, Status: models.BookStatusWanted,
			Genres: []string{}, MetadataProvider: "ol", Monitored: true, MediaType: models.MediaTypeEbook}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	return books, mk("one"), mk("two"), ctx
}

func writeFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReplacedFileRemover(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	books, one, two, ctx := replacedRemoverFixture(t)
	remove := NewReplacedFileRemover(NewLibraryRoots(staticRoots{root}), books)

	t.Run("inside the roots is removed", func(t *testing.T) {
		p := filepath.Join(root, "a", "Book.mobi")
		writeFile(t, p)
		skipped, err := remove(ctx, one.ID, p)
		if err != nil || skipped {
			t.Fatalf("skipped=%v err=%v", skipped, err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Error("file still there")
		}
	})

	t.Run("outside the roots is refused", func(t *testing.T) {
		p := filepath.Join(outside, "Book.mobi")
		writeFile(t, p)
		skipped, err := remove(ctx, one.ID, p)
		if err != nil || !skipped {
			t.Fatalf("skipped=%v err=%v, want skipped", skipped, err)
		}
		if _, err := os.Stat(p); err != nil {
			t.Error("a file outside the library roots was deleted")
		}
	})

	t.Run("under a folder another book tracks is refused", func(t *testing.T) {
		dir := filepath.Join(root, "shared")
		p := filepath.Join(dir, "Book.mobi")
		writeFile(t, p)
		if err := books.AddBookFile(ctx, two.ID, models.MediaTypeAudiobook, dir); err != nil {
			t.Fatal(err)
		}
		skipped, err := remove(ctx, one.ID, p)
		if err != nil || !skipped {
			t.Fatalf("skipped=%v err=%v, want skipped", skipped, err)
		}
		if _, err := os.Stat(p); err != nil {
			t.Error("a file under another book's folder was deleted")
		}
	})
}
