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
	// series "Demo Series", series number "2", genre "Fantasy", ext "epub".
	releaseDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	author := &models.Author{Name: "Jane Doe"}
	book := &models.Book{
		Title:       "Sample Book",
		ASIN:        "B01ABCDEFG",
		ReleaseDate: &releaseDate,
		Genres:      []string{"Fantasy"},
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
			template: "{Author}|{SortAuthor}|{Title}|{Year}|{ASIN}|{Series}|{SeriesNumber}|{Genre}|{ext}",
			ext:      "epub",
			want:     "Jane Doe|Doe, Jane|Sample Book|2024|B01ABCDEFG|Demo Series|2|Fantasy|epub",
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

// TestSanitizePathPreviewDriftGuard pins sanitizePath for the characters the TS
// mirror handles, so a change to the Go replacer set is caught here.
func TestSanitizePathPreviewDriftGuard(t *testing.T) {
	if got := sanitizePath("A: B / C? <D>"); got != "A- B - C D" {
		t.Errorf("sanitizePath = %q, want %q", got, "A- B - C D")
	}
}
