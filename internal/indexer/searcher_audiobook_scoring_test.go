package indexer

import (
	"math"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// The ranking tests for #2740. They cover both boundaries the feature turns
// on: a configured profile reorders releases by density, and a profile with
// no scoring block reproduces the historical scores bit for bit. The latter
// is what stops this from silently reordering everyone's grabs.

const testMiB = 1024 * 1024

// audiobookRelease is one release of a 10-hour audiobook; only its size varies,
// so every score difference below comes from the size terms.
func audiobookRelease(guid string, sizeMiB int64) newznab.SearchResult {
	return newznab.SearchResult{
		Title: "Project.Hail.Mary.Weir.UNABRIDGED.m4b",
		GUID:  guid,
		Size:  sizeMiB * testMiB,
	}
}

// tenHourCriteria is the ranking input for that book: 36,000 seconds, an
// audiobook media type, and an m4b target of 1.0 MiB/min with a 0.2 band.
func tenHourCriteria() MatchCriteria {
	return MatchCriteria{
		Title:           "Project Hail Mary",
		Author:          "Andy Weir",
		MediaType:       models.MediaTypeAudiobook,
		DurationSeconds: 10 * 60 * 60,
		Scoring: &models.AudiobookScoring{
			CodecTargets:          map[string]float64{"m4b": 1.0},
			ToleranceMiBPerMinute: 0.2,
			SizePerMinuteWeight:   10,
		},
	}
}

func float64Ptr(v float64) *float64 { return &v }

func assertScore(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s: score = %.4f, want %.4f", what, got, want)
	}
}

// Before #2740 the ranking was monotonic in raw size: the 1,200 MiB encoding
// (2.0 MiB/min for this ten-hour book) outscored the 600 MiB one at the
// preferred 1.0 MiB/min. With scoring configured the density target wins.
func TestRankResultsAudiobookSizePerMinuteBeatsOversized(t *testing.T) {
	crit := tenHourCriteria()
	results := []newznab.SearchResult{
		audiobookRelease("600", 600),
		audiobookRelease("1200", 1200),
		audiobookRelease("300", 300),
	}
	rankResults(results, crit)
	if results[0].GUID != "600" {
		t.Fatalf("the release at the preferred density should rank first, got order: %v", resultTitles(results))
	}
	// 600 MiB sits inside the band, so the density term is zero and the flat
	// size bonus it replaced is gone: 900 (m4b) + 30 (unabridged).
	assertScore(t, scoreResult(audiobookRelease("600", 600), crit), 930, "within band")
	// 1,200 MiB is 2.0 MiB/min: 0.8 over the band, charged 10 points each.
	assertScore(t, scoreResult(audiobookRelease("1200", 1200), crit), 922, "oversized")
	// 300 MiB is 0.5 MiB/min: 0.3 under the band, charged the same way.
	assertScore(t, scoreResult(audiobookRelease("300", 300), crit), 927, "undersized")
}

// The same density must score the same on books of different lengths. A
// 300 MiB / 5 h release and a 600 MiB / 10 h release are both 1.0 MiB/min, so
// they land on the same score and neither wins for being longer.
func TestAudiobookSizePerMinuteEquivalentDensityAcrossLengths(t *testing.T) {
	short := audiobookRelease("short", 300)
	long := audiobookRelease("long", 600)

	shortCrit := tenHourCriteria()
	shortCrit.DurationSeconds = 5 * 60 * 60
	longCrit := tenHourCriteria()
	longCrit.DurationSeconds = 10 * 60 * 60

	shortScore := scoreResult(short, shortCrit)
	longScore := scoreResult(long, longCrit)
	if math.Abs(shortScore-longScore) > 1e-9 {
		t.Errorf("equal density should score equally, got %.4f (300 MiB/5h) vs %.4f (600 MiB/10h)", shortScore, longScore)
	}
}

// A profile that never configured scoring must produce exactly the numbers
// main produced before this change: 900 + 30 + the capped flat size bonus.
func TestAudiobookScoringDefaultsReproduceHistoricalRanking(t *testing.T) {
	crit := MatchCriteria{Title: "Project Hail Mary", Author: "Andy Weir", MediaType: models.MediaTypeAudiobook}
	if crit.Scoring != nil {
		t.Fatal("test setup: the default criteria must carry no scoring")
	}
	assertScore(t, scoreResult(audiobookRelease("1200", 1200), crit), 940.24, "1200 MiB")
	assertScore(t, scoreResult(audiobookRelease("600", 600), crit), 936.00, "600 MiB")
	assertScore(t, scoreResult(audiobookRelease("300", 300), crit), 933.00, "300 MiB")

	results := []newznab.SearchResult{
		audiobookRelease("600", 600),
		audiobookRelease("300", 300),
		audiobookRelease("1200", 1200),
	}
	rankResults(results, crit)
	if results[0].GUID != "1200" {
		t.Errorf("the historical ranking prefers the largest release, got order: %v", resultTitles(results))
	}
}

// Missing runtime is the common case for a book whose metadata never carried
// one. The term must step aside rather than divide by zero, and the release
// must keep the historical ranking instead of being dropped.
func TestAudiobookSizePerMinuteDegradesWithoutDuration(t *testing.T) {
	crit := tenHourCriteria()
	crit.DurationSeconds = 0
	assertScore(t, scoreResult(audiobookRelease("1200", 1200), crit), 940.24, "no runtime, 1200 MiB")
	assertScore(t, scoreResult(audiobookRelease("600", 600), crit), 936.00, "no runtime, 600 MiB")

	results := []newznab.SearchResult{
		audiobookRelease("600", 600),
		audiobookRelease("1200", 1200),
	}
	rankResults(results, crit)
	if results[0].GUID != "1200" {
		t.Errorf("without a runtime the historical order should stand, got order: %v", resultTitles(results))
	}
}

// A codec the profile did not list has no target, so it falls back the same
// way a missing runtime does. The extension alone never implies a codec.
func TestAudiobookSizePerMinuteDegradesWithoutCodecTarget(t *testing.T) {
	crit := tenHourCriteria()
	crit.Scoring.CodecTargets = map[string]float64{"flac": 3.0}
	mp3 := newznab.SearchResult{Title: "Project.Hail.Mary.Weir.UNABRIDGED.mp3", GUID: "mp3", Size: 1200 * testMiB}
	// mp3 ranks 7, so the expected score is 700 + 30 (unabridged) plus the
	// flat, capped size bonus the term did not replace.
	assertScore(t, scoreResult(mp3, crit), 700+30+10.24, "unlisted codec keeps the flat bonus")
}

// grabsWeight is the second configurable weight: absent keeps the historical
// 10, and zero turns popularity off so it can't swamp the density preference.
func TestAudiobookGrabsWeightScalesPopularity(t *testing.T) {
	quiet := audiobookRelease("quiet", 600)
	quiet.Grabs = 1
	popular := audiobookRelease("popular", 600)
	popular.Grabs = 1000

	historical := MatchCriteria{Title: "Project Hail Mary", Author: "Andy Weir", MediaType: models.MediaTypeAudiobook}
	if hs, ls := scoreResult(popular, historical), scoreResult(quiet, historical); hs <= ls {
		t.Errorf("the default grabs weight should still reward popularity: %.4f vs %.4f", hs, ls)
	}

	off := tenHourCriteria()
	off.Scoring.GrabsWeight = float64Ptr(0)
	if ps, qs := scoreResult(popular, off), scoreResult(quiet, off); math.Abs(ps-qs) > 1e-9 {
		t.Errorf("grabsWeight 0 should erase the popularity difference, got %.4f vs %.4f", ps, qs)
	}
}
