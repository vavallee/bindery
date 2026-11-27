package main

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
)

func quiet(string, ...any) {}

func TestSeedWritesLibraryAndTree(t *testing.T) {
	dir := t.TempDir()
	spec := SeedSpec{Books: 80, Seed: 7, LibraryDir: filepath.Join(dir, "library")}
	m, err := Seed(context.Background(), filepath.Join(dir, "seed"), spec, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if m.Books != 80 || m.Authors != 5 || m.Editions < 80 || m.Series == 0 || m.History == 0 || m.Blocklist != 4 || m.Queue == 0 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if m.BookFiles != m.TreeFiles || m.TreeFiles == 0 || m.TreeFiles == 80 {
		t.Fatalf("a tracked seed tracks every file on disk and leaves some books without one: files=%d tracked=%d", m.TreeFiles, m.BookFiles)
	}

	// The rows are really there, read back through the same package.
	database, err := db.Open(m.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	books, err := db.NewBookRepo(database).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 80 {
		t.Fatalf("books in database = %d, want 80", len(books))
	}
	imported, onDisk := 0, 0
	for _, b := range books {
		if b.Status == "imported" {
			imported++
		}
		if b.FilePath != "" {
			if _, err := os.Stat(b.FilePath); err != nil {
				t.Fatalf("book %d points at a file that is not on disk: %v", b.ID, err)
			}
			onDisk++
		}
	}
	if imported != m.BookFiles || onDisk != m.BookFiles {
		t.Fatalf("imported=%d with a path=%d, want both %d", imported, onDisk, m.BookFiles)
	}
	// History is spread over the past year, not stamped with one second.
	var days int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(DISTINCT substr(created_at, 1, 10)) FROM history").Scan(&days); err != nil {
		t.Fatal(err)
	}
	if days < 30 {
		t.Fatalf("history spans %d distinct days, want it spread over the year", days)
	}

	// A second call with the same spec reuses the directory.
	before, _ := os.Stat(m.DBPath)
	again, err := Seed(ctx, filepath.Join(dir, "seed"), spec, quiet)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(m.DBPath)
	if !reflect.DeepEqual(m, again) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("a matching seed directory was rebuilt instead of reused")
	}
	// A different spec does not.
	spec.Seed = 8
	if _, ok := loadManifest(filepath.Join(dir, "seed"), spec); ok {
		t.Fatal("a manifest for another seed was accepted")
	}
}

func TestSeedUntrackedLeavesEveryFileForTheScan(t *testing.T) {
	dir := t.TempDir()
	m, err := Seed(context.Background(), filepath.Join(dir, "seed"), SeedSpec{Books: 40, Seed: 1, Untracked: true, LibraryDir: filepath.Join(dir, "library")}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if m.TreeFiles != 40 || m.BookFiles != 0 {
		t.Fatalf("untracked seed: files=%d tracked=%d, want 40 and 0", m.TreeFiles, m.BookFiles)
	}
	var epubs, m4bs int
	_ = filepath.Walk(filepath.Join(dir, "library"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".epub":
			epubs++
			if importer.TooSmallToBeABook(path, info.Size()) {
				t.Errorf("%s is %d bytes, which the scan would not take for a book", path, info.Size())
			}
		case ".m4b":
			m4bs++
		}
		return nil
	})
	if epubs != 35 || m4bs != 5 {
		t.Fatalf("tree holds %d epub and %d m4b, want 35 and 5", epubs, m4bs)
	}
}

func TestSeedRejectsEmptyLibrary(t *testing.T) {
	if _, err := Seed(context.Background(), t.TempDir(), SeedSpec{}, quiet); err == nil {
		t.Fatal("a seed of zero books must be refused")
	}
}

func TestPlanIsDeterministicAndTitlesAreUnique(t *testing.T) {
	spec := SeedSpec{Books: 5000, Seed: 3, LibraryDir: "/lib"}
	_, a := planBooks(spec)
	_, b := planBooks(spec)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the same spec planned two different libraries")
	}
	spec.Seed = 4
	if _, c := planBooks(spec); reflect.DeepEqual(a, c) {
		t.Fatal("a different seed planned the same library")
	}
	seen := map[string]bool{}
	for _, p := range a {
		if seen[p.title] {
			t.Fatalf("title %q planned twice", p.title)
		}
		seen[p.title] = true
	}
	total := len(adjectives) * len(nouns) * len(places)
	if got := bookTitle(total + 1); !strings.Contains(got, "Volume 2") {
		t.Fatalf("a title past the vocabulary must be numbered, got %q", got)
	}
	if a, b := authorName(0), authorName(len(firstNames)*len(lastNames)); a == b || !strings.HasSuffix(b, " 2") {
		t.Fatalf("an author past the name list must be numbered, got %q and %q", a, b)
	}
}

func TestEpubFixtureIsAReadableArchive(t *testing.T) {
	raw, err := epubBytes("Salt & <Stone>", "Ada Abbott")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.epub")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Fatal("an EPUB starts with a stored mimetype entry")
	}
	rc, err := zr.Open("OEBPS/content.opf")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	opf := make([]byte, 2048)
	n, _ := rc.Read(opf)
	if !strings.Contains(string(opf[:n]), "<dc:title>Salt &amp; &lt;Stone&gt;</dc:title>") {
		t.Fatalf("package file does not carry the escaped title: %s", opf[:n])
	}
	if !strings.HasPrefix(string(m4bBytes()[4:]), "ftypM4B ") {
		t.Fatal("the audiobook fixture starts with an M4B file type box")
	}
}
