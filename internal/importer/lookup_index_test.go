package importer

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The ref* functions below are verbatim copies of titleMatch, titleWordsMatch
// and matchUnit as they were before catalogueIndex (main at b1ec22be): every
// book compared in a loop, both titles normalised on every call. They exist
// only so the equivalence tests can hold the indexed path to the old results.

func refTitleTokenSet(s string) map[string]bool {
	toks := titleSigTokens(s)
	set := make(map[string]bool, len(toks))
	for _, w := range toks {
		set[w] = true
	}
	return set
}

func refTitleMatch(bookTitle, parsedTitle string) bool {
	if parsedTitle == "" || bookTitle == "" {
		return false
	}
	if normalizeTitle(bookTitle) == normalizeTitle(parsedTitle) {
		return true
	}
	if differentVolumes(bookTitle, parsedTitle) {
		return false
	}
	return refTitleWordsMatch(bookTitle, parsedTitle)
}

func refTitleWordsMatch(bookTitle, parsedTitle string) bool {
	if parsedTitle == "" || bookTitle == "" {
		return false
	}
	if normalizeTitle(bookTitle) == normalizeTitle(parsedTitle) {
		return true
	}
	btSet := refTitleTokenSet(bookTitle)
	ptSet := refTitleTokenSet(parsedTitle)
	if len(btSet) == 0 || len(ptSet) == 0 {
		return false
	}
	overlap := 0
	for w := range ptSet {
		if btSet[w] {
			overlap++
		}
	}
	minLen := len(ptSet)
	if len(btSet) < minLen {
		minLen = len(btSet)
	}
	required := 2
	if minLen < 2 {
		required = minLen
	}
	if required == 0 {
		required = 1
	}
	return overlap >= required
}

func refMatchUnit(parsed ParsedFile, embedded EpubMetadata, layoutAuthor, layoutTitle, overrideAuthor string, books []models.Book, authorNames map[int64]string, authorMatch func(parsed, catalogue string) bool) LookupResult {
	effTitle := firstNonEmpty(embedded.Title, parsed.Title, layoutTitle)
	effAuthor := firstNonEmpty(embedded.Author, parsed.Author, layoutAuthor)

	result := LookupResult{
		ParsedTitle:  effTitle,
		ParsedAuthor: effAuthor,
	}

	if parsed.ASIN != "" {
		for i := range books {
			if books[i].ASIN == parsed.ASIN {
				result.Match = "confident"
				result.Book = &books[i]
				return result
			}
		}
	}

	if effTitle == "" {
		result.Match = "none"
		return result
	}

	var titled []models.Book
	for i := range books {
		if refTitleMatch(books[i].Title, effTitle) {
			titled = append(titled, books[i])
		}
	}
	matches, effAuthor := narrowByAuthor(titled, effAuthor, authorMatch, overrideAuthor, authorNames)
	result.ParsedAuthor = effAuthor

	switch len(matches) {
	case 0:
		result.Match = "none"
	case 1:
		exactTitle := normalizeTitle(matches[0].Title) == normalizeTitle(effTitle)
		authorCorroborated := effAuthor != "" && lookupAuthorMatch(effAuthor, authorNames[matches[0].AuthorID])
		if exactTitle || authorCorroborated {
			result.Match = "confident"
			result.Book = &matches[0]
		} else {
			result.Match = "ambiguous"
			result.Candidates = matches
		}
	default:
		result.Match = "ambiguous"
		result.Candidates = matches
	}
	return result
}

// equivalenceCorpus is the catalogue and the unit readings the equivalence
// test runs: a generated catalogue plus the indexer's search corpus and the
// shared text folding fixtures, and generated unit paths plus the corpus
// releases.
type equivalenceCorpus struct {
	books       []models.Book
	authorNames map[int64]string
	root        string
	paths       []string
	titles      []string // every title the test also compares pairwise
}

func loadEquivalenceCorpus(t *testing.T, generated, units int) equivalenceCorpus {
	t.Helper()
	cat := generateCatalogue(generated, 1234)
	c := equivalenceCorpus{
		books:       cat.books,
		authorNames: make(map[int64]string, len(cat.authors)),
		root:        filepath.Join(string(filepath.Separator), "scan"),
	}
	for _, a := range cat.authors {
		c.authorNames[a.ID] = a.Name
	}
	nextAuthor := int64(len(cat.authors))
	authorID := func(name string) int64 {
		for id, n := range c.authorNames {
			if n == name {
				return id
			}
		}
		nextAuthor++
		c.authorNames[nextAuthor] = name
		return nextAuthor
	}
	addBook := func(title, author string) {
		id := int64(len(c.books) + 1)
		c.books = append(c.books, models.Book{ID: id, AuthorID: authorID(author), Title: title, SortTitle: title, ForeignID: fmt.Sprintf("cb-%d", id)})
	}

	var search []struct {
		WantedTitle  string   `json:"wantedTitle"`
		WantedAuthor string   `json:"wantedAuthor"`
		Release      string   `json:"release"`
		AuthorTitles []string `json:"authorTitles"`
	}
	readJSON(t, filepath.Join("..", "indexer", "testdata", "search_corpus.json"), &search)
	var folds []struct {
		Input string `json:"input"`
		Want  string `json:"want"`
	}
	readJSON(t, filepath.Join("..", "textutil", "testdata", "search_fixtures.json"), &folds)

	for _, e := range search {
		addBook(e.WantedTitle, e.WantedAuthor)
		for _, at := range e.AuthorTitles {
			addBook(at, e.WantedAuthor)
		}
		rel := strings.ReplaceAll(e.Release, "/", " ")
		c.paths = append(c.paths,
			filepath.Join(c.root, rel+".epub"),
			filepath.Join(c.root, e.WantedAuthor, rel+".epub"),
			filepath.Join(c.root, e.WantedAuthor, e.WantedTitle+".m4b"),
			filepath.Join(c.root, e.WantedTitle+" - "+e.WantedAuthor+".epub"),
		)
		c.titles = append(c.titles, e.WantedTitle, e.Release)
	}
	for _, f := range folds {
		addBook(f.Input, "Fixture Author")
		c.paths = append(c.paths, filepath.Join(c.root, "Fixture Author", f.Want+".epub"), filepath.Join(c.root, f.Input+".epub"))
		c.titles = append(c.titles, f.Input, f.Want)
	}
	// Hand made edge cases: article forms, volumes, one word and numeric
	// titles, a title made only of stopwords or punctuation, and a book with
	// no title at all, which nothing may ever match.
	for _, s := range []string{
		"A Darker Shade of Magic", "Darker Shade of Magic, A", "The Fragile Threads of Power",
		"Defiance of the Fall 01", "Defiance of the Fall 17", "The Primal Hunter 3",
		"The Primal Hunter 17: A LitRPG Adventure", "1984", "2001: A Space Odyssey",
		"It", "The", "Of the And", "!!!", "", "Tom Clancy Enemy Contact", "Wife",
		"The Kitchen God's Wife", "11. Track 11",
	} {
		addBook(s, "Edge Author")
		c.titles = append(c.titles, s)
		if s != "" {
			c.paths = append(c.paths, filepath.Join(c.root, "Edge Author", strings.ReplaceAll(s, "/", " ")+".epub"))
		}
	}
	// The generated units come last so the corpus books above are reachable
	// from them too.
	gen := genCatalogue{books: c.books, authors: make([]models.Author, 0, len(c.authorNames))}
	for id := int64(1); id <= nextAuthor; id++ {
		gen.authors = append(gen.authors, models.Author{ID: id, Name: c.authorNames[id]})
	}
	c.paths = append(c.paths, generateUnitPaths(gen, c.root, units, 99)...)
	for i := 0; i < len(c.books); i += 7 {
		c.titles = append(c.titles, c.books[i].Title)
	}
	return c
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // G304: fixed test fixture path
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// unitReading is one matchUnit call lookupUnit can make for a unit.
type unitReading struct {
	parsed         ParsedFile
	embedded       EpubMetadata
	layoutAuthor   string
	layoutTitle    string
	overrideAuthor string
	confident      bool // authorMatch is confidentAuthorMatch, not lookupAuthorMatch
}

// readingsFor returns every reading lookupUnit can try for path (the parse as
// it is, the flipped parse, the author folder override), plus the same
// readings with embedded metadata standing in front, as an EPUB would put it.
func readingsFor(root, path string, embedded []EpubMetadata) []unitReading {
	parsed := ParseFilename(path)
	la, lt, _ := authorTitleFromLayout(path, root)
	la, lt = releaseFolderLayout(la, lt, isFolderUnit(path))
	var out []unitReading
	for _, em := range append([]EpubMetadata{{}}, embedded...) {
		out = append(out,
			unitReading{parsed: parsed, embedded: em, layoutAuthor: la, layoutTitle: lt},
			unitReading{parsed: parsed, embedded: em, layoutAuthor: la, layoutTitle: lt, overrideAuthor: la},
			unitReading{parsed: parsed, embedded: em, layoutAuthor: la, layoutTitle: lt, overrideAuthor: la, confident: true},
		)
		if flipped, ok := flipByLayout(parsed, la); ok {
			out = append(out, unitReading{parsed: flipped, embedded: em, layoutAuthor: la, layoutTitle: lt, overrideAuthor: la, confident: true})
		}
	}
	return out
}

// TestMatchUnitIndexEquivalence holds matchUnit over a catalogueIndex to the
// old linear matchUnit (refMatchUnit) on every reading of every unit in the
// corpus: same Match, same ParsedTitle and ParsedAuthor, the same Book (book
// IDs are unique, so the same tie winner), and the same Candidates in the
// same order.
func TestMatchUnitIndexEquivalence(t *testing.T) {
	t.Parallel()
	// The reference compares every reading with every book, so the corpus is
	// sized for that side's cost, not the index's.
	generated, units := 1000, 800
	if testing.Short() {
		generated, units = 300, 300
	}
	c := loadEquivalenceCorpus(t, generated, units)
	idx := newCatalogueIndex(c.books)

	rng := rand.New(rand.NewSource(5)) //nolint:gosec // G404: deterministic test data
	type job struct {
		path     string
		embedded []EpubMetadata
	}
	jobs := make([]job, len(c.paths))
	for i, p := range c.paths {
		jobs[i].path = p
		switch i % 5 {
		case 1: // an EPUB carrying a catalogue title and author
			b := c.books[rng.Intn(len(c.books))]
			jobs[i].embedded = []EpubMetadata{{Title: b.Title, Author: c.authorNames[b.AuthorID]}}
		case 3: // an EPUB carrying only an author
			b := c.books[rng.Intn(len(c.books))]
			jobs[i].embedded = []EpubMetadata{{Author: c.authorNames[b.AuthorID]}}
		}
	}

	var (
		mu       sync.Mutex
		tally    = map[string]int{}
		failures []string
		readings int
		ties     int
	)
	work := make(chan job)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for j := range work {
				for _, r := range readingsFor(c.root, j.path, j.embedded) {
					match := lookupAuthorMatch
					if r.confident {
						match = confidentAuthorMatch
					}
					want := refMatchUnit(r.parsed, r.embedded, r.layoutAuthor, r.layoutTitle, r.overrideAuthor, c.books, c.authorNames, match)
					got := matchUnit(r.parsed, r.embedded, r.layoutAuthor, r.layoutTitle, r.overrideAuthor, idx, c.authorNames, match)
					same := reflect.DeepEqual(got, want)
					mu.Lock()
					readings++
					tally[want.Match]++
					if len(want.Candidates) > 1 {
						ties++
					}
					if !same && len(failures) < 20 {
						failures = append(failures, fmt.Sprintf("%s %+v:\n  got  %s\n  want %s", j.path, r, describeResult(got), describeResult(want)))
					}
					mu.Unlock()
				}
			}
		})
	}
	for _, j := range jobs {
		work <- j
	}
	close(work)
	wg.Wait()

	for _, f := range failures {
		t.Error(f)
	}
	t.Logf("%d books, %d units, %d readings: %v, %d with several candidates", len(c.books), len(c.paths), readings, tally, ties)
	// The corpus has to reach every outcome or the comparison proves little.
	for _, m := range []string{"confident", "ambiguous", "none"} {
		if tally[m] < 50 {
			t.Errorf("only %d %q readings: the corpus no longer exercises that outcome", tally[m], m)
		}
	}
	if ties < 50 {
		t.Errorf("only %d readings with several candidates: the corpus no longer exercises tie order", ties)
	}
}

func describeResult(r LookupResult) string {
	book := "<nil>"
	if r.Book != nil {
		book = fmt.Sprintf("#%d %q", r.Book.ID, r.Book.Title)
	}
	ids := make([]int64, len(r.Candidates))
	for i, b := range r.Candidates {
		ids[i] = b.ID
	}
	return fmt.Sprintf("match=%s book=%s candidates=%v title=%q author=%q", r.Match, book, ids, r.ParsedTitle, r.ParsedAuthor)
}

// TestTitleMatchFormsEquivalence compares titleMatch and titleWordsMatch,
// now computed over titleForms, with the old per call versions on every pair
// of corpus titles, including the empty and whitespace only ones.
func TestTitleMatchFormsEquivalence(t *testing.T) {
	t.Parallel()
	c := loadEquivalenceCorpus(t, 300, 300)
	titles := append(slices.Clone(c.titles), " ", "  The  ", "a", "Vol 2", "Part 3", "Book 1")
	for _, p := range c.paths {
		titles = append(titles, ParseFilename(p).Title)
	}
	slices.Sort(titles)
	titles = slices.Compact(titles)
	// Every pair runs four comparisons, two of them the slow reference, so
	// keep an even spread of at most 300 titles.
	if len(titles) > 300 {
		step := float64(len(titles)) / 300
		sampled := make([]string, 0, 300)
		for i := range 300 {
			sampled = append(sampled, titles[int(float64(i)*step)])
		}
		titles = sampled
	}
	pairs, matched := 0, 0
	for _, a := range titles {
		for _, b := range titles {
			pairs++
			want := refTitleMatch(a, b)
			if want {
				matched++
			}
			if got := titleMatch(a, b); got != want {
				t.Errorf("titleMatch(%q, %q) = %v, want %v", a, b, got, want)
			}
			if got, want := titleWordsMatch(a, b), refTitleWordsMatch(a, b); got != want {
				t.Errorf("titleWordsMatch(%q, %q) = %v, want %v", a, b, got, want)
			}
		}
	}
	t.Logf("%d titles, %d pairs, %d matched", len(titles), pairs, matched)
}

// TestCatalogueIndexASINFirstWins pins the ASIN tier to the linear scan's
// answer when two books share an ASIN, and to no answer for an empty one.
func TestCatalogueIndexASINFirstWins(t *testing.T) {
	t.Parallel()
	books := []models.Book{
		{ID: 1, Title: "No ASIN"},
		{ID: 2, Title: "First", ASIN: "B000000001"},
		{ID: 3, Title: "Second", ASIN: "B000000001"},
	}
	idx := newCatalogueIndex(books)
	b, ok := idx.byASIN("B000000001")
	if !ok || b != &books[1] {
		t.Fatalf("byASIN = %v, %v; want the first book carrying it", b, ok)
	}
	if b, ok := idx.byASIN(""); ok {
		t.Fatalf("byASIN(\"\") = %v, want no match for an empty ASIN", b)
	}
	if b, ok := idx.byASIN("B999999999"); ok {
		t.Fatalf("byASIN(unknown) = %v, want no match", b)
	}
}

// TestCatalogueIndexMemory measures what the index costs on top of the
// catalogue it indexes at 40k generated books, and fails if that grows past
// 40 MB, so a change to the index layout that multiplies its size shows up.
func TestCatalogueIndexMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 40k book catalogue")
	}
	cat := generateCatalogue(40000, 42)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	idx := newCatalogueIndex(cat.books)
	runtime.GC()
	runtime.ReadMemStats(&after)
	used := int64(after.HeapAlloc) - int64(before.HeapAlloc) //nolint:gosec // G115: heap sizes fit in int64
	runtime.KeepAlive(idx)
	t.Logf("catalogueIndex over %d books: %.1f MB (%d tokens, %d distinct normalised titles)", len(cat.books), float64(used)/(1<<20), len(idx.postings), len(idx.byNorm))
	if used > 40<<20 {
		t.Errorf("catalogueIndex over 40k books uses %.1f MB, want under 40 MB", float64(used)/(1<<20))
	}
}
