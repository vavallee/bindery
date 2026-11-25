package indexer

import (
	"strings"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// laterVolumePenalty is the score taken off a release that names a later
// volume of a book whose own volume is unknown (#4173). It is larger than one
// quality step (100) and smaller than the media type mismatch penalty (500).
const laterVolumePenalty = 150.0

// namesLaterVolume reports whether release names an explicit volume of 2 or
// more straight after the wanted title, for a book with no stored series
// position and a title that carries no volume number of its own.
//
// This is a ranking signal and not a filter on purpose. A plain title does not
// say its book is the first: "Words of Radiance" and "The Wishsong of
// Shannara" are volumes 2 and 3 of their series, and "Words of Radiance #2" is
// the right release for the first. Without a position the filter has to keep
// such a release (see trailingNegativeCorpus), so the rank only prefers an
// unnumbered or "1" release when the indexer offers one. A book that does name
// its volume, or has a stored position, is judged by the filter instead.
//
// The number is read the way the filter reads one (explicitVolumeAfterMain),
// so a year, a bitrate, a disc count, "Part 1 of 2" and a number that is part
// of the wanted title are not volumes.
func namesLaterVolume(release string, c MatchCriteria) bool {
	if strings.TrimSpace(c.SeriesPosition) != "" {
		return false
	}
	title := newznab.StripFormatQualifiers(newznab.NormalizeQueryTitle(c.Title))
	if _, _, ok := trailingVolume(title); ok || isSetTitle(title) {
		return false
	}
	if f := strings.Fields(title); len(f) > 0 {
		// "Henry V", "Rocky II": the numeral is the title.
		last := strings.Trim(f[len(f)-1], ".,:;!?()[]")
		if last != "" && last == strings.ToUpper(last) && romanRe.MatchString(last) {
			return false
		}
	}
	idents := [][]string{titleIdentityKeywords(title)}
	if p := primaryTitle(title); p != title {
		idents = append(idents, titleIdentityKeywords(p))
	}
	return explicitVolumeAfterMain(release, NormalizeRelease(release), idents...) >= 2
}
