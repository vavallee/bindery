package dnb

import (
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func book(title, id string, year int) models.Book {
	b := models.Book{ForeignID: idPrefix + id, Title: title, SortTitle: title, MetadataProvider: "dnb"}
	if year != 0 {
		t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		b.ReleaseDate = &t
	}
	return b
}

// TestCollapseAuthorWorks_MergesPrintingsKeepsVolumes reproduces the real DNB
// catalogue for "Die Furcht des Weisen" (#1574): the two "Band 1" printings
// collapse into one book, the two "Band 2" printings into another, the combined
// single-volume edition (vol 0) is dropped as redundant, so the two-volume work
// surfaces as exactly two entries — not six, not one — while genuinely distinct
// titles pass through untouched.
func TestCollapseAuthorWorks_MergesPrintingsKeepsVolumes(t *testing.T) {
	books := []models.Book{
		book("Der Name des Windes", "1207871044", 2008),
		book("Die Furcht des Weisen (1): Die Königsmörder-Chronik. Zweiter Tag. Band 1", "1073137554", 2013),
		book("Die Furcht des Weisen/Band 1: Die Königsmörder-Chronik. Zweiter Tag", "1038456037", 2012),
		book("Die Furcht des Weisen", "1243544937", 2011),        // Teil 1 via 245 $n
		book("Die Furcht des Weisen: Roman", "1244247731", 2021), // combined edition, no volume
		book("Die Furcht des Weisen (2): Die Königsmörder-Chronik. Zweiter Tag. Band 2", "1073139808", 2013),
		book("Die Furcht des Weisen/Band 2: Die Königsmörder-Chronik. Zweiter Tag", "1038456010", 2012),
		book("Die Musik der Stille", "1264403445", 2015),
	}
	// Volume numbers as the caller derives them (title marker, or 245 $n for the
	// plain first-volume record); the combined edition has none.
	vols := []int{0, 1, 1, 1, 0, 2, 2, 0}

	got := collapseAuthorWorks(books, vols)

	titles := make([]string, len(got))
	for i, b := range got {
		titles[i] = b.Title
	}
	want := []string{
		"Der Name des Windes",
		"Die Furcht des Weisen 1",
		"Die Furcht des Weisen 2",
		"Die Musik der Stille",
	}
	if len(got) != len(want) {
		t.Fatalf("collapsed to %d books, want %d: %v", len(got), len(want), titles)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("book[%d] = %q, want %q (order must be preserved)", i, titles[i], want[i])
		}
	}

	// Volume 1 must survive as the plain work record and carry the earliest date
	// gathered from its merged printings.
	v1 := got[1]
	if v1.ForeignID != idPrefix+"1243544937" {
		t.Errorf("volume 1 foreign id = %q, want the plain work record dnb:1243544937", v1.ForeignID)
	}
	if v1.ReleaseDate == nil || v1.ReleaseDate.Year() != 2011 {
		t.Errorf("volume 1 release date = %v, want earliest (2011)", v1.ReleaseDate)
	}
}

func marc245(subs ...string) marcRecord {
	df := marcDataField{Tag: "245"}
	for i := 0; i+1 < len(subs); i += 2 {
		df.Subfields = append(df.Subfields, marcSubfield{Code: subs[i], Value: subs[i+1]})
	}
	return marcRecord{DataFields: []marcDataField{df}}
}

// TestSeriesPartTitle covers the series-catalogued editions the user flagged: a
// record whose 245 $a is the collective series title but whose $p names the
// individual book must resolve to that book's title (and its post-$p volume, if
// any), so it collapses onto the standalone edition despite a different ISBN. A
// record with no $p reports ok=false so the caller keeps the assembled title
// (#1574).
func TestSeriesPartTitle(t *testing.T) {
	cases := []struct {
		name  string
		rec   marcRecord
		title string
		vol   int
		ok    bool
	}{
		{
			name:  "series part resolves to individual title, no sub-volume",
			rec:   marc245("a", "Die Königsmörder-Chronik", "n", "Tag 1.", "p", "Der Name des Windes : [Mit Bonuskapitel aus Bd. 2]"),
			title: "Der Name des Windes", vol: 0, ok: true,
		},
		{
			name:  "series part with a splitting volume after $p",
			rec:   marc245("a", "Die Königsmörder-Chronik", "n", "Tag 2.", "p", "Die Furcht des Weisen / [übertr. von …]", "n", "Teil 1"),
			title: "Die Furcht des Weisen", vol: 1, ok: true,
		},
		{
			name: "no $p reports not-a-part",
			rec:  marc245("a", "Die Furcht des Weisen", "n", "Teil 1."),
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, vol, ok := seriesPartTitle(tc.rec)
			if ok != tc.ok || (ok && (title != tc.title || vol != tc.vol)) {
				t.Errorf("seriesPartTitle = (%q, %d, %v), want (%q, %d, %v)", title, vol, ok, tc.title, tc.vol, tc.ok)
			}
		})
	}
}

// TestCollectSeriesTitles verifies series names are gathered from MARC 490/800 so
// a whole-series record titled with the series name can be dropped (#1574).
func TestCollectSeriesTitles(t *testing.T) {
	rec := marcRecord{DataFields: []marcDataField{
		{Tag: "245", Subfields: []marcSubfield{{Code: "a", Value: "Der Name des Windes"}}},
		{Tag: "490", Subfields: []marcSubfield{{Code: "a", Value: "Die Königsmörder-Chronik"}, {Code: "v", Value: "1. Tag"}}},
		{Tag: "800", Subfields: []marcSubfield{{Code: "t", Value: "Die Königsmörder-Chronik"}, {Code: "v", Value: "1. Tag"}}},
	}}
	got := collectSeriesTitles([]marcRecord{rec})
	if !got[workDedupKey("Die Königsmörder-Chronik")] {
		t.Errorf("expected series title to be collected, got %v", got)
	}
	if got[workDedupKey("Der Name des Windes")] {
		t.Errorf("the record's own book title must not be treated as a series title")
	}
}

func TestTitleVolumeNumber(t *testing.T) {
	cases := map[string]int{
		"Die Furcht des Weisen": 0,
		"Die Furcht des Weisen (1): Die Königsmörder-Chronik. Zweiter Tag. Band 1": 1,
		"Die Furcht des Weisen/Band 1: Die Königsmörder-Chronik. Zweiter Tag":      1,
		"Die Furcht des Weisen (2)": 2,
		"Der Report der Magd (IV)":  4,
		"Fahrenheit 451":            0, // bare trailing number is not a volume
		"Blade Runner 2":            0,
	}
	for in, want := range cases {
		if got := titleVolumeNumber(in); got != want {
			t.Errorf("titleVolumeNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseVolumeToken(t *testing.T) {
	cases := map[string]int{"Teil 1.": 1, "Band 2": 2, "3": 3, "iv": 4, "": 0, "Teil": 0}
	for in, want := range cases {
		if got := parseVolumeToken(in); got != want {
			t.Errorf("parseVolumeToken(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestWorkBaseTitle(t *testing.T) {
	cases := map[string]string{
		"Die Furcht des Weisen": "Die Furcht des Weisen",
		"Die Furcht des Weisen (1): Die Königsmörder-Chronik. Zweiter Tag. Band 1": "Die Furcht des Weisen",
		"Die Furcht des Weisen/Band 1: Die Königsmörder-Chronik. Zweiter Tag":      "Die Furcht des Weisen",
		"Sturz der Titanen: Die Jahrhundert-Saga":                                  "Sturz der Titanen",
	}
	for in, want := range cases {
		if got := workBaseTitle(in); got != want {
			t.Errorf("workBaseTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCollapseAuthorWorks_SingletonKeepsSubtitle guards against over-collapsing:
// a standalone title with a real subtitle and no volume (nothing to merge with)
// must keep its full title rather than being stripped to the pre-colon head.
func TestCollapseAuthorWorks_SingletonKeepsSubtitle(t *testing.T) {
	books := []models.Book{book("Sturz der Titanen: Die Jahrhundert-Saga", "1", 2010)}
	got := collapseAuthorWorks(books, []int{0})
	if len(got) != 1 || got[0].Title != "Sturz der Titanen: Die Jahrhundert-Saga" {
		t.Fatalf("standalone subtitle must be preserved, got %+v", got)
	}
}
