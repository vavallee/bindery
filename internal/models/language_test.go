package models

import (
	"reflect"
	"testing"
)

func TestParseAllowedLanguages(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"any", nil},
		{"ANY", nil},
		{"eng", []string{"eng"}},
		{"eng,fre,ger", []string{"eng", "fre", "ger"}},
		{" Eng , FRE ,  ger ", []string{"eng", "fre", "ger"}},
		{"eng,,fre", []string{"eng", "fre"}},
		// A single "any" anywhere short-circuits to no filter — having a
		// mixed list with "any" in it is contradictory and we treat the
		// broader setting as the user's real intent.
		{"eng,any,fre", nil},
	}
	for _, tc := range cases {
		got := ParseAllowedLanguages(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseAllowedLanguages(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsLanguageAllowed(t *testing.T) {
	cases := []struct {
		code    string
		allowed []string
		want    bool
	}{
		{"eng", nil, true},               // no filter
		{"", []string{"eng"}, true},      // unknown language = keep
		{"eng", []string{"eng"}, true},   // exact match
		{"ENG", []string{"eng"}, true},   // case-insensitive
		{" eng ", []string{"eng"}, true}, // whitespace-tolerant
		{"fre", []string{"eng"}, false},  // mismatch drops
		{"fre", []string{"eng", "fre"}, true},
		{"ger", []string{"eng", "fre"}, false},
	}
	for _, tc := range cases {
		got := IsLanguageAllowed(tc.code, tc.allowed)
		if got != tc.want {
			t.Errorf("IsLanguageAllowed(%q, %v) = %v, want %v", tc.code, tc.allowed, got, tc.want)
		}
	}
}
