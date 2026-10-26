package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestDerivedEbookPathFollowsFormatPreference covers #3467: a release holding
// azw3, epub and mobi imports in name order, and the book used to render the
// first one imported (the azw3). It must render the EPUB whatever the order.
func TestDerivedEbookPathFollowsFormatPreference(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := mkAuthor(t, authors, ctx, "OL-PREF-A")

	dir := t.TempDir()
	cases := []struct {
		name  string
		order []string
		want  string
	}{
		{"azw3 imported first", []string{"b.azw3", "b.epub", "b.mobi"}, "b.epub"},
		{"mobi then pdf", []string{"b.pdf", "b.mobi"}, "b.mobi"},
		{"kepub beats azw3", []string{"b.azw3", "b.kepub.epub"}, "b.kepub.epub"},
		{"unlisted last", []string{"b.fb2", "b.pdf"}, "b.pdf"},
		{"unlisted ties break by extension", []string{"b.fb2", "b.cbz"}, "b.cbz"},
		{"same format keeps lowest id", []string{"a.epub", "b.epub"}, "a.epub"},
	}
	for i, tc := range cases {
		tc := tc
		b := mkBook(t, books, ctx, author.ID, "OL-PREF-"+string(rune('a'+i)), tc.name, models.BookStatusWanted)
		sub := filepath.Join(dir, tc.name)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range tc.order {
			p := filepath.Join(sub, n)
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, p); err != nil {
				t.Fatalf("%s: AddBookFile %s: %v", tc.name, n, err)
			}
		}
		got, err := books.GetByID(ctx, b.ID)
		if err != nil || got == nil {
			t.Fatalf("%s: GetByID: %v", tc.name, err)
		}
		if want := filepath.Join(sub, tc.want); got.EbookFilePath != want {
			t.Errorf("%s: EbookFilePath = %q, want %q", tc.name, got.EbookFilePath, want)
		}
	}
}

// TestDerivedEbookPathAsFilesComeAndGo: an EPUB arriving after an AZW3 takes
// over, and when the EPUB's file vanishes the live AZW3 is rendered again.
func TestDerivedEbookPathAsFilesComeAndGo(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-PREF-B")
	b := mkBook(t, books, ctx, author.ID, "OL-PREF-B1", "Flow", models.BookStatusWanted)

	dir := t.TempDir()
	azw3 := filepath.Join(dir, "b.azw3")
	epub := filepath.Join(dir, "b.epub")
	for _, p := range []string{azw3, epub} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rendered := func() string {
		t.Helper()
		got, err := books.GetByID(ctx, b.ID)
		if err != nil || got == nil {
			t.Fatalf("GetByID: %v", err)
		}
		return got.EbookFilePath
	}

	if err := books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, azw3); err != nil {
		t.Fatal(err)
	}
	if got := rendered(); got != azw3 {
		t.Fatalf("only an azw3: rendered %q", got)
	}
	if err := books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatal(err)
	}
	if got := rendered(); got != epub {
		t.Fatalf("epub added after azw3: rendered %q, want %q", got, epub)
	}

	// The preferred file vanishes; a refresh falls back to the live azw3 and
	// keeps the dead row.
	if err := os.Remove(epub); err != nil {
		t.Fatal(err)
	}
	if err := books.RefreshBookStatus(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if got := rendered(); got != azw3 {
		t.Fatalf("epub vanished: rendered %q, want %q", got, azw3)
	}
	if files, err := books.ListFiles(ctx, b.ID); err != nil || len(files) != 2 {
		t.Fatalf("rows kept: %d, %v", len(files), err)
	}
}
