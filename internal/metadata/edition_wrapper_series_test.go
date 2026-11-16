package metadata

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The seven rows from #3317. In five the SERIES title is a real franchise name
// and the wrapper is the one book Hardcover filed under it; in two the series
// title itself names the edition.
func TestIsEditionWrapperSeriesRef_ReportedWrappers(t *testing.T) {
	cases := []struct{ series, book string }{
		{"The Liveship Traders", "The Liveship Traders Trilogy"},
		{"The Maze Runner", "Maze Runner Complete Collection"},
		{"Sherlock Holmes", "The Complete Sherlock Holmes, Volume II"},
		{"Legends of the Dragonrealm", "Legends of the Dragonrealm, Vol."},
		{"Midnight Breed", "The Midnight Breed Series Coloring Book"},
		{"The Amazing Spider-Man (2018) (Single Issues)", "Amazing Spider-Man, Vol. 3"},
		{"Dresden Files: Storm Front Volume II", "Storm Front, Volume 2"},
	}
	for _, c := range cases {
		if !IsEditionWrapperSeriesRef(models.SeriesRef{Title: c.series}, c.book) {
			t.Errorf("series %q book %q: want wrapper", c.series, c.book)
		}
	}
}

func TestIsEditionWrapperSeriesRef_RealSeriesStay(t *testing.T) {
	cases := []struct{ series, book string }{
		{"The Hollows", "Million Dollar Demon"},
		{"Richard Sharpe", "Sharpe's Havoc"},
		{"Discworld (Death)", "Hogfather"},
		{"Discworld (Witches)", "Wyrd Sisters"},
		{"Discworld (City Watch)", "Guards! Guards!"},
		{"Alex Cross", "Cross Justice"},
		{"The Complete Chronicles of Conan", "The Coming of Conan"},
		{"The Collection", "A Study in Scarlet"},
		{"The Volume Wars", "Volume Wars: Opening Moves"},
		{"Mushoku Tensei: Jobless Reincarnation", "Mushoku Tensei: Jobless Reincarnation, Vol. 3"},
		{"The Hollows", "The Hollows, Vol. 3"},
		{"The Wheel of Time", "The Eye of the World: Book One of the Wheel of Time"},
		{"Sherlock Holmes", "Sherlock Holmes: The Complete Novels"},
		{"Sherlock Holmes", "A Study in Scarlet"},
		{"The Maze Runner", "The Maze Runner Volume 2"},
		{"The Dresden Files", "Storm Front, Volume 2"},
		{"Cosmere (Stormlight Archive)", "Oathbringer"},
		{"Les Rougon-Macquart", "Germinal"},
		{"Die drei ???", "Die drei ??? Band 2"},
		{"Le Seigneur des Anneaux", "Le Seigneur des Anneaux: La Communaute de l'Anneau"},
		{"Harry Potter", "Harry Potter and the Philosopher's Stone"},
		{"Set in Stone", "Set in Stone: Chapter One"},
		{"", "Anything"},
	}
	for _, c := range cases {
		if IsEditionWrapperSeriesRef(models.SeriesRef{Title: c.series}, c.book) {
			t.Errorf("series %q book %q: real series must stay", c.series, c.book)
		}
	}
}

func TestDropEditionWrapperSeriesRefs(t *testing.T) {
	refs := []models.SeriesRef{
		{ForeignID: "hc-series:1", Title: "The Liveship Traders", Primary: true},
		{ForeignID: "hc-series:2", Title: "Realm of the Elderlings"},
	}
	got := DropEditionWrapperSeriesRefs("The Liveship Traders Trilogy", refs)
	if len(got) != 1 || got[0].ForeignID != "hc-series:2" || !got[0].Primary {
		t.Fatalf("got %+v, want only the real series, promoted to primary", got)
	}
	if got := DropEditionWrapperSeriesRefs("Ship of Magic", refs); len(got) != 2 {
		t.Errorf("real member lost refs: %+v", got)
	}
	if got := DropEditionWrapperSeriesRefs("The Liveship Traders Trilogy", refs[:1]); got != nil {
		t.Errorf("sole wrapper ref kept: %+v", got)
	}
}
