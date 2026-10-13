package recommender

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestHardFilter_PreferredLanguageMatchesStoredCode: the preference is the
// two-letter search.preferredLanguage setting ("en", also the default) while a
// candidate carries the ISO 639-2/B code books are stored under ("eng"). They
// are compared normalized, or every candidate in the preferred language is
// thrown away as a foreign one (#3086).
func TestHardFilter_PreferredLanguageMatchesStoredCode(t *testing.T) {
	p := &UserProfile{
		OwnedForeignIDs:     map[string]bool{},
		DismissedForeignIDs: map[string]bool{},
		ExcludedAuthors:     map[string]bool{},
		PreferredLanguage:   "en",
	}
	candidates := []models.RecommendationCandidate{
		{ForeignID: "ENG", Title: "Stored code", Language: "eng", RatingsCount: 100, Rating: 4.0},
		{ForeignID: "EN", Title: "Two letters", Language: "en", RatingsCount: 100, Rating: 4.0},
		{ForeignID: "NONE", Title: "No language", RatingsCount: 100, Rating: 4.0},
		{ForeignID: "GER", Title: "Other language", Language: "ger", RatingsCount: 100, Rating: 4.0},
	}
	got := hardFilter(candidates, p)
	kept := map[string]bool{}
	for _, c := range got {
		kept[c.ForeignID] = true
	}
	for id, want := range map[string]bool{"ENG": true, "EN": true, "NONE": true, "GER": false} {
		if kept[id] != want {
			t.Errorf("candidate %s kept = %v, want %v", id, kept[id], want)
		}
	}
}
