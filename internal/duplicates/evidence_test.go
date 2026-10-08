package duplicates

import (
	"reflect"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func year(y int) *time.Time {
	t := time.Date(y, time.June, 1, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestNewEvidenceNormalizes(t *testing.T) {
	b := models.Book{
		ID:                1,
		ASIN:              " b00abc123 ",
		ReleaseDate:       year(2015),
		EbookFilePath:     "/books/Andy Weir/The Martian/The Martian.epub",
		AudiobookFilePath: "/audio/Andy Weir/The Martian",
	}
	files := []FileRef{
		{Kind: "ebook", Path: "/books/Andy Weir/The Martian/The Martian.epub"},
		{Kind: "ebook", Path: "/books/Andy Weir/The Martian/The Martian.EPUB"},
		{Kind: "ebook", Path: `C:\Books\The Martian.mobi`},
	}
	isbns := []string{"0-553-41802-5", "9780553418026", "978-0-553-41802-6", "9780000000000"}
	ev := NewEvidence(b, files, isbns, []string{"B00ABC123", "B0XYZ"}, []SeriesEvidence{{SeriesID: 3, Title: "Solo", Position: "1"}})

	wantFiles := []FileEvidence{
		{Kind: "audiobook", Format: ""}, // folder: from the column fallback, no format
		{Kind: "ebook", Format: "epub"},
		{Kind: "ebook", Format: "mobi"},
	}
	if !reflect.DeepEqual(ev.Files, wantFiles) {
		t.Errorf("files = %+v, want %+v", ev.Files, wantFiles)
	}
	// The ISBN-10 converts to the same ISBN-13; the bad check digit one is
	// kept raw, so two rows carrying it would still match.
	if want := []string{"9780000000000", "9780553418026"}; !reflect.DeepEqual(ev.ISBNs, want) {
		t.Errorf("isbns = %v, want %v", ev.ISBNs, want)
	}
	if ev.ISBNCount != 2 {
		t.Errorf("isbnCount = %d, want 2", ev.ISBNCount)
	}
	if want := []string{"B00ABC123", "B0XYZ"}; !reflect.DeepEqual(ev.ASINs, want) {
		t.Errorf("asins = %v, want %v", ev.ASINs, want)
	}
	if ev.Year != 2015 {
		t.Errorf("year = %d, want 2015", ev.Year)
	}
}

func TestNewEvidenceCapsShownISBNsButComparesAll(t *testing.T) {
	isbns := []string{"9780000000001", "9780000000002", "9780000000003", "9780000000004", "9780000000005", "9780000000006", "9780000000007"}
	a := NewEvidence(models.Book{ID: 1}, nil, isbns, nil, nil)
	if len(a.ISBNs) != maxShownISBNs || a.ISBNCount != len(isbns) {
		t.Fatalf("shown %d of %d, want %d of %d", len(a.ISBNs), a.ISBNCount, maxShownISBNs, len(isbns))
	}
	// The shared ISBN is past the display cap; the comparison still finds it.
	b := NewEvidence(models.Book{ID: 2}, nil, []string{"9780000000007"}, nil, nil)
	g := Group{Members: []Member{{Book: models.Book{ID: 1}}, {Book: models.Book{ID: 2}}}}
	Annotate(&g, map[int64]Evidence{1: a, 2: b})
	if len(g.Signals) != 1 || g.Signals[0].Kind != SignalSharedISBN || g.Signals[0].Values[0] != "9780000000007" {
		t.Fatalf("signals = %+v, want one shared-isbn on 9780000000007", g.Signals)
	}
}

func TestFileFormat(t *testing.T) {
	cases := map[string]string{
		"/a/b.epub":            "epub",
		"/a/b.M4B":             "m4b",
		"/a/Vol. 2":            "",
		"/a/J. R. R. Tolkien":  "",
		"/a/folder":            "",
		`D:\Audio\Book.mp3`:    "mp3",
		"/a/b.backup-original": "",
	}
	for in, want := range cases {
		if got := fileFormat(in); got != want {
			t.Errorf("fileFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

type evMember struct {
	id       int64
	excluded bool
	lang     string
	title    string // "Dune" when empty
	ev       Evidence
}

func annotated(members ...evMember) Group {
	g := Group{}
	evidence := map[int64]Evidence{}
	for _, m := range members {
		title := m.title
		if title == "" {
			title = "Dune"
		}
		g.Members = append(g.Members, Member{Book: models.Book{ID: m.id, Excluded: m.excluded, Language: m.lang, Title: title}})
		evidence[m.id] = m.ev
	}
	Annotate(&g, evidence)
	return g
}

func withFiles(kind string) Evidence {
	return NewEvidence(models.Book{}, []FileRef{{Kind: kind, Path: "/x/book." + map[string]string{"ebook": "epub", "audiobook": "m4b"}[kind]}}, nil, nil, nil)
}

func signalKinds(g Group) []SignalKind {
	var out []SignalKind
	for _, s := range g.Signals {
		out = append(out, s.Kind)
	}
	return out
}

func TestAnnotateAgreementSignals(t *testing.T) {
	series := []SeriesEvidence{{SeriesID: 9, Title: "Discworld", Position: "3"}}
	g := annotated(
		evMember{id: 1, ev: NewEvidence(models.Book{ASIN: "B01"}, nil, []string{"9780553418026"}, nil, series)},
		evMember{id: 2, ev: NewEvidence(models.Book{ASIN: "b01"}, nil, []string{"0553418025"}, nil, series)},
	)
	want := []SignalKind{SignalSharedISBN, SignalSharedASIN, SignalSameSeriesPosition}
	if !reflect.DeepEqual(signalKinds(g), want) {
		t.Fatalf("signals = %v, want %v", signalKinds(g), want)
	}
	if g.Conflict {
		t.Error("agreement only, but conflict = true")
	}
	for _, s := range g.Signals {
		if !reflect.DeepEqual(s.BookIDs, []int64{1, 2}) {
			t.Errorf("%s bookIds = %v, want [1 2]", s.Kind, s.BookIDs)
		}
	}
	if got := g.Signals[2].Values; !reflect.DeepEqual(got, []string{"Discworld", "3"}) {
		t.Errorf("series values = %v", got)
	}
}

func TestAnnotateConflictSignals(t *testing.T) {
	cases := []struct {
		name     string
		a, b     evMember
		conflict SignalKind
	}{
		{
			name:     "different series positions",
			a:        evMember{id: 1, ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 4, Title: "Foundation", Position: "1"}})},
			b:        evMember{id: 2, ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 4, Title: "Foundation", Position: "2"}})},
			conflict: SignalSeriesPositionConflict,
		},
		{
			name:     "years more than one apart",
			a:        evMember{id: 1, ev: NewEvidence(models.Book{ReleaseDate: year(2010)}, nil, nil, nil, nil)},
			b:        evMember{id: 2, ev: NewEvidence(models.Book{ReleaseDate: year(2012)}, nil, nil, nil, nil)},
			conflict: SignalYearConflict,
		},
		{
			name:     "different languages",
			a:        evMember{id: 1, lang: "en"},
			b:        evMember{id: 2, lang: "German"},
			conflict: SignalLanguageConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := annotated(tc.a, tc.b)
			if !g.Conflict {
				t.Fatalf("conflict = false, signals = %+v", g.Signals)
			}
			if kinds := signalKinds(g); len(kinds) != 1 || kinds[0] != tc.conflict {
				t.Fatalf("signals = %v, want [%s]", kinds, tc.conflict)
			}
			if !g.Signals[0].Conflict {
				t.Error("signal not marked as a conflict")
			}
		})
	}
}

func TestAnnotateNoFalseConflicts(t *testing.T) {
	cases := []struct {
		name string
		a, b evMember
	}{
		{"one year apart", evMember{id: 1, ev: NewEvidence(models.Book{ReleaseDate: year(2010)}, nil, nil, nil, nil)}, evMember{id: 2, ev: NewEvidence(models.Book{ReleaseDate: year(2011)}, nil, nil, nil, nil)}},
		{"unknown year", evMember{id: 1, ev: NewEvidence(models.Book{ReleaseDate: year(2010)}, nil, nil, nil, nil)}, evMember{id: 2}},
		{"same language spelled differently", evMember{id: 1, lang: "en"}, evMember{id: 2, lang: "eng"}},
		{"region subtag", evMember{id: 1, lang: "en-US"}, evMember{id: 2, lang: "English"}},
		{"unknown language", evMember{id: 1, lang: "en"}, evMember{id: 2}},
		{"unknown series position", evMember{id: 1, ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 4, Position: "1"}})}, evMember{id: 2, ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 4}})}},
		{"different series", evMember{id: 1, ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 4, Position: "1"}})}, evMember{id: 2, ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 5, Position: "2"}})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := annotated(tc.a, tc.b)
			if g.Conflict {
				t.Errorf("conflict = true, signals = %+v", g.Signals)
			}
		})
	}
}

func TestAnnotateIgnoresExcludedMembersForSignals(t *testing.T) {
	g := annotated(
		evMember{id: 1, lang: "en"},
		evMember{id: 2, lang: "en"},
		evMember{id: 3, lang: "fr", excluded: true},
	)
	if g.Conflict || len(g.Signals) != 0 {
		t.Errorf("excluded member drove signals: %+v", g.Signals)
	}
}

func TestAnnotateKeeperAndSuggestion(t *testing.T) {
	t.Run("one row with files is the keeper, empty rows suggested", func(t *testing.T) {
		g := annotated(
			evMember{id: 1},
			evMember{id: 2, ev: withFiles("ebook")},
			evMember{id: 3},
			evMember{id: 4, excluded: true},
		)
		if g.KeeperID != 2 {
			t.Errorf("keeper = %d, want 2", g.KeeperID)
		}
		if want := []int64{1, 3}; !reflect.DeepEqual(g.SuggestedExcludeIDs, want) {
			t.Errorf("suggested = %v, want %v (already excluded row 4 is not suggested again)", g.SuggestedExcludeIDs, want)
		}
		if !g.Members[1].HasFiles || g.Members[0].HasFiles {
			t.Error("hasFiles flags wrong")
		}
	})
	t.Run("conflict keeps the keeper but drops the suggestion", func(t *testing.T) {
		g := annotated(
			evMember{id: 1, lang: "en"},
			evMember{id: 2, lang: "de", ev: withFiles("audiobook")},
		)
		if g.KeeperID != 2 {
			t.Errorf("keeper = %d, want 2", g.KeeperID)
		}
		if len(g.SuggestedExcludeIDs) != 0 {
			t.Errorf("suggested = %v for a conflicted group, want none", g.SuggestedExcludeIDs)
		}
	})
	t.Run("two rows with files: no keeper, no suggestion", func(t *testing.T) {
		g := annotated(
			evMember{id: 1, ev: withFiles("ebook")},
			evMember{id: 2, ev: withFiles("audiobook")},
			evMember{id: 3},
		)
		if g.KeeperID != 0 || len(g.SuggestedExcludeIDs) != 0 {
			t.Errorf("keeper = %d, suggested = %v; want neither", g.KeeperID, g.SuggestedExcludeIDs)
		}
	})
	t.Run("no files anywhere: no keeper, no suggestion", func(t *testing.T) {
		g := annotated(evMember{id: 1}, evMember{id: 2})
		if g.KeeperID != 0 || len(g.SuggestedExcludeIDs) != 0 {
			t.Errorf("keeper = %d, suggested = %v; want neither", g.KeeperID, g.SuggestedExcludeIDs)
		}
		if g.SuggestedExcludeIDs == nil || g.Signals == nil {
			t.Error("slices must serialize as [] not null")
		}
	})
}

// TestAnnotateNeverSuggestsARowWithFiles sweeps every combination of files,
// exclusion and a language conflict over a three-row group: whatever the
// mix, no suggested row has files.
func TestAnnotateNeverSuggestsARowWithFiles(t *testing.T) {
	for mask := 0; mask < 1<<9; mask++ {
		var members []evMember
		for i := 0; i < 3; i++ {
			m := evMember{id: int64(i + 1), lang: "en"}
			if mask&(1<<i) != 0 {
				m.ev = withFiles("ebook")
			}
			if mask&(1<<(i+3)) != 0 {
				m.excluded = true
			}
			if mask&(1<<(i+6)) != 0 {
				m.lang = "fr"
			}
			members = append(members, m)
		}
		g := annotated(members...)
		files := map[int64]bool{}
		for _, m := range g.Members {
			files[m.ID] = m.HasFiles
		}
		for _, id := range g.SuggestedExcludeIDs {
			if files[id] {
				t.Fatalf("mask %b: suggested excluding row %d, which has files", mask, id)
			}
			if id == g.KeeperID {
				t.Fatalf("mask %b: suggested excluding the keeper", mask)
			}
		}
		if len(g.SuggestedExcludeIDs) > 0 && g.Conflict {
			t.Fatalf("mask %b: suggestion offered on a conflicted group", mask)
		}
	}
}

// TestAnnotateSuggestionNeedsPositiveEvidence is the #3004 review case: no
// conflict is not agreement. A series opener with files next to an empty
// sequel row that matched only by substring must not be offered for
// exclusion; an exact title pair, or a substring pair that shares an ISBN or
// a series position, still is.
func TestAnnotateSuggestionNeedsPositiveEvidence(t *testing.T) {
	t.Run("substring only: withheld", func(t *testing.T) {
		g := annotated(
			evMember{id: 1, title: "Mistborn", ev: withFiles("ebook")},
			evMember{id: 2, title: "Mistborn: The Hero of Ages"},
		)
		if g.KeeperID != 1 {
			t.Errorf("keeper = %d, want 1", g.KeeperID)
		}
		if len(g.SuggestedExcludeIDs) != 0 || g.SuggestionWithheld != WithheldNoEvidence {
			t.Errorf("suggested = %v withheld = %q; want none, no-evidence", g.SuggestedExcludeIDs, g.SuggestionWithheld)
		}
	})
	t.Run("exact title: offered", func(t *testing.T) {
		g := annotated(
			evMember{id: 1, title: "Dune", ev: withFiles("ebook")},
			evMember{id: 2, title: "Dune"},
		)
		if !reflect.DeepEqual(g.SuggestedExcludeIDs, []int64{2}) || g.SuggestionWithheld != "" {
			t.Errorf("suggested = %v withheld = %q; want [2]", g.SuggestedExcludeIDs, g.SuggestionWithheld)
		}
	})
	t.Run("article and edition rules count", func(t *testing.T) {
		g := annotated(
			evMember{id: 1, title: "The Nightingale (Unabridged)", ev: withFiles("audiobook")},
			evMember{id: 2, title: "Nightingale"},
		)
		if !reflect.DeepEqual(g.SuggestedExcludeIDs, []int64{2}) {
			t.Errorf("suggested = %v, want [2]", g.SuggestedExcludeIDs)
		}
	})
	t.Run("substring plus shared ISBN: offered", func(t *testing.T) {
		keeper := NewEvidence(models.Book{}, []FileRef{{Kind: "ebook", Path: "/x.epub"}}, []string{"9780553418026"}, nil, nil)
		g := annotated(
			evMember{id: 1, title: "Mistborn", ev: keeper},
			evMember{id: 2, title: "Mistborn: The Final Empire", ev: NewEvidence(models.Book{}, nil, []string{"0553418025"}, nil, nil)},
		)
		if !reflect.DeepEqual(g.SuggestedExcludeIDs, []int64{2}) {
			t.Errorf("suggested = %v, want [2]", g.SuggestedExcludeIDs)
		}
	})
	t.Run("substring plus same series position: offered", func(t *testing.T) {
		series := []SeriesEvidence{{SeriesID: 3, Title: "Mistborn", Position: "1"}}
		g := annotated(
			evMember{id: 1, title: "Mistborn", ev: NewEvidence(models.Book{}, []FileRef{{Kind: "ebook", Path: "/x.epub"}}, nil, nil, series)},
			evMember{id: 2, title: "Mistborn: The Final Empire", ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 3, Title: "Mistborn", Position: "1.0"}})},
		)
		if !reflect.DeepEqual(g.SuggestedExcludeIDs, []int64{2}) {
			t.Errorf("suggested = %v, want [2]", g.SuggestedExcludeIDs)
		}
	})
	t.Run("one unlinked empty row withholds the whole suggestion", func(t *testing.T) {
		g := annotated(
			evMember{id: 1, title: "Mistborn", ev: withFiles("ebook")},
			evMember{id: 2, title: "Mistborn"},
			evMember{id: 3, title: "Mistborn: The Hero of Ages"},
		)
		if len(g.SuggestedExcludeIDs) != 0 || g.SuggestionWithheld != WithheldNoEvidence {
			t.Errorf("suggested = %v withheld = %q", g.SuggestedExcludeIDs, g.SuggestionWithheld)
		}
	})
	t.Run("withheld reasons", func(t *testing.T) {
		if g := annotated(evMember{id: 1}, evMember{id: 2}); g.SuggestionWithheld != WithheldNoFiles {
			t.Errorf("no files: %q", g.SuggestionWithheld)
		}
		if g := annotated(evMember{id: 1, ev: withFiles("ebook")}, evMember{id: 2, ev: withFiles("ebook")}); g.SuggestionWithheld != WithheldSeveralWithFiles {
			t.Errorf("several: %q", g.SuggestionWithheld)
		}
		if g := annotated(evMember{id: 1, lang: "en", ev: withFiles("ebook")}, evMember{id: 2, lang: "fr"}); g.SuggestionWithheld != WithheldConflict {
			t.Errorf("conflict: %q", g.SuggestionWithheld)
		}
	})
}

func TestAnnotateSeriesPositionsCompareNumerically(t *testing.T) {
	g := annotated(
		evMember{id: 1, ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 4, Title: "Saga", Position: "1"}})},
		evMember{id: 2, ev: NewEvidence(models.Book{}, nil, nil, nil, []SeriesEvidence{{SeriesID: 4, Title: "Saga", Position: "1.0"}})},
	)
	if g.Conflict {
		t.Fatalf("1 and 1.0 reported as a conflict: %+v", g.Signals)
	}
	if kinds := signalKinds(g); len(kinds) != 1 || kinds[0] != SignalSameSeriesPosition {
		t.Errorf("signals = %v, want same-series-position", kinds)
	}
}

func TestAnnotateUndeterminedLanguageIsUnknown(t *testing.T) {
	for _, code := range []string{"und", "mul", "zxx", "UND"} {
		g := annotated(evMember{id: 1, lang: "en"}, evMember{id: 2, lang: code})
		if g.Conflict {
			t.Errorf("%q vs en reported as a language conflict", code)
		}
	}
}
