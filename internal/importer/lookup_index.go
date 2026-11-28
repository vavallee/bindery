package importer

import (
	"slices"
	"sync"

	"github.com/vavallee/bindery/internal/models"
)

// catalogueIndex answers matchUnit's two catalogue scans, the ASIN lookup and
// the title match, without comparing a unit against every book.
//
// matchUnit used to run titleMatch against every book for every reading of
// every unit: up to three readings per unit and 1000 units per scan request,
// so a 40k book catalogue meant up to 120 million titleMatch calls, each
// normalising both titles again. A 1000 unit scan took 110 s against 40k books.
//
// The index changes which books are compared, never how. It yields a
// candidate set that is a superset of the books titleMatch can accept, in
// catalogue order, and matchUnit then runs titleMatch's own comparison
// (titleMatchForms) over those candidates only. A book is a candidate when
// either
//
//   - its normalizeTitle form equals the unit title's (titleMatch's exact
//     path), found through byNorm; or
//   - it shares at least requiredOverlap distinct significant tokens with the
//     unit title, counted through the postings lists. titleWordsMatch accepts
//     nothing below that overlap, and the volume veto only ever rejects.
//
// Every other book fails both of titleMatch's accepting branches, so leaving
// it out cannot change a result. Candidates are evaluated in ascending
// catalogue position, the order the linear scan visited them, so the titled
// list, and with it which book wins among several, is the same.
//
// The index is built once per LookupBatchLayout call from the catalogue that
// call loaded, read concurrently by the unit workers, and never written after
// newCatalogueIndex returns. Memory: the per book titleForms plus one int32
// per (book, distinct token) posting; measured at about 11 MB for 40k generated books
// (see TestCatalogueIndexMemory).
type catalogueIndex struct {
	books []models.Book
	forms []titleForms

	// asin maps an ASIN to the first catalogue position carrying it, the book
	// the linear scan returned. Books with no ASIN are not indexed.
	asin map[string]int32
	// byNorm maps a normalizeTitle form to the catalogue positions with that
	// form, ascending.
	byNorm map[string][]int32
	// postings maps a significant title token to the catalogue positions whose
	// title carries it, ascending, each position at most once per token.
	postings map[string][]int32

	scratch sync.Pool // *indexScratch
}

// indexScratch is one worker's candidate counting state, reused across units
// so a lookup allocates nothing proportional to the catalogue.
type indexScratch struct {
	count   []uint8 // shared tokens per catalogue position; 255 marks an exact title
	touched []int32 // positions with a non-zero count, to reset afterwards
}

// exactMark is the count given to a byNorm candidate: above any overlap
// requiredOverlap can ask for, so it always survives the threshold.
const exactMark = 255

func newCatalogueIndex(books []models.Book) *catalogueIndex {
	x := &catalogueIndex{
		books:    books,
		forms:    make([]titleForms, len(books)),
		asin:     make(map[string]int32),
		byNorm:   make(map[string][]int32, len(books)),
		postings: make(map[string][]int32),
	}
	for i := range books {
		pos := int32(i) //nolint:gosec // G115: a catalogue never approaches 2^31 books
		if a := books[i].ASIN; a != "" {
			if _, seen := x.asin[a]; !seen {
				x.asin[a] = pos
			}
		}
		if books[i].Title == "" {
			// titleMatch rejects an empty book title outright.
			continue
		}
		f := titleFormsOf(books[i].Title)
		x.forms[i] = f
		x.byNorm[f.norm] = append(x.byNorm[f.norm], pos)
		for _, t := range f.toks {
			x.postings[t] = append(x.postings[t], pos)
		}
	}
	n := len(books)
	x.scratch.New = func() any { return &indexScratch{count: make([]uint8, n)} }
	return x
}

// byASIN returns the book the linear ASIN scan would have returned for asin.
func (x *catalogueIndex) byASIN(asin string) (*models.Book, bool) {
	if asin == "" {
		return nil, false
	}
	pos, ok := x.asin[asin]
	if !ok {
		return nil, false
	}
	return &x.books[pos], true
}

// titleMatches returns every book titleMatch accepts for title, in catalogue
// order: exactly what appending each matching book in a loop over the whole
// catalogue produced.
func (x *catalogueIndex) titleMatches(title string) []models.Book {
	if title == "" {
		return nil
	}
	pf := titleFormsOf(title)
	cands := x.candidates(pf)
	var out []models.Book
	for _, pos := range cands {
		if titleMatchForms(x.forms[pos], pf) {
			out = append(out, x.books[pos])
		}
	}
	return out
}

// candidates returns the catalogue positions that could pass titleMatchForms
// against pf, ascending. See catalogueIndex for why the set is a superset.
func (x *catalogueIndex) candidates(pf titleForms) []int32 {
	s := x.scratch.Get().(*indexScratch)
	defer x.scratch.Put(s)

	for _, pos := range x.byNorm[pf.norm] {
		if s.count[pos] == 0 {
			s.touched = append(s.touched, pos)
		}
		s.count[pos] = exactMark
	}
	for _, t := range pf.toks {
		for _, pos := range x.postings[t] {
			switch s.count[pos] {
			case 0:
				s.touched = append(s.touched, pos)
				s.count[pos] = 1
			case exactMark:
			default:
				if s.count[pos] < exactMark-1 {
					s.count[pos]++
				}
			}
		}
	}

	var out []int32
	for _, pos := range s.touched {
		c := s.count[pos]
		s.count[pos] = 0
		if c == exactMark || int(c) >= requiredOverlap(len(x.forms[pos].toks), len(pf.toks)) {
			out = append(out, pos)
		}
	}
	s.touched = s.touched[:0]
	slices.Sort(out)
	return out
}
