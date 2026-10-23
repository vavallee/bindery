package metadata

import (
	"regexp"
	"strings"
)

// Bundle-title detection, split into two confidence tiers.
//
// These patterns were ported from the interim external workaround
// (denoise_author.py) in #1968 after being confirmed empirically, over
// several authors, against real OpenLibrary titles. They are a title-text
// heuristic rather than a work-type field because OpenLibrary does not
// reliably distinguish a "bundle" work from a regular one anywhere in its
// API response: a box set arrives as an ordinary Work, passes every other
// filter (non-empty title, language set, media type matches) and lands in
// the wanted list indistinguishable from a real book.
//
// The tiers exist because those patterns do not all carry the same risk
// (#1780). The unambiguous ones name a physical bundle in words no
// single-book title uses for anything else, so they run on every catalogue
// ingestion regardless of settings. The ambiguous ones have documented
// real-world false positives, so they stay behind the opt-in SkipPartBooks
// metadata-profile setting where their risk was accepted deliberately.
//
// "trilogy" is deliberately in neither tier: far too many real single books
// are subtitled "Book One of the X Trilogy".

// unambiguousBundleTitleRe matches the tier that runs unconditionally. Every
// branch names a multi-volume product explicitly ("box set", "3 Books Set",
// "Carton of 10 Signed Copies"); none of them is phrasing a real single book
// uses to describe itself.
var unambiguousBundleTitleRe = regexp.MustCompile(`(?i)` +
	`\bbox\s*set\b` +
	`|\bboxed\s*set\b` +
	// Parenthesized bare "(Boxed)". Real OL titles drop "set" entirely,
	// e.g. "4 Vol. (Boxed)". Deliberately NOT a bare \bboxed\b: that would
	// also match "boxed" used as an ordinary word in a real title ("Boxed
	// In"), which the parenthesized form this was ported from never does.
	`|\(\s*boxed\s*\)` +
	// Trailing parenthesized "(Set)" (#3013), e.g. "Just Beyond (Set)". The
	// parentheses carry the same weight as in "(Boxed)": a real title that
	// uses the word says "Set in Stone" or "Upset", and a longer
	// parenthetical such as "(Set in Paris)" or "(Settings)" does not match
	// because only the whole word between the brackets counts. Anchored to
	// the end of the title: a "(Set)" with text after it ("Foo (Set): A
	// Novel", "Ready (Set) Go") is part of a title, not a product label.
	// Residual: a trailing "(Set)" on a title that really is one book is
	// caught; the observed catalogue shape is always the trailing label.
	`|\(\s*set\s*\)\s*$` +
	`|\bcollection\s*set\b` +
	`|\bcarton\s+of\s+\d+\s+signed\s+cop` +
	`|\b\d+\s*(?:books?|vol(?:ume)?s?)\s+set\b`) // "3 Books Set", "5 Volumes Set"

// ambiguousBundleTitleRe matches the tier that stays behind SkipPartBooks.
// Each branch has a known false-positive shape, recorded below, which is why
// switching it on is left to the user rather than applied to everyone.
//
// The two slash branches require actual whitespace around every "/" ("\s+"
// either side, not "\s*"). Real anthology naming is always spaced ("Title A
// / Title B"); an unspaced slash is far more often a title using "/" as its
// own punctuation (a two-character-choice title like "He/She/It", or
// "Rock/Paper/Scissors") than a bundle. Confirmed against real
// maintainer-reported false positives (vavallee, PR #1968 review).
//
// Even spaced and with 3+ segments the shape is not conclusive: a genuine
// single-volume edition bundling several distinct classic works looks
// identical, e.g. the Penguin Nietzsche edition "The Anti-Christ / Ecce Homo
// / Twilight of the Idols".
var ambiguousBundleTitleRe = regexp.MustCompile(`(?i:` +
	`\bcollection\s+of\s+\d` + // also matches a real "A Collection of 12 Stories".
	`|\bbooks?\s+\d+\s*-\s*\d+\b` + // "Books 1-3". Could also match a real single
	// volume a publisher numbered like "Book 1 to 2", which is not observed
	// in the wild but is noted rather than left a surprise.
	// "Harry Potter (series) 1-3" (#3013). The parenthesised word needs a
	// numeric range right after it: a bare "Redwall (Series)" is a label some
	// providers append to a single volume, not a count.
	`|\(\s*series\s*\)\s*\d+\s*-\s*\d+\b` +
	// A trailing count of two or more with no "set" word: "How To Train Your
	// Dragon - 10 Books", "Dragon Riders (3 Books)", with one optional trailing
	// "!" or "." ("... - 10 Books!"). Anchored to the end of the
	// title so "10 Books That Changed America" (a single nonfiction title) is
	// spared; a count of one is a single book. Residual: a real title that
	// merely ends in "<N> Books" ("Top 100 Books") is caught, which is why it
	// sits behind SkipPartBooks. Spelled numbers are never matched.
	`|\b(?:[2-9]|\d{2,})\s+books\s*[)\]]?\s*[!.]?\s*$` +
	// A leading count with up to three words before "Books!": the retailer
	// listing shape "2 Daniel Silva Books! 1) The Order of Death". The "!"
	// glued to "Books" is the discriminator; "100 Books You Must Read" and
	// "10 Books That Changed America" have no "!" there and are spared.
	`|^\s*(?:[2-9]|\d{2,})\s+(?:[^\s!]+\s+){0,3}books\s*!` +
	`)` +
	`|(?:[^/]+\s+/\s+){2,}[^/]+` + // "Title A / Title B / Title C" multi-title anthology naming
	`|\([^()]+\s+/\s+[^()]+\)\s*$`) // "Prefix (Title A / Title B)", 2-title anthology naming inside parens

// bracketContentRe extracts the content of each bracketed span in a title,
// for hasJoinedBundleBracket.
var bracketContentRe = regexp.MustCompile(`\[([^\]]*)\]`)

// hasJoinedBundleBracket reports whether title has a bracketed annotation
// naming a bundle, e.g. "The Hobbit & The Lord of the Rings [collection/set]".
// Requires the bracket content to contain BOTH a "/" AND one of
// collection/set/boxed, not just any one of those words alone, which a
// real single book's bracketed edition/provenance note could plausibly also
// contain for an unrelated reason (e.g. "[Author's Personal Collection]",
// "[Set in Wartime London]"). The joined-pair shape ("X/Y") is what was
// actually observed on real OpenLibrary bundle records; a lone keyword
// wasn't. Ambiguous tier: it leans on the same slash convention as the
// branches above.
func hasJoinedBundleBracket(title string) bool {
	for _, m := range bracketContentRe.FindAllStringSubmatch(title, -1) {
		content := strings.ToLower(m[1])
		if !strings.Contains(content, "/") {
			continue
		}
		if strings.Contains(content, "collection") || strings.Contains(content, "set") || strings.Contains(content, "boxed") {
			return true
		}
	}
	return false
}

// omnibusWordRe backs hasNonLeadingOmnibus. The leading-article strip reuses
// leadingArticleRe (aggregator_author_works.go), which is the same
// "The"/"A"/"An" prefix pattern.
var omnibusWordRe = regexp.MustCompile(`(?i)\bomnibus\b`)

// hasNonLeadingOmnibus reports whether "omnibus" appears in title as a
// description of a bundle rather than as the title's own subject. Real
// compilation titles put "omnibus" after the name of what's bundled ("The
// Dune Omnibus", "The Silmarillion Omnibus"); a single real book can also use
// "Omnibus" as its own proper-noun title ("The Omnibus of Crime", a genuine
// Dorothy Sayers volume) or as a publisher's brand name leading the title
// ("Omnibus Press Presents..."). Both shapes put "omnibus" at (or immediately
// after only a leading article before) the very start of the title, which
// this excludes.
//
// Known residual gap, and the reason a bare "omnibus" stays in the ambiguous
// tier: this assumes real non-bundle usage always leads and real
// bundle-descriptor usage always trails. Two real books break that. "The New
// Turing Omnibus" and "Thrown under the omnibus" both use "omnibus"
// metaphorically/idiomatically in a trailing position, so they are still
// wrongly caught. The real distinguishing signal (does this book actually
// bundle other separately-catalogued works) is not recoverable from title
// text alone for a bare keyword the way it is for the slash-joined case
// (pruneAuthorWorkRedundantTitles can check named segments against known
// titles). Hardcover's IsCompilation classification gets both right when
// configured.
func hasNonLeadingOmnibus(title string) bool {
	if !omnibusWordRe.MatchString(title) {
		return false
	}
	stripped := leadingArticleRe.ReplaceAllString(strings.TrimSpace(title), "")
	return !strings.HasPrefix(strings.ToLower(stripped), "omnibus")
}

// IsUnambiguousBundleTitle reports whether title names a box set, boxed set,
// collection set, trailing parenthesised "(Set)", signed-copy carton or "N
// books/volumes set": the tier confident enough to prune from every author
// catalogue without waiting on a profile setting (#1780). See unambiguousBundleTitleRe.
func IsUnambiguousBundleTitle(title string) bool {
	return unambiguousBundleTitleRe.MatchString(title)
}

// isAmbiguousBundleTitle reports whether title matches one of the
// lower-confidence bundle shapes: a bare trailing "omnibus", the
// slash-separated multi-title namings, a bundle-naming bracket, "Books
// N-M", "(series) N-M", or an "N Books" count. Only consulted for profiles
// that opted in via SkipPartBooks.
func isAmbiguousBundleTitle(title string) bool {
	return ambiguousBundleTitleRe.MatchString(title) ||
		hasNonLeadingOmnibus(title) ||
		hasJoinedBundleBracket(title)
}

// IsBundleTitle reports whether title looks like a box set, omnibus, carton
// or anthology rather than a single book, across both confidence tiers. This
// is the full set the SkipPartBooks metadata-profile setting screens out; the
// unconditional catalogue-ingestion prune uses IsUnambiguousBundleTitle
// instead.
func IsBundleTitle(title string) bool {
	return IsUnambiguousBundleTitle(title) || isAmbiguousBundleTitle(title)
}
