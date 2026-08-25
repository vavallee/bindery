package models

import "testing"

func TestBookWantsEbook(t *testing.T) {
	cases := []struct {
		mt   string
		want bool
	}{
		{MediaTypeEbook, true},
		{MediaTypeBoth, true},
		{MediaTypeAudiobook, false},
		{"", false},
	}
	for _, c := range cases {
		b := &Book{MediaType: c.mt}
		if got := b.WantsEbook(); got != c.want {
			t.Errorf("WantsEbook(%q) = %v, want %v", c.mt, got, c.want)
		}
	}
}

func TestBookWantsAudiobook(t *testing.T) {
	cases := []struct {
		mt   string
		want bool
	}{
		{MediaTypeAudiobook, true},
		{MediaTypeBoth, true},
		{MediaTypeEbook, false},
		{"", false},
	}
	for _, c := range cases {
		b := &Book{MediaType: c.mt}
		if got := b.WantsAudiobook(); got != c.want {
			t.Errorf("WantsAudiobook(%q) = %v, want %v", c.mt, got, c.want)
		}
	}
}

func TestBookNeedsEbook(t *testing.T) {
	cases := []struct {
		mt            string
		ebookFilePath string
		want          bool
	}{
		{MediaTypeEbook, "", true},                // wanted, no file → needed
		{MediaTypeEbook, "/lib/book.epub", false}, // wanted, has file → satisfied
		{MediaTypeBoth, "", true},                 // both wanted, no ebook → needed
		{MediaTypeBoth, "/lib/book.epub", false},  // both wanted, ebook present
		{MediaTypeAudiobook, "", false},           // not watching ebook → not needed
	}
	for _, c := range cases {
		b := &Book{MediaType: c.mt, EbookFilePath: c.ebookFilePath}
		if got := b.NeedsEbook(); got != c.want {
			t.Errorf("NeedsEbook(mt=%q, ebookPath=%q) = %v, want %v",
				c.mt, c.ebookFilePath, got, c.want)
		}
	}
}

func TestBookNeedsAudiobook(t *testing.T) {
	cases := []struct {
		mt                string
		audiobookFilePath string
		want              bool
	}{
		{MediaTypeAudiobook, "", true},
		{MediaTypeAudiobook, "/ab/book", false},
		{MediaTypeBoth, "", true},
		{MediaTypeBoth, "/ab/book", false},
		{MediaTypeEbook, "", false},
	}
	for _, c := range cases {
		b := &Book{MediaType: c.mt, AudiobookFilePath: c.audiobookFilePath}
		if got := b.NeedsAudiobook(); got != c.want {
			t.Errorf("NeedsAudiobook(mt=%q, abPath=%q) = %v, want %v",
				c.mt, c.audiobookFilePath, got, c.want)
		}
	}
}

// TestBothFullySatisfied checks that a 'both' book with both file paths
// reports no further needs.
func TestBothFullySatisfied(t *testing.T) {
	b := &Book{
		MediaType:         MediaTypeBoth,
		EbookFilePath:     "/lib/book.epub",
		AudiobookFilePath: "/ab/book",
	}
	if b.NeedsEbook() {
		t.Error("NeedsEbook should be false when EbookFilePath is set")
	}
	if b.NeedsAudiobook() {
		t.Error("NeedsAudiobook should be false when AudiobookFilePath is set")
	}
}

// TestBook_HasFileForCurrentFormat is the gate the author refresh uses to
// decide whether a book is the user's already (#2096). A dual-format book
// counts as owned only when both halves are on disk, because the whole point
// of the gate is "do not invent a want", and a book still missing one format
// already has one.
func TestBook_HasFileForCurrentFormat(t *testing.T) {
	cases := []struct {
		name string
		book Book
		want bool
	}{
		{"ebook with a file", Book{MediaType: MediaTypeEbook, EbookFilePath: "/b/a.epub"}, true},
		{"ebook with only the legacy column", Book{MediaType: MediaTypeEbook, FilePath: "/b/a.epub"}, true},
		{"ebook with nothing", Book{MediaType: MediaTypeEbook}, false},
		{"ebook holding only an audiobook path", Book{MediaType: MediaTypeEbook, AudiobookFilePath: "/b/a.m4b"}, false},
		{"audiobook with a file", Book{MediaType: MediaTypeAudiobook, AudiobookFilePath: "/b/a.m4b"}, true},
		{"audiobook with only the legacy column", Book{MediaType: MediaTypeAudiobook, FilePath: "/b/a.m4b"}, true},
		{"audiobook with nothing", Book{MediaType: MediaTypeAudiobook}, false},
		{"both with both files", Book{MediaType: MediaTypeBoth, EbookFilePath: "/b/a.epub", AudiobookFilePath: "/b/a.m4b"}, true},
		{"both with only the ebook", Book{MediaType: MediaTypeBoth, EbookFilePath: "/b/a.epub"}, false},
		{"both with only the audiobook", Book{MediaType: MediaTypeBoth, AudiobookFilePath: "/b/a.m4b"}, false},
		{"no media type at all", Book{EbookFilePath: "/b/a.epub"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.book.HasFileForCurrentFormat(); got != tc.want {
				t.Errorf("HasFileForCurrentFormat() = %v, want %v", got, tc.want)
			}
		})
	}
}
