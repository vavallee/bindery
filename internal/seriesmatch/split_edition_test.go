package seriesmatch

import "testing"

func TestSplitEditionPartOf(t *testing.T) {
	cases := []struct {
		name                 string
		partTitle, partPos   string
		wholeTitle, wholePos string
		want                 bool
	}{
		{"stormlight part 1", "The Way of Kings, Part 1", "1.1", "The Way of Kings", "1", true},
		{"stormlight part 2", "The Way of Kings, Part 2", "1.2", "The Way of Kings", "1", true},
		{"pt abbreviation", "The Way of Kings Pt. 2", "1.2", "The Way of Kings", "1", true},
		{"subtitle after marker", "The Great Hunt, Part 2 of 2: New Threads in the Pattern", "2.2", "The Great Hunt", "2", true},
		{"novella without marker", "Edgedancer", "2.5", "Words of Radiance", "2", false},
		{"part of a different book", "Words of Radiance, Part 1", "1.1", "The Way of Kings", "1", false},
		{"whole position elsewhere", "The Way of Kings, Part 1", "1.1", "The Way of Kings", "2", false},
		{"integer part position", "The Way of Kings, Part 1", "1", "The Way of Kings", "1", false},
		{"bare marker", "Part 1", "1.1", "", "1", false},
		{"blank position", "The Way of Kings, Part 1", "", "The Way of Kings", "1", false},
		{"unparseable position", "The Way of Kings, Part 1", "1a", "The Way of Kings", "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SplitEditionPartOf(tc.partTitle, tc.partPos, tc.wholeTitle, tc.wholePos); got != tc.want {
				t.Fatalf("SplitEditionPartOf(%q, %q, %q, %q) = %v, want %v",
					tc.partTitle, tc.partPos, tc.wholeTitle, tc.wholePos, got, tc.want)
			}
		})
	}
}

func TestSplitEditionTitleOf(t *testing.T) {
	cases := []struct {
		name, partTitle, wholeTitle string
		want                        bool
	}{
		{"marker and subtitle", "The Eye of the World, Part 1 of 2: From The Two Rivers", "The Eye of the World", true},
		{"pt abbreviation", "The Way of Kings Pt. 2", "The Way of Kings", true},
		{"no marker", "The Eye of the World: The Graphic Novel, Volume One", "The Eye of the World", false},
		{"part of a different book", "The Great Hunt, Part 1 of 2", "The Eye of the World", false},
		{"bare marker", "Part 1", "The Eye of the World", false},
		{"blank whole", "The Eye of the World, Part 1", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SplitEditionTitleOf(tc.partTitle, tc.wholeTitle); got != tc.want {
				t.Fatalf("SplitEditionTitleOf(%q, %q) = %v, want %v", tc.partTitle, tc.wholeTitle, got, tc.want)
			}
		})
	}
}
