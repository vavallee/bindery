package importer

import (
	"html"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/textutil"
)

// Strict matching, used only by the gate that lets an upgrade delete a file
// (#2420). The content check itself stays loose on purpose: it refuses only a
// definite mismatch, because refusing a good file costs a download. Deleting
// the file a user already has is a different cost, so the delete gate asks for
// identity, not compatibility.
//
// It is deliberately dumb. Every earlier attempt to be clever (strip brackets,
// strip series numbers, handle ranges) opened a hole, so there is none of
// that: the file's whole title must equal the book's whole title after
// cosmetic normalisation, and the author must match by full name. Anything
// that fails only loses the delete: the upgrade is still imported and the old
// file stays. That refuses many legitimate removals (subtitles, series
// suffixes), which is the accepted cost of an opt-in setting.

// strictTitleKey normalises a title for equality. It lowercases, decodes HTML
// entities and drops apostrophes. Only a small whitelist is ignored: whitespace,
// hyphens and other dashes, and the sentence punctuation . , ; ! ? " & / (each
// turns into a word break). Every other symbol survives as a token of its own,
// brackets and colons included, and every number survives, so "C++" and "C#",
// "Naruto" and "Naruto \u2461", "Dune (Messiah)", "Dune: Messiah" and "Dune 2"
// are all different. A leading article is not dropped: "The Stand" is not
// "Stand".
func strictTitleKey(title string) string {
	t := strings.ToLower(html.UnescapeString(strings.TrimSpace(title)))
	var b strings.Builder
	space := true
	sep := func() {
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	for _, r := range t {
		switch {
		case textutil.IsApostrophe(r):
			// "Ender's" and "Enders" are the same word.
		case unicode.IsNumber(r) || unicode.IsLetter(r) || unicode.IsMark(r):
			b.WriteRune(r)
			space = false
		case unicode.IsSpace(r) || unicode.Is(unicode.Dash, r) || r == '\u2212' ||
			strings.ContainsRune(".,;!?\"&/\u201c\u201d\u201e", r):
			sep()
		default:
			sep()
			b.WriteRune(r)
			space = false
			sep()
		}
	}
	return strings.TrimSpace(b.String())
}

// strictTitleEqual reports whether the file's title is the book's title.
func strictTitleEqual(bookTitle, fileTitle string) bool {
	b := strictTitleKey(bookTitle)
	return b != "" && b == strictTitleKey(fileTitle)
}

// strictNoAuthor are credits that name nobody. They never count as a match,
// even against an author record that carries the same placeholder.
var strictNoAuthor = map[string]bool{
	"various": true, "authors": true, "author": true, "unknown": true, "anonymous": true,
}

// strictAuthorMatch reports whether the creator is the author by a full name:
// the same surname and given names, where "Last, First", initials, a "Jr."
// and trailing life dates are accepted and a bare surname or a single name is
// not. A creator crediting several people matches when one of them is the
// author.
func strictAuthorMatch(names []string, creator string) bool {
	creator = strings.TrimSpace(creator)
	if creator == "" || len(names) == 0 {
		return false
	}
	for _, c := range strictCreatorNames(creator) {
		for _, n := range names {
			if strictSamePerson(strictNameTokens(n), c) {
				return true
			}
		}
	}
	return false
}

// strictCreatorNames splits a credit into candidate people, as token lists.
// Every reading that could be right is produced: "Le Guin, Ursula K." as one
// person in last-first order, and each comma piece on its own for a list of
// people. A wrong reading only matters if it equals the author's full name.
func strictCreatorNames(creator string) [][]string {
	repl := strings.NewReplacer(";", "\n", " and ", "\n", " & ", "\n", " | ", "\n", " / ", "\n")
	var out [][]string
	for _, part := range strings.Split(repl.Replace(creator), "\n") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Pieces that are only a date or a suffix ("1947-", "Jr.") vanish here.
		var pieces [][]string
		for _, p := range strings.Split(part, ",") {
			if toks := strictNameTokens(p); len(toks) > 0 {
				pieces = append(pieces, toks)
			}
		}
		if len(pieces) == 2 {
			out = append(out, append(append([]string{}, pieces[1]...), pieces[0]...))
		}
		out = append(out, pieces...)
	}
	return out
}

// strictNameTokens lowercases and folds a name into words, dropping numbers
// (life dates) and generational suffixes.
func strictNameTokens(s string) []string {
	folded := strings.ToLower(textutil.FoldForSearch(html.UnescapeString(s)))
	var toks []string
	for _, w := range strings.FieldsFunc(folded, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		switch w {
		case "jr", "sr", "ii", "iii", "iv":
			continue
		}
		if strings.IndexFunc(w, unicode.IsLetter) < 0 {
			continue
		}
		toks = append(toks, w)
	}
	return toks
}

// strictSamePerson compares two token lists, given names first and surname
// last. Both need a given name and a surname, neither may be a placeholder
// credit, the surnames must be equal and each given name must be equal or an
// initial of the other.
func strictSamePerson(a, b []string) bool {
	if len(a) < 2 || len(b) < 2 || len(a) != len(b) {
		return false
	}
	if strictPlaceholder(a) || strictPlaceholder(b) {
		return false
	}
	if a[len(a)-1] != b[len(b)-1] {
		return false
	}
	for i := 0; i < len(a)-1; i++ {
		x, y := a[i], b[i]
		if x == y {
			continue
		}
		if (len(x) == 1 && strings.HasPrefix(y, x)) || (len(y) == 1 && strings.HasPrefix(x, y)) {
			continue
		}
		return false
	}
	return true
}

func strictPlaceholder(toks []string) bool {
	for _, t := range toks {
		if !strictNoAuthor[t] {
			return false
		}
	}
	return true
}
