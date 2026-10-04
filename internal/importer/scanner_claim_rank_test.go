package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// claimRankFixture creates a Wanted ebook book under an {Author}/{Title}/ folder
// and returns the scanner, book repo, settings repo, that folder, the book and
// ctx. It is dualFormatFixture's sibling: this one hands back the settings repo
// too, because the claim-ranking assertions are about what lands in the
// persisted scan result as much as about what lands in book_files.
func claimRankFixture(t *testing.T) (*Scanner, *db.BookRepo, *db.SettingsRepo, string, *models.Book, context.Context) {
	t.Helper()
	s, _, books, authors, settings, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "ol:gibson", Name: "William Gibson", SortName: "Gibson, William", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:bc", AuthorID: author.ID, Title: "Burning Chrome", SortTitle: "burning chrome",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(libraryDir, "William Gibson", "Burning Chrome")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return s, books, settings, dir, book, ctx
}

// scanCountsPayload reads back the counters of the persisted scan result.
type scanCountsPayload struct {
	FilesFound     int `json:"files_found"`
	Reconciled     int `json:"reconciled"`
	Unmatched      int `json:"unmatched"`
	AlreadyTracked int `json:"already_tracked"`
}

func readScanCounts(t *testing.T, ctx context.Context, settings *db.SettingsRepo) scanCountsPayload {
	t.Helper()
	setting, err := settings.Get(ctx, "library.lastScan")
	if err != nil {
		t.Fatalf("get library.lastScan: %v", err)
	}
	if setting == nil {
		t.Fatal("expected library.lastScan to be persisted, got nil")
	}
	var p scanCountsPayload
	if err := json.Unmarshal([]byte(setting.Value), &p); err != nil {
		t.Fatalf("unmarshal scan result %q: %v", setting.Value, err)
	}
	return p
}

// walkOrder returns the paths under root in the order filepath.Walk yields
// them, which is what the scan's file loop consumes. The ranking tests assert
// the order they claim to be testing rather than assuming it, so a filesystem
// that ordered a directory differently would fail loudly instead of quietly
// testing the same case twice.
func walkOrder(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && IsBookFile(path) {
			got = append(got, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestScanLibrary_EbookContainerOutranksTextSidecar is the #2188 regression,
// reported in Discussion #1617: "There were 2 files in that folder, one epub and
// one txt, and it was matching the txt, not the epub."
//
// .txt and .rtf are book extensions and resolve to the ebook format, so a notes
// sidecar reached the matching tiers as a candidate ebook edition on equal
// footing with the real container. The (book, format) slot is claimed
// first-come, which made directory order decide the book's ebook file and sent
// the loser to Unmatched. Both orderings must now end the same way.
func TestScanLibrary_EbookContainerOutranksTextSidecar(t *testing.T) {
	// sidecarName is chosen per case purely for where it sorts against
	// "Burning Chrome.epub": "Burning Chrome (notes).txt" sorts before it
	// (' ' < '.'), "notes.txt" after it ('n' > 'B').
	for _, tc := range []struct {
		name        string
		sidecarName string
		sidecarLast bool
	}{
		{name: "sidecar reached first", sidecarName: "Burning Chrome (notes).txt"},
		{name: "container reached first", sidecarName: "notes.txt", sidecarLast: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, books, settings, dir, book, ctx := claimRankFixture(t)

			epub := filepath.Join(dir, "Burning Chrome.epub")
			writeEpubAt(t, epub, "Burning Chrome", "William Gibson", "9780060539825")
			sidecar := filepath.Join(dir, tc.sidecarName)
			if err := os.WriteFile(sidecar, bookSized("scanned from the paperback, chapter notes\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			// Pin the walk order this case exists to exercise.
			order := walkOrder(t, dir)
			if len(order) != 2 {
				t.Fatalf("expected the walk to yield 2 files, got %v", order)
			}
			if gotSidecarLast := order[1] == sidecar; gotSidecarLast != tc.sidecarLast {
				t.Fatalf("walk order %v does not exercise this case (want sidecar last = %v)", order, tc.sidecarLast)
			}

			s.ScanLibrary(ctx)

			got := bookFileFormats(t, books, ctx, book.ID)
			if got[models.MediaTypeEbook] != filepath.Clean(epub) {
				t.Errorf("ebook file = %q, want the container %q (all files: %v)",
					got[models.MediaTypeEbook], epub, got)
			}

			// The passed-over sidecar is the container's companion, not an
			// orphan the user has to go and fix, so it must not show up in the
			// Unmatched list either.
			for _, f := range readUnmatchedFiles(t, ctx, s) {
				if filepath.Clean(f.UnitPath) == filepath.Clean(sidecar) {
					t.Errorf("sidecar reported unmatched with reason %q; it is the epub's companion", f.Reason)
				}
			}
			counts := readScanCounts(t, ctx, settings)
			want := scanCountsPayload{FilesFound: 2, Reconciled: 1, Unmatched: 0, AlreadyTracked: 1}
			if counts != want {
				t.Errorf("scan counts = %+v, want %+v", counts, want)
			}
		})
	}
}

// TestScanLibrary_TextOnlyFolderStillReconciles is the other side of the
// ranking rule, and the reason the fix ranks instead of dropping .txt/.rtf from
// bookExtensions: a supplement-class extension is only ever outranked by a
// better file for the SAME book. With nothing else competing, a text-only
// library is a legitimate ebook library and must keep reconciling — the same
// property TestScanLibrary_PDFWithoutAudioStillReconciles pins for .pdf.
func TestScanLibrary_TextOnlyFolderStillReconciles(t *testing.T) {
	s, books, settings, dir, book, ctx := claimRankFixture(t)

	txt := filepath.Join(dir, "Burning Chrome.txt")
	if err := os.WriteFile(txt, bookSized("BURNING CHROME\n\nIt was hot, the night...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got := bookFileFormats(t, books, ctx, book.ID)
	if got[models.MediaTypeEbook] != filepath.Clean(txt) {
		t.Fatalf("text-only book was not reconciled: %v", got)
	}
	if counts := readScanCounts(t, ctx, settings); counts.Reconciled != 1 || counts.Unmatched != 0 {
		t.Errorf("scan counts = %+v, want 1 reconciled and 0 unmatched", counts)
	}
}

// TestScanLibrary_SidecarForADifferentBookStillReconciles pins the limit of the
// ranking rule in a flat {Author}/ layout, where every one of an author's files
// shares a folder. Ranking is per (book, format): a .txt is only passed over
// when a container claimed the book IT matched, so a text edition of one book
// must still reconcile beside an epub of another. A folder-level rule ("a .txt
// in a folder that also holds an epub is a sidecar") would have stranded it.
func TestScanLibrary_SidecarForADifferentBookStillReconciles(t *testing.T) {
	s, books, settings, dir, book, ctx := claimRankFixture(t)

	// Flat layout: both files sit directly in the author folder.
	authorDir := filepath.Dir(dir)
	second := &models.Book{
		ForeignID: "ol:neuromancer", AuthorID: book.AuthorID, Title: "Neuromancer", SortTitle: "neuromancer",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	epub := filepath.Join(authorDir, "Burning Chrome.epub")
	writeEpubAt(t, epub, "Burning Chrome", "William Gibson", "9780060539825")
	txt := filepath.Join(authorDir, "Neuromancer.txt")
	if err := os.WriteFile(txt, bookSized("The sky above the port...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	if got := bookFileFormats(t, books, ctx, book.ID); got[models.MediaTypeEbook] != filepath.Clean(epub) {
		t.Errorf("epub was not reconciled: %v", got)
	}
	if got := bookFileFormats(t, books, ctx, second.ID); got[models.MediaTypeEbook] != filepath.Clean(txt) {
		t.Errorf("text edition of a different book was not reconciled: %v", got)
	}
	if counts := readScanCounts(t, ctx, settings); counts.Reconciled != 2 {
		t.Errorf("scan counts = %+v, want 2 reconciled", counts)
	}
}
