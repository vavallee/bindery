package models

import "testing"

func TestIsManualForeignID(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"manual:book:1700000000000000000": true,
		"manual:author:dan-abnett":        true,
		"manual:series:1700000000":        true,
		"  MANUAL:book:1  ":               true,
		"OL27482W":                        false,
		"hc:the-first-heretic":            false,
		"calibre:book:12":                 false,
		"manual":                          false,
		"":                                false,
	}
	for id, want := range cases {
		if got := IsManualForeignID(id); got != want {
			t.Errorf("IsManualForeignID(%q) = %v, want %v", id, got, want)
		}
	}
}
