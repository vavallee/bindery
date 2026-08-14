package indexer

import (
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestAuthorSurname(t *testing.T) {
	cases := map[string]string{
		"Mary Doria Russell": "russell",
		"Ursula K. Le Guin":  "guin",
		"Asimov":             "asimov",
		"":                   "",
		"   ":                "",
	}
	for in, want := range cases {
		if got := AuthorSurname(in); got != want {
			t.Errorf("AuthorSurname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWordBoundaryRegex(t *testing.T) {
	re := WordBoundaryRegex("sparrow")
	if !re.MatchString("the sparrow russell epub") {
		t.Error("expected match for isolated token")
	}
	if re.MatchString("sparrowhawk epub") {
		t.Error("should not match inside a longer word (sparrowhawk)")
	}
	if re.MatchString("sparrows epub") {
		t.Error("should not match plural (sparrows)")
	}
}

func TestContainsPhrase(t *testing.T) {
	ok := ContainsPhrase("mary doria russell the sparrow epub", []string{"the", "sparrow"})
	if !ok {
		t.Error("expected phrase match for 'the sparrow' in author+title NZB")
	}
	if ContainsPhrase("falcon and the wooden sparrow epub", []string{"the", "sparrow"}) {
		// "the" is not directly adjacent to "sparrow" — the sequence is the
		// wooden sparrow, which breaks the phrase.
		// This matches the intended behaviour — test ensures we don't regress.
		t.Error("should not match when tokens are not adjacent")
	}
	// Contiguous via separators — normalized haystack has word gaps
	if !ContainsPhrase("dune messiah epub retail", []string{"dune", "messiah"}) {
		t.Error("expected multi-word phrase match")
	}
}

func TestNormalizeRelease(t *testing.T) {
	in := "Mary.Doria.Russell.-.The.Sparrow.(1996).epub"
	want := "mary doria russell the sparrow 1996 epub"
	if got := NormalizeRelease(in); got != want {
		t.Errorf("NormalizeRelease = %q, want %q", got, want)
	}
}

func TestStripArticles(t *testing.T) {
	if got := StripArticles("the sparrow of god"); got != "sparrow god" {
		t.Errorf("StripArticles = %q, want %q", got, "sparrow god")
	}
}

func TestParseRelease(t *testing.T) {
	p := ParseRelease("Mary.Doria.Russell.-.The.Sparrow.(1996).RETAIL.EPUB-GROUP")
	if p.Year != 1996 {
		t.Errorf("Year = %d, want 1996", p.Year)
	}
	if p.Format != "epub" {
		t.Errorf("Format = %q, want epub", p.Format)
	}
	if !p.Retail {
		t.Error("Retail should be true")
	}
	if p.Unabridged || p.Abridged {
		t.Error("audiobook flags should be false")
	}
	if p.ReleaseGroup != "GROUP" {
		t.Errorf("ReleaseGroup = %q, want GROUP", p.ReleaseGroup)
	}
}

func TestParseReleaseAudiobook(t *testing.T) {
	p := ParseRelease("Dune.Messiah.UNABRIDGED.2020.m4b")
	if p.Format != "m4b" {
		t.Errorf("Format = %q, want m4b", p.Format)
	}
	if !p.Unabridged {
		t.Error("Unabridged should be true")
	}
	if p.Year != 2020 {
		t.Errorf("Year = %d, want 2020", p.Year)
	}
}

func TestParseReleaseAbridged(t *testing.T) {
	p := ParseRelease("The.Sparrow.ABRIDGED.mp3")
	if !p.Abridged {
		t.Error("Abridged should be true")
	}
	if p.Unabridged {
		t.Error("Unabridged should be false when only ABRIDGED is present")
	}
}

func TestParseReleaseISBN(t *testing.T) {
	p := ParseRelease("The.Sparrow.9780449912553.epub")
	if p.ISBN != "9780449912553" {
		t.Errorf("ISBN = %q, want 9780449912553", p.ISBN)
	}
}

// TestReleaseFormats pins what ParsedRelease.Format cannot answer: which
// containers a release actually names. Format keeps the first match in
// formatTokens order, and that order lists every ebook container before every
// audio one, so an audiobook with a PDF booklet reduces to "pdf" there.
func TestReleaseFormats(t *testing.T) {
	cases := []struct {
		title string
		want  []string
	}{
		{"Bob the Drag Queen - Harriet Tubman (Unabridged) [M4B + PDF]", []string{"pdf", "m4b"}},
		{"Andy Weir - Project Hail Mary M4B/PDF booklet-SEEDPOOL", []string{"pdf", "m4b"}},
		{"Andy Weir - Project Hail Mary [EPUB+MOBI+AZW3]", []string{"epub", "azw3", "mobi"}},
		{"Andy Weir - Project Hail Mary.epub", []string{"epub"}},
		{"Andy Weir - Project Hail Mary-SEEDPOOL", nil},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			got := ReleaseFormats(c.title)
			if len(got) != len(c.want) {
				t.Fatalf("ReleaseFormats(%q) = %v, want %v", c.title, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("ReleaseFormats(%q) = %v, want %v", c.title, got, c.want)
				}
			}
		})
	}
}

// TestMediaTypeForFormat asserts the mapping is total over formatTokens. The
// importer used to keep its own copy of these lists, which drifted: it carried
// "opus" and "aac", tokens ParseRelease can never produce, and would have
// missed any token added here. Nothing may be a recognised release format and
// belong to neither slot.
func TestMediaTypeForFormat(t *testing.T) {
	for _, token := range formatTokens {
		mediaType := MediaTypeForFormat(token)
		if mediaType != models.MediaTypeEbook && mediaType != models.MediaTypeAudiobook {
			t.Errorf("MediaTypeForFormat(%q) = %q — every parseable format token must map to a slot",
				token, mediaType)
		}
		if IsAudiobookFormat(token) != (mediaType == models.MediaTypeAudiobook) {
			t.Errorf("MediaTypeForFormat(%q) = %q disagrees with IsAudiobookFormat(%q) = %v",
				token, mediaType, token, IsAudiobookFormat(token))
		}
	}
	for _, unknown := range []string{"", "  ", "web-dl", "opus", "aac", "flac ", "M4B"} {
		want := ""
		if strings.EqualFold(strings.TrimSpace(unknown), "m4b") || strings.EqualFold(strings.TrimSpace(unknown), "flac") {
			want = models.MediaTypeAudiobook
		}
		if got := MediaTypeForFormat(unknown); got != want {
			t.Errorf("MediaTypeForFormat(%q) = %q, want %q", unknown, got, want)
		}
	}
}

func TestParseReleaseASIN(t *testing.T) {
	p := ParseRelease("Dune.Herbert.[B0036S4B2G].m4b")
	if p.ASIN != "B0036S4B2G" {
		t.Errorf("ASIN = %q, want B0036S4B2G", p.ASIN)
	}
	// Must not match random words that don't fit the Audible shape.
	p2 := ParseRelease("Title.release.mp3")
	if p2.ASIN != "" {
		t.Errorf("unexpected ASIN extracted: %q", p2.ASIN)
	}
	// Must not match lowercase (Audible ASINs are uppercase).
	p3 := ParseRelease("title b0036s4b2g.mp3")
	if p3.ASIN != "" {
		t.Errorf("lowercase ASIN-shape should not match: %q", p3.ASIN)
	}
}
