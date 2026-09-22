package indexer

import (
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// This file is the one definition of how a quality profile's stored order
// ranks releases (#2733). A profile is two ordered lists, one per media type,
// top is best, and only ticked entries take part.
//
// For profile P and media type M, ProfileList(P, M) is P.Items filtered to
// the tokens MediaTypeForFormat maps to M, in stored order. P has an opinion
// on M when that list is non empty. Without an opinion nothing changes:
// decision.QualityAllowed fails open and scoreResult ranks by
// models.QualityRank exactly as it did before the order was read. A search
// that names no media type at all takes that same fallback, since a rank is
// only meaningful against the list it came from.
//
// decision.QualityAllowed and the ranker below must not each re derive the
// list; both go through ProfileList so they cannot drift.

// ProfileList returns the profile's entries whose format belongs to
// mediaType (models.MediaTypeEbook or models.MediaTypeAudiobook), in stored
// order, top first. Tokens MediaTypeForFormat does not recognise belong to no
// list and are never returned. A nil profile has no lists.
func ProfileList(p *models.QualityProfile, mediaType string) []models.QualityItem {
	if p == nil || mediaType == "" {
		return nil
	}
	var out []models.QualityItem
	for _, it := range p.Items {
		if MediaTypeForFormat(it.Quality) == mediaType {
			out = append(out, it)
		}
	}
	return out
}

// profileRanks is a profile compiled for scoring: per media type, each ticked
// token's rank, where a list of n entries scores n at the top down to 1 at
// the bottom. Unticked and absent tokens have no entry and score 0. A media
// type with no list has no map, which is how hasOpinion answers.
//
// rankResults builds one per call and hands it to scoreResult, so the per
// result loop allocates nothing for the profile however long it is.
type profileRanks struct {
	ranks map[string]map[string]int
}

// newProfileRanks compiles p. A nil profile compiles to nil, which every
// method treats as "no opinion on anything".
func newProfileRanks(p *models.QualityProfile) *profileRanks {
	if p == nil {
		return nil
	}
	pr := &profileRanks{ranks: make(map[string]map[string]int, 2)}
	for _, mt := range []string{models.MediaTypeEbook, models.MediaTypeAudiobook} {
		list := ProfileList(p, mt)
		if len(list) == 0 {
			continue
		}
		m := make(map[string]int, len(list))
		for i, it := range list {
			if !it.Allowed {
				continue
			}
			q := strings.ToLower(strings.TrimSpace(it.Quality))
			// validateQualityProfile refuses duplicates on write; a row that
			// predates it keeps the higher of the two positions.
			if _, seen := m[q]; !seen {
				m[q] = len(list) - i
			}
		}
		pr.ranks[mt] = m
	}
	return pr
}

// hasOpinion reports whether the profile lists anything for mediaType, ticked
// or not. A list with every entry unticked is still an opinion: it says no
// format of that kind is wanted, and ranking must not fall back to
// QualityRank and quietly prefer one anyway.
func (pr *profileRanks) hasOpinion(mediaType string) bool {
	if pr == nil {
		return false
	}
	_, ok := pr.ranks[mediaType]
	return ok
}

// formatScore returns the best rank among formats, considering only tokens
// whose media type is mediaType, so an ebook search never scores a release by
// an audio token it happens to carry. Every score it returns therefore comes
// from one list and one scale. mediaType must name a media type: scoring
// across both lists would compare a rank out of 12 with a rank out of 5, so
// an empty one scores nothing and the caller falls back to
// models.QualityRank.
func (pr *profileRanks) formatScore(formats []string, mediaType string) int {
	if pr == nil || mediaType == "" {
		return 0
	}
	best := 0
	for _, f := range formats {
		if MediaTypeForFormat(f) != mediaType {
			continue
		}
		if r := pr.ranks[mediaType][strings.ToLower(f)]; r > best {
			best = r
		}
	}
	return best
}
