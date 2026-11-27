package newznab

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/textutil"
)

// genericSubtitleWords are words that name a form or a position, not a book. A
// subtitle made only of them ("A Novel", "Book Two", "Volume 3", "An Omnibus
// Edition") names no particular book, so "Surname + subtitle" would match every
// one of the author's books. Spelled out numbers are added from numberWords.
var genericSubtitleWords = map[string]bool{
	"novel": true, "novels": true, "book": true, "books": true, "volume": true,
	"volumes": true, "vol": true, "part": true, "parts": true, "edition": true,
	"story": true, "stories": true, "tale": true, "tales": true, "saga": true,
	"series": true, "trilogy": true, "omnibus": true, "collection": true,
	"unabridged": true, "abridged": true, "audiobook": true, "ebook": true,
	"chapter": true, "chapters": true, "episode": true, "season": true,
}

// authorNameSuffixes are trailing name parts that are not the surname.
var authorNameSuffixes = map[string]bool{
	"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "phd": true, "md": true,
}

// anonymousAuthors are metadata placeholders: a release cannot be checked
// against them, so no subtitle query is built for them.
var anonymousAuthors = map[string]bool{
	"various": true, "unknown": true, "anonymous": true, "authors": true, "n a": true,
}

// subtitleOnlyPlan is what subtitleOnlyQuery decides: the one extra query and
// what a result must carry to be accepted from it.
type subtitleOnlyPlan struct {
	Query    string // "Arelius Will of Mana"
	Subtitle string // "Will of Mana"
	Surname  string // "Arelius"
	Author   string // "Ryan Arelius", every name word a release may carry
}

// releaseTag is one word of a release name, folded, with whether a bare hyphen
// joins it to the next word ("Lee-Smith").
type releaseTag struct {
	w        string
	joinNext bool
}

// tagWords splits s into folded words. Apostrophes stay inside a word ("Ender's"
// is "enders"), every other non letter is a break, and "and" is dropped so "&"
// (which is no word at all) and "and" compare equal. Only a lone "-" between two
// words counts as joining them, so "Lee-Smith" is one name and "Arelius - Will"
// or a scene style "Mana-ENG" is not.
func tagWords(s string) []releaseTag {
	var out []releaseTag
	var run, sep []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		words := strings.Fields(textutil.FoldForTitleMatch(string(run)))
		run = run[:0]
		for _, w := range words {
			if w != "and" {
				out = append(out, releaseTag{w: w})
			}
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '\'' || r == '’' {
			if len(run) == 0 && len(out) > 0 && string(sep) == "-" {
				out[len(out)-1].joinNext = true
			}
			if len(run) == 0 {
				sep = sep[:0]
			}
			run = append(run, r)
			continue
		}
		flush()
		sep = append(sep, r)
	}
	flush()
	return out
}

func tagStrings(t []releaseTag) []string {
	out := make([]string, len(t))
	for i := range t {
		out[i] = t[i].w
	}
	return out
}

// subtitleReleaseTags are the words, besides the author's name, that may sit
// around the subtitle in a release name: the language and format tags indexers
// add ("[ENG / M4B]"), and "by". Anything else before or after the subtitle is
// another title word, a series marker or a different language, and refuses the
// release.
var subtitleReleaseTags = map[string]bool{
	"by": true, "eng": true, "english": true, "en": true,
	"m4b": true, "m4a": true, "mp3": true, "mp4": true, "flac": true, "aac": true, "ogg": true, "opus": true,
	"epub": true, "mobi": true, "azw": true, "azw3": true, "kepub": true, "pdf": true, "cbz": true, "cbr": true,
	"audiobook": true, "audio": true, "ebook": true, "unabridged": true, "abridged": true, "retail": true,
	"kbps": true, "jr": true, "sr": true,
}

var bitrateRe = regexp.MustCompile(`^\d{2,3}k(bps)?$`)
var yearRe = regexp.MustCompile(`^(19|20)\d\d$`)

// subtitleOnlyQuery builds the subtitle only query for a "Series, Book N:
// Subtitle" title (#4183), or reports false when the title or author gives no
// safe one. It uses the same reading of the title as the relevance filter's
// series position reading (SeriesPositionTitle, after the same spelled out
// number fold and qualifier stripping), so the query is only sent for a shape
// whose subtitle only release the filter would approve afterwards. A query for
// "Dune: Messiah" would find "Messiah by Frank Herbert" and then see it dropped.
//
// Not sent when:
//   - the part before the first colon does not end in a position number
//     ("Dune: Messiah", "Foo, Book: Bar"). Only the first colon splits, and what
//     follows it is kept as written, later colons included ("Series 2: A: B"
//     sends "Lee A: B"), except as listed below;
//   - the subtitle has fewer than two significant words (SigWords: three bytes
//     or more, no stop words), which also covers one that is too short or all
//     stop words;
//   - every significant word of the subtitle is generic ("A Novel", "Book Two",
//     "Volume 3": see genericSubtitleWords and numberWords);
//   - the author is empty, a placeholder ("Various"), or has no surname token
//     of at least two letters (an initial alone).
//
// Changes to the subtitle: a trailing " - tag" is cut ("Will of Mana - A LitRPG
// Adventure" is "Will of Mana", since no release carries the tag), a leading
// "The" is dropped, and "&" and "and" are left out of the query, so both
// spellings of a release are found and both are accepted.
func subtitleOnlyQuery(title, author string) (subtitleOnlyPlan, bool) {
	t := StripFormatQualifiers(NormalizeQueryTitle(FoldNumberWords(title)))
	sub := SeriesPositionTitle(t)
	if i := strings.Index(sub, " - "); i > 0 {
		sub = sub[:i]
	}
	sub = strings.Join(strings.Fields(sub), " ")
	if sub == "" {
		return subtitleOnlyPlan{}, false
	}
	sig := SigWords(sub)
	if len(sig) < 2 {
		return subtitleOnlyPlan{}, false
	}
	specific := false
	for _, w := range sig {
		if _, isNumber := numberWords[w]; !genericSubtitleWords[w] && !isNumber {
			specific = true
			break
		}
	}
	if !specific {
		return subtitleOnlyPlan{}, false
	}
	surname := subtitleAuthorSurname(author)
	if surname == "" {
		return subtitleOnlyPlan{}, false
	}
	var qw []string
	for i, w := range strings.Fields(wildcardToSpace(sub)) {
		lw := strings.ToLower(w)
		if lw == "&" || lw == "and" || (i == 0 && lw == "the") {
			continue
		}
		qw = append(qw, w)
	}
	return subtitleOnlyPlan{
		Query:    TransliterateQuery(surname + " " + strings.Join(qw, " ")),
		Subtitle: sub,
		Surname:  surname,
		Author:   author,
	}, true
}

// authorParenRe is a parenthesised or bracketed note in an author field
// ("Ann Lee (Narrator)").
var authorParenRe = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)

// authorListSepRe separates the names of an author list.
var authorListSepRe = regexp.MustCompile(`(?i)\s*(?:&|;|,|/|\band\b)\s*`)

// subtitleAuthorSurname returns the surname of the first listed author: the
// last word of the name once notes in brackets and a "Jr." style suffix are
// dropped. A list ("A & B", "A, B", "A and B", "A; B") is read as its first
// name, so the query carries one surname and a release is accepted on that one.
// "" when there is nothing usable.
func subtitleAuthorSurname(author string) string {
	first := authorListSepRe.Split(authorParenRe.ReplaceAllString(author, " "), 2)[0]
	words := strings.Fields(first)
	for len(words) > 0 && authorNameSuffixes[strings.ToLower(strings.Trim(words[len(words)-1], "."))] {
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		return ""
	}
	surname := strings.Trim(words[len(words)-1], ".,")
	folded := textutil.FoldForTitleMatch(surname)
	if anonymousAuthors[folded] || anonymousAuthors[textutil.FoldForTitleMatch(strings.Join(words, " "))] {
		return ""
	}
	letters := 0
	for _, r := range folded {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		}
	}
	if letters < 2 {
		return ""
	}
	return surname
}

// phraseSpan returns the [start, end) position of the first contiguous run of
// phrase in tokens at or after from, or -1, -1.
func phraseSpan(tokens, phrase []string, from int) (int, int) {
	if len(phrase) == 0 {
		return -1, -1
	}
	for i := from; i+len(phrase) <= len(tokens); i++ {
		match := true
		for j, p := range phrase {
			if tokens[i+j] != p {
				match = false
				break
			}
		}
		if match {
			return i, i + len(phrase)
		}
	}
	return -1, -1
}

// subtitleOnlyAccepts reports whether a result from the subtitle only query is
// the wanted book. All of these must hold:
//
//   - the release name holds the whole subtitle as consecutive words;
//   - everything before and after those words is the author's name, "by", or a
//     language or format tag ("[ENG / M4B]", "64 kbps", a year). Another title
//     word ("Will of Mana Rising", "The Will of Mana Wars"), a number ("Will of
//     Mana 2"), a series note ("(Other Saga Book 5)", "Dragon Saga, Book 1 Will of
//     Mana"), an omnibus marker or another language ("(German)") refuses it. A
//     leading "The" is allowed when the subtitle itself starts with it;
//   - the author's surname is a whole word outside the subtitle's words, or in
//     the indexer's author attribute. "Lee-Smith" is not "Lee".
//
// The author is the point. A bare subtitle is a common phrase, and accepting it
// alone is the loose subtitle reading #3268 removed. The rules around the
// subtitle keep the check to a release that IS the subtitle, not one that
// contains it.
func subtitleOnlyAccepts(p subtitleOnlyPlan, r SearchResult) bool {
	rel := tagWords(r.Title)
	words := tagStrings(rel)
	sub := tagStrings(tagWords(p.Subtitle))
	allowThe := len(sub) > 1 && sub[0] == "the"
	if allowThe {
		sub = sub[1:]
	}
	start, end := phraseSpan(words, sub, 0)
	if start < 0 {
		return false
	}
	allowed := map[string]bool{}
	for _, t := range tagWords(p.Author) {
		allowed[t.w] = true
	}
	for _, t := range tagWords(p.Surname) {
		allowed[t.w] = true
	}
	for i, w := range words {
		if i >= start && i < end {
			continue
		}
		if allowed[w] || subtitleReleaseTags[w] || yearRe.MatchString(w) || bitrateRe.MatchString(w) {
			continue
		}
		if allowThe && w == "the" && i == start-1 {
			continue
		}
		// A bitrate written "128 kbps".
		if i+1 < len(words) && i >= end && words[i+1] == "kbps" && len(w) >= 2 && len(w) <= 3 && isAllDigits(w) {
			continue
		}
		return false
	}
	// The surname, as a whole word and not inside the subtitle's own words.
	// The sentinel keeps the phrase from bridging the gap the subtitle leaves
	// or the release's name and its author attribute.
	sentinel := releaseTag{w: "|"}
	rest := append([]releaseTag{}, rel[:start]...)
	rest = append(rest, sentinel)
	rest = append(rest, rel[end:]...)
	rest = append(rest, sentinel)
	rest = append(rest, tagWords(r.Author)...)
	restWords := tagStrings(rest)
	want := tagStrings(tagWords(p.Surname))
	for from := 0; ; {
		s, e := phraseSpan(restWords, want, from)
		if s < 0 {
			return false
		}
		// A hyphen to another name word makes a longer surname; one to a tag
		// ("Arelius-ENG") is only a scene style separator.
		joins := func(w string) bool { return w != "|" && !allowed[w] && !subtitleReleaseTags[w] }
		joinedBefore := s > 0 && rest[s-1].joinNext && joins(rest[s-1].w)
		joinedAfter := rest[e-1].joinNext && e < len(rest) && joins(rest[e].w)
		if !joinedBefore && !joinedAfter {
			return true
		}
		from = s + 1
	}
}

// subtitleOnlyVariant is the subtitle only fallback (#4183). A book titled
// "Path to Transcendence, Book 1: Will of Mana" is listed by some indexers as
// "Will of Mana by Ryan Arelius", and an indexer that needs every query word in
// the release name returns nothing for any query that carries the series and
// position. Like the number word (#3476), apostrophe (#3895) and volume marker
// (#4176) fallbacks it is the last resort: one query per indexer, through the
// same query budget, sent only after all of them ended without a relevant
// result. It is skipped when a relevant result exists, when an earlier fallback
// failed or hit the daily cap, and when the title or author gives no safe query
// (see subtitleOnlyQuery).
//
// Unlike them, its results are not accepted as a batch: each must carry the
// whole subtitle and the author's surname (subtitleOnlyAccepts), and only those
// replace the earlier results. The relevance filter that runs afterwards still
// judges them as it judges any release.
//
// Format plays no part: the categories are the caller's, chosen for the wanted
// media type, and the query is the same for an ebook and an audiobook.
//
// Errors are handled as in volumeMarkerVariant.
func (c *Client) subtitleOnlyVariant(ctx context.Context, title, author string, results []SearchResult, categories []int, trace SearchTrace) ([]SearchResult, SearchTrace, error) {
	if trace.BudgetExhausted || trace.NumberWordError != "" || trace.ApostropheError != "" || trace.VolumeMarkerError != "" || ctx.Err() != nil {
		return results, trace, nil
	}
	plan, ok := subtitleOnlyQuery(title, author)
	if !ok {
		return results, trace, nil
	}
	if len(results) > 0 && titleHasRelevantResult(TransliterateQuery(primaryTitleForQuery(title)), results) {
		return results, trace, nil
	}
	slog.Debug("indexer query subtitle only variant", "title", plan.Query)
	trace.SubtitleQuery = plan.Query
	extra, err := c.Search(ctx, plan.Query, categories)
	if err != nil {
		slog.Debug("indexer query subtitle only variant failed", "error", err)
		trace.SubtitleError = err.Error()
		if errors.Is(err, ErrQueryBudgetExhausted) {
			trace.BudgetExhausted = true
		}
		if IsHardIndexerError(err) {
			return nil, trace, err
		}
		return results, trace, nil
	}
	var kept []SearchResult
	for _, r := range extra {
		if subtitleOnlyAccepts(plan, r) {
			kept = append(kept, r)
		}
	}
	trace.SubtitleResults = len(kept)
	if len(kept) > 0 {
		return kept, trace, nil
	}
	return results, trace, nil
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
