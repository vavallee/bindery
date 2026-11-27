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
// the two things a result must carry to be accepted from it.
type subtitleOnlyPlan struct {
	Query    string // "Arelius Will of Mana"
	Subtitle string // "Will of Mana"
	Surname  string // "Arelius"
}

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
//     ("Dune: Messiah", "Foo, Book: Bar"); the first colon is the one that
//     counts, so "Series 2: A: B" queries "A: B" as the words "A B";
//   - the subtitle has fewer than two significant words (SigWords: three bytes
//     or more, no stop words), which also covers one that is too short or all
//     stop words;
//   - every significant word of the subtitle is generic ("A Novel", "Book Two",
//     "Volume 3": see genericSubtitleWords and numberWords);
//   - the author is empty, a placeholder ("Various"), or has no surname token
//     of at least two letters (an initial alone).
func subtitleOnlyQuery(title, author string) (subtitleOnlyPlan, bool) {
	t := StripFormatQualifiers(NormalizeQueryTitle(FoldNumberWords(title)))
	sub := strings.Join(strings.Fields(SeriesPositionTitle(t)), " ")
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
	return subtitleOnlyPlan{
		Query:    TransliterateQuery(surname + " " + wildcardToSpace(sub)),
		Subtitle: sub,
		Surname:  surname,
	}, true
}

// authorListSepRe separates the names of an author list.
var authorListSepRe = regexp.MustCompile(`(?i)\s*(?:&|;|,|/|\band\b)\s*`)

// subtitleAuthorSurname returns the surname of the first listed author: the
// last word of the name once a "Jr." style suffix is dropped. A list ("A & B",
// "A, B", "A and B", "A; B") is read as its first name, so the query carries one
// surname and a release is accepted on that one. "" when there is nothing
// usable.
func subtitleAuthorSurname(author string) string {
	first := authorListSepRe.Split(author, 2)[0]
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
// phrase in tokens, or -1, -1.
func phraseSpan(tokens, phrase []string) (int, int) {
	if len(phrase) == 0 {
		return -1, -1
	}
	for i := 0; i+len(phrase) <= len(tokens); i++ {
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

// subtitleOnlyAccepts reports whether a result from the subtitle only query
// names the wanted book: the whole subtitle as consecutive words, AND the
// author's surname as a word elsewhere in the release name or in its author
// attribute. The surname must be outside the words that matched the subtitle,
// so a surname that is also a word of the subtitle cannot vouch for itself.
//
// The author is the point. A bare subtitle is a common phrase, and accepting it
// alone is the loose subtitle reading #3268 removed, which approved another
// author's book for a wanted one. "Will of Mana by Someone Else" is rejected.
func subtitleOnlyAccepts(p subtitleOnlyPlan, r SearchResult) bool {
	tokens := strings.Fields(textutil.FoldForTitleMatch(r.Title))
	start, end := phraseSpan(tokens, strings.Fields(textutil.FoldForTitleMatch(p.Subtitle)))
	if start < 0 {
		return false
	}
	// A sentinel word that no fold produces keeps the surname phrase from
	// bridging the gap the subtitle leaves, or the release's title and author.
	rest := append(append([]string{}, tokens[:start]...), "|")
	rest = append(rest, tokens[end:]...)
	rest = append(rest, "|")
	rest = append(rest, strings.Fields(textutil.FoldForTitleMatch(r.Author))...)
	s, _ := phraseSpan(rest, strings.Fields(textutil.FoldForTitleMatch(p.Surname)))
	return s >= 0
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
