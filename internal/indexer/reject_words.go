package indexer

import (
	"strings"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// SettingRejectWords is the settings key holding the words and phrases a
// release title may not contain (#3416). The value is one list: entries are
// separated by commas or new lines. Empty, the default, rejects nothing.
// internal/api registers the key; the scheduler and the interactive search
// both read it into MatchCriteria.RejectWords.
const SettingRejectWords = "search.rejectWords"

// ParseRejectWords splits the stored setting value into its entries, trimmed,
// with blanks and repeats removed. Entries are plain words and phrases: there
// is no pattern syntax, so "c.st" never means "cast".
func ParseRejectWords(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		e = strings.TrimSpace(e)
		key := strings.Join(strings.Fields(NormalizeRelease(e)), " ")
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
// entry from the rest. Titles and entries both go through NormalizeRelease, the
// normaliser the title matching uses, so case, accents and every separator
// ("Graphic.Audio", "Graphic_Audio", "graphic-audio") read alike. An entry
// matches only as whole, adjacent tokens, so "cast" does not hit "Broadcast".
func splitRejectedWords(results []newznab.SearchResult, entries []string) ([]newznab.SearchResult, []DroppedRelease) {
	var rejects []rejectEntry
	for _, e := range entries {
		if toks := strings.Fields(NormalizeRelease(e)); len(toks) > 0 {
			rejects = append(rejects, rejectEntry{text: e, tokens: toks})
		}
	}
	if len(rejects) == 0 {
		return results, nil
	}
	kept := make([]newznab.SearchResult, 0, len(results))
	var dropped []DroppedRelease
	for _, r := range results {
		hay := strings.Fields(NormalizeRelease(r.Title))
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
