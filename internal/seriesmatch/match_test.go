package seriesmatch

import "testing"

func TestSamePosition(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "exact", a: "1", b: "1", want: true},
		{name: "trimmed exact", a: " 1 ", b: "\t1\n", want: true},
		{name: "numeric equivalent", a: "1.0", b: "1", want: true},
		{name: "decimal tolerance", a: "1.0009", b: "1", want: true},
		{name: "empty left", a: "", b: "1", want: false},
		{name: "empty right", a: "1", b: "", want: false},
		{name: "same non numeric", a: "prelude", b: "prelude", want: true},
		{name: "different non numeric", a: "prelude", b: "1", want: false},
		{name: "different numeric", a: "1", b: "2", want: false},
		{name: "outside tolerance", a: "1.01", b: "1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SamePosition(tt.a, tt.b); got != tt.want {
				t.Fatalf("SamePosition(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestNormalizeSeriesName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "trims and lowercases", input: "  The Stormlight Archive  ", want: "the stormlight archive"},
		{name: "strips series suffix", input: "The Stormlight Archive Series", want: "the stormlight archive"},
		{name: "strips trilogy suffix", input: "Red Rising Trilogy", want: "red rising"},
		{name: "strips saga suffix", input: "The Expanse Saga", want: "the expanse"},
		{name: "strips chronicles suffix", input: "Narnia Chronicles", want: "narnia"},
		{name: "keeps single word suffix term", input: "Saga", want: "saga"},
		{name: "keeps names without configured suffix", input: "Wayward Children", want: "wayward children"},
		{name: "empty", input: "  ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeSeriesName(tt.input); got != tt.want {
				t.Fatalf("NormalizeSeriesName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "removes articles punctuation and novel noise", input: "The Way-of-Kings: A Novel!", want: "way of kings"},
		{name: "removes book noise", input: "Book 1: Leviathan Wakes", want: "1 leviathan wakes"},
		{name: "collapses whitespace", input: "  Words   of   Radiance  ", want: "words of radiance"},
		{name: "empty", input: "  ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanTitle(tt.input); got != tt.want {
				t.Fatalf("CleanTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTitleScore(t *testing.T) {
	if got := TitleScore("", "Dune"); got != 0 {
		t.Fatalf("empty title score = %d, want 0", got)
	}
	if got := TitleScore("The Way of Kings: A Novel", "Way of Kings"); got < 95 {
		t.Fatalf("equivalent title score = %d, want at least 95", got)
	}
	if got := TitleScore("The Way of Kings", "Words of Radiance"); got >= 70 {
		t.Fatalf("unrelated title score = %d, want below 70", got)
	}
}
