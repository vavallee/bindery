package importer

import "testing"

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
