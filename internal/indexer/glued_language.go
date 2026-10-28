package indexer

import (
	"slices"
	"strings"
	"unicode"
)

// gluedLanguageNames are the language NAMES that count when they sit at the
// edge of a CamelCase token, "EspañolNeutro" or "ItalianoAudiolibro" (#3497).
// Never a bare two letter code ("it", "de", "es", "no" are words), and never a
// scene word such as "vf" or "livre audio". It is a subset of
// releaseLanguageTags, looked up through it so the code mapping stays in one
// place.
//
// "polish" is left out on purpose: as the suffix of a CamelCase token it is
// far more often the verb ("NailPolish", "FrenchPolish") than the language,
// and "polski" still counts. "russe" is a French word that has to stay whole.
var gluedLanguageNames = []string{
	"english", "french", "francais", "german", "deutsch",
	"spanish", "espanol", "español", "dutch", "nederlands",
	"italian", "italiano", "portuguese", "portugues",
	"russian", "japanese", "chinese", "mandarin", "korean", "arabic",
	"swedish", "svenska", "norwegian", "norsk", "danish", "dansk",
	"polski", "czech", "turkish", "hindi",
}

// camelParts splits one letter run at lower to upper boundaries and at the end
// of an upper case run that is followed by a capitalised word, so
// "EspañolNeutro" is Español|Neutro and "ITALIANOAudiolibro" is ITALIANO|
// Audiolibro. A run with no such boundary comes back as one part.
func camelParts(tok string) []string {
	rs := []rune(tok)
	var parts []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		split := unicode.IsLower(prev) && unicode.IsUpper(cur)
		if !split && unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1]) {
			split = true
		}
		if split {
			parts = append(parts, string(rs[start:i]))
			start = i
		}
	}
	return append(parts, string(rs[start:]))
}

// gluedLanguageCodes returns the codes of language names joined to another
// word inside one token of the raw release title: only the first or last
// CamelCase part of a token counts, and a token that is a single part is left
// to releaseLanguageCodes, which already reads it whole. Separators (space,
// underscore, dot, hyphen) split tokens before this runs, so "Carl_Espanol_
// Neutro" never gets here.
func gluedLanguageCodes(title string) []string {
	var out []string
	for _, tok := range sceneTokenRe.FindAllString(title, -1) {
		parts := camelParts(tok)
		if len(parts) < 2 {
			continue
		}
		for _, p := range []string{parts[0], parts[len(parts)-1]} {
			p = strings.ToLower(p)
			code, ok := gluedName[p]
			if !ok {
				code, ok = gluedName[accentFold.Replace(p)]
			}
			if ok && !slices.Contains(out, code) {
				out = append(out, code)
			}
		}
	}
	return out
}

// accentFold strips the accents of the names above that are written with one
// in their own language, so "Français" and "Português" find "francais" and
// "portugues". ñ is not folded: "español" is listed as itself.
var accentFold = strings.NewReplacer("ç", "c", "ê", "e", "é", "e", "ã", "a", "ô", "o", "í", "i", "ó", "o", "á", "a", "ú", "u")

var gluedName = func() map[string]string {
	m := make(map[string]string, len(gluedLanguageNames))
	for _, n := range gluedLanguageNames {
		m[n] = releaseLanguageTags[n]
	}
	return m
}()
