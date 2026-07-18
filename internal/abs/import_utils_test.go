package abs

import (
	"strings"
	"testing"
)

func TestNormalizeLibraryIDsPrependsLegacyPrimary(t *testing.T) {
	t.Parallel()

	got := normalizeLibraryIDs(" lib-legacy ", []string{" lib-books ", "lib-audio", "lib-books", ""})
	if got, want := strings.Join(got, ","), "lib-legacy,lib-books,lib-audio"; got != want {
		t.Fatalf("library ids = %q, want %q", got, want)
	}
}

// TestNormalizeTitle_StripsABSEditionNoise verifies that the ABS title
// normalization collapses the bracket/paren/series/edition noise that ABS
// titles routinely carry, so an item title and the corresponding clean
// metadata-provider work title produce the same dedup key (#762).
func TestNormalizeTitle_StripsABSEditionNoise(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain title unchanged", "Leviathan Wakes", "leviathan wakes"},
		{"trailing series parenthetical", "The Eye of the World (The Wheel of Time, Book 1)", "the eye of the world"},
		{"trailing hash series parenthetical", "Leviathan Wakes (The Expanse #1)", "leviathan wakes"},
		{"square-bracket unabridged tag", "The Eye of the World [Unabridged]", "the eye of the world"},
		{"square-bracket dramatized tag", "The Way of Kings [Dramatized Adaptation]", "the way of kings"},
		{"colon novel subtitle and bracket tag", "Pandora's Star: A Novel [Unabridged]", "pandora's star"},
		{"stacked bracket tags", "Dune [Unabridged] [2021]", "dune"},
		{"paren edition then bracket tag", "Mistborn (Unabridged) [Audiobook]", "mistborn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeTitle(tc.in); got != tc.want {
				t.Fatalf("normalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeTitle_MatchesNoisyABSTitleToCleanProviderTitle is the core
// matching invariant: a noisy ABS item title and the clean provider work
// title for the same book must produce the same key, so lookupUpstreamBook
// and findBookByNormalizedTitle link them instead of queuing for review.
func TestNormalizeTitle_MatchesNoisyABSTitleToCleanProviderTitle(t *testing.T) {
	t.Parallel()

	pairs := [][2]string{
		{"The Eye of the World (The Wheel of Time, Book 1)", "The Eye of the World"},
		{"Pandora's Star: A Novel [Unabridged]", "Pandora's Star"},
		{"Leviathan Wakes (The Expanse #1)", "Leviathan Wakes"},
		{"The Way of Kings [Unabridged]", "The Way of Kings"},
		{"Project Hail Mary [Unabridged]", "Project Hail Mary"},
	}
	for _, pair := range pairs {
		absKey := normalizeTitle(pair[0])
		providerKey := normalizeTitle(pair[1])
		if absKey != providerKey {
			t.Errorf("titles must share a dedup key:\n  ABS      %q -> %q\n  provider %q -> %q",
				pair[0], absKey, pair[1], providerKey)
		}
	}
}

// TestNormalizeTitle_DistinctBooksStayDistinct guards against the bracket
// stripping over-collapsing genuinely different books to the same key, which
// would create false ambiguity and send correct items to review.
func TestNormalizeTitle_DistinctBooksStayDistinct(t *testing.T) {
	t.Parallel()

	distinct := [][2]string{
		{"Leviathan Wakes (The Expanse #1)", "Caliban's War (The Expanse #2)"},
		{"The Eye of the World [Unabridged]", "The Great Hunt [Unabridged]"},
		{"Pandora's Star [Unabridged]", "Judas Unchained [Unabridged]"},
	}
	for _, pair := range distinct {
		if a, b := normalizeTitle(pair[0]), normalizeTitle(pair[1]); a == b {
			t.Errorf("distinct books collapsed to same key %q:\n  %q\n  %q", a, pair[0], pair[1])
		}
	}
}

// ── Supplementary ebook fallback (#1565, Discussion #1556) ──────────────────

func supplementaryTrue() *bool { b := true; return &b }

// A combined audiobook+ebook item from a library with "Audiobooks only"
// enabled carries no media.ebookFile — the epub is only a supplementary
// libraryFiles entry. Normalization must still surface it as EbookPath so
// the import derives an ebook edition.
func TestNormalizeLibraryItem_SupplementaryEbookFallback(t *testing.T) {
	item := LibraryItem{
		ID:        "li_1",
		MediaType: "book",
		Media: BookMedia{
			AudioFiles: []AudioFile{{INO: "ino_a1", Index: 1, Path: "/books/A/T/[Audio]/1.mp3"}},
		},
		LibraryFiles: []LibraryFile{
			{INO: "ino_mp3", FileType: "audio", Metadata: LibraryFileMetadata{Ext: ".mp3", Path: "/books/A/T/[Audio]/1.mp3"}},
			{INO: "ino_pdf", FileType: "ebook", IsSupplementary: supplementaryTrue(), Metadata: LibraryFileMetadata{Ext: ".pdf", Path: "/books/A/T/extras.pdf"}},
			{INO: "ino_epub", FileType: "ebook", IsSupplementary: supplementaryTrue(), Metadata: LibraryFileMetadata{Ext: ".epub", Path: "/books/A/T/Title.epub"}},
			{INO: "ino_jpg", FileType: "image", Metadata: LibraryFileMetadata{Ext: ".jpg", Path: "/books/A/T/cover.jpg"}},
		},
	}

	out := NormalizeLibraryItem(item, true)
	if out.EbookPath != "/books/A/T/Title.epub" {
		t.Fatalf("EbookPath = %q, want the supplementary epub (preferred over pdf)", out.EbookPath)
	}
	if out.EbookINO != "ino_epub" {
		t.Errorf("EbookINO = %q, want ino_epub", out.EbookINO)
	}
}

// When ABS did promote a primary ebook, media.ebookFile wins and the
// supplementary fallback must not override it.
func TestNormalizeLibraryItem_PrimaryEbookFileWinsOverSupplementary(t *testing.T) {
	item := LibraryItem{
		ID: "li_2",
		Media: BookMedia{
			EbookFile: &EbookFile{Path: "/books/A/T/Primary.epub", INO: "ino_primary"},
		},
		LibraryFiles: []LibraryFile{
			{INO: "ino_other", FileType: "ebook", Metadata: LibraryFileMetadata{Ext: ".epub", Path: "/books/A/T/Other.epub"}},
		},
	}
	out := NormalizeLibraryItem(item, true)
	if out.EbookPath != "/books/A/T/Primary.epub" || out.EbookINO != "ino_primary" {
		t.Fatalf("EbookPath/INO = %q/%q, want the primary ebookFile", out.EbookPath, out.EbookINO)
	}
}

// No ebook-typed library files at all: EbookPath stays empty (audiobook-only
// item), no accidental promotion of images or audio files.
func TestNormalizeLibraryItem_NoEbookFilesNoFallback(t *testing.T) {
	item := LibraryItem{
		ID: "li_3",
		LibraryFiles: []LibraryFile{
			{INO: "ino_mp3", FileType: "audio", Metadata: LibraryFileMetadata{Ext: ".mp3", Path: "/x/1.mp3"}},
			{INO: "ino_jpg", FileType: "image", Metadata: LibraryFileMetadata{Ext: ".jpg", Path: "/x/c.jpg"}},
		},
	}
	if out := NormalizeLibraryItem(item, true); out.EbookPath != "" || out.EbookINO != "" {
		t.Fatalf("EbookPath/INO = %q/%q, want empty", out.EbookPath, out.EbookINO)
	}
}

// The detail fetch is what carries libraryFiles; the merge must not drop the
// list item's copy when the detail response omits it (defensive symmetry with
// every other MergeLibraryItem field).
func TestMergeLibraryItem_CarriesLibraryFiles(t *testing.T) {
	listItem := LibraryItem{
		ID: "li_4",
		LibraryFiles: []LibraryFile{
			{INO: "ino_epub", FileType: "ebook", Metadata: LibraryFileMetadata{Ext: ".epub", Path: "/x/T.epub"}},
		},
	}
	merged := MergeLibraryItem(listItem, LibraryItem{ID: "li_4"})
	if len(merged.LibraryFiles) != 1 || merged.LibraryFiles[0].INO != "ino_epub" {
		t.Fatalf("merged.LibraryFiles = %+v, want the list item's entry carried over", merged.LibraryFiles)
	}
}
