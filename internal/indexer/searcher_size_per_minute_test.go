package indexer

import (
	"math"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// Ranking tests for #2740: the size bonus is normalised by a known book
// runtime, and the popularity bonus is capped. Neither needs a setting, so
// these pin both the new numbers and the ones that must not move — a book
// with no stored runtime, and a ten-hour book, still scores exactly what it
// scored before.

const testMiB = 1024 * 1024

func assertScore(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s: score = %.4f, want %.4f", what, got, want)
	}
}

// score compiles the criteria's profile once, the way rankResults does, and
// scores one release against it, so the tests exercise the same base term the
// searcher uses.
func score(r newznab.SearchResult, c MatchCriteria) float64 {
	return scoreResult(r, c, newProfileRanks(c.Profile))
}

// orderedAudioProfile is the profile the callers hand the searcher: one
// ordered audiobook list, best first. m4b is second in a five-format list and
// #2733 scores a list from 5 down to 1, so its format term is 400.
func orderedAudioProfile() *models.QualityProfile {
	return &models.QualityProfile{
		Name: "Audiobooks",
		Items: []models.QualityItem{
			{Quality: "flac", Allowed: true},
			{Quality: "m4b", Allowed: true},
			{Quality: "m4a", Allowed: true},
			{Quality: "mp3", Allowed: true},
			{Quality: "ogg", Allowed: true},
		},
	}
}

// audiobookRelease is one m4b release; only its size varies, so every score
// difference below comes from the size term.
func audiobookRelease(guid string, sizeMiB int64) newznab.SearchResult {
	return newznab.SearchResult{
		Title: "Project.Hail.Mary.Weir.UNABRIDGED.m4b",
		GUID:  guid,
		Size:  sizeMiB * testMiB,
	}
}

// audioCriteria is an audiobook search for a book of the given runtime, with
// the ordered profile the callers pass.
func audioCriteria(durationSeconds int) MatchCriteria {
	return MatchCriteria{
		Title:           "Project Hail Mary",
		Author:          "Andy Weir",
		MediaType:       models.MediaTypeAudiobook,
		Profile:         orderedAudioProfile(),
		DurationSeconds: durationSeconds,
	}
}

// noProfileCriteria is the same search for an author with no quality profile,
// which ranks the format by models.QualityRank.
func noProfileCriteria(durationSeconds int) MatchCriteria {
	return MatchCriteria{
		Title:           "Project Hail Mary",
		Author:          "Andy Weir",
		MediaType:       models.MediaTypeAudiobook,
		DurationSeconds: durationSeconds,
	}
}

// The same density must score the same whatever the book's length. A 300 MiB /
// 5 h release, a 600 MiB / 10 h one and a 1,200 MiB / 20 h one are all
// 1.0 MiB/min, so they land on one score instead of the largest winning for
// being attached to the longest book.
func TestSizeTermIsNormalisedByDurationWhenKnown(t *testing.T) {
	short := score(audiobookRelease("short", 300), audioCriteria(5*60*60))
	long := score(audiobookRelease("long", 600), audioCriteria(10*60*60))
	longer := score(audiobookRelease("longer", 1200), audioCriteria(20*60*60))

	if math.Abs(short-long) > 1e-9 || math.Abs(long-longer) > 1e-9 {
		t.Errorf("equal densities should score equally, got %.4f (300 MiB/5h), %.4f (600 MiB/10h), %.4f (1200 MiB/20h)", short, long, longer)
	}
	// 400 (m4b in the profile) + 30 (unabridged) + 6 points for 1.0 MiB/min.
	assertScore(t, short, 436, "300 MiB over 5 h")
}

// The reference length is ten hours, the book length the flat 1024 MiB cap
// implies, so a ten-hour book keeps the numbers it had before this change.
func TestSizeTermLeavesATenHourBookAlone(t *testing.T) {
	crit := audioCriteria(10 * 60 * 60)
	assertScore(t, score(audiobookRelease("1200", 1200), crit), 440.24, "1200 MiB")
	assertScore(t, score(audiobookRelease("600", 600), crit), 436.00, "600 MiB")
	assertScore(t, score(audiobookRelease("300", 300), crit), 433.00, "300 MiB")
}

// The zero-runtime path is the contract, not an edge case. A book whose
// metadata never carried a runtime must score bit for bit what it scored
// before, on both bases: models.QualityRank when there is no profile, and the
// profile's own order when there is one.
func TestSizeTermWithoutDurationMatchesTheOldScoring(t *testing.T) {
	t.Run("no profile falls back to QualityRank", func(t *testing.T) {
		crit := noProfileCriteria(0)
		assertScore(t, score(audiobookRelease("1200", 1200), crit), 940.24, "1200 MiB, no runtime")
		assertScore(t, score(audiobookRelease("600", 600), crit), 936.00, "600 MiB, no runtime")
		assertScore(t, score(audiobookRelease("300", 300), crit), 933.00, "300 MiB, no runtime")

		results := []newznab.SearchResult{
			audiobookRelease("600", 600),
			audiobookRelease("300", 300),
			audiobookRelease("1200", 1200),
		}
		rankResults(results, crit)
		if results[0].GUID != "1200" {
			t.Errorf("without a runtime the largest release still wins, got order: %v", resultTitles(results))
		}
	})

	t.Run("an ordered profile keeps its own base", func(t *testing.T) {
		crit := audioCriteria(0)
		assertScore(t, score(audiobookRelease("1200", 1200), crit), 440.24, "1200 MiB, no runtime")
		assertScore(t, score(audiobookRelease("600", 600), crit), 436.00, "600 MiB, no runtime")
		assertScore(t, score(audiobookRelease("300", 300), crit), 433.00, "300 MiB, no runtime")
	})
}

// The runtime describes an audiobook, so a release that is not an audio
// container keeps the flat bonus even when the book carries one. Otherwise a
// dual-format book's ebook leg would be scored against the audiobook's
// runtime.
func TestSizeTermIgnoresTheRuntimeForNonAudioReleases(t *testing.T) {
	crit := MatchCriteria{
		Title:     "Project Hail Mary",
		MediaType: models.MediaTypeEbook,
		Profile: &models.QualityProfile{Items: []models.QualityItem{
			{Quality: "azw3", Allowed: true},
			{Quality: "epub", Allowed: true},
			{Quality: "mobi", Allowed: true},
			{Quality: "pdf", Allowed: true},
		}},
		DurationSeconds: 5 * 60 * 60,
	}
	epub := newznab.SearchResult{Title: "Project.Hail.Mary.Weir.UNABRIDGED.epub", GUID: "epub", Size: 6 * testMiB}
	// 300 (epub is third in the list) + 30 (unabridged) + the flat 6 MiB
	// bonus. Normalising by the 5 h runtime would have doubled it.
	assertScore(t, score(epub, crit), 330.06, "ebook keeps the flat size bonus")
}

// The popularity term is capped, so a release with thousands of grabs carries
// the same bonus as one with a hundred and cannot outweigh the format it is.
func TestGrabsBonusIsCapped(t *testing.T) {
	crit := noProfileCriteria(0)
	atCap := audiobookRelease("at-cap", 0)
	atCap.Grabs = maxGrabCount
	overCap := audiobookRelease("over-cap", 0)
	overCap.Grabs = 10000
	underCap := audiobookRelease("under-cap", 0)
	underCap.Grabs = 50

	assertScore(t, score(overCap, crit), score(atCap, crit), "10000 grabs should score as 100")
	// 900 (m4b by QualityRank) + 30 (unabridged) + log10(101)*10.
	assertScore(t, score(atCap, crit), 930+math.Log10(maxGrabCount+1)*10, "bonus at the cap")
	if score(underCap, crit) >= score(atCap, crit) {
		t.Errorf("a below-cap release should still gain less from grabs: %.4f vs %.4f", score(underCap, crit), score(atCap, crit))
	}
}
