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
	"regexp"
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
	// RuleSubstring: one title is a whole separator-delimited part of the
	// other, the one-sided subtitle case ("Mistborn" vs "Mistborn: The Final
	// Empire"). The API value keeps its original name; see substringMatch.
	// This is the rule with the highest false-positive rate (a novella whose
	// title is near-embedded in the parent novel's); it exists because a
	// human confirms before anything happens.
	RuleSubstring RuleID = "substring"
)

// Guard thresholds for the substring rule. Both apply: the shorter key must
// be at least minSubstringKeyLen characters (so "Go" never matches "Gone"),
// and the longer key must carry at least minResidualLen characters BEYOND the
// embedded one (so "The Martian" never matches "The Martian Child", where the
// residual "child" is a different book, not a subtitle).
//
// On top of the length guards the containment must sit on a separator
// boundary (see titleSegments): the shorter title has to be a whole part of
// the longer one as the longer one is punctuated. A bare substring grouped a
// series opener with every sequel carrying its name ("Foundation" with
// "Prelude to Foundation", "The Science of Discworld" with "The Science of
// Discworld II: The Globe", "Carrie" with "Carrie Soto Is Back") whenever the
// catalogue had no series links to suppress it with.
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

// SeriesSlot is the minimal series-membership fact Scan needs to suppress a
// substring false positive between two genuinely different books in the same
// series (#1970 review: "Foundation" grouping with "Foundation and Empire").
// Two different positions in one series are, by definition, different works,
// even when one title is a prefix of the other. Position is compared as an
// opaque string ("1" and "1.5" differ, correctly); an empty Position is
// unknown and never suppresses anything, so a catalogue with no series data
// keeps today's behaviour.
type SeriesSlot struct {
	SeriesID int64
	Position string
}

// sameSeriesDifferentPosition reports whether a and b share a series but hold
// different known positions in it. Only a resolved Position on both sides
// counts — an unresolved one (no position recorded) never suppresses, because
// "unknown" must not be read as "different".
func sameSeriesDifferentPosition(a, b []SeriesSlot) bool {
	for _, sa := range a {
		if sa.Position == "" {
			continue
		}
		for _, sb := range b {
			if sb.SeriesID == sa.SeriesID && sb.Position != "" && sb.Position != sa.Position {
				return true
			}
		}
	}
	return false
}

// aggressiveFold reduces a title to the shared comparison alphabet — NFC,
// lowercase, apostrophes deleted, umlauts expanded, everything else a space
// (textutil.FoldForTitleMatch, the alphabet every title comparison in the
// repo uses) — with one addition: '&' expands to "and" rather than folding to
// a space. That is the same spelling decision the dedup path makes in
// foldPunctuation ("Foundation & Empire" and "Foundation and Empire" are one
// book), applied here independently so this package never calls a dedup
// function.
//
// The folded result then goes through textutil.FoldForSlug, which drops the
// remaining Latin and Greek diacritics, so "Les Misérables" and "Les
// Miserables" share a key. Umlauts are expanded first ("ü" to "ue"), so a
// German title keeps matching its transliterated spelling.
func aggressiveFold(title string) string {
	return textutil.FoldForSlug(textutil.FoldForTitleMatch(strings.ReplaceAll(title, "&", " and ")))
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

// dropArticle is dropLeadingArticle that also understands the library
// filing form, where the article is moved behind a comma: "Trace of Death, A"
// is "A Trace of Death" (#1691). The comma has to be read from the raw title,
// because folding turns it into a space. Only a lone article after the last
// comma counts, so "Love, Actually" and "Me, Myself and I" are left alone.
func dropArticle(title string, words []string) []string {
	if idx := strings.LastIndex(title, ","); idx >= 0 && len(words) > 1 {
		tail := foldWords(title[idx+1:])
		if len(tail) == 1 && words[len(words)-1] == tail[0] {
			if _, ok := leadingArticles[tail[0]]; ok {
				return words[:len(words)-1]
			}
		}
	}
	return dropLeadingArticle(words)
}

// ArticleKey is the aggressive key with any leading article dropped, or a
// trailing one in the "Title, The" filing form.
func ArticleKey(title string) string {
	return strings.Join(dropArticle(title, foldWords(title)), "")
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

// segmentSplitter matches the punctuation that separates a title from its
// subtitle or a trailing qualifier: a colon or semicolon, brackets, a spaced
// hyphen, or an en or em dash. A bare hyphen inside a word ("Half-Blood") is
// not a separator.
var segmentSplitter = regexp.MustCompile(`[:;()\[\]{}]|\s-+\s|[\x{2013}\x{2014}]`)

// titleSegments returns the alnum and article-stripped keys of every
// separator-delimited part of title, or nil when the title has only one part
// (then no part is a PROPER part, and the whole-title keys already cover it).
// "Mistborn: The Final Empire" yields mistborn and thefinalempire;
// "Hogfather (Discworld, #20)" yields hogfather and discworld20.
func titleSegments(title string) [][2]string {
	parts := segmentSplitter.Split(title, -1)
	var segs [][2]string
	for _, part := range parts {
		words := foldWords(part)
		if len(words) == 0 {
			continue
		}
		segs = append(segs, [2]string{
			strings.Join(words, ""),
			strings.Join(dropLeadingArticle(words), ""),
		})
	}
	if len(segs) < 2 {
		return nil
	}
	return segs
}

// substringMatch reports whether the shorter title is a substantial, whole
// part of the longer one: its key is at least minSubstringKeyLen long,
// leaves at least minResidualLen characters of residual on the longer side,
// the residual is not made up solely of edition-marker words, and the
// shorter key equals one separator-delimited segment of the longer title
// (compared with and without a leading article). The marker guard kills the
// "Complete Works of X" vs "Works of X" false positive, where the residual
// "complete" is doing edition-marker work, not subtitle work.
func substringMatch(shorter, longer bookKeys) bool {
	if len(shorter.alnum) < minSubstringKeyLen {
		return false
	}
	idx := strings.Index(longer.alnum, shorter.alnum)
	if idx < 0 {
		return false
	}
	if len(longer.alnum)-len(shorter.alnum) < minResidualLen {
		return false
	}
	residual := longer.alnum[:idx] + longer.alnum[idx+len(shorter.alnum):]
	if residualIsMarkerOnly(residual) {
		return false
	}
	for _, seg := range longer.segments {
		if seg[0] == shorter.alnum || (seg[1] != "" && seg[1] == shorter.article) {
			return true
		}
	}
	return false
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
// fired for it against the other members. Evidence and HasFiles are filled by
// Annotate (#2999); Scan and Detect leave them zero.
type Member struct {
	models.Book
	Rules    []RuleID `json:"rules"`
	Evidence Evidence `json:"evidence"`
	HasFiles bool     `json:"hasFiles"`
}

// Group is a set of books the detector believes may be the same work. A group
// with a single member is never returned. Rules is the union of every rule
// that fired on any pair within the group, so the UI can explain the group as
// a whole.
//
// AuthorID is set by Detect. AuthorName, Signals, Conflict, KeeperID and
// SuggestedExcludeIDs are review annotations (#2999): the caller fills the
// name, Annotate fills the rest. None of them changes which books group.
type Group struct {
	Key        string   `json:"key"`
	AuthorID   int64    `json:"authorId"`
	AuthorName string   `json:"authorName,omitempty"`
	Rules      []RuleID `json:"rules"`
	Members    []Member `json:"books"`
	// Signals are the agreements and conflicts Annotate found between the
	// group's non-excluded members, in a stable order.
	Signals []Signal `json:"signals"`
	// Conflict is true when any signal is a conflict: the evidence says at
	// least two members may be different books.
	Conflict bool `json:"conflict"`
	// KeeperID is the one non-excluded member that has files, or 0 when no
	// member or more than one member has files. The UI marks it as the row to
	// keep; it is never a suggestion to exclude anything.
	KeeperID int64 `json:"keeperId,omitempty"`
	// SuggestedExcludeIDs are the non-excluded members without files, offered
	// as one confirmed "exclude the empty rows" action. It is non-empty only
	// when there is a KeeperID, no conflict, and positive evidence linking
	// every empty member to the keeper; it never contains a member that has
	// files.
	SuggestedExcludeIDs []int64 `json:"suggestedExcludeIds"`
	// SuggestionWithheld says why SuggestedExcludeIDs is empty: one of the
	// Withheld* values, or empty when there is a suggestion.
	SuggestionWithheld string `json:"suggestionWithheld,omitempty"`
}

type bookKeys struct {
	alnum    string
	article  string
	edition  string
	combined string
	// segments are the keys of each separator-delimited part of the title,
	// nil for a title with one part. Only the substring rule reads them.
	segments [][2]string
}

// bookKeyFor precomputes every key form for one title. Scan calls it once per
// book so the pairwise pass compares precomputed strings.
func bookKeyFor(title string) bookKeys {
	words := foldWords(title)
	return bookKeys{
		alnum:    strings.Join(words, ""),
		article:  strings.Join(dropArticle(title, words), ""),
		edition:  strings.Join(dropTrailingEditionMarkers(words), ""),
		combined: strings.Join(dropTrailingEditionMarkers(dropArticle(title, words)), ""),
		segments: titleSegments(title),
	}
}

// Scan groups the books into duplicate candidate groups. It is a pure
// function: no I/O, no DB, and it deliberately does not look at the
// Excluded flag — exclusion policy (which groups are worth showing, which
// members count as active) belongs to the caller, which is the only layer
// that can act on the result.
//
// seriesSlots is an optional (nil is fine) lookup from book ID to that book's
// series memberships, used solely to suppress the substring rule for two
// books that are different, known positions in the same series (see
// SeriesSlot). The caller collects it because Scan itself does no I/O.
//
// Algorithm: keys are precomputed per book; every pair is evaluated (full
// O(n²) sweep, substring rule gated by maxPairwiseBooks); a substring-only
// match between two different series positions is discarded; pairs with at
// least one surviving rule are unioned; the connected components are the
// groups. Output is deterministic: groups sorted by key, members by book ID.
func Scan(books []models.Book, seriesSlots map[int64][]SeriesSlot) []Group {
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
			if containsRule(rules, RuleSubstring) &&
				sameSeriesDifferentPosition(seriesSlots[books[i].ID], seriesSlots[books[j].ID]) {
				rules = removeRule(rules, RuleSubstring)
			}
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
			if substringMatch(b, a) {
				rules = append(rules, RuleSubstring)
			}
		} else if substringMatch(a, b) {
			rules = append(rules, RuleSubstring)
		}
	}
	return rules
}

func addRule(set map[RuleID]struct{}, rule RuleID) {
	set[rule] = struct{}{}
}

func containsRule(rules []RuleID, target RuleID) bool {
	for _, r := range rules {
		if r == target {
			return true
		}
	}
	return false
}

// removeRule drops one rule from a freshly-built pairRules result. Safe to
// mutate in place: rules is always a slice pairRules allocated for this call,
// never one shared with a caller.
func removeRule(rules []RuleID, target RuleID) []RuleID {
	out := rules[:0]
	for _, r := range rules {
		if r != target {
			out = append(out, r)
		}
	}
	return out
}
