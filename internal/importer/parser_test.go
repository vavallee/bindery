package importer

import (
	"strings"
	"testing"
)

func TestParseFilename(t *testing.T) {
	tests := []struct {
		input      string
		wantTitle  string
		wantAuthor string
		wantISBN   string
		wantASIN   string
		wantFormat string
	}{
		{
			input:      "Dark Matter - Author Name.epub",
			wantTitle:  "Dark Matter",
			wantAuthor: "Author Name",
			wantFormat: "epub",
		},
		{
			input:      "Recursion by Another Author.mobi",
			wantTitle:  "Recursion",
			wantAuthor: "Another Author",
			wantFormat: "mobi",
		},
		{
			input:      "The Shining [978-0385121675] (2012).pdf",
			wantTitle:  "The Shining",
			wantISBN:   "9780385121675",
			wantFormat: "pdf",
		},
		{
			input:      "Simple Title.epub",
			wantTitle:  "Simple Title",
			wantAuthor: "",
			wantFormat: "epub",
		},
		{
			input:      "Title With Year (2024) - Some Author.azw3",
			wantTitle:  "Title With Year",
			wantAuthor: "Some Author",
			wantFormat: "azw3",
		},
		{
			input:      "audiobook.m4b",
			wantTitle:  "audiobook",
			wantFormat: "m4b",
		},
		{
			// ASIN in filename should be extracted and not pollute the title
			input:      "The Sparrow B01LVSUORS - Mary Doria Russell.epub",
			wantTitle:  "The Sparrow",
			wantAuthor: "Mary Doria Russell",
			wantASIN:   "B01LVSUORS",
			wantFormat: "epub",
		},
		{
			// Bare ASIN as the whole filename
			input:      "B09H42KSJF.azw3",
			wantTitle:  "",
			wantASIN:   "B09H42KSJF",
			wantFormat: "azw3",
		},
		{
			// issue #1014: "Author - [Series NN] - Title (tags)" must NOT be split
			// into title="Peter F Hamilton", author="- Pandora's Star". The author
			// leads, the title trails the series tag.
			input:      "Peter F Hamilton - [Commonwealth Saga 01] - Pandora's Star (US) (retail) (epub).epub",
			wantTitle:  "Pandora's Star",
			wantAuthor: "Peter F Hamilton",
			wantFormat: "epub",
		},
		{
			// em dash (U+2014) separator must split like a hyphen does. Before the
			// dash normalization this fell through to title="Dark Matter — Author
			// Name" and matched nothing.
			input:      "Dark Matter — Author Name.epub",
			wantTitle:  "Dark Matter",
			wantAuthor: "Author Name",
			wantFormat: "epub",
		},
		{
			// en dash (U+2013) separator.
			input:      "Recursion – Blake Crouch.mobi",
			wantTitle:  "Recursion",
			wantAuthor: "Blake Crouch",
			wantFormat: "mobi",
		},
		{
			// em dash in the Author-leading series convention (issue #1014 shape).
			input:      "Peter F Hamilton — [Commonwealth Saga 01] — Pandora's Star.epub",
			wantTitle:  "Pandora's Star",
			wantAuthor: "Peter F Hamilton",
			wantFormat: "epub",
		},
		{
			// A hyphenated compound in the title (no surrounding spaces) must NOT
			// be treated as a Title-Author separator.
			input:      "Spider-Man.epub",
			wantTitle:  "Spider-Man",
			wantAuthor: "",
			wantFormat: "epub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			p := ParseFilename(tt.input)
			if p.Title != tt.wantTitle {
				t.Errorf("title: got %q, want %q", p.Title, tt.wantTitle)
			}
			if p.Author != tt.wantAuthor {
				t.Errorf("author: got %q, want %q", p.Author, tt.wantAuthor)
			}
			if tt.wantISBN != "" && p.ISBN != tt.wantISBN {
				t.Errorf("isbn: got %q, want %q", p.ISBN, tt.wantISBN)
			}
			if tt.wantASIN != "" && p.ASIN != tt.wantASIN {
				t.Errorf("asin: got %q, want %q", p.ASIN, tt.wantASIN)
			}
			if p.Format != tt.wantFormat {
				t.Errorf("format: got %q, want %q", p.Format, tt.wantFormat)
			}
		})
	}
}

func TestIsBookFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"book.epub", true},
		{"book.mobi", true},
		{"book.pdf", true},
		{"book.azw3", true},
		{"audiobook.m4b", true},
		{"audio.mp3", true},
		{"audio.opus", true},
		{"audio.OPUS", true},
		{"image.jpg", false},
		{"readme.md", false},
		{"file.nzb", false},
		{"book.EPUB", true},
	}
	for _, tt := range tests {
		got := IsBookFile(tt.path)
		if got != tt.want {
			t.Errorf("IsBookFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestParseFilenameSeriesExtraction(t *testing.T) {
	cases := []struct {
		name          string
		path          string
		wantSeries    string
		wantSeriesNum string
		wantTitle     string
	}{
		{
			name:          "bracket with hash",
			path:          "/lib/[Dune Chronicles #1] Dune - Frank Herbert.m4b",
			wantSeries:    "Dune Chronicles",
			wantSeriesNum: "1",
			// parent dir has no number; parser returns "01" as title via titleAuthorRe
		},
		{
			name:          "bracket with Book keyword",
			path:          "/lib/[Stormlight Archive, Book 1] The Way of Kings - Brandon Sanderson.epub",
			wantSeries:    "Stormlight Archive",
			wantSeriesNum: "1",
			wantTitle:     "The Way of Kings",
		},
		{
			name:          "paren with hash",
			path:          "/lib/The Way of Kings (Stormlight Archive #1) - Brandon Sanderson.epub",
			wantSeries:    "Stormlight Archive",
			wantSeriesNum: "1",
		},
		{
			name:          "paren with Book keyword",
			path:          "/lib/Dune (Dune Chronicles, Book 1) - Frank Herbert.m4b",
			wantSeries:    "Dune Chronicles",
			wantSeriesNum: "1",
		},
		{
			name:          "ABS folder layout with leading number",
			path:          "/lib/Frank Herbert/Dune Chronicles/01 - Dune.m4b",
			wantSeries:    "",
			wantSeriesNum: "",
			// parent dir has no number; parser returns "01" as title via titleAuthorRe
		},
		{
			name:          "ISBN in brackets not mistaken for series",
			path:          "/lib/The Shining [978-0385121675] (2012).pdf",
			wantSeries:    "",
			wantSeriesNum: "",
		},
		{
			name:          "year in parens not mistaken for series",
			path:          "/lib/The Shining (2012).epub",
			wantSeries:    "",
			wantSeriesNum: "",
		},
		{
			name:          "inline Book N separates series from title",
			path:          "Convergence_Book_2_-_Dragonslayer.m4b",
			wantSeries:    "Convergence",
			wantSeriesNum: "2",
			wantTitle:     "Dragonslayer",
		},
		{
			name:          "inline Vol N with author",
			path:          "Mistborn_Vol_1_-_The_Final_Empire_-_Brandon_Sanderson.epub",
			wantSeries:    "Mistborn",
			wantSeriesNum: "1",
			wantTitle:     "The Final Empire",
		},
		{
			// issue #1234: Readarr "{Series} #{N} - {Title}" book folder.
			// Series + position come from the parent dir; the title comes from
			// the "Author - Title" filename so it transposes to the author here —
			// the library scan corrects the title from the folder layout (#754).
			name:          "readarr hash series folder surfaces series and position",
			path:          "/books/Terry Pratchett/Discworld/Discworld #8 - Guards! Guards!/Terry Pratchett - Guards! Guards!.epub",
			wantSeries:    "Discworld",
			wantSeriesNum: "8",
		},
		{
			name:          "readarr hash series folder with decimal position",
			path:          "/books/Terry Pratchett/Discworld/Discworld #16.5 - The Last Hero/Terry Pratchett - The Last Hero.epub",
			wantSeries:    "Discworld",
			wantSeriesNum: "16.5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseFilename(tc.path)
			if got.Series != tc.wantSeries {
				t.Errorf("Series: got %q, want %q", got.Series, tc.wantSeries)
			}
			if got.SeriesNumber != tc.wantSeriesNum {
				t.Errorf("SeriesNumber: got %q, want %q", got.SeriesNumber, tc.wantSeriesNum)
			}
			if tc.wantTitle != "" && got.Title != tc.wantTitle {
				t.Errorf("Title: got %q, want %q", got.Title, tc.wantTitle)
			}
		})
	}
}

// TestParseFilenameStripsTrackCounterPrefix covers #2547: numbered audiobook
// rips name every file "NNN-TOTAL - Title", and the Title - Author split took
// the counter as the title and the real title as the author, so none of the
// 193 files matched the catalogued book.
func TestParseFilenameStripsTrackCounterPrefix(t *testing.T) {
	cases := []struct {
		path, wantTitle, wantAuthor string
	}{
		{"/lib/girl_with_all_the_gifts/001-190 - The Girl with All the Gifts.mp3", "The Girl with All the Gifts", ""},
		{"/lib/x/190-190 - The Girl with All the Gifts.mp3", "The Girl with All the Gifts", ""},
		{"/lib/x/07 of 12 - Dune.m4b", "Dune", ""},
		{"/lib/x/001-190 - The Girl with All the Gifts - M R Carey.mp3", "The Girl with All the Gifts", "M R Carey"},
		// Titles that start with numbers are not counters.
		{"/lib/x/1984 - George Orwell.epub", "1984", "George Orwell"},
		{"/lib/x/11-22-63 - Stephen King.epub", "11-22-63", "Stephen King"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := ParseFilename(c.path)
			if got.Title != c.wantTitle || got.Author != c.wantAuthor {
				t.Fatalf("ParseFilename(%q) = title %q author %q, want %q / %q", c.path, got.Title, got.Author, c.wantTitle, c.wantAuthor)
			}
		})
	}
}

// TestParseFilenameReleaseFolderNames covers the release folders a manual
// import scan hands ParseFilename whole. A folder has no file extension, but
// filepath.Ext still found one at the last dot, so "H. G. Wells [m4b]" lost
// everything after "H. G" and parsed as the author "H G". Initials keep their
// dots now, and format, quality and year tags come off both sides.
func TestParseFilenameReleaseFolderNames(t *testing.T) {
	cases := []struct {
		path, wantTitle, wantAuthor string
	}{
		{"/in/The War of the Worlds - H. G. Wells [m4b]", "The War of the Worlds", "H. G. Wells"},
		// The parser already read this one right on main; the reported
		// author came from the bulk scan's layout reading, which
		// TestLookupBatchLayout_ReleaseFolders covers. Kept here so the
		// parser side stays pinned.
		{"/in/TEST-bare [epub]", "TEST-bare", ""},
		// An all caps word after a dot is not an extension either.
		{"/in/Title - H.G.WELLS", "Title", "H.G.WELLS"},
		{"/in/Title - J.R.R. Tolkien [epub]", "Title", "J.R.R. Tolkien"},
		{"/in/Title - J. R. R. Tolkien", "Title", "J. R. R. Tolkien"},
		{"/in/Title - JRR Tolkien", "Title", "JRR Tolkien"},
		{"/in/Title - Ursula K. Le Guin (Unabridged) [m4b]", "Title", "Ursula K. Le Guin"},
		{"/in/Title - Author (2019) [epub]", "Title", "Author"},
		{"/in/Title (Book 2) - Author", "Title", "Author"},
		{"/in/Title - Author {B00ABCDEFG}", "Title", "Author"},
		{"/in/Title - Author [mp3] {Unabridged}", "Title", "Author"},
		{"/in/A Title Without Any Separator", "A Title Without Any Separator", ""},
		{"/in/A Title Without Any Separator (2019) [m4b]", "A Title Without Any Separator", ""},
		// Unicode authors and titles.
		{"/in/Cien años de soledad - Gabriel García Márquez [m4b]", "Cien años de soledad", "Gabriel García Márquez"},
		{"/in/三体 - 刘慈欣 [epub]", "三体", "刘慈欣"},
		{"/in/Мастер и Маргарита \u2014 Михаил Булгаков (Unabridged)", "Мастер и Маргарита", "Михаил Булгаков"},
		// The same names as files: the real extension still comes off, and
		// the initials keep their dots there too.
		{"/in/The War of the Worlds - H. G. Wells.m4b", "The War of the Worlds", "H. G. Wells"},
		{"/in/Title - J.R.R. Tolkien.epub", "Title", "J.R.R. Tolkien"},
		// A dot separated release name still splits into words.
		{"/in/The.Martian.-.Andy.Weir.epub", "The Martian", "Andy Weir"},
		{"/in/The_Martian_-_Andy_Weir.epub", "The Martian", "Andy Weir"},
		// The Author leading series shape keeps its reading.
		{"/in/Peter F. Hamilton - [Commonwealth Saga 01] - Pandora's Star (retail) [epub]", "Pandora's Star", "Peter F. Hamilton"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := ParseFilename(c.path)
			if got.Title != c.wantTitle || got.Author != c.wantAuthor {
				t.Fatalf("ParseFilename(%q) = title %q author %q, want %q / %q", c.path, got.Title, got.Author, c.wantTitle, c.wantAuthor)
			}
		})
	}
	if got := ParseFilename("/in/Title - Author {B00ABCDEFG}"); got.ASIN != "B00ABCDEFG" {
		t.Errorf("ASIN in braces: got %q, want B00ABCDEFG", got.ASIN)
	}
	if got := ParseFilename("/in/Title - Author (2019) [epub]"); got.Year != "2019" {
		t.Errorf("year: got %q, want 2019", got.Year)
	}
}

// A file name with a Latin-1 byte is legal on Linux. The parsed fields must
// stay substrings of the name, because the title is used to look files up
// again; a rune round trip had turned the byte into the 3 byte U+FFFD.
func TestParseFilename_InvalidUTF8StaysSubstring(t *testing.T) {
	for _, in := range []string{" 00\xbd0", " \xe5", "Caf\xe9 Society - Anne Author.epub", "Anne Author - Caf\xe9 Society.epub"} {
		p := ParseFilename(in)
		for name, f := range map[string]string{"title": p.Title, "author": p.Author, "series": p.Series} {
			if f != "" && !strings.Contains(in, f) {
				t.Errorf("%q: %s %q is not a substring of the input", in, name, f)
			}
		}
	}
	if p := ParseFilename("Caf\xe9 Society - Anne Author.epub"); p.Title != "Caf\xe9 Society" || p.Author != "Anne Author" {
		t.Errorf("got title %q author %q", p.Title, p.Author)
	}
}
