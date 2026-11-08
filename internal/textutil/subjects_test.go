package textutil

import (
	"slices"
	"testing"
)

func TestIsJunkSubject(t *testing.T) {
	tests := []struct {
		subject string
		want    bool
	}{
		{"seriesRed Rising Trilogy", true},
		{"series:Red_Rising", true},
		{"Series:Harry_Potter", true},
		{"series_red_rising", true},
		{"nyt:hardcover-fiction=2015-01-25", true},
		{"NYT:combined-print-and-e-book-fiction", true},
		{"collectionID:greatbooks", true},
		{"collection:fantasy", true},
		{"place:Mars", true},
		{"person:Darrow (Fictitious character)", true},
		{"time:Future", true},
		{"award:hugo_award=1966", true},
		{"Science fiction", false},
		{"Fiction, dystopian", false},
		{"Series fiction", false},
		{"Collections", false},
		{"Collection", false},
		{"Series", false},
		{"Space colonies", false},
		{"Mars (Planet), fiction", false},
		{"Timeless tales", false},
		{"Personal space", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsJunkSubject(tt.subject); got != tt.want {
			t.Errorf("IsJunkSubject(%q) = %v, want %v", tt.subject, got, tt.want)
		}
	}
}

func TestDropJunkSubjects(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays nil", nil, nil},
		{"empty", []string{}, []string{}},
		{"nothing to drop", []string{"Fantasy", "Science fiction"}, []string{"Fantasy", "Science fiction"}},
		{"drops tags keeps order",
			[]string{"seriesRed Rising Trilogy", "Science fiction", "nyt:hardcover-fiction=2015-01-25", "Dystopias", "place:Mars"},
			[]string{"Science fiction", "Dystopias"}},
		{"all junk", []string{"time:Future", "person:Darrow"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DropJunkSubjects(tt.in)
			if !slices.Equal(got, tt.want) || (tt.in != nil && got == nil) {
				t.Errorf("DropJunkSubjects(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
