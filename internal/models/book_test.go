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
