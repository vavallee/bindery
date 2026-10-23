package indexer

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// Tests for the third review round on #3268.

// subtitledSeriesBooks are "Main: Subtitle" books after the first of their
// series. The plain "Author - Main (epub)" release is the commonest form and
// must be approved: a stored position of 2 or more is not evidence that the
// release is wrong.
var subtitledSeriesBooks = []struct {
	title, author string
	releases      []string
}{
	{"Caliban's War: A Novel", "James S. A. Corey", []string{"James S. A. Corey - Caliban's War (epub)", "James S. A. Corey - Caliban's War (Unabridged) MP3"}},
	{"Words of Radiance: Book Two of the Stormlight Archive", "Brandon Sanderson", []string{"Brandon Sanderson - Words of Radiance (epub)", "Brandon Sanderson - Words of Radiance (Unabridged) MP3"}},
	{"A Clash of Kings: Book Two of A Song of Ice and Fire", "George R. R. Martin", []string{"George R. R. Martin - A Clash of Kings (epub)", "George R. R. Martin - A Clash of Kings (Unabridged) MP3"}},
	{"Catching Fire: The Second Book of the Hunger Games", "Suzanne Collins", []string{"Suzanne Collins - Catching Fire (epub)", "Suzanne Collins - Catching Fire (Unabridged) MP3"}},
	{"The Two Towers: Being the Second Part of The Lord of the Rings", "J.R.R. Tolkien", []string{"J.R.R. Tolkien - The Two Towers (epub)", "J.R.R. Tolkien - The Two Towers (Unabridged) MP3"}},
	{"Foundation and Empire: Book Two", "Isaac Asimov", []string{"Isaac Asimov - Foundation and Empire (epub)", "Isaac Asimov - Foundation and Empire (Unabridged) MP3"}},
	{"Dune Messiah: Dune Chronicles, Book 2", "Frank Herbert", []string{"Frank Herbert - Dune Messiah (epub)", "Frank Herbert - Dune Messiah (Unabridged) MP3"}},
}

func TestSubtitledLaterSeriesBooksKeepTheirPlainRelease(t *testing.T) {
	contexts := map[string]func(pos string) bookContext{
		"no context":    func(string) bookContext { return bookContext{} },
		"position only": func(pos string) bookContext { return bookContext{seriesPosition: pos} },
		"full context": func(pos string) bookContext {
			return bookContext{seriesPosition: pos, seriesTitle: "The Series", otherTitles: []string{"Leviathan Wakes", "Stormlight Archive Companion", "The Fellowship of the Ring"}}
		},
		"series as book": func(pos string) bookContext {
			return bookContext{seriesPosition: pos, seriesTitle: "The Series", otherTitles: []string{"The Series"}}
		},
	}
	for _, b := range subtitledSeriesBooks {
		for _, pos := range []string{"2", "3", "12"} {
			for cname, mk := range contexts {
				for _, rel := range b.releases {
					if kept, reason := relevanceVerdict(b.title, b.author, rel, mk(pos)); !kept {
						t.Errorf("%q at position %s (%s): %q dropped (%s)", b.title, pos, cname, rel, reason)
					}
				}
			}
		}
	}
}

// TestMainTitleOnlySignalsThatRemain says which signal catches each reporter
// case now that a stored position alone is not a trigger.
func TestMainTitleOnlySignalsThatRemain(t *testing.T) {
	// Signal (a): the main title is another library book that is not the series.
	got := batchVerdicts("Legion: Skin Deep", "Brandon Sanderson",
		[]string{"Brandon Sanderson - Legion (2012) MP3", "Brandon Sanderson - Legion Novella (2012) MP3"},
		bookContext{seriesPosition: "2", otherTitles: []string{"Legion"}})
	for rel, v := range got {
		if v != reasonMainTitleOnlySequel {
			t.Errorf("signal (a): %q verdict %q", rel, v)
		}
	}
	// The batch rule: the full title of the same media type is in the batch.
	got = batchVerdicts("Legion: Skin Deep", "Brandon Sanderson",
		[]string{"Brandon Sanderson - Legion (2012) MP3", "Brandon Sanderson - Legion - Skin Deep (2014) MP3"}, bookContext{})
	if got["Brandon Sanderson - Legion (2012) MP3"] != reasonMainTitleOnlyFullFound {
		t.Errorf("batch rule: %q", got["Brandon Sanderson - Legion (2012) MP3"])
	}
	// Signal (b): the subtitle numbers the book and the release names a
	// different number. No number named means no evidence.
	if k, _ := relevanceVerdict("Dune: Part Two", "Frank Herbert", "Frank Herbert - Dune III MP3", bookContext{}); k {
		t.Errorf("signal (b): a different number must reject")
	}
	if k, reason := relevanceVerdict("Dune: Part Two", "Frank Herbert", "Frank Herbert - Dune (Unabridged) MP3", bookContext{}); !k {
		t.Errorf("signal (b): a release naming no number must be kept (%s)", reason)
	}
	// Rule A with the position: High Druid of Shannara 3 - Straken.
	if k, reason := relevanceVerdict("High Druid of Shannara: Jarka Ruus", "Terry Brooks",
		"Terry Brooks - The High Druid of Shannara 3 - Straken MP3", bookContext{seriesPosition: "1"}); k || reason != reasonLaterNumber {
		t.Errorf("rule A: kept=%v reason=%q", k, reason)
	}
}

// A library title used as a series label is not another book.
func TestSeriesLabelIsNotAnotherBook(t *testing.T) {
	keep := []struct {
		title, author, release string
		lib                    []string
	}{
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower 1 (epub)", []string{"The Dark Tower"}},
		{"Battle of the Dum Diddys", "R. L. Stine", "R L Stine - Battle of the Dum Diddys - Rotten School 12 (epub)", []string{"Rotten School"}},
		{"Battle of the Dum Diddys", "R. L. Stine", "R L Stine - Battle of the Dum Diddys (Rotten School Book 12) (epub)", []string{"Rotten School"}},
		{"Battle of the Dum Diddys", "R. L. Stine", "R L Stine - Battle of the Dum Diddys (Rotten School, #12) (epub)", []string{"Rotten School"}},
		{"Katt Loves Dogg", "James Patterson", "James Patterson - Katt Loves Dogg - Katt vs Dogg 02 (epub)", []string{"Katt vs. Dogg"}},
		{"Katt Loves Dogg", "James Patterson", "James Patterson - Katt Loves Dogg (Katt vs. Dogg Book Two) (epub)", []string{"Katt vs. Dogg"}},
		{"The Name of the Wind", "Patrick Rothfuss", "Patrick Rothfuss - The Name of the Wind - The Kingkiller Chronicle 1 (epub)", []string{"The Kingkiller Chronicle"}},
	}
	for _, c := range keep {
		if k, reason := relevanceVerdict(c.title, c.author, c.release, bookContext{otherTitles: c.lib}); !k {
			t.Errorf("%q dropped for %q with no series link (%s)", c.release, c.title, reason)
		}
	}
	reject := []struct {
		title, author, release string
		lib                    []string
	}{
		{"King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars 02 - Rule of Wolves.par2", []string{"Rule of Wolves"}},
		{"Katt vs. Dogg", "James Patterson", "James Patterson & Chris Grabenstein - [Katt vs Dogg 02] - Katt Loves Dogg (retail) (epub)", []string{"Katt Loves Dogg"}},
		{"Rotten School", "R. L. Stine", "R L Stine - [Rotten School 12] - Battle of the Dum Diddys (retail) (epub)", []string{"Battle of the Dum Diddys"}},
		// The library title appears twice: once as a label, once as a title.
		{"The Gunslinger", "Stephen King", "Stephen King - The Gunslinger - The Dark Tower 1 - The Dark Tower (epub)", []string{"The Dark Tower"}},
	}
	for _, c := range reject {
		if k, _ := relevanceVerdict(c.title, c.author, c.release, bookContext{otherTitles: c.lib}); k {
			t.Errorf("%q kept for %q, want rejected", c.release, c.title)
		}
	}
	// The limit: a different book whose title ends with a number is read as a
	// series label and skipped.
	if k, _ := relevanceVerdict("King of Scars", "Leigh Bardugo", "Leigh Bardugo - King of Scars - Rule of Wolves 2 (epub)",
		bookContext{otherTitles: []string{"Rule of Wolves"}}); !k {
		t.Errorf("limit changed: a library title followed by a number is read as a series label")
	}
}

// Rule A reads capital roman numerals once the position is known.
func TestRuleAReadsRomanNumerals(t *testing.T) {
	type c struct {
		title, author, release, pos string
		wantKept                    bool
	}
	cases := []c{
		{"Mistborn", "Brandon Sanderson", "Brandon Sanderson - Mistborn III (epub)", "2", false},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune III", "2", false},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune III - Messiah", "1", false},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune (III) epub", "1", false},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune Book III epub", "1", false},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune XXX", "1", false},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune XXXI", "1", true},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune iii", "1", true},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune II epub", "2", true},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune II", "1-3", true},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune III Mix epub", "1", true},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune III", "", true},
		{"Rocky II", "Sylvester Stallone", "Sylvester Stallone - Rocky II (retail) (epub)", "2", true},
		{"Rocky II", "Sylvester Stallone", "Sylvester Stallone - Rocky II (retail) (epub)", "1", true},
		{"Henry V", "William Shakespeare", "William Shakespeare - Henry V (epub)", "1", true},
		{"Henry V", "William Shakespeare", "William Shakespeare - Henry V (epub)", "5", true},
		{"Fear", "Bob Woodward", "Bob Woodward - Fear - I Know What You Did (epub)", "1", true},
		{"Fear", "Bob Woodward", "Bob Woodward - Fear V Is for Victory (epub)", "1", true},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune I (epub)", "2", true},
	}
	for _, x := range cases {
		kept, reason := relevanceVerdict(x.title, x.author, x.release, bookContext{seriesPosition: x.pos})
		if kept != x.wantKept {
			t.Errorf("%q wanted %q at position %q: kept=%v (%s), want %v", x.release, x.title, x.pos, kept, reason, x.wantKept)
		}
	}
}

// The n < 2 bound of rule A: a zero padded 1 never rejects, whatever the position.
func TestRuleAIgnoresNumberOne(t *testing.T) {
	for _, rel := range []string{"Frank Herbert - Dune 01", "Frank Herbert - Dune 1", "Frank Herbert - Dune Book 1 - Messiah", "Frank Herbert - Dune 01 - Messiah"} {
		for _, pos := range []string{"2", "5", "1-3"} {
			if k, reason := relevanceVerdict("Dune", "Frank Herbert", rel, bookContext{seriesPosition: pos}); !k {
				t.Errorf("%q dropped at position %s (%s)", rel, pos, reason)
			}
		}
	}
	if k, _ := relevanceVerdict("Dune", "Frank Herbert", "Frank Herbert - Dune 02", bookContext{seriesPosition: "1"}); k {
		t.Errorf("a 2 at position 1 must reject")
	}
}

// Roman numerals are read for equality only in capitals and up to 30.
func TestNumbersAfterRomanRules(t *testing.T) {
	ident := titleIdentityKeywords("Dune")
	read := func(raw string) []int {
		return numbersAfter(raw, NormalizeRelease(raw), false, ident)
	}
	if got := read("Dune XXX"); len(got) != 1 || got[0] != 30 {
		t.Errorf("XXX = %v, want [30]", got)
	}
	if got := read("Dune XXXI"); len(got) != 0 {
		t.Errorf("XXXI = %v, want none", got)
	}
	if got := read("Dune xx"); len(got) != 0 {
		t.Errorf("lowercase xx = %v, want none", got)
	}
	if got := read("Dune XX"); len(got) != 1 || got[0] != 20 {
		t.Errorf("XX = %v, want [20]", got)
	}
}

// Derivative wording and furniture decide whether a full title result counts
// for the batch rule. Each word is tested on its own, placed where only the
// derivative wording check can see it (before the title, after the author),
// and the furniture check is tested with words the derivative list lacks.
func TestFullTitleCountsDerivativeWording(t *testing.T) {
	const title = "Sapiens: A Brief History of Humankind"
	ident := titleIdentityKeywords(title)
	authors := [][]string{authorTokens("Yuval Noah Harari")}
	counts := func(raw string) bool {
		return fullTitleCounts(raw, NormalizeRelease(raw), authors, ident)
	}
	for _, w := range []string{"Summary", "Summaries", "Analysis", "Analyses", "Study Guide", "Workbook", "Companion", "Review", "Reviews", "Notes", "Guide", "CliffsNotes", "SparkNotes", "Highlights", "Synopsis", "Key Takeaways", "Insights", "Cheat Sheet", "Book Review"} {
		before := "Yuval Noah Harari - " + w + " Sapiens: A Brief History of Humankind M4B"
		if counts(before) {
			t.Errorf("%q: derivative word %q before the title must not count", before, w)
		}
	}
	for _, rel := range []string{
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Cheat Sheet (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Key Takeaways (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Book Review (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Large Print (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (Dramatized) MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (Illustrated Edition) (epub)",
	} {
		if counts(rel) {
			t.Errorf("%q must not count for the batch rule", rel)
		}
	}
	for _, rel := range []string{
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (Unabridged) 64kbps MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (2014) (retail) (epub)",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (Unabridged) (retail) (epub)",
	} {
		if !counts(rel) {
			t.Errorf("%q is the book itself and must count", rel)
		}
	}
}

// The same cases through the whole filter: a same format derivative or a
// result that is not clean leaves the main title only release approved.
func TestBatchRuleIgnoresDerivativesSameFormat(t *testing.T) {
	const main = "Yuval Noah Harari - Sapiens (2014) MP3"
	for _, other := range []string{
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Summary and Analysis MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Study Guide MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Cheat Sheet MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Key Takeaways MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Book Review MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Workbook MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Analysis MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind - Large Print MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (Dramatized) MP3",
		"Yuval Noah Harari - Sapiens: A Brief History of Humankind (Illustrated Edition) MP3",
	} {
		got := batchVerdicts("Sapiens: A Brief History of Humankind", "Yuval Noah Harari", []string{main, other}, bookContext{})
		if v, ok := got[main]; !ok || v != "" {
			t.Errorf("main title release dropped (%q) next to %q", v, other)
		}
	}
}

// Rule B skips a library title contained in the wanted title, and the series
// label skip needs a number: the unit checks that fail if either is removed.
func TestRuleBSkips(t *testing.T) {
	tr := newTrailingRules("The Hobbit: There and Back Again", bookContext{otherTitles: []string{"There and Back Again", "Brandon Sanderson Companion"}},
		titleIdentityKeywords("The Hobbit: There and Back Again"))
	for _, ot := range tr.others {
		if strings.Join(ot.ident, " ") == "there back again" {
			t.Errorf("the library title contained in the wanted title was kept: %v", ot.ident)
		}
	}
	rest := func(s string) []relTok {
		var out []relTok
		for _, w := range strings.Fields(s) {
			out = append(out, relTok{text: w, raw: w, sep: " "})
		}
		return out
	}
	trl := newTrailingRules("The Gunslinger", bookContext{otherTitles: []string{"The Dark Tower"}}, titleIdentityKeywords("The Gunslinger"))
	if trl.otherTitleIn(rest("dark tower 1 epub")) != "" {
		t.Errorf("a number after the library title makes it a series label")
	}
	if trl.otherTitleIn(rest("dark tower book 1 epub")) != "" {
		t.Errorf("a marker and a number after the library title makes it a series label")
	}
	if trl.otherTitleIn(rest("dark tower book one epub")) != "" {
		t.Errorf("a marker and a number word after the library title makes it a series label")
	}
	if trl.otherTitleIn(rest("dark tower epub")) != reasonAnotherTitle {
		t.Errorf("a library title with no number after it is another book")
	}
	if trl.otherTitleIn(rest("dark tower book epub")) != reasonAnotherTitle {
		t.Errorf("a marker with no number is not a series label")
	}
}

// WarnLibraryContextMissing logs once however often it is called.
func TestWarnLibraryContextMissingLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	var once sync.Once
	for i := 0; i < 5; i++ {
		WarnLibraryContextMissing(&once, "test", true, false)
	}
	if n := strings.Count(buf.String(), "missing library context"); n != 1 {
		t.Errorf("logged %d times, want 1: %s", n, buf.String())
	}
	buf.Reset()
	var quiet sync.Once
	WarnLibraryContextMissing(&quiet, "test", false, false)
	if buf.Len() != 0 {
		t.Errorf("nothing missing must not log, got %q", buf.String())
	}
}
