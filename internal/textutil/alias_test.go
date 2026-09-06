package textutil

import "testing"

func TestLatinAliasBinds(t *testing.T) {
	tests := []struct {
		name      string
		canonical string
		alias     string
		want      bool
	}{
		// Pure ASCII on both sides: no script gap, nothing to bind.
		{"ascii canonical, ascii alias", "Stephen King", "Richard Bachman", false},
		{"ascii canonical, non-latin alias", "Stephen King", "村上春樹", false},

		// Accented Latin is Latin, not "some other script". An unrelated Latin
		// alias beside one carries no evidence and must not bind.
		{"accented latin canonical", "Jo Nesbø", "Karin Fossum", false},
		{"accented latin canonical, accented alias", "Bodil Östergaard", "Jo Nesbø", false},

		// ...but the ASCII transliteration of that same name must bind: it is
		// the only route from a release named "Jo.Nesbo.-.The.Snowman" to the
		// author, because the release alphabet transliterates umlauts only.
		{"ascii transliteration of accented canonical", "Jo Nesbø", "Jo Nesbo", true},
		{"ascii transliteration, umlaut", "Bodil Östergaard", "Bodil Ostergaard", true},
		{"expanded umlaut transliteration", "Bodil Östergaard", "Bodil Oestergaard", true},
		{"ascii transliteration, polish", "Łukasz Orbitowski", "Lukasz Orbitowski", true},
		{"ascii transliteration, icelandic", "Halldór Laxness", "Halldor Laxness", true},

		// The same-name ground is Exact only. These land in the fuzzy-auto
		// band and are the two-different-people shape the guard must refuse.
		{"near-typo given name does not bind", "Jo Nesbø", "Jon Nesbø", false},
		{"near-typo given name does not bind, ascii", "Brandon Sanderson", "Brendon Sanderson", false},
		{"shared surname does not bind", "Emily Brontë", "Charlotte Brontë", false},

		// A spelling variant of a plain ASCII name binds on the same ground —
		// the rule is "same name", not "same name in an interesting script".
		{"punctuation variant of ascii name", "R.R. Haywood", "RR Haywood", true},
		{"accented latin alias binds cjk canonical", "村上春樹", "Jo Nesbø", true},
		{"accented latin alias binds cyrillic canonical", "Фёдор Достоевский", "Bodil Östergaard", true},

		// Pure CJK canonical with a Latin romanisation: the case the rule exists for.
		{"cjk canonical, latin alias", "村上春樹", "Haruki Murakami", true},
		{"cjk canonical, cjk alias", "村上春樹", "村上 春樹", false},

		// Mixed-script names are non-Latin: one non-Latin letter is enough.
		{"mixed canonical, latin alias", "村上 Haruki", "Haruki Murakami", true},
		{"latin canonical, mixed alias", "村上春樹", "Haruki 村上", false},

		// Other scripts.
		{"cyrillic canonical, latin alias", "Фёдор Достоевский", "Fyodor Dostoevsky", true},
		{"greek canonical, latin alias", "Νίκος Καζαντζάκης", "Nikos Kazantzakis", true},
		{"cyrillic alias does not bind greek canonical", "Νίκος Καζαντζάκης", "Фёдор Достоевский", false},

		// Digits and punctuation are not letters and must not decide anything.
		{"digits in latin alias", "村上春樹", "50 Cent", true},
		{"digits in latin canonical", "50 Cent", "Curtis Jackson", false},
		{"initials in latin alias", "村上春樹", "J.R.R. Tolkien", true},

		// Letterless strings are not names in any script.
		{"letterless alias", "村上春樹", "1234", false},
		{"punctuation-only alias", "村上春樹", "???", false},
		{"letterless canonical", "1234", "Haruki Murakami", false},
		{"empty alias", "村上春樹", "", false},
		{"empty canonical", "", "Haruki Murakami", false},
		{"both empty", "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LatinAliasBinds(tc.canonical, tc.alias); got != tc.want {
				t.Errorf("LatinAliasBinds(%q, %q) = %v, want %v", tc.canonical, tc.alias, got, tc.want)
			}
		})
	}
}
