package indexer

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// SettingRejectWords is the settings key holding the words and phrases a
// release title may not contain (#3416). The value is one list: entries are
// separated by commas or new lines. Empty, the default, rejects nothing.
// internal/api registers the key; the scheduler and the interactive search
// both read it into MatchCriteria.RejectWords.
const SettingRejectWords = "search.rejectWords"

// rejectFold is NormalizeRelease plus accent folding, for the rejected words
// matcher alone: the shared title matcher keeps accents. Latin letters are
// decomposed and their combining marks dropped, so "Dramatisé" and "dramatise"
// read alike. Marks on other scripts (kana voicing, Hangul, Indic vowel signs)
// are kept, because there they change the letter.
func rejectFold(s string) string {
	s = NormalizeRelease(s)
	var b strings.Builder
	var base rune
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) && unicode.Is(unicode.Latin, base) {
			continue
		}
		base = r
		b.WriteRune(r)
	}
	return b.String()
}

// rejectTokens returns the folded tokens of s.
func rejectTokens(s string) []string { return strings.Fields(rejectFold(s)) }

// ParseRejectWords splits the stored setting value into its entries, trimmed,
// with blanks and repeats removed. Entries are plain words and phrases: there
// is no pattern syntax, so "c.st" never means "cast".
func ParseRejectWords(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		e = strings.TrimSpace(e)
		key := strings.Join(rejectTokens(e), " ")
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

// rejectedWordReason is the Filtered reason for a release carrying a rejected
// word or phrase, as typed in the setting.
func rejectedWordReason(entry string) string {
	return `release title contains the rejected word "` + entry + `"`
}

type rejectEntry struct {
	text   string
	tokens []string
}

// splitRejectedWords separates the releases whose title contains any rejected
// entry from the rest. Titles and entries both go through rejectFold, so case,
// accents and every separator ("Graphic.Audio", "Graphic_Audio", "graphic-audio")
// read alike. An entry matches only as whole, adjacent tokens, so "cast" does
// not hit "Broadcast".
//
// An entry made only of words from the wanted book's own title or author (the
// own strings) is not applied: rejecting "Cast" must not empty the search for
// "Cast Away", nor "Frank" the search for Frank Herbert. An entry with any other
// word, such as "Full Cast", still applies.
func splitRejectedWords(results []newznab.SearchResult, entries []string, own []string) ([]newznab.SearchResult, []DroppedRelease) {
	ownTokens := map[string]bool{}
	for _, o := range own {
		for _, t := range rejectTokens(o) {
			ownTokens[t] = true
		}
	}
	var rejects []rejectEntry
	for _, e := range entries {
		toks := rejectTokens(e)
		if len(toks) == 0 {
			continue
		}
		allOwn := true
		for _, t := range toks {
			if !ownTokens[t] {
				allOwn = false
				break
			}
		}
		if !allOwn {
			rejects = append(rejects, rejectEntry{text: e, tokens: toks})
		}
	}
	if len(rejects) == 0 {
		return results, nil
	}
	kept := make([]newznab.SearchResult, 0, len(results))
	var dropped []DroppedRelease
	for _, r := range results {
		hay := rejectTokens(r.Title)
		hit := ""
		for _, e := range rejects {
			if containsTokens(hay, e.tokens) {
				hit = e.text
				break
			}
		}
		if hit == "" {
			kept = append(kept, r)
			continue
		}
		dropped = append(dropped, DroppedRelease{Result: r, Stage: StageRelevance, Reason: rejectedWordReason(hit)})
	}
	return kept, dropped
}

// containsTokens reports whether needle occurs in hay as a contiguous run.
func containsTokens(hay, needle []string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		j := 0
		for j < len(needle) && hay[i+j] == needle[j] {
			j++
		}
		if j == len(needle) {
			return true
		}
	}
	return false
}
