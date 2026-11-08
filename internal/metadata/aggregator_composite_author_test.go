package metadata

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestCompositeAuthorName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Tavia Gilbert, William Dufris, John Scalzi", true},
		{"Gilbert, Tavia, Scalzi, John", true},
		{"Wil Wheaton,John Scalzi,Tavia Gilbert", true},
		{"John Scalzi", false},
		{"Scalzi, John", false},
		{"Martin Luther King, Jr.", false},
		{"King, Martin Luther, Jr.", false},
		{"Davis, Sammy, Jr., III", false},
		{"Twain, Mark, 1835-1910", false},
		{"Tavia Gilbert, John Scalzi", false},
		{"Black, Chuck, Black, Chuck", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := compositeAuthorName(tt.name); got != tt.want {
			t.Errorf("compositeAuthorName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestAggregator_SearchAuthors_DropsNarratorCastsWhenExactMatchExists is the
// case from bindery-dev: searching "John Scalzi" returned OpenLibrary author
// records that are really an audiobook's cast list. With the real John Scalzi
// in the results they are only noise, so they go.
func TestAggregator_SearchAuthors_DropsNarratorCastsWhenExactMatchExists(t *testing.T) {
	stats := func(n int) *models.AuthorStats { return &models.AuthorStats{BookCount: n} }
	primary := &mockProvider{name: "ol", searchAuthors: []models.Author{
		{Name: "John Scalzi, Wil Wheaton, Tavia Gilbert", ForeignID: "OL3A", Statistics: stats(400)},
		{Name: "Tavia Gilbert, William Dufris, John Scalzi", ForeignID: "OL1A", Statistics: stats(3)},
		{Name: "John Scalzi", ForeignID: "OL2A", Statistics: stats(120)},
		{Name: "John Scalzo", ForeignID: "OL4A", Statistics: stats(2)},
	}}
	agg := newTestAggregator(primary)

	got, err := agg.SearchAuthors(context.Background(), "John Scalzi")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(got) == 0 || got[0].ForeignID != "OL2A" {
		t.Fatalf("first result = %+v, want the single name John Scalzi record", got)
	}
	for _, a := range got {
		if a.ForeignID == "OL1A" || a.ForeignID == "OL3A" {
			t.Errorf("cast list record %q kept despite an exact single name match: %+v", a.Name, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d results, want John Scalzi and John Scalzo: %+v", len(got), got)
	}
}

// TestAggregator_SearchAuthors_RanksCompositesBelowSingleNames covers the
// case with no exact single name match: the cast list records stay, since one
// may be all there is, but below every single name record however well they
// match the query text.
func TestAggregator_SearchAuthors_RanksCompositesBelowSingleNames(t *testing.T) {
	stats := func(n int) *models.AuthorStats { return &models.AuthorStats{BookCount: n} }
	primary := &mockProvider{name: "ol", searchAuthors: []models.Author{
		{Name: "John Scalzi, Wil Wheaton, Tavia Gilbert", ForeignID: "OL3A", Statistics: stats(400)},
		{Name: "John M. Scalzi", ForeignID: "OL5A", Statistics: stats(1)},
	}}
	agg := newTestAggregator(primary)

	got, err := agg.SearchAuthors(context.Background(), "John Scalzi")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(got) != 2 || got[0].ForeignID != "OL5A" || got[1].ForeignID != "OL3A" {
		t.Fatalf("got %+v, want the single name record first and the cast list kept second", got)
	}
}

// TestAggregator_SearchAuthors_CompositeQueryKeepsItsRecordFirst guards the
// bulk importers, which take the first result carrying an id: an imported
// author string that is itself a cast list must still get that record, not
// the first single person named in it.
func TestAggregator_SearchAuthors_CompositeQueryKeepsItsRecordFirst(t *testing.T) {
	stats := func(n int) *models.AuthorStats { return &models.AuthorStats{BookCount: n} }
	primary := &mockProvider{name: "ol", searchAuthors: []models.Author{
		{Name: "Tavia Gilbert, William Dufris, John Scalzi", ForeignID: "OL1A", Statistics: stats(3)},
		{Name: "Tavia Gilbert", ForeignID: "OL6A", Statistics: stats(40)},
	}}
	agg := newTestAggregator(primary)

	got, err := agg.SearchAuthors(context.Background(), "Tavia Gilbert, William Dufris, John Scalzi")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(got) != 2 || got[0].ForeignID != "OL1A" {
		t.Fatalf("got %+v, want the exact cast list record first", got)
	}
}

// TestAggregator_SearchAuthors_SingleNameWithOneCommaIsNotComposite guards the
// legitimate comma: "Martin Luther King, Jr." is one person and must not be
// demoted or dropped, while a real cast list naming him still ranks below.
func TestAggregator_SearchAuthors_SingleNameWithOneCommaIsNotComposite(t *testing.T) {
	stats := func(n int) *models.AuthorStats { return &models.AuthorStats{BookCount: n} }
	primary := &mockProvider{name: "ol", searchAuthors: []models.Author{
		{Name: "Coretta Scott King, Martin Luther King, Jr., Clayborne Carson", ForeignID: "OL9A", Statistics: stats(90)},
		{Name: "Martin Luther King, Jr.", ForeignID: "OL7A", Statistics: stats(60)},
		{Name: "Martin Luther", ForeignID: "OL8A", Statistics: stats(500)},
	}}
	agg := newTestAggregator(primary)

	got, err := agg.SearchAuthors(context.Background(), "Martin Luther King")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	pos := map[string]int{}
	for i, a := range got {
		pos[a.ForeignID] = i
	}
	mlk, ok := pos["OL7A"]
	if !ok {
		t.Fatalf("Martin Luther King, Jr. dropped: %+v", got)
	}
	if mlk != 0 {
		t.Errorf("Martin Luther King, Jr. at position %d, want first: %+v", mlk, got)
	}
	if cast, ok := pos["OL9A"]; ok && cast < pos["OL8A"] {
		t.Errorf("cast list ranked above a single name record: %+v", got)
	}
}
