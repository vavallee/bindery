package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// TestFilterRelevant_ApostropheVariantResults pins that releases found through
// the apostrophe free query variants (#3895) survive the relevance filter for
// the apostrophe title, whichever way the release spells it.
func TestFilterRelevant_ApostropheVariantResults(t *testing.T) {
	for _, name := range []string{
		"Matt Dinniman - Carls Doomsday Scenario (Audiobook)",
		"Matt Dinniman - Carl Doomsday Scenario (Audiobook)",
		"Matt Dinniman - Carl s Doomsday Scenario (Audiobook)",
		"Matt Dinniman - [Dungeon Crawler Carl 02] - Carl's Doomsday Scenario (retail) (epub)",
	} {
		res := []newznab.SearchResult{{Title: name, GUID: name}}
		got, _ := filterRelevantDetailed(res, "Carl’s Doomsday Scenario", "Matt Dinniman", nil)
		if len(got) != 1 {
			t.Errorf("%q dropped by the relevance filter", name)
		}
	}
}
