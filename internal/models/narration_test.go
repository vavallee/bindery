package models

import (
	"reflect"
	"testing"
)

func TestSplitNarrators(t *testing.T) {
	cases := map[string][]string{
		"":                            {},
		"Scott Brick":                 {"Scott Brick"},
		"Scott Brick, Orlagh Cassidy": {"Scott Brick", "Orlagh Cassidy"},
		"A B & C D":                   {"A B", "C D"},
		"A B and C D; E F":            {"A B", "C D", "E F"},
		"A B, a b":                    {"A B"},
	}
	for in, want := range cases {
		if got := SplitNarrators(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitNarrators(%q) = %#v, want %#v", in, got, want)
		}
	}
}

func TestEffectiveNarrators(t *testing.T) {
	if got := EffectiveNarrators([]string{"X Y"}, "A B"); !reflect.DeepEqual(got, []string{"X Y"}) {
		t.Fatalf("stored list must win: %#v", got)
	}
	if got := EffectiveNarrators(nil, "A B, C D"); !reflect.DeepEqual(got, []string{"A B", "C D"}) {
		t.Fatalf("fallback to book string: %#v", got)
	}
}

func TestFoldName(t *testing.T) {
	if FoldName("Rosamund Pike") != FoldName("rosamund  PIKE.") || FoldName("José Núñez") != "jose nunez" {
		t.Fatal("fold mismatch")
	}
	if FoldName("Scott Brick") == FoldName("Scott Bricks") {
		t.Fatal("fold must not be fuzzy")
	}
}

func TestNormalizers(t *testing.T) {
	if NormalizeEditionType(" Unabridged ") != EditionTypeUnabridged || NormalizeEditionType("garbage") != "" {
		t.Fatal("edition type")
	}
	if NormalizeEditionPreference("garbage") != "" || NormalizeEditionPreference("require_unabridged") != EditionPrefRequireUnabridged {
		t.Fatal("edition preference")
	}
}
