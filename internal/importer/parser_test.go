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
