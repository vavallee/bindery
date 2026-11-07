package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestEbookUpgradeEligible(t *testing.T) {
	profile := func(items ...models.QualityItem) *models.QualityProfile {
		return &models.QualityProfile{
			Name:           "ebook upgrades",
			UpgradeAllowed: true,
			Cutoff:         "epub",
			Items:          items,
		}
	}

	for _, tc := range []struct {
		name      string
		profile   *models.QualityProfile
		existing  []string
		candidate []string
		eligible  bool
	}{
		{
			name:      "improves a below cutoff ebook",
			profile:   profile(ticked("epub"), ticked("mobi"), ticked("pdf")),
			existing:  []string{"/library/Book.pdf"},
			candidate: []string{"epub"},
			eligible:  true,
		},
		{
			name:      "does not replace cutoff quality with equal quality",
			profile:   profile(ticked("epub"), ticked("mobi"), ticked("pdf")),
			existing:  []string{"/library/Book.epub"},
			candidate: []string{"epub"},
		},
		{
			name:      "does not replace cutoff quality with a higher preference",
			profile:   profile(ticked("azw3"), ticked("epub"), ticked("pdf")),
			existing:  []string{"/library/Book.epub"},
			candidate: []string{"azw3"},
		},
		{
			name:      "rejects candidate below cutoff",
			profile:   profile(ticked("epub"), ticked("mobi"), ticked("pdf")),
			existing:  []string{"/library/Book.pdf"},
			candidate: []string{"mobi"},
		},
		{
			name:      "rejects unlisted candidate",
			profile:   profile(ticked("epub"), ticked("pdf")),
			existing:  []string{"/library/Book.pdf"},
			candidate: []string{"mobi"},
		},
		{
			name:      "fails closed for an unranked owned ebook",
			profile:   profile(ticked("epub"), ticked("pdf")),
			existing:  []string{"/library/Book.mobi"},
			candidate: []string{"epub"},
		},
		{
			name:      "fails closed for an unticked owned ebook",
			profile:   profile(ticked("epub"), unticked("mobi"), ticked("pdf")),
			existing:  []string{"/library/Book.mobi"},
			candidate: []string{"epub"},
		},
		{
			name:      "normalizes whitespace around owned extension",
			profile:   profile(ticked("epub"), ticked("pdf")),
			existing:  []string{"/library/Book. pdf "},
			candidate: []string{"epub"},
			eligible:  true,
		},
		{
			name:      "selects the best known candidate format",
			profile:   profile(ticked("epub"), ticked("mobi"), ticked("pdf")),
			existing:  []string{"/library/Book.pdf"},
			candidate: []string{"unknown", " mobi ", "EPUB"},
			eligible:  true,
		},
		{
			name:      "requires an owned ebook",
			profile:   profile(ticked("epub"), ticked("pdf")),
			candidate: []string{"epub"},
		},
		{
			name:      "fails closed for an unknown owned path",
			profile:   profile(ticked("epub"), ticked("pdf")),
			existing:  []string{"/library/Book.bin"},
			candidate: []string{"epub"},
		},
		{
			name:      "fails closed for an audio owned path",
			profile:   profile(ticked("epub"), ticked("pdf"), ticked("m4b")),
			existing:  []string{"/library/Book.m4b"},
			candidate: []string{"epub"},
		},
		{
			name:      "requires upgrade opt in",
			profile:   &models.QualityProfile{Cutoff: "epub", Items: []models.QualityItem{ticked("epub"), ticked("pdf")}},
			existing:  []string{"/library/Book.pdf"},
			candidate: []string{"epub"},
		},
		{
			name:      "requires an allowed ebook cutoff",
			profile:   &models.QualityProfile{Name: "disabled cutoff", UpgradeAllowed: true, Cutoff: "pdf", Items: []models.QualityItem{ticked("epub"), unticked("pdf")}},
			existing:  []string{"/library/Book.pdf"},
			candidate: []string{"epub"},
		},
		{
			name:      "does not compare ebook ranks with audio ranks",
			profile:   profile(ticked("m4b"), ticked("epub"), ticked("pdf")),
			existing:  []string{"/library/Book.pdf"},
			candidate: []string{"epub"},
			eligible:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EbookUpgradeEligible(tc.profile, tc.existing, tc.candidate); got != tc.eligible {
				t.Errorf("EbookUpgradeEligible() = %v, want %v", got, tc.eligible)
			}
		})
	}
}
