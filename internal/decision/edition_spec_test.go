package decision

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestEditionAllowed(t *testing.T) {
	req := &models.QualityProfile{EditionPreference: models.EditionPrefRequireUnabridged}
	pref := &models.QualityProfile{EditionPreference: models.EditionPrefPreferUnabridged}
	cases := []struct {
		name    string
		spec    EditionAllowed
		title   string
		allowed bool
	}{
		{"required rejects stated abridged", EditionAllowed{Profile: req, MediaType: "audiobook", Title: "Dune"}, "Dune [Abridged] m4b", false},
		{"required passes unabridged", EditionAllowed{Profile: req, MediaType: "audiobook", Title: "Dune"}, "Dune [Unabridged] m4b", true},
		{"required passes unknown", EditionAllowed{Profile: req, MediaType: "audiobook", Title: "Dune"}, "Dune m4b", true},
		{"required passes abridged in book name", EditionAllowed{Profile: req, MediaType: "audiobook", Title: "The Abridged History of Rome"}, "The Abridged History of Rome m4b", true},
		{"prefer never rejects", EditionAllowed{Profile: pref, MediaType: "audiobook", Title: "Dune"}, "Dune [Abridged] m4b", true},
		{"no profile passes", EditionAllowed{MediaType: "audiobook", Title: "Dune"}, "Dune [Abridged] m4b", true},
		{"unset preference passes", EditionAllowed{Profile: &models.QualityProfile{}, MediaType: "audiobook", Title: "Dune"}, "Dune [Abridged] m4b", true},
		{"ebook search passes", EditionAllowed{Profile: req, MediaType: "ebook", Title: "Dune"}, "Dune [Abridged] epub", true},
	}
	for _, tc := range cases {
		ok, reason := tc.spec.IsSatisfiedBy(Release{Title: tc.title}, models.Book{})
		if ok != tc.allowed {
			t.Errorf("%s: got %v (%q), want %v", tc.name, ok, reason, tc.allowed)
		}
	}
}
