package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// seedLayoutBook inserts an author + book and returns the book. It takes no
// ISBNs: books.Create has no ISBN column to write them to (#1893), so a seeded
// ISBN would silently vanish and any test relying on one would prove nothing.
func seedLayoutBook(t *testing.T, books *db.BookRepo, authors *db.AuthorRepo, ctx context.Context, authorName, title string) *models.Book {
	t.Helper()
	a := &models.Author{Name: authorName, ForeignID: "la-" + authorName, SortName: authorName}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{AuthorID: a.ID, Title: title, ForeignID: "lb-" + title, Status: "wanted"}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestLookupBatchLayout_FolderAuthorConfirmsTitle is the core #1434 case: the
// filename carries only the title, and the AUTHOR comes from the folder layout
// (<root>/<Author>/<file>). The folder author corroborates the single title
// match, so it is confident — no more "no catalogue match" for a classic
// Author/Title.epub library.
func TestLookupBatchLayout_FolderAuthorConfirmsTitle(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	book := seedLayoutBook(t, books, authors, ctx, "Andy Weir", "Project Hail Mary")

	root := t.TempDir()
	p := filepath.Join(root, "Andy Weir", "Project Hail Mary.epub")
	writeFileAt(t, p)

	res, err := s.LookupBatchLayout(ctx, root, []string{p})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if res[0].Match != "confident" {
		t.Fatalf("match = %q, want confident (folder author corroborates)", res[0].Match)
	}
	if res[0].Book == nil || res[0].Book.ID != book.ID {
		t.Errorf("book = %v, want id=%d", res[0].Book, book.ID)
	}
	if res[0].ParsedAuthor != "Andy Weir" {
		t.Errorf("ParsedAuthor = %q, want folder-derived 'Andy Weir'", res[0].ParsedAuthor)
	}
}

// TestLookupBatchLayout_EmbeddedMetadataWins verifies embedded EPUB metadata
// beats a misleading filename and folder: the file name is garbage and the
// folder author is wrong, but the embedded title+author match the catalogue.
func TestLookupBatchLayout_EmbeddedMetadataWins(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	book := seedLayoutBook(t, books, authors, ctx, "Andy Weir", "Project Hail Mary")

	root := t.TempDir()
	p := filepath.Join(root, "Wrong Author", "book_final_v2.epub")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpubAt(t, p, "Project Hail Mary", "Andy Weir", "")

	res, err := s.LookupBatchLayout(ctx, root, []string{p})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if res[0].Match != "confident" {
		t.Fatalf("match = %q, want confident (embedded metadata)", res[0].Match)
	}
	if res[0].Book == nil || res[0].Book.ID != book.ID {
		t.Errorf("book = %v, want id=%d", res[0].Book, book.ID)
	}
}

// TestLookupBatchLayout_DemotesLooseSingleMatch is the #1402 box-set fix: a lone
// loose (non-exact, token-overlap) title match with NO author signal must NOT be
// auto-applied as confident. It is demoted to ambiguous so the wizard asks.
func TestLookupBatchLayout_DemotesLooseSingleMatch(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	seedLayoutBook(t, books, authors, ctx, "Cal Newport", "Deep Work")

	root := t.TempDir()
	// File directly under root → no folder author; title only loosely overlaps.
	p := filepath.Join(root, "Deep Work Boxed Set Collection.epub")
	writeFileAt(t, p)

	res, err := s.LookupBatchLayout(ctx, root, []string{p})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if res[0].Match != "ambiguous" {
		t.Fatalf("match = %q, want ambiguous (loose single match, no author)", res[0].Match)
	}
	if len(res[0].Candidates) != 1 {
		t.Errorf("candidates = %d, want 1 (the demoted match surfaced for review)", len(res[0].Candidates))
	}
	if res[0].Book != nil {
		t.Errorf("Book should be nil for a demoted ambiguous match, got %+v", res[0].Book)
	}
}

// TestLookupBatchLayout_ExactTitleNoAuthorStaysConfident guards the demotion
// from over-firing: an EXACT title match with no author is still confident.
func TestLookupBatchLayout_ExactTitleNoAuthorStaysConfident(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	book := seedLayoutBook(t, books, authors, ctx, "Frank Herbert", "Dune")

	root := t.TempDir()
	p := filepath.Join(root, "Dune.epub")
	writeFileAt(t, p)

	res, err := s.LookupBatchLayout(ctx, root, []string{p})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if res[0].Match != "confident" {
		t.Fatalf("match = %q, want confident (exact title)", res[0].Match)
	}
	if res[0].Book == nil || res[0].Book.ID != book.ID {
		t.Errorf("book = %v, want id=%d", res[0].Book, book.ID)
	}
}

// TestLookupBatchLayout_LoadsCatalogueOnce is the #1473 regression guard mirrored
// for the layout path: many paths, one catalogue load, results aligned.
func TestLookupBatchLayout_LoadsCatalogueOnce(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	seedLayoutBook(t, books, authors, ctx, "Andy Weir", "Project Hail Mary")

	root := t.TempDir()
	match := filepath.Join(root, "Andy Weir", "Project Hail Mary.epub")
	miss := filepath.Join(root, "Nobody", "Unknown Book.epub")
	writeFileAt(t, match)
	writeFileAt(t, miss)

	res, err := s.LookupBatchLayout(ctx, root, []string{match, miss})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("results = %d, want 2 aligned with inputs", len(res))
	}
	if res[0].Match != "confident" || res[1].Match != "none" {
		t.Errorf("matches = %q,%q; want confident,none", res[0].Match, res[1].Match)
	}
}

// TestDetectUnitFormat covers the format-by-contents fix (#1434): a directory is
// no longer blindly "audiobook" — an all-ebook folder is ebook, an audio folder
// is audiobook, and files go by extension.
func TestDetectUnitFormat(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	ebookDir := filepath.Join(tmp, "ebook-folder")
	writeFileAt(t, filepath.Join(ebookDir, "Title.epub"))
	writeFileAt(t, filepath.Join(ebookDir, "Title.mobi"))
	if got := detectUnitFormat(ebookDir); got != models.MediaTypeEbook {
		t.Errorf("all-ebook dir: got %q, want ebook", got)
	}

	audioDir := filepath.Join(tmp, "audio-folder")
	writeFileAt(t, filepath.Join(audioDir, "CD1", "01.mp3"))
	if got := detectUnitFormat(audioDir); got != models.MediaTypeAudiobook {
		t.Errorf("audio dir: got %q, want audiobook", got)
	}

	epub := filepath.Join(tmp, "loose.epub")
	writeFileAt(t, epub)
	if got := detectUnitFormat(epub); got != models.MediaTypeEbook {
		t.Errorf("epub file: got %q, want ebook", got)
	}

	m4b := filepath.Join(tmp, "loose.m4b")
	writeFileAt(t, m4b)
	if got := detectUnitFormat(m4b); got != models.MediaTypeAudiobook {
		t.Errorf("m4b file: got %q, want audiobook", got)
	}
}

// TestLookupBatchLayout_FolderAuthorBeatsBackwardsFilename is #2331. A bulk
// import pointed at a library root must not let an "Author - Title" filename,
// which ParseFilename reads as "Title - Author", override the author folder.
// That is #754's rule, and the library scan already followed it. Rows 3 and 4
// are the issue's repro table; row 5 has no book folder, so the title can only
// come from the filename once it is read the right way round.
func TestLookupBatchLayout_FolderAuthorBeatsBackwardsFilename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rel  []string
	}{
		{"title only", []string{"Christopher Pike", "Evil Thirst", "Evil Thirst.epub"}},
		{"title then author", []string{"Christopher Pike", "Evil Thirst", "Evil Thirst - Christopher Pike.epub"}},
		{"author then title", []string{"Christopher Pike", "Evil Thirst", "Christopher Pike - Evil Thirst.epub"}},
		{"inverted author then title", []string{"Christopher Pike", "Evil Thirst", "Pike, Christopher - Evil Thirst.epub"}},
		{"author then title, author folder only", []string{"Christopher Pike", "Christopher Pike - Evil Thirst.epub"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, books, authors, ctx := scannerFixture(t, t.TempDir())
			book := seedLayoutBook(t, books, authors, ctx, "Christopher Pike", "Evil Thirst")

			root := t.TempDir()
			p := filepath.Join(append([]string{root}, c.rel...)...)
			writeFileAt(t, p) // not a real EPUB: no embedded metadata to mask the bug

			res, err := s.LookupBatchLayout(ctx, root, []string{p})
			if err != nil {
				t.Fatalf("LookupBatchLayout: %v", err)
			}
			if res[0].Match != "confident" || res[0].Book == nil || res[0].Book.ID != book.ID {
				t.Fatalf("match = %q book = %v, want confident id=%d (parsed %q / %q)",
					res[0].Match, res[0].Book, book.ID, res[0].ParsedTitle, res[0].ParsedAuthor)
			}
			if res[0].ParsedTitle != "Evil Thirst" || res[0].ParsedAuthor != "Christopher Pike" {
				t.Errorf("parsed = %q / %q, want Evil Thirst / Christopher Pike", res[0].ParsedTitle, res[0].ParsedAuthor)
			}
		})
	}
}

// TestLookupBatchLayout_FilenameAuthorWhenFolderIsNotAnAuthor is the other half
// of #2331. A bulk import root is wherever the user pointed it, so the first
// folder under it is not always an author. When nothing corroborates the folder
// as an author (no catalogue author by that name, and the filename does not
// name it) the filename stays the author evidence, as it does for a flat folder.
func TestLookupBatchLayout_FilenameAuthorWhenFolderIsNotAnAuthor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rel  []string
	}{
		{"flat", []string{"Evil Thirst - Christopher Pike.epub"}},
		{"genre folder", []string{"Horror", "Evil Thirst - Christopher Pike.epub"}},
		{"genre and book folders", []string{"Horror", "Evil Thirst", "Evil Thirst - Christopher Pike.epub"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, books, authors, ctx := scannerFixture(t, t.TempDir())
			book := seedLayoutBook(t, books, authors, ctx, "Christopher Pike", "Evil Thirst")

			root := t.TempDir()
			p := filepath.Join(append([]string{root}, c.rel...)...)
			writeFileAt(t, p)

			res, err := s.LookupBatchLayout(ctx, root, []string{p})
			if err != nil {
				t.Fatalf("LookupBatchLayout: %v", err)
			}
			if res[0].Match != "confident" || res[0].Book == nil || res[0].Book.ID != book.ID {
				t.Fatalf("match = %q book = %v, want confident id=%d (parsed %q / %q)",
					res[0].Match, res[0].Book, book.ID, res[0].ParsedTitle, res[0].ParsedAuthor)
			}
			if res[0].ParsedAuthor != "Christopher Pike" {
				t.Errorf("ParsedAuthor = %q, want the filename's Christopher Pike", res[0].ParsedAuthor)
			}
		})
	}
}

// TestLookup_FolderAuthorBeatsBackwardsFilenameInLibrary covers the single item
// Manual Import lookup for #2331. It uses the filename alone, so a Readarr named
// file inside the library's <Author>/<Book>/ tree could never match there
// either. When that finds nothing, a path under the library root now tries the
// flipped reading, and keeps it because the catalogue confirms it.
func TestLookup_FolderAuthorBeatsBackwardsFilenameInLibrary(t *testing.T) {
	t.Parallel()
	libDir := t.TempDir()
	s, books, authors, ctx := scannerFixture(t, libDir)
	book := seedLayoutBook(t, books, authors, ctx, "Christopher Pike", "Evil Thirst")

	p := filepath.Join(libDir, "Christopher Pike", "Evil Thirst", "Christopher Pike - Evil Thirst.epub")
	writeFileAt(t, p)

	res, err := s.Lookup(ctx, p)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if res.Match != "confident" || res.Book == nil || res.Book.ID != book.ID {
		t.Fatalf("match = %q book = %v, want confident id=%d (parsed %q / %q)",
			res.Match, res.Book, book.ID, res.ParsedTitle, res.ParsedAuthor)
	}
	if res.ParsedTitle != "Evil Thirst" || res.ParsedAuthor != "Christopher Pike" {
		t.Errorf("parsed = %q / %q, want Evil Thirst / Christopher Pike", res.ParsedTitle, res.ParsedAuthor)
	}
}

// TestLookupBatchLayout_BookFolderIsNotFlipped is the review finding on #2331:
// a bulk import pointed at one author's folder sees book folders first, and a
// correct "Title - Author" name has a title side that names its book folder,
// just as a backwards name names its author folder. Both matched before #2331
// and must still match as parsed, never flipped.
func TestLookupBatchLayout_BookFolderIsNotFlipped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		author, title string
		rel           []string
	}{
		{"book folder", "Christopher Pike", "Evil Thirst", []string{"Evil Thirst", "Evil Thirst - Christopher Pike.epub"}},
		{"short title", "Stephen King", "It", []string{"It", "It - Stephen King.epub"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, books, authors, ctx := scannerFixture(t, t.TempDir())
			book := seedLayoutBook(t, books, authors, ctx, c.author, c.title)

			root := t.TempDir()
			p := filepath.Join(append([]string{root}, c.rel...)...)
			writeFileAt(t, p)

			res, err := s.LookupBatchLayout(ctx, root, []string{p})
			if err != nil {
				t.Fatalf("LookupBatchLayout: %v", err)
			}
			if res[0].Match != "confident" || res[0].Book == nil || res[0].Book.ID != book.ID {
				t.Fatalf("match = %q book = %v, want confident id=%d (parsed %q / %q)",
					res[0].Match, res[0].Book, book.ID, res[0].ParsedTitle, res[0].ParsedAuthor)
			}
			if res[0].ParsedTitle != c.title || res[0].ParsedAuthor != c.author {
				t.Errorf("parsed = %q / %q, want %q / %q", res[0].ParsedTitle, res[0].ParsedAuthor, c.title, c.author)
			}
		})
	}
}

// TestLookup_SingleFileMatchesAsBefore pins the single file Manual Import
// lookup to its pre #2331 behaviour. It matches on the filename alone, so a
// first folder under the library root that is a book folder, a category or a
// misfiled author never enters the match. An earlier draft of #2331 read the
// folder as the author here, and every one of these went from confident to no
// match.
func TestLookup_SingleFileMatchesAsBefore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		author, title string
		rel           []string
		wantAuthor    string
	}{
		{"flat book folder", "Christopher Pike", "Evil Thirst", []string{"Evil Thirst", "Evil Thirst - Christopher Pike.epub"}, "Christopher Pike"},
		{"flat book folder, short title", "Stephen King", "It", []string{"It", "It - Stephen King.epub"}, "Stephen King"},
		{"category folder", "Christopher Pike", "Evil Thirst", []string{"Fiction", "Evil Thirst.epub"}, ""},
		{"author folder below a category", "Christopher Pike", "Evil Thirst", []string{"ebooks", "Christopher Pike", "Evil Thirst", "Evil Thirst.epub"}, ""},
		{"misfiled under another author", "Christopher Pike", "Evil Thirst", []string{"Stephen King", "Evil Thirst.epub"}, ""},
		{"Calibre library below the root", "Christopher Pike", "Evil Thirst", []string{"Calibre Library", "Christopher Pike", "Evil Thirst (12)", "Evil Thirst.epub"}, ""},
		{"book folder with a year", "Christopher Pike", "Evil Thirst", []string{"Evil Thirst (2012)", "Evil Thirst.epub"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			libDir := t.TempDir()
			s, books, authors, ctx := scannerFixture(t, libDir)
			book := seedLayoutBook(t, books, authors, ctx, c.author, c.title)

			p := filepath.Join(append([]string{libDir}, c.rel...)...)
			writeFileAt(t, p)

			res, err := s.Lookup(ctx, p)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if res.Match != "confident" || res.Book == nil || res.Book.ID != book.ID {
				t.Fatalf("match = %q book = %v, want confident id=%d (parsed %q / %q)",
					res.Match, res.Book, book.ID, res.ParsedTitle, res.ParsedAuthor)
			}
			if res.ParsedTitle != c.title || res.ParsedAuthor != c.wantAuthor {
				t.Errorf("parsed = %q / %q, want %q / %q", res.ParsedTitle, res.ParsedAuthor, c.title, c.wantAuthor)
			}
		})
	}
}

// TestLookupBatchLayout_AmbiguousAuthorIsNeverAutomatic: the flipped reading
// and the folder override both pick an author with nobody asked, and
// textutil.MatchAuthorName's ambiguous band never auto matches. "Stanley Paul"
// against "Paul Stanley" is its textbook ambiguous pair (an unsignposted order
// swap), which lookupAuthorMatch accepts.
func TestLookupBatchLayout_AmbiguousAuthorIsNeverAutomatic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rel  []string
	}{
		{"folder override", []string{"Stanley Paul", "Face the Music - Someone Else.epub"}},
		{"flipped reading", []string{"Paul Stanley", "Stanley Paul - Face the Music.epub"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, books, authors, ctx := scannerFixture(t, t.TempDir())
			// The first row's "Stanley Paul" folder is only an ambiguous match
			// for this catalogue author, and the second row's "Stanley Paul"
			// title side is only an ambiguous match for its folder.
			seedLayoutBook(t, books, authors, ctx, "Paul Stanley", "Face the Music")

			root := t.TempDir()
			p := filepath.Join(append([]string{root}, c.rel...)...)
			writeFileAt(t, p)

			res, err := s.LookupBatchLayout(ctx, root, []string{p})
			if err != nil {
				t.Fatalf("LookupBatchLayout: %v", err)
			}
			if res[0].Match != "none" {
				t.Fatalf("match = %q book = %v, want none (parsed %q / %q)",
					res[0].Match, res[0].Book, res[0].ParsedTitle, res[0].ParsedAuthor)
			}
		})
	}
}

// seedAuthorBooks inserts one author with a book per title, for a catalogue
// where one author has several books (seedLayoutBook makes an author per call).
func seedAuthorBooks(t *testing.T, books *db.BookRepo, authors *db.AuthorRepo, ctx context.Context, authorName string, titles ...string) map[string]*models.Book {
	t.Helper()
	a := &models.Author{Name: authorName, ForeignID: "la-" + authorName, SortName: authorName}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*models.Book, len(titles))
	for _, title := range titles {
		b := &models.Book{AuthorID: a.ID, Title: title, ForeignID: "lb-" + title, Status: "wanted"}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		out[title] = b
	}
	return out
}

// TestLookupBatchLayout_TitleNamingItsAuthorIsNotOverridden is the second
// review finding on #2331. Read as it is, "Tom Clancy - Red Storm Rising.epub"
// in Tom Clancy/ has "Tom Clancy" for a title, and titleMatch pairs that with
// any catalogue title carrying the name. The folder override used to run on
// that reading before the flip was tried, so bulk import took "Tom Clancy
// Enemy Contact" with confidence, or offered only such titles, where main said
// none. The flip now comes first, and the override never runs on a reading
// whose title is its own author folder, even when the flip finds nothing.
// The single file Lookup has no override and is pinned alongside.
func TestLookupBatchLayout_TitleNamingItsAuthorIsNotOverridden(t *testing.T) {
	t.Parallel()
	const (
		clancy   = "Tom Clancy"
		christie = "Agatha Christie"
		rsr      = "Red Storm Rising"
		oe       = "Murder on the Orient Express"
		styles   = "The Mysterious Affair at Styles"
		contact  = "Tom Clancy Enemy Contact"
		honor    = "Tom Clancy Code of Honor"
		autobio  = "Agatha Christie: An Autobiography"
	)
	cases := []struct {
		name   string
		author string
		titles []string
		rel    []string
		want   string // "" means none
	}{
		{"Clancy", clancy, []string{contact, rsr}, []string{clancy, clancy + " - " + rsr + ".epub"}, rsr},
		{"Clancy with a book folder", clancy, []string{contact, rsr}, []string{clancy, rsr, clancy + " - " + rsr + ".epub"}, rsr},
		{"Clancy, two titles carry his name", clancy, []string{contact, honor, rsr}, []string{clancy, clancy + " - " + rsr + ".epub"}, rsr},
		{"Clancy, two titles carry his name, book folder", clancy, []string{contact, honor, rsr}, []string{clancy, rsr, clancy + " - " + rsr + ".epub"}, rsr},
		{"Christie", christie, []string{autobio, oe}, []string{christie, christie + " - " + oe + ".epub"}, oe},
		{"Christie with a book folder", christie, []string{autobio, oe}, []string{christie, oe, christie + " - " + oe + ".epub"}, oe},
		{"Christie, book not in the catalogue", christie, []string{autobio}, []string{christie, christie + " - " + styles + ".epub"}, ""},
		{"Christie, book not in the catalogue, book folder", christie, []string{autobio}, []string{christie, styles, christie + " - " + styles + ".epub"}, ""},
		{"Clancy, book not in the catalogue", clancy, []string{contact}, []string{clancy, clancy + " - " + rsr + ".epub"}, ""},
		{"Clancy, book not in the catalogue, two titles carry his name", clancy, []string{contact, honor}, []string{clancy, clancy + " - " + rsr + ".epub"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			check := func(t *testing.T, res LookupResult) {
				t.Helper()
				got := ""
				if res.Book != nil {
					got = res.Book.Title
				}
				if c.want == "" {
					if res.Match != "none" {
						t.Fatalf("match = %q book = %q candidates = %d, want none (parsed %q / %q)",
							res.Match, got, len(res.Candidates), res.ParsedTitle, res.ParsedAuthor)
					}
					return
				}
				if res.Match != "confident" || got != c.want {
					t.Fatalf("match = %q book = %q candidates = %d, want confident %q (parsed %q / %q)",
						res.Match, got, len(res.Candidates), c.want, res.ParsedTitle, res.ParsedAuthor)
				}
			}
			t.Run("bulk", func(t *testing.T) {
				s, books, authors, ctx := scannerFixture(t, t.TempDir())
				seedAuthorBooks(t, books, authors, ctx, c.author, c.titles...)
				root := t.TempDir()
				p := filepath.Join(append([]string{root}, c.rel...)...)
				writeFileAt(t, p)
				res, err := s.LookupBatchLayout(ctx, root, []string{p})
				if err != nil {
					t.Fatalf("LookupBatchLayout: %v", err)
				}
				check(t, res[0])
			})
			t.Run("single file", func(t *testing.T) {
				lib := t.TempDir()
				s, books, authors, ctx := scannerFixture(t, lib)
				seedAuthorBooks(t, books, authors, ctx, c.author, c.titles...)
				p := filepath.Join(append([]string{lib}, c.rel...)...)
				writeFileAt(t, p)
				res, err := s.Lookup(ctx, p)
				if err != nil {
					t.Fatalf("Lookup: %v", err)
				}
				check(t, res)
			})
		})
	}
}
