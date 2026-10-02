package newznab

import "testing"

// TestPrimaryTitleForQueryStripsMidTitleFormatQualifier pins the query text for
// a title carrying a format qualifier in the MIDDLE, where the trailing
// parenthesis rule in NormalizeQueryTitle never reached it. An indexer whose
// release names never contain "(Light Novel)" answered such a query with
// nothing at all.
func TestPrimaryTitleForQueryStripsMidTitleFormatQualifier(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"The Rising of the Shield Hero (Light Novel) Vol. 17", "The Rising of the Shield Hero Vol. 17"},
		{"The Rising of the Shield Hero (light novel) Vol. 17", "The Rising of the Shield Hero Vol. 17"},
		{"Berserk (Manga) Volume 3", "Berserk Volume 3"},
		{"Watchmen (Graphic Novel) Deluxe", "Watchmen Deluxe"},
		{"Saga ( Comic ) Vol 1", "Saga Vol 1"},
		{"Overlord (Novel) Vol. 2: The Dark Warrior", "Overlord Vol. 2"},
		// The trailing case was already handled and must stay the same.
		{"The Rising of the Shield Hero Vol. 17 (Light Novel)", "The Rising of the Shield Hero Vol. 17"},
		// Parenthesised words that are real title content stay put.
		{"The (Mis)Adventures of Tom Stone", "The (Mis)Adventures of Tom Stone"},
		{"I (Heart) Novels Vol. 2", "I (Heart) Novels Vol. 2"},
		{"The Novel (Is Dead) Club", "The Novel (Is Dead) Club"},
		{"Manga (Mostly) Explained", "Manga (Mostly) Explained"},
	}
	for _, tc := range cases {
		if got := primaryTitleForQuery(tc.in); got != tc.want {
			t.Errorf("primaryTitleForQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTitleHasRelevantResultVolumeSpelling: the canned feed detector must not
// judge a response irrelevant because the title says "Volume" and the release
// says "Vol". It does a substring test, so the reverse already passed.
func TestTitleHasRelevantResultVolumeSpelling(t *testing.T) {
	results := []SearchResult{{Title: "Aneko Yusagi - The Rising of the Shield Hero - 17 - The Rising of the Shield Hero Vol 17 - epub"}}
	if !titleHasRelevantResult("The Rising of the Shield Hero Volume 17", results) {
		t.Error("Volume title vs Vol release judged a canned feed")
	}
	if !titleHasRelevantResult("The Rising of the Shield Hero Vol. 17", results) {
		t.Error("Vol title vs Vol release judged a canned feed")
	}
	if titleHasRelevantResult("The Rising of the Spear Hero Volume 17", results) {
		t.Error("a different title must still be judged irrelevant")
	}
}
