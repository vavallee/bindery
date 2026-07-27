package textutil

import (
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestFoldForTitleMatch(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercases", "The Hobbit", "the hobbit"},
		{"collapses punctuation runs", "Guards! Guards! - (1989) [epub]", "guards guards 1989 epub"},
		{"deletes ascii apostrophes", "Ender's Game", "enders game"},
		{"deletes unicode apostrophes", "Ender’s Game", "enders game"},
		{"expands umlauts", "Die Höhle", "die hoehle"},
		{"expands eszett", "Der Prozeß", "der prozess"},
		{"already-expanded form is stable", "Die Hoehle", "die hoehle"},
		{"keeps cjk", "三体", "三体"},
		{"keeps cyrillic", "Преступление и наказание", "преступление и наказание"},
		{"keeps interior diacritics", "Perdido Street Station by China Miéville", "perdido street station by china miéville"},
		{"trims and collapses whitespace", "  a   b  ", "a b"},
		{"empty stays empty", "", ""},
		{"punctuation only reduces to empty", "--- !!! ---", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FoldForTitleMatch(tc.in); got != tc.want {
				t.Fatalf("FoldForTitleMatch(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFoldForTitleMatchUnicodeForms pins the NFC step. Without it a combining
// mark (category Mn, not a letter) takes the separator branch and truncates the
// word at the accent, so the decomposed spelling of a title produces a token
// the composed spelling can never equal.
func TestFoldForTitleMatchUnicodeForms(t *testing.T) {
	for _, in := range []string{
		"Café Society", "Die Höhle", "José Saramago", "Miéville",
		"Amélie", "Ångström", "Señor", "Dvořák",
	} {
		nfc, nfd := norm.NFC.String(in), norm.NFD.String(in)
		if nfc == nfd {
			t.Fatalf("%q has no distinct NFD form; the case would be vacuous", in)
		}
		gotC, gotD := FoldForTitleMatch(nfc), FoldForTitleMatch(nfd)
		if gotC != gotD {
			t.Errorf("FoldForTitleMatch disagrees on Unicode form of %q: NFC %q vs NFD %q", in, gotC, gotD)
		}
	}
}

// TestFoldForTitleMatchIsIdempotent guards the property every caller relies on:
// a folded string is already in the target alphabet, so folding it again is a
// no-op. Without this, a value that passes through two normalizers (say a
// dedup key derived from an already-normalized release) could drift.
func TestFoldForTitleMatchIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"Ender's Game", "Die Höhle", "Der Prozeß", "三体", "Åsa Larsson",
		"Guards! Guards! - (1989) [epub]", "Преступление и наказание",
	} {
		once := FoldForTitleMatch(in)
		if twice := FoldForTitleMatch(once); twice != once {
			t.Errorf("not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func TestTransliterateUmlauts(t *testing.T) {
	cases := map[string]string{
		"müller": "mueller",
		"höhle":  "hoehle",
		"bär":    "baer",
		"straße": "strasse",
		"muller": "muller",
		"":       "",
	}
	for in, want := range cases {
		if got := TransliterateUmlauts(in); got != want {
			t.Errorf("TransliterateUmlauts(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFoldNonDecomposableLatin(t *testing.T) {
	cases := map[string]string{
		"nesbø":  "nesbo",
		"łukasz": "lukasz",
		"ærø":    "aero",
		"þór":    "thór", // only the non-decomposable letter is folded here
		"smith":  "smith",
	}
	for in, want := range cases {
		if got := FoldNonDecomposableLatin(in); got != want {
			t.Errorf("FoldNonDecomposableLatin(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFoldForTitleMatchMatchesLegacyPipeline pins FoldForTitleMatch to the
// sequence the three call sites used to open-code, so the consolidation in
// #1648 cannot have silently changed release matching. Inputs are composed
// (NFC), which is the form every producer already emitted — the added NFC step
// is a no-op for them and only changes decomposed input, which previously had
// no consistent behaviour at all (see TestFoldForTitleMatchUnicodeForms).
func TestFoldForTitleMatchMatchesLegacyPipeline(t *testing.T) {
	legacy := func(s string) string {
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, "'", "")
		s = strings.ReplaceAll(s, "’", "")
		s = strings.ReplaceAll(s, "ä", "ae")
		s = strings.ReplaceAll(s, "ö", "oe")
		s = strings.ReplaceAll(s, "ü", "ue")
		s = strings.ReplaceAll(s, "ß", "ss")
		var b strings.Builder
		for _, r := range s {
			switch {
			case ('a' <= r && r <= 'z') || ('0' <= r && r <= '9'):
				b.WriteRune(r)
			case r > 127:
				b.WriteRune(r)
			default:
				b.WriteByte(' ')
			}
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	for _, in := range []string{
		"Ender's Game", "The Hobbit (1937) [EPUB]", "Die Höhle", "Der Prozeß",
		"Foundation & Empire", "1984", "Guards! Guards!", "Åsa Larsson - Solstorm",
	} {
		if got, want := FoldForTitleMatch(in), legacy(in); got != want {
			t.Errorf("FoldForTitleMatch(%q) = %q, legacy pipeline gave %q", in, got, want)
		}
	}
}
