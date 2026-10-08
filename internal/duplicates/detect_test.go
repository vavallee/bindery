package duplicates

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func authoredBook(id, authorID int64, title string, excluded bool) models.Book {
	return models.Book{ID: id, AuthorID: authorID, Title: title, Excluded: excluded}
}

// groupShape reduces groups to what detection decides (key, author, rules,
// member IDs and their rules) so two detection paths can be compared
// regardless of annotation.
type groupShape struct {
	Key      string
	AuthorID int64
	Rules    []RuleID
	Members  []int64
	PerRules [][]RuleID
}

func shapes(groups []Group) []groupShape {
	out := make([]groupShape, 0, len(groups))
	for _, g := range groups {
		s := groupShape{Key: g.Key, AuthorID: g.AuthorID, Rules: g.Rules}
		for _, m := range g.Members {
			s.Members = append(s.Members, m.ID)
			s.PerRules = append(s.PerRules, m.Rules)
		}
		out = append(out, s)
	}
	return out
}

// TestDetectIsScanPlusActiveFilter pins the refactor: Detect must return
// exactly what the per-author handler computed inline before #2999 (Scan,
// then keep groups with at least two non-excluded members).
func TestDetectIsScanPlusActiveFilter(t *testing.T) {
	books := []models.Book{
		authoredBook(1, 7, "Dune", true),
		authoredBook(2, 7, "Dune", true),
		authoredBook(3, 7, "The Martian", false),
		authoredBook(4, 7, "The Martian", false),
		authoredBook(5, 7, "Martian", true),
		authoredBook(6, 7, "Hyperion", false),
		authoredBook(7, 7, "Hyperion", true),
		authoredBook(8, 7, "Mistborn", false),
		authoredBook(9, 7, "Mistborn: The Final Empire", false),
	}

	var want []Group
	for _, g := range Scan(books, nil) {
		n := 0
		for _, m := range g.Members {
			if !m.Excluded {
				n++
			}
		}
		if n >= 2 {
			g.AuthorID = 7
			want = append(want, g)
		}
	}

	got := Detect(books, nil)
	if !reflect.DeepEqual(shapes(got), shapes(want)) {
		t.Fatalf("Detect differs from Scan + filter:\ngot  %+v\nwant %+v", shapes(got), shapes(want))
	}
	if len(got) != 2 || got[0].Key != "mistborn" || got[1].Key != "themartian" {
		t.Fatalf("groups = %+v, want mistborn and themartian only", shapes(got))
	}
}

func TestDetectNeverNil(t *testing.T) {
	if got := Detect(nil, nil); got == nil {
		t.Error("Detect(nil) = nil, want empty slice")
	}
	if got := DetectByAuthor(nil, nil); got == nil {
		t.Error("DetectByAuthor(nil) = nil, want empty slice")
	}
}

// TestDetectByAuthorMatchesPerAuthorDetect: the library-wide view must find
// exactly the groups each author's own window would, in author order, and
// must never group the same title across two authors.
func TestDetectByAuthorMatchesPerAuthorDetect(t *testing.T) {
	// Interleaved on purpose: the library query orders by author, but the
	// function must not depend on it.
	books := []models.Book{
		authoredBook(1, 2, "Dune", false),
		authoredBook(2, 1, "Dune", false),
		authoredBook(3, 2, "Dune", false),
		authoredBook(4, 1, "Hyperion", false),
		authoredBook(5, 3, "The Martian", false),
		authoredBook(6, 3, "Martian", false),
		authoredBook(7, 1, "Endymion", false),
		authoredBook(8, 3, "Artemis", false),
	}
	byAuthor := map[int64][]models.Book{}
	for _, b := range books {
		byAuthor[b.AuthorID] = append(byAuthor[b.AuthorID], b)
	}
	var want []Group
	for _, id := range []int64{1, 2, 3} {
		want = append(want, Detect(byAuthor[id], nil)...)
	}

	got := DetectByAuthor(append([]models.Book{}, books...), nil)
	if !reflect.DeepEqual(shapes(got), shapes(want)) {
		t.Fatalf("DetectByAuthor differs from per-author Detect:\ngot  %+v\nwant %+v", shapes(got), shapes(want))
	}
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2 (author 2's Dune pair, author 3's Martian pair); author 1's lone Dune must not join author 2's", len(got))
	}
	if got[0].AuthorID != 2 || got[1].AuthorID != 3 {
		t.Errorf("group authors = %d, %d; want 2, 3", got[0].AuthorID, got[1].AuthorID)
	}
}

// TestDetectByAuthorScale is the library-wide budget: the reporter's library
// is about 4,500 books. 10,000 books over 500 authors plus one 1,500 book
// author (an anthology "Various" row) must stay well inside a page load.
func TestDetectByAuthorScale(t *testing.T) {
	var books []models.Book
	id := int64(0)
	for a := int64(1); a <= 500; a++ {
		for i := 0; i < 17; i++ {
			id++
			books = append(books, authoredBook(id, a, fmt.Sprintf("Author %d Book %d", a, i), false))
		}
		id++
		books = append(books, authoredBook(id, a, fmt.Sprintf("Author %d Book 0", a), false))
	}
	for i := 0; i < 1500; i++ {
		id++
		books = append(books, authoredBook(id, 9999, fmt.Sprintf("Anthology Volume %d", i), false))
	}

	start := time.Now()
	groups := DetectByAuthor(books, nil)
	elapsed := time.Since(start)

	budget := 2 * time.Second
	if raceEnabled {
		budget *= 10
	}
	if elapsed > budget {
		t.Errorf("DetectByAuthor(%d books) took %s, budget %s", len(books), elapsed, budget)
	}
	if len(groups) != 500 {
		t.Errorf("got %d groups, want 500 (one planted pair per author)", len(groups))
	}
}
