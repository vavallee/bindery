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

// TestSanitizePathPreviewDriftGuard pins sanitizePath for the characters the TS
// mirror handles, so a change to the Go replacer set is caught here.
func TestSanitizePathPreviewDriftGuard(t *testing.T) {
	if got := sanitizePath("A: B / C? <D>"); got != "A- B - C D" {
		t.Errorf("sanitizePath = %q, want %q", got, "A- B - C D")
	}
}
