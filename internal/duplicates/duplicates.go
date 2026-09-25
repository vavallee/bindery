// Package duplicates implements the read-only duplicate-title detection pass
// requested in #1970: a separate, deliberately permissive layer that finds
// book rows a human would recognize as the same work even when
// indexer.CanonicalDedupKey does not collapse them (a differently-phrased
// title variant, a subtitle that doesn't follow the ": subtitle" pattern, two
// OpenLibrary works that are really one book).
//
// # Boundary — read this before touching anything in this package
//
// This package is DETECTION ONLY. It never decides whether an incoming record
// is a new book, never populates books.dedup_key, and is never called from an
// ingestion path. The exact-key dedup machinery in internal/indexer
// (CanonicalDedupKey, NormalizeTitleForDedup, CompareTitles) stays the single
// authoritative identity function for book creation (#940, #2042); loosening
// THAT to catch more duplicates would trade missed-duplicate noise for
// silent false merges, which loses data and is the worse failure mode.
//
// The trade this package makes is the deliberate one from #1970: its
// normalization and rules are more aggressive than the ingestion key, in
// exchange for the fact that its output is only ever SHOWN to a person, who
// confirms before anything is excluded. Nothing in this package writes.
//
// The import boundary is enforced by TestIngestionPackagesNeverImportThis in
// imports_test.go: no package outside internal/api may import this one.
package duplicates

import (
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// RuleID names one detection rule. The values are stable identifiers the API
// returns and the UI translates, so renaming one is a user-visible breaking
// change — add new rules, don't rename old ones.
type RuleID string

const (
	// RuleAlnumEqual: the aggressive keys are equal. The core case — the same
	// work whose title differs only in punctuation, case, Unicode form,
	// umlaut representation, or apostrophe style.
	RuleAlnumEqual RuleID = "alnum-equal"
	// RuleArticleStrip: the keys differ, but become equal once a leading
	// article is dropped from each side ("Martian" vs "The Martian").
	RuleArticleStrip RuleID = "article-strip"
	// RuleEditionSuffix: the keys differ, but become equal once a trailing
	// edition qualifier ("Unabridged", "Expanded Edition", ...) is dropped
	// from each side.
	RuleEditionSuffix RuleID = "edition-suffix"
	// RuleSubstring: one key is a substantial substring of the other — the
	// one-sided subtitle case ("Mistborn" vs "Mistborn: The Final Empire").
	// This is the rule with the highest false-positive rate (a novella whose
	// title is near-embedded in the parent novel's); it exists because a
	// human confirms before anything happens.
	RuleSubstring RuleID = "substring"
)

// Guard thresholds for the substring rule. Both apply: the shorter key must
// be at least minSubstringKeyLen characters (so "Go" never matches "Gone"),
// and the longer key must carry at least minResidualLen characters BEYOND the
// embedded one (so "The Martian" never matches "The Martian Child" — the
// residual "child" is a different book, not a subtitle).
const (
	minSubstringKeyLen = 6
	minResidualLen     = 6
)

// maxPairwiseBooks gates the substring rule. The pairwise loop always runs
// its full O(n²) sweep, but every pair check is a cheap string comparison
// except substring, which scans; above this catalogue size the scan is
// skipped, so a pathological catalogue stays fast — correct, just less
// aggressive.
const maxPairwiseBooks = 2000

// aggressiveFold reduces a title to the shared comparison alphabet — NFC,
// lowercase, apostrophes deleted, umlauts expanded, everything else a space
// (textutil.FoldForTitleMatch, the alphabet every title comparison in the
// repo uses) — with one addition: '&' expands to "and" rather than folding to
// a space. That is the same spelling decision the dedup path makes in
// foldPunctuation ("Foundation & Empire" and "Foundation and Empire" are one
// book), applied here independently so this package never calls a dedup
// function.
func aggressiveFold(title string) string {
	return textutil.FoldForTitleMatch(strings.ReplaceAll(title, "&", " and "))
}

// foldWords is aggressiveFold tokenized into lowercase alnum words.
func foldWords(title string) []string {
	return strings.Fields(aggressiveFold(title))
}

// AggressiveTitleKey is the detection key: the title folded to alphanumerics
// only, with no spaces. It is LOSSY by design — it exists to be permissive,
// and its output is compared for equality and substring, never stored as an
// identity and never written to books.dedup_key.
//
// It is intentionally NOT the same function as indexer.CanonicalDedupKey:
// the dedup key is lossless and authoritative, this one is aggressive and
// advisory. Keeping them separate functions is the whole point of #1970's
// "separate, read-only detection pass".
func AggressiveTitleKey(title string) string {
	return strings.Join(foldWords(title), "")
}

// leadingArticles are the whole-word leading articles dropped by the
// article-strip rule. Deliberately a closed list, matched as a complete first
// word: "Theatre of Blood" must not collapse because "Theatre" starts with
// "the".
var leadingArticles = map[string]struct{}{
	// English
	"the": {}, "a": {}, "an": {},
	// German
	"der": {}, "die": {}, "das": {},
	// French / Spanish / Portuguese
	"le": {}, "la": {}, "les": {}, "el": {}, "los": {}, "las": {}, "de": {},
	// Italian
	"il": {}, "gli": {}, "una": {},
	// Dutch
	"een": {}, "het": {},
}

// dropLeadingArticle removes the first word when it is a whole-word leading
// article. The len > 1 guard keeps a title that IS an article ("A", "Die")
// from keying to empty — an empty key must never match anything.
func dropLeadingArticle(words []string) []string {
	if len(words) > 1 {
		if _, ok := leadingArticles[words[0]]; ok {
			return words[1:]
		}
	}
	return words
}

// ArticleKey is the aggressive key with any leading article dropped.
func ArticleKey(title string) string {
	return strings.Join(dropLeadingArticle(foldWords(title)), "")
}

// editionMarkers are the trailing qualifier sequences the edition-suffix rule
// drops, longest first (a two-word marker must win over the bare "edition").
// Matching is strictly TRAILING: "Complete Works of Shakespeare" keeps its
// leading "complete", because that word is doing title work there.
var editionMarkers = [][]string{
	{"expanded", "edition"},
	{"anniversary", "edition"},
	{"deluxe", "edition"},
	{"definitive", "edition"},
	{"revised", "edition"},
	{"unabridged"},
	{"abridged"},
	{"audiobook"},
	{"dramatized"},
	{"dramatised"},
	{"illustrated"},
	{"complete"},
	{"edition"},
}

// dropTrailingEditionMarkers repeatedly strips a known edition qualifier from
// the end of the word list ("Title [Unabridged] [2021]" style stacking),
// stopping when nothing matches. The len > n guard keeps a title that IS a
// marker ("Unabridged") from keying to empty.
func dropTrailingEditionMarkers(words []string) []string {
	for {
		removed := false
		for _, marker := range editionMarkers {
			n := len(marker)
			if len(words) > n && wordsEqual(words[len(words)-n:], marker) {
				words = words[:len(words)-n]
				removed = true
			}
		}
		if !removed {
			return words
		}
	}
}

// EditionKey is the aggressive key with trailing edition qualifiers dropped.
func EditionKey(title string) string {
	return strings.Join(dropTrailingEditionMarkers(foldWords(title)), "")
}

func wordsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// substringMatch reports whether the shorter key is a substantial part of the
// longer one: at least minSubstringKeyLen long itself, leaving at least
// minResidualLen characters of residual on the longer side, and the residual
// is not made up solely of edition-marker words. That last guard kills the
// "Complete Works of X" vs "Works of X" false positive, where the residual
// "complete" is doing edition-marker work, not subtitle work.
func substringMatch(shorter, longer string) bool {
	if len(shorter) < minSubstringKeyLen {
		return false
	}
	idx := strings.Index(longer, shorter)
	if idx < 0 {
		return false
	}
	if len(longer)-len(shorter) < minResidualLen {
		return false
	}
	residual := longer[:idx] + longer[idx+len(shorter):]
	return !residualIsMarkerOnly(residual)
}

// markerWords is the flat set of every word that appears in editionMarkers,
// used by residualIsMarkerOnly.
var markerWords = map[string]struct{}{
	"expanded": {}, "anniversary": {}, "deluxe": {}, "definitive": {}, "revised": {},
	"edition": {}, "unabridged": {}, "abridged": {}, "audiobook": {},
	"dramatized": {}, "dramatised": {}, "illustrated": {}, "complete": {},
}

func residualIsMarkerOnly(residual string) bool {
	if residual == "" {
		return false
	}
	for _, w := range strings.Fields(residual) {
		if _, ok := markerWords[w]; !ok {
			return false
		}
	}
	return true
}

// MatchRules evaluates every rule independently against a pair of titles and
// returns all that fire, in canonical order. A blank key on either side never
// matches: an untitled row must not collapse onto every other untitled row
// (the same invariant CompareTitles enforces for the exact key).
//
// One composition case: when neither transformation alone equalizes the keys
// but BOTH together do ("Hobbit Unabridged" vs "The Hobbit"), both
// RuleArticleStrip and RuleEditionSuffix are reported — the pair genuinely
// needed both rewrites, and the UI's explanation is "differs only in a
// leading article and an edition qualifier".
func MatchRules(a, b string) []RuleID {
	ka, kb := AggressiveTitleKey(a), AggressiveTitleKey(b)
	if ka == "" || kb == "" {
		return nil
	}
	return pairRules(bookKeyFor(a), bookKeyFor(b), true)
}

// Member is one book in a candidate group, annotated with the rules that
// fired for it against the other members.
type Member struct {
	models.Book
	Rules []RuleID `json:"rules"`
}

// Group is a set of books the detector believes may be the same work. A group
// with a single member is never returned. Rules is the union of every rule
// that fired on any pair within the group, so the UI can explain the group as
// a whole.
type Group struct {
	Key     string   `json:"key"`
	Rules   []RuleID `json:"rules"`
	Members []Member `json:"books"`
}

type bookKeys struct {
	alnum    string
	article  string
	edition  string
	combined string
}

// bookKeyFor precomputes every key form for one title. Scan calls it once per
// book so the pairwise pass compares precomputed strings.
func bookKeyFor(title string) bookKeys {
	words := foldWords(title)
	return bookKeys{
		alnum:    strings.Join(words, ""),
		article:  strings.Join(dropLeadingArticle(words), ""),
		edition:  strings.Join(dropTrailingEditionMarkers(words), ""),
		combined: strings.Join(dropTrailingEditionMarkers(dropLeadingArticle(words)), ""),
	}
}

// Scan groups the books into duplicate candidate groups. It is a pure
// function: no I/O, no DB, and it deliberately does not look at the
// Excluded flag — exclusion policy (which groups are worth showing, which
// members count as active) belongs to the caller, which is the only layer
// that can act on the result.
//
// Algorithm: keys are precomputed per book; every pair is evaluated (full
// O(n²) sweep, substring rule gated by maxPairwiseBooks); pairs with at
// least one firing rule are unioned; the connected components are the
// groups. Output is deterministic:
// groups sorted by key, members by book ID.
func Scan(books []models.Book) []Group {
	n := len(books)
	keys := make([]bookKeys, n)
	for i, b := range books {
		keys[i] = bookKeyFor(b.Title)
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	rulesByBook := make([]map[RuleID]struct{}, n)
	for i := range rulesByBook {
		rulesByBook[i] = make(map[RuleID]struct{})
	}
	useSubstring := n <= maxPairwiseBooks

	for i := 0; i < n; i++ {
		if keys[i].alnum == "" {
			continue
		}
		for j := i + 1; j < n; j++ {
			if keys[j].alnum == "" {
				continue
			}
			rules := pairRules(keys[i], keys[j], useSubstring)
			if len(rules) == 0 {
				continue
			}
			union(i, j)
			for _, rule := range rules {
				addRule(rulesByBook[i], rule)
				addRule(rulesByBook[j], rule)
			}
		}
	}

	byRoot := make(map[int][]int)
	for i := 0; i < n; i++ {
		root := find(i)
		byRoot[root] = append(byRoot[root], i)
	}

	groups := make([]Group, 0, len(byRoot))
	for _, idxs := range byRoot {
		if len(idxs) < 2 {
			continue
		}
		sort.Slice(idxs, func(a, b int) bool { return books[idxs[a]].ID < books[idxs[b]].ID })
		group := Group{
			Key:     keys[idxs[0]].alnum,
			Members: make([]Member, 0, len(idxs)),
		}
		groupRuleSet := make(map[RuleID]struct{})
		for _, idx := range idxs {
			m := Member{Book: books[idx]}
			for rule := range rulesByBook[idx] {
				m.Rules = append(m.Rules, rule)
				groupRuleSet[rule] = struct{}{}
			}
			sort.Slice(m.Rules, func(a, b int) bool { return m.Rules[a] < m.Rules[b] })
			group.Members = append(group.Members, m)
		}
		for rule := range groupRuleSet {
			group.Rules = append(group.Rules, rule)
		}
		sort.Slice(group.Rules, func(a, b int) bool { return group.Rules[a] < group.Rules[b] })
		groups = append(groups, group)
	}
	sort.Slice(groups, func(a, b int) bool { return groups[a].Key < groups[b].Key })
	return groups
}

// pairRules evaluates the rules for a pair using precomputed keys. The
// alnum-equal check is a bucket comparison; article and edition are key
// comparisons; the combined key catches pairs that need both rewrites;
// substring is the guarded containment check.
func pairRules(a, b bookKeys, useSubstring bool) []RuleID {
	var rules []RuleID
	if a.alnum == b.alnum {
		return []RuleID{RuleAlnumEqual}
	}
	articleEq := a.article != "" && b.article != "" && a.article == b.article
	editionEq := a.edition != "" && b.edition != "" && a.edition == b.edition
	if articleEq {
		rules = append(rules, RuleArticleStrip)
	}
	if editionEq {
		rules = append(rules, RuleEditionSuffix)
	}
	if !articleEq && !editionEq &&
		a.combined != "" && b.combined != "" && a.combined == b.combined {
		rules = append(rules, RuleArticleStrip, RuleEditionSuffix)
	}
	if useSubstring {
		if len(a.alnum) > len(b.alnum) {
			if substringMatch(b.alnum, a.alnum) {
				rules = append(rules, RuleSubstring)
			}
		} else if substringMatch(a.alnum, b.alnum) {
			rules = append(rules, RuleSubstring)
		}
	}
	return rules
}

func addRule(set map[RuleID]struct{}, rule RuleID) {
	set[rule] = struct{}{}
}
