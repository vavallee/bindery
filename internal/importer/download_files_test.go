package importer

import (
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestOrderByBookTitle(t *testing.T) {
	foundation := []string{
		"/d/Forward the Foundation (4048)/Forward the Foundation - Isaac Asimov.epub",
		"/d/Foundation (4047)/Foundation - Isaac Asimov.epub",
		"/d/Foundation and Chaos (4054)/Foundation and Chaos - Isaac Asimov.epub",
	}
	for _, tc := range []struct {
		name  string
		title string
		in    []string
		want  []string
	}{
		{"the issue's Foundation case", "Foundation", foundation,
			[]string{foundation[1], foundation[0], foundation[2]}},
		{"series number prefix, wanted file second in walk order", "The Eye of the World",
			[]string{"/d/Wheel of Time 02 - The Great Hunt.epub", "/d/Wheel of Time 01 - The Eye of the World.epub"},
			[]string{"/d/Wheel of Time 01 - The Eye of the World.epub", "/d/Wheel of Time 02 - The Great Hunt.epub"}},
		{"series number prefix, wanted file already first", "The Eye of the World",
			[]string{"/d/Wheel of Time 01 - The Eye of the World.epub", "/d/Wheel of Time 02 - The Great Hunt.epub"},
			[]string{"/d/Wheel of Time 01 - The Eye of the World.epub", "/d/Wheel of Time 02 - The Great Hunt.epub"}},
		{"the title inside a longer name", "The Great Hunt",
			[]string{"/d/a/WoT 1 The Eye of the World.epub", "/d/b/WoT 2 The Great Hunt.epub"},
			[]string{"/d/b/WoT 2 The Great Hunt.epub", "/d/a/WoT 1 The Eye of the World.epub"}},
		{"names with no title keep walk order", "Origin",
			[]string{"/d/book1.epub", "/d/book2.epub"},
			[]string{"/d/book1.epub", "/d/book2.epub"}},
		{"equal scores keep walk order", "Origin",
			[]string{"/d/a/Origin.epub", "/d/b/Origin.epub"},
			[]string{"/d/a/Origin.epub", "/d/b/Origin.epub"}},
		{"only a weak match keeps walk order", "Origin",
			[]string{"/d/Zebra Crossing.epub", "/d/Quiet Harbor.epub"},
			[]string{"/d/Zebra Crossing.epub", "/d/Quiet Harbor.epub"}},
		{"each format picks its own", "Origin",
			[]string{"/d/Before the Origin.epub", "/d/Origin.epub", "/d/Before the Origin.mobi", "/d/Origin.mobi"},
			[]string{"/d/Origin.epub", "/d/Origin.mobi", "/d/Before the Origin.epub", "/d/Before the Origin.mobi"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := orderByBookTitle(&models.Book{Title: tc.title}, tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("order = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldMoveAhead pins the two constants: a score just under the floor, or
// a lead just under the margin, leaves walk order alone, and the values on the
// line move the file. Setting either constant to 0 breaks a case here.
func TestShouldMoveAhead(t *testing.T) {
	for _, tc := range []struct {
		name         string
		best, leader float64
		want         bool
	}{
		{"at the floor and far ahead", 0.85, 0.50, true},
		{"just under the floor", 0.84, 0.50, false},
		{"exactly the margin", 0.95, 0.90, true},
		{"just under the margin", 0.94, 0.90, false},
		{"no lead", 0.95, 0.95, false},
	} {
		if got := shouldMoveAhead(tc.best, tc.leader); got != tc.want {
			t.Errorf("%s: shouldMoveAhead(%v, %v) = %v, want %v", tc.name, tc.best, tc.leader, got, tc.want)
		}
	}
}
