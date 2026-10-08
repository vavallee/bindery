package duplicates

import (
	"sort"

	"github.com/vavallee/bindery/internal/models"
)

// Detect is the review policy both duplicate views apply on top of Scan: a
// group is shown only while at least two of its members are not excluded. An
// excluded member stays in a group that is still shown, so the reviewer can
// see what was already set aside and undo it, but a group whose members are
// all excluded but one has nothing left to decide and is dropped.
//
// Each returned group carries the AuthorID of its first member. Detect is
// meant to be called on one author's books at a time (the per-author window
// passes exactly that); DetectByAuthor does the partitioning for a list that
// spans authors. The result is never nil.
func Detect(books []models.Book, seriesSlots map[int64][]SeriesSlot) []Group {
	groups := Scan(books, seriesSlots)
	active := make([]Group, 0, len(groups))
	for i := range groups {
		n := 0
		for _, m := range groups[i].Members {
			if !m.Excluded {
				n++
			}
		}
		if n < 2 {
			continue
		}
		groups[i].AuthorID = groups[i].Members[0].AuthorID
		active = append(active, groups[i])
	}
	return active
}

// DetectByAuthor runs Detect once per author over a list that spans authors,
// so the library-wide view (#2999) reports exactly the groups each author's
// own window would: a title is only ever compared with titles by the same
// author, never across authors.
//
// It sorts books in place by AuthorID (stable, so the caller's order within an
// author survives) and scans each author's run as a sub-slice, which costs no
// copy of the input. Scan's output does not depend on input order within an
// author, so a caller that already ordered by author pays only the check.
// Cost is the sum over authors of each author's Scan, not one O(n²) sweep of
// the whole library. Groups come back ordered by AuthorID, then key. The
// result is never nil.
func DetectByAuthor(books []models.Book, seriesSlots map[int64][]SeriesSlot) []Group {
	sort.SliceStable(books, func(i, j int) bool { return books[i].AuthorID < books[j].AuthorID })
	out := []Group{}
	for start := 0; start < len(books); {
		end := start + 1
		for end < len(books) && books[end].AuthorID == books[start].AuthorID {
			end++
		}
		out = append(out, Detect(books[start:end], seriesSlots)...)
		start = end
	}
	return out
}
