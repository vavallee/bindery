package importer

import (
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestRenamerPreviewSampleDriftGuard pins the renderer output for the canned
// sample book used by the File Naming settings UI live-preview
// (web/src/pages/settings/namingTemplate.ts, SAMPLE_BOOK). The TS mirror
// re-implements apply()/sanitizePath() client-side; if either diverges from
// this Go engine, this test breaks loudly so the preview can't silently lie
// about what the importer actually writes. Keep the expectations here in sync
// with the SAMPLE_BOOK fixture and the renderer tests in
// NamingTemplateField.test.tsx.
func TestRenamerPreviewSampleDriftGuard(t *testing.T) {
	// Mirrors SAMPLE_BOOK: author "Jane Doe" (sort "Doe, Jane"),
	// title "Sample Book", year 2024, ASIN "B01ABCDEFG",
	// series "Demo Series", series number "2", genre "Fantasy", lang "en",
	// ext "epub".
	releaseDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	author := &models.Author{Name: "Jane Doe"}
	book := &models.Book{
		Title:       "Sample Book",
		ASIN:        "B01ABCDEFG",
		ReleaseDate: &releaseDate,
		Genres:      []string{"Fantasy"},
		Language:    "en",
	}
	const series = "Demo Series"
	const seriesNumber = "2"

	r := NewRenamer("")

	cases := []struct {
		name     string
		template string
		ext      string
		want     string
	}{
		{
			name:     "all tokens (ebook)",
			template: "{Author}|{SortAuthor}|{Title}|{Year}|{ASIN}|{Series}|{SeriesNumber}|{Genre}|{Lang}|{ext}",
			ext:      "epub",
			want:     "Jane Doe|Doe, Jane|Sample Book|2024|B01ABCDEFG|Demo Series|2|Fantasy|en|epub",
		},
		{
			name:     "default ebook template",
			template: "{Author}/{Title} ({Year})/{Title} - {Author}.{ext}",
			ext:      "epub",
			want:     "Jane Doe/Sample Book (2024)/Sample Book - Jane Doe.epub",
		},
		{
			name:     "ext empty for audiobook folder",
			template: "{Title}.{ext}",
			ext:      "", // AudiobookDestDir passes ext=""
			want:     "Sample Book.",
		},
		// #1127 conditional groups + width modifiers. Every case here is
		// mirrored in NamingTemplateField.test.tsx — keep them in lockstep.
		{
			name:     "conditional group renders literal with value",
			template: "{Title}{ - Series}.{ext}",
			ext:      "epub",
			want:     "Sample Book - Demo Series.epub",
		},
		{
			name:     "width modifier zero-pads a numeric value",
			template: "{SeriesNumber:2} - {Title}",
			ext:      "epub",
			want:     "02 - Sample Book",
		},
		{
			name:     "width modifier inside a conditional group",
			template: "{Title}{ #SeriesNumber:3}",
			ext:      "epub",
			want:     "Sample Book #002",
		},
		{
			name:     "width on a non-numeric value is a no-op",
			template: "{Series:2}",
			ext:      "epub",
			want:     "Demo Series",
		},
		{
			name:     "non-keyword words in a conditional group stay literal",
			template: "{Vol SeriesNumber}",
			ext:      "epub",
			want:     "Vol 2",
		},
		{
			name:     "group with no known token stays verbatim",
			template: "{ - Titel}",
			ext:      "epub",
			want:     "{ - Titel}",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.apply(tc.template, author, book, series, seriesNumber, tc.ext)
			if got != tc.want {
				t.Errorf("apply(%q) = %q, want %q (TS preview mirror must match)", tc.template, got, tc.want)
			}
		})
	}
}

// TestRenamerConditionalGroupCollapse pins the empty-token side of #1127:
// a conditional group's literal text collapses with its token(s), and 3+
// digit modifiers keep their historical default-text meaning. Mirrored in
// NamingTemplateField.test.tsx.
func TestRenamerConditionalGroupCollapse(t *testing.T) {
	author := &models.Author{Name: "Jane Doe"}
	book := &models.Book{Title: "Sample Book"} // no series, no year, no genre

	r := NewRenamer("")

	cases := []struct {
		name     string
		template string
		want     string
	}{
		{"conditional group collapses when empty", "{Title}{ - Series}.{ext}", "Sample Book.epub"},
		{"conditional-only segment is dropped", "{Vol SeriesNumber}/{Title}.{ext}", "Sample Book.epub"},
		{"width does not invent a value", "{SeriesNumber:2} - {Title}", "Sample Book"},
		{"3+ digit modifier stays a default", "{Year:2024}", "2024"},
		{"1-2 digit modifier is a width, not a default", "{Year:20}", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.apply(tc.template, author, book, "", "", "epub")
			if got != tc.want {
				t.Errorf("apply(%q) = %q, want %q (TS preview mirror must match)", tc.template, got, tc.want)
			}
		})
	}
}

// TestRenamerWidthThenLiteral is the #1690 regression test: a single token
// carrying BOTH a zero-pad width and trailing literal text.
//
// simpleGroupRe's modifier capture is greedy, so "{SeriesNumber:3 - }" used to
// parse as one token with the modifier "3 - ". parseWidth rejected that for
// length and, because the value was non-empty, the default-text branch never
// ran either — so the padding AND the literal were both dropped ("2Sample
// Book"). With an empty series it was worse: the modifier text itself was
// substituted as the default, putting a literal "3 -" into the filename of
// every standalone book.
//
// The multi-token spelling always worked because it took the conditional
// branch, whose ":\d{1,2}" capture is bounded; these cases pin that the
// single-token spelling now agrees with it. Mirrored in namingTemplate.test.ts.
func TestRenamerWidthThenLiteral(t *testing.T) {
	author := &models.Author{Name: "Jane Doe"}
	book := &models.Book{Title: "Sample Book"}
	r := NewRenamer("")

	cases := []struct {
		name                 string
		template             string
		series, seriesNumber string
		want                 string
	}{
		{"width then literal renders both", "{SeriesNumber:3 - }{Title}", "Demo Series", "2", "002 - Sample Book"},
		{"literal without width still works", "{SeriesNumber - }{Title}", "Demo Series", "2", "2 - Sample Book"},
		{"agrees with the multi-token spelling", "{Series SeriesNumber:3 - }{Title}", "Demo Series", "2", "Demo Series 002 - Sample Book"},
		{"leading literal form is unchanged", "{Title}{ - SeriesNumber:3}", "Demo Series", "2", "Sample Book - 002"},
		{"bare width is unchanged", "{SeriesNumber:3}-{Title}", "Demo Series", "2", "002-Sample Book"},
		// The empty-value case: the whole group collapses. It must NOT leak
		// the modifier text ("3 -") as though it were default text.
		{"collapses whole group when empty", "{SeriesNumber:3 - }{Title}", "", "", "Sample Book"},
		{"no modifier text leaks when empty", "{SeriesNumber:12 vol }{Title}", "", "", "Sample Book"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.apply(tc.template, author, book, tc.series, tc.seriesNumber, "epub")
			if got != tc.want {
				t.Errorf("apply(%q) = %q, want %q (TS preview mirror must match)", tc.template, got, tc.want)
			}
		})
	}
}

// TestRenamerWidthThenLiteralKeepsDefaultText guards the boundary the fix must
// not cross: an all-digit modifier keeps its historical default-text meaning,
// so "{Year:2024}" is not re-read as width 20 plus the literal "24".
func TestRenamerWidthThenLiteralKeepsDefaultText(t *testing.T) {
	r := NewRenamer("")
	author := &models.Author{Name: "Jane Doe"}
	for _, tc := range []struct{ template, want string }{
		{"{Year:2024}", "2024"},          // empty year → 4-digit modifier is default text
		{"{Genre:Unsorted}", "Unsorted"}, // non-digit modifier is default text
	} {
		if got := r.apply(tc.template, author, &models.Book{Title: "T"}, "", "", "epub"); got != tc.want {
			t.Errorf("apply(%q) = %q, want %q", tc.template, got, tc.want)
		}
	}
}

// TestSanitizePathPreviewDriftGuard pins sanitizePath for the characters the TS
// mirror handles, so a change to the Go replacer set is caught here.
func TestSanitizePathPreviewDriftGuard(t *testing.T) {
	if got := sanitizePath("A: B / C? <D>"); got != "A - B - C D" {
		t.Errorf("sanitizePath = %q, want %q", got, "A - B - C D")
	}
}
