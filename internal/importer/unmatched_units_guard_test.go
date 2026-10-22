package importer

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestRankCandidates_LooseGuessesAreKept: below the scan's gate a suggestion
// is a loose guess for a person to judge, and different words are what a loose
// guess looks like, so the different-work guard leaves it alone.
func TestRankCandidates_LooseGuessesAreKept(t *testing.T) {
	martian := &models.Book{ID: 7, AuthorID: 1, Title: "The Martian"}
	catalogue := []scanBook{newScanBook(martian, true)}
	got := rankCandidates("A Martyrs Tale", "", catalogue, catalogue, map[int64][]int{1: {0}}, map[int64]bool{1: true})
	if len(got) != 1 || got[0].Score >= differentWorkSuggestionFloor {
		t.Errorf("candidates = %+v, want The Martian as a loose guess under %.2f", got, differentWorkSuggestionFloor)
	}
}

// TestRankCandidates_DifferentWorkGuard: a similar title of another work is
// not suggested, unless the book folder's title names it (the file's own title
// can be a chapter or a stray name while the folder says which book it is).
func TestRankCandidates_DifferentWorkGuard(t *testing.T) {
	phoenix := &models.Book{ID: 3328, AuthorID: 1, Title: "Harry Potter en de Orde van de Feniks"}
	catalogue := []scanBook{newScanBook(phoenix, true)}
	byAuthor := map[int64][]int{1: {0}}
	authors := map[int64]bool{1: true}

	tests := []struct {
		name         string
		title        string
		layout       string
		wantSuggests bool
	}{
		{"file and folder both name another work", "Harry Potter en de Relieken van de Dood", "Harry Potter en de Relieken van de Dood", false},
		{"no folder title, another work", "Harry Potter en de Relieken van de Dood", "", false},
		{"folder names the candidate", "Harry Potter en de Relieken van de Dood", "Harry Potter en de Orde van de Feniks", true},
		{"same work", "Harry Potter en de Orde van de Feniks", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rankCandidates(tc.title, tc.layout, catalogue, catalogue, byAuthor, authors)
			if (len(got) > 0) != tc.wantSuggests {
				t.Errorf("rankCandidates(%q, %q) = %+v, want suggestion = %v", tc.title, tc.layout, got, tc.wantSuggests)
			}
		})
	}
}
