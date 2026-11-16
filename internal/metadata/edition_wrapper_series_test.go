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

// Manga, comic and light novel shapes that are numbered members of a real
// series. None may be dropped.
func TestIsEditionWrapperSeriesRef_VolumeShapesStay(t *testing.T) {
	cases := []struct{ series, book string }{
		{"Naruto", "Naruto, Vol. 3"},
		{"Naruto", "Naruto Vol 3"},
		{"Naruto", "Vol. 03"},
		{"Naruto", "Volume Three"},
		{"Naruto", "Book 3"},
		{"Naruto", "Part 2"},
		{"Saga", "Saga, Volume 3"},
		{"The Sandman", "The Sandman, Vol. 1"},
		{"Fate/Zero", "Fate/Zero, Vol. 2"},
		// Numbered omnibus and deluxe volumes of a real series are still
		// members of it: the omnibus number is the position.
		{"Naruto", "Naruto Omnibus 2 (3-in-1)"},
		{"Attack on Titan", "Attack on Titan Omnibus 1"},
		{"Berserk", "Berserk Deluxe Edition Volume 1"},
		// The edition words are English only (documented): other scripts
		// show up as series.
		{"进击的巨人", "进击的巨人 全集"},
		{"Метро 2033", "Метро 2033 Полное собрание"},
		// One generic word is not a series name the edition rule can wrap.
		{"Stories", "Collected Stories"},
	}
	for _, c := range cases {
		if IsEditionWrapperSeriesRef(models.SeriesRef{Title: c.series}, c.book) {
			t.Errorf("series %q book %q: must stay", c.series, c.book)
		}
	}
}

func TestIsEditionWrapperSeriesRef_BoxSetsConsistent(t *testing.T) {
	cases := []struct{ series, book string }{
		{"One Piece", "One Piece, Vol. 3 Box Set"},
		{"One Piece", "One Piece Box Set 1: Volumes 1-23"},
		{"One Piece", "One Piece Box Set"},
		{"One Piece Box Set 1: Volumes 1-23", "One Piece, Vol. 1"},
		{"Attack on Titan", "Attack on Titan Omnibus"},
		{"Discworld", "Discworld Complete Collection"},
		{"The Chronicles of Narnia", "The Complete Chronicles of Narnia"},
	}
	for _, c := range cases {
		if !IsEditionWrapperSeriesRef(models.SeriesRef{Title: c.series}, c.book) {
			t.Errorf("series %q book %q: want wrapper", c.series, c.book)
		}
	}
}

// A series title ending in a volume marker, or containing "box set", names a
// real series unless the book is itself the wrapper.
func TestIsEditionWrapperSeriesRef_SeriesTitleNeedsTheBook(t *testing.T) {
	cases := []struct{ series, book string }{
		{"Fate/Zero Vol. 2", "Fate/Zero, Vol. 2"},
		{"Gundam: Volume 4", "Gundam: Volume 4"},
		{"Rocky Vol. IV", "Rocky IV"},
		{"Star Wars: Darth Vader (2015) Volume 1", "Star Wars: Darth Vader, Vol. 1"},
		{"The Boxed Set Murders", "The Boxed Set Murders"},
		{"The Box Set Murders", "A Body in the Box"},
	}
	for _, c := range cases {
		if IsEditionWrapperSeriesRef(models.SeriesRef{Title: c.series}, c.book) {
			t.Errorf("series %q book %q: must stay", c.series, c.book)
		}
	}
	// Unambiguous markers still decide on the series title alone.
	for _, s := range []string{"X (Single Issues)", "X Complete Collection", "X Coloring Book", "Foo Box Set", "Foo Boxed Set 2"} {
		if !IsEditionWrapperSeriesRef(models.SeriesRef{Title: s}, "Anything") {
			t.Errorf("series %q: want wrapper", s)
		}
	}
}
