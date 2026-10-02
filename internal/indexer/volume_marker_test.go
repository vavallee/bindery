package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// shieldHeroRelease is the release from the Discord report, in the
// "{Author} - {Series} - {Position} - {Title}" shape that indexer uses.
const shieldHeroRelease = "Aneko Yusagi - The Rising of the Shield Hero - 17 - The Rising of the Shield Hero Vol 17 - epub"

// TestFilterRelevantVolumeMarkerSpelling pins that the spelling of a volume
// marker does not decide relevance. SigWords keeps "volume" (and "vol") as a
// keyword, so a title saying "Volume 17" demanded the literal word "volume"
// and dropped a release that says "Vol 17", while "Vol. 17" and a bare "17"
// were kept.
//
// Both relevance filters are checked: filterRelevantDebug is the one
// production search goes through, and the two must not drift apart.
//
// wantPlain and wantDebug are separate only because the two filters already
// differ on a same-spelling wrong volume: filterRelevant carries the
// title-identity gate (which keeps numbers) and filterRelevantDebug does not.
// That difference predates this change and is pinned here as-is so the volume
// equivalence can be shown not to loosen either of them.
func TestFilterRelevantVolumeMarkerSpelling(t *testing.T) {
	const author = "Aneko Yusagi"
	cases := []struct {
		name      string
		title     string
		release   string
		wantPlain bool
		wantDebug bool
	}{
		{
			name:      "repro: Volume in the title, Vol in the release",
			title:     "The Rising of the Shield Hero Volume 17",
			release:   shieldHeroRelease,
			wantPlain: true, wantDebug: true,
		},
		{
			name:      "repro with a comma before Volume",
			title:     "The Rising of the Shield Hero, Volume 17",
			release:   shieldHeroRelease,
			wantPlain: true, wantDebug: true,
		},
		{
			name:      "control: Vol. in the title was already kept",
			title:     "The Rising of the Shield Hero, Vol. 17",
			release:   shieldHeroRelease,
			wantPlain: true, wantDebug: true,
		},
		{
			name:      "control: bare number in the title was already kept",
			title:     "The Rising of the Shield Hero 17",
			release:   shieldHeroRelease,
			wantPlain: true, wantDebug: true,
		},
		{
			name:      "reverse: Vol. in the title, Volume in the release",
			title:     "The Rising of the Shield Hero Vol. 17",
			release:   "Aneko Yusagi - The Rising of the Shield Hero Volume 17 - epub",
			wantPlain: true, wantDebug: true,
		},
		{
			name:      "plural and dotted forms are the same marker",
			title:     "The Rising of the Shield Hero Volumes 17",
			release:   "Aneko.Yusagi.The.Rising.of.the.Shield.Hero.Vol.17.epub",
			wantPlain: true, wantDebug: true,
		},
		{
			// The plain filter's identity gate compares "7" and "07" as
			// different words, with or without this change ("Vol. 7" against
			// "Vol 07" is dropped there too). The volume guard itself ignores
			// zero padding, which the debug filter shows.
			name:      "zero padded volume number in the release",
			title:     "The Rising of the Shield Hero Volume 7",
			release:   "Aneko Yusagi - The Rising of the Shield Hero Vol 07 - epub",
			wantPlain: false, wantDebug: true,
		},
		{
			name:      "mid title format qualifier is not a keyword",
			title:     "The Rising of the Shield Hero (Light Novel) Vol. 17",
			release:   shieldHeroRelease,
			wantPlain: true, wantDebug: true,
		},
		{
			name:      "mid title format qualifier plus Volume",
			title:     "The Rising of the Shield Hero (Manga) Volume 17",
			release:   shieldHeroRelease,
			wantPlain: true, wantDebug: true,
		},

		// Negatives.
		{
			name:      "different title that merely contains vol",
			title:     "The Rising of the Shield Hero Volume 17",
			release:   "Aneko Yusagi - The Reprise of the Spear Hero Vol 17 - epub",
			wantPlain: false, wantDebug: false,
		},
		{
			name:      "a word starting with vol is not a volume marker",
			title:     "The Rising of the Shield Hero Volume 17",
			release:   "Aneko Yusagi - The Rising of the Shield Hero Volcano 17 - epub",
			wantPlain: false, wantDebug: false,
		},
		{
			name:      "Vol 16 release for a Volume 17 title stays dropped",
			title:     "The Rising of the Shield Hero Volume 17",
			release:   "Aneko Yusagi - The Rising of the Shield Hero - 16 - The Rising of the Shield Hero Vol 16 - epub",
			wantPlain: false, wantDebug: false,
		},
		{
			name:      "Volume 16 release for a Vol. 17 title stays dropped",
			title:     "The Rising of the Shield Hero Vol. 17",
			release:   "Aneko Yusagi - The Rising of the Shield Hero Volume 16 - epub",
			wantPlain: false, wantDebug: false,
		},
		{
			// Same spelling on both sides: unchanged by this fix. The plain
			// filter's identity gate drops it; the debug filter has no volume
			// number check at all and keeps it, as it did before.
			name:      "Volume 16 release for a Volume 17 title: no looser than before",
			title:     "The Rising of the Shield Hero Volume 17",
			release:   "Aneko Yusagi - The Rising of the Shield Hero Volume 16 - epub",
			wantPlain: false, wantDebug: true,
		},
		{
			name:      "cross spelling release naming another author is dropped",
			title:     "The Rising of the Shield Hero Volume 17",
			release:   "The Rising of the Shield Hero Vol 17 - Some Other Writer",
			wantPlain: false, wantDebug: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []newznab.SearchResult{{Title: tc.release}}
			gotPlain := len(filterRelevant(in, tc.title, author, nil)) == 1
			kept, _ := filterRelevantDebug(in, tc.title, author, nil)
			gotDebug := len(kept) == 1
			if gotPlain != tc.wantPlain {
				t.Errorf("filterRelevant(%q, %q) kept=%v, want %v", tc.title, tc.release, gotPlain, tc.wantPlain)
			}
			if gotDebug != tc.wantDebug {
				t.Errorf("filterRelevantDebug(%q, %q) kept=%v, want %v", tc.title, tc.release, gotDebug, tc.wantDebug)
			}
		})
	}
}
