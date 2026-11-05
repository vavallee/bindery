package indexer

import (
	"regexp"
	"strconv"
	"strings"
)

// Multi-book pack detection for release names (#2276).
//
// A Bindery download row carries exactly one BookID, and the importer
// computes exactly one destination folder from it. A release that is
// explicitly several books therefore has no correct outcome: every file in
// it lands in one book's folder. Nothing downstream can recover from that,
// so the release has to be refused at selection time.
//
// This is deliberately NOT the bundle detection in internal/metadata
// (bundle_titles.go). That one reads OpenLibrary catalogue titles, which are
// editorial prose; these are scene release names, which are punctuation
// soup. Its multi-title branch keys on spaced slashes, and the release that
// reported this issue carries "[ENG / M4B MP3]" — a tag separator, not an
// anthology. Reusing it here would have produced exactly the wrong rejection
// for exactly the release that motivated the work.
//
// What is matched, and why the boundaries sit where they do:
//
//   - An explicit numbered range after "book", "vol" or "volume": "Books 1-4",
//     "Book 1 to 4", "Vols. 1 & 2". Both numbers are capped at two digits so
//     "Book 1 - 2020 Edition" is not read as a range of 2020 books.
//   - A counted bundle: "4 Book Set", "3 Volume Collection".
//   - "box set" / "boxed set" / "boxset", "omnibus", and "complete series" or
//     "complete collection".
//
// What is deliberately NOT matched:
//
//   - "part". A single audiobook split into "Part 1-2" and "Part 3" is the
//     normal shape of a long recording, and is in fact the exact shape of the
//     download in #2275. Treating it as a pack would refuse ordinary
//     audiobooks.
//   - "trilogy". Real single books are routinely subtitled "Book III of the
//     Red Rising Trilogy" — again, the very release under discussion.
//   - A bare "collection", "anthology" or "series". Single books use all
//     three about themselves ("The Collected Stories", "Red Rising Series -
//     Book 1"). The shapes of those words that only a pack uses are matched
//     instead, see below.
//   - A bare numeric range with no preceding noun ("01-04"), which in a
//     release name is far more often a year span, a bitrate, or a chapter
//     count than a book range.
//
// Added for #3224, from the release names of one search for "Foundation":
//
//   - A counted bundle in brackets: "(11 Books)", "[03 Ebooks]", never "[1 Book]". The count and
//     the noun sit inside brackets, which is where indexers put what the
//     release contains, not what the book is called.
//   - "Books Collection" (plural only: "The Poe Book Collection" is one
//     volume), "Collection Pack", "Series Pack" and a bracketed
//     "(Foundation Pack)": the word that makes the collection a product.
//   - "Saga eBooks", "Series eBooks": the plural noun after the series word
//     is the series' books, not one of them.
//   - "<name> series by <author>" when nothing but format or noise words
//     (EPUB, ebook, kindle, complete, a year, a bracketed tag) follows the
//     author. A release of one book gives its title after a dash or colon
//     ("Discworld Series by Terry Pratchett - Guards! Guards!") or a book
//     number ("Red Rising Series by Pierce Brown, Book 1"). "A Series of
//     Unfortunate Events" is a title, so "series of" never matches.
//   - A numeric range after "series", either written compactly right after it
//     ("Discworld Series 1-41", "Series (1-7)", "Series - 1-7") or after at
//     least one word ("Series - Isaac Asimov 1-7"), unless the number is a
//     part or disc ("Series Part 1-2" is one split recording). A spaced
//     "Series 1 - 2" with nothing between is left alone.
//     The range must be one or two digits on both sides and rising, so a year
//     span ("2014-2016") or an edition year ("1 - 2020") is not read as one.
var multiBookPackRe = regexp.MustCompile(`(?i)` +
	`\b(?:books?|vols?|volumes?)\.?\s*\d{1,2}\s*(?:[-–—+&]|to|thru|through)\s*\d{1,2}\b` +
	`|\b\d{1,2}\s*(?:books?|vol(?:ume)?s?)\s+(?:set|collection|bundle|pack|omnibus)\b` +
	`|\bbox(?:ed)?\s*set\b` +
	`|\bomnibus\b` +
	`|\bcomplete\s+(?:series|collection|saga)\b` +
	`|[(\[]\s*0*(?:[2-9]|[1-9]\d{1,2})\s*(?:e-?books?|books?|novels?|volumes?|titles?)\s*[)\]]` +
	`|\b(?:e-?)?books\s+collection\b` +
	`|\b(?:book|series|collection|saga)\s+pack\b` +
	`|\bpack\s*[)\]]` +
	`|\b(?:series|saga)\s+e-?books\b`)

var (
	// seriesByRe matches "series by" ("series of" is a title, never a match).
	seriesByRe = regexp.MustCompile(`(?i)\bseries\s+by\b`)
	// bookNumberRe finds a release naming one numbered book or volume.
	bookNumberRe = regexp.MustCompile(`(?i)\b(?:book|vol(?:ume)?|bk|no|part|pt)\.?\s*#?\s*(?:\d+|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten)\b|#\s*\d+`)
	// seriesRangeRe finds a one or two digit range after the series word.
	seriesRangeRe = regexp.MustCompile(`(?i)\bseries\b([^\n]{0,60}?[a-z][^\n]{0,60}?)\b(\d{1,2})\s*[-–—]\s*(\d{1,2})\b`)
	// seriesCompactRangeRe finds a range with no spaces around its dash that
	// follows "series" directly, past separators and an opening bracket only.
	seriesCompactRangeRe = regexp.MustCompile(`(?i)\bseries\b([\s\-–—:(\[]*)(\d{1,2})[-–—](\d{1,2})\b`)
	// partWordRe ends text that introduces a split recording or disc set.
	partWordRe = regexp.MustCompile(`(?i)\b(?:parts?|pts?|discs?|cds?|chapters?|ch)\.?\s*$`)
)

// seriesPackMarker reports the series wording of title that only a whole
// series is named with, or "" (#3224). See the notes above multiBookPackRe.
func seriesPackMarker(title string) string {
	for _, re := range []*regexp.Regexp{seriesRangeRe, seriesCompactRangeRe} {
		if m := re.FindStringSubmatchIndex(title); m != nil {
			lo, _ := strconv.Atoi(title[m[4]:m[5]])
			hi, _ := strconv.Atoi(title[m[6]:m[7]])
			if lo < hi && !partWordRe.MatchString(title[:m[4]]) {
				return strings.TrimSpace(title[m[0]:m[1]])
			}
		}
	}
	if loc := seriesByRe.FindStringIndex(title); loc != nil && !bookNumberRe.MatchString(title) &&
		onlyNoiseAfterSeriesBy(title[loc[1]:]) {
		return title[loc[0]:loc[1]]
	}
	return ""
}

var (
	bracketGroupRe = regexp.MustCompile(`[(\[][^)\]]*[)\]]`)
	titleSepRe     = regexp.MustCompile(`\s[-–—]\s|[:,]`)
	yearRe         = regexp.MustCompile(`^(?:19|20)\d\d$`)
	countRe        = regexp.MustCompile(`^\d{1,3}$`)
	noiseWords     = map[string]bool{
		"epub": true, "mobi": true, "azw": true, "azw3": true, "pdf": true,
		"ebook": true, "ebooks": true, "e-book": true, "e-books": true,
		"kindle": true, "complete": true, "collection": true, "retail": true,
		"book": true, "books": true, "full": true, "series": true, "pack": true,
		"edition": true, "updated": true, "set": true, "saga": true,
	}
)

// onlyNoiseAfterSeriesBy reports whether, past the first dash, colon or comma
// of the text after "series by", there is nothing a book could be titled with.
// The text before it is the author. Bracketed groups are tags and are dropped.
// What follows may be pack or format noise ("7 Books", "Full Series", "eBook
// Pack", "Kindle Edition", "Updated 2021"), or a list of titles, which is a
// pack too: two or more commas, or three titles joined by "and". One title
// after the separator is a single book.
func onlyNoiseAfterSeriesBy(rest string) bool {
	rest = bracketGroupRe.ReplaceAllString(rest, " ")
	loc := titleSepRe.FindStringIndex(rest)
	if loc == nil {
		return true
	}
	tail := rest[loc[1]:]
	if strings.Count(tail, ",") >= 2 || strings.Count(strings.ToLower(tail), " and ") >= 2 {
		return true
	}
	for _, w := range strings.Fields(strings.ToLower(tail)) {
		w = strings.Trim(w, ".,;!?&+-")
		if w != "" && !noiseWords[w] && !yearRe.MatchString(w) && !countRe.MatchString(w) {
			return false
		}
	}
	return true
}

// MultiBookPackMarker returns the substring of title that identifies it as a
// pack of several books, or "" when it names a single book. The matched text
// is returned rather than a bool so callers can tell the user which words
// they are being judged on.
func MultiBookPackMarker(title string) string {
	if m := multiBookPackRe.FindString(title); m != "" {
		return m
	}
	return seriesPackMarker(title)
}

// PackContext is what the library knows about the wanted book, for the pack
// shapes that can only be recognised against it (#3247). Every field may be
// empty, in which case the shapes that need it simply never match.
type PackContext struct {
	// Title is the wanted book's title as stored.
	Title string
	// SeriesTitle is the title of the wanted book's primary series.
	SeriesTitle string
	// AuthorTitles are the titles of the author's other books in the library.
	AuthorTitles []string
}

// MultiBookPackMarkerFor is MultiBookPackMarker plus the pack shapes that are
// only a pack because of the book being searched for (#3247):
//
//   - The wanted book's core title, or its series title, followed directly by
//     a compact range: "Dominion of Blades 1-2" for "Dominion of Blades: A
//     LitRPG Adventure (Volume 1)". A bare range stays unmatched (see above);
//     this one is anchored on the book's own name, so a year span or an
//     edition year cannot produce it. The core title is the main title with
//     its subtitle and trailing bracketed qualifiers dropped, as
//     SplitSubtitle gives it. Both numbers are one or two digits, rising,
//     from 1, and written without spaces around the dash, so "Title 1 -
//     Subtitle" and "Title 2019-2020" are left alone. After the core title
//     the range ends at 20 at most and is never zero padded: audiobook
//     releases name a single book "Title 01-49" or "Title 1-24" for its
//     tracks. After a series title that differs from the core title the rules
//     are stricter (see namedRangeMarker): a range alone in square brackets
//     ("[Heart of Texas 01-03]") goes up to 99, anything else up to 20 with
//     no book title after it. A
//     part, disc, cd, chapter or track word right before the range makes it
//     a split recording, never a pack. A format tag elsewhere in the name
//     does not: "Mistborn 1-3 (m4b)" is a pack. The known cost is a single
//     audiobook split in two and named like "It 1-2 (1986) mp3", which reads
//     as a pack; interactive search shows it labelled and still grabbable.
//   - A list separated by semicolons naming at least two of the author's
//     books, the wanted one included: "Three Classic Thrillers (The Firm;
//     The Appeal; The Chamber)". The items are checked against the library
//     rather than counted, because a semicolon also separates author names
//     ("Nora Roberts; J. D. Robb"). The wanted title, its main title and its
//     subtitle all count as the one wanted book.
func MultiBookPackMarkerFor(title string, want PackContext) string {
	if m := MultiBookPackMarker(title); m != "" {
		return m
	}
	if m := namedRangeMarker(title, want); m != "" {
		return m
	}
	return titleListMarker(title, want)
}

// coreTitle is the wanted title the named range marker anchors on: the main
// title without its subtitle or trailing bracketed qualifiers.
func coreTitle(title string) string {
	main, _ := SplitSubtitle(title)
	return main
}

var packWordRe = regexp.MustCompile(`[\pL\pN]+`)

// rangeAfterRe builds the pattern of name followed directly by a compact
// range. name's words may be separated by any punctuation in the release, so
// a dotted scene name matches as well as a spaced one.
func rangeAfterRe(name string) *regexp.Regexp {
	words := packWordRe.FindAllString(dropApostrophes(name), -1)
	if len(words) == 0 {
		return nil
	}
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(?i)(?:^|[^\pL\pN])(` + strings.Join(words, `[^\pL\pN]*`) +
		`[\s._]+(\d{1,2})[-–—](\d{1,2}))(?:$|[^\pL\pN])`)
}

// maxTitleRangeEnd caps the range after the core title. A pack named after one
// of its books ("Dominion of Blades 1-2") holds a handful; a larger number
// after a single title is a track or chapter count ("Dune 1-49 [mp3]").
//
// Format tags and stray words elsewhere in the name (mp3, m4b, Audiobook, a
// "CD cover", a "bonus chapter") say nothing about the range: an audiobook
// pack is still a pack. A range is a part, disc or track range only when that
// word stands right before it ("Dune CD 1-12"), which this shape never matches
// because the range must follow the name directly; a name that itself ends in
// such a word is skipped.
const maxTitleRangeEnd = 20

// namedRangeMarker matches the wanted book's core title or series title
// followed directly by a range. See MultiBookPackMarkerFor.
//
// The series reading is narrower still, because a single audiobook is often
// named after its series with a track count ("The Dark Tower 1-24 (mp3)",
// "Harry Potter 1-17 - Philosopher's Stone"). It is skipped when the series is
// named like the book (the title reading already covers that name), and
// it requires one of:
//   - the range alone in square brackets, the indexer's tag position: up to 99,
//     zero padded or not ("[Heart of Texas 01-03]", "[Discworld 01-41]");
//   - otherwise, up to 20, never zero padded (zero padding is accepted in
//     round brackets too), and with no book title after the range: past a
//     separator only format or pack noise may follow, as for "series by".
func namedRangeMarker(title string, want PackContext) string {
	t := dropApostrophes(title)
	core := coreTitle(want.Title)
	if m := firstNamedRange(t, core, func(_, hi int, padded bool, _, _ int) bool {
		return !padded && hi <= maxTitleRangeEnd
	}); m != "" {
		return m
	}
	series := StripBracketSuffixes(strings.TrimSpace(want.SeriesTitle))
	if titleKey(series) == titleKey(core) {
		return ""
	}
	return firstNamedRange(t, series, func(_, hi int, padded bool, start, end int) bool {
		if enclosedBy(t, start, end, '[', ']') {
			return true
		}
		if padded && !enclosedBy(t, start, end, '(', ')') {
			return false
		}
		return hi <= maxTitleRangeEnd && onlyNoiseAfterSeriesBy(t[end:])
	})
}

// firstNamedRange returns the first "name N-M" in t with 1 <= N < M that ok
// accepts, or "". ok gets the numbers, whether either is zero padded, and the
// byte span of the matched name and range in t.
func firstNamedRange(t, name string, ok func(lo, hi int, padded bool, start, end int) bool) string {
	if titleKey(name) == "" || partWordRe.MatchString(name) {
		return ""
	}
	re := rangeAfterRe(name)
	if re == nil {
		return ""
	}
	for _, m := range re.FindAllStringSubmatchIndex(t, -1) {
		a, b := t[m[4]:m[5]], t[m[6]:m[7]]
		lo, _ := strconv.Atoi(a)
		hi, _ := strconv.Atoi(b)
		padded := strings.HasPrefix(a, "0") || strings.HasPrefix(b, "0")
		if lo >= 1 && lo < hi && ok(lo, hi, padded, m[2], m[3]) {
			return strings.TrimSpace(t[m[2]:m[3]])
		}
	}
	return ""
}

// enclosedBy reports whether t[start:end] sits directly inside open and close.
func enclosedBy(t string, start, end int, open, close byte) bool {
	return start > 0 && end < len(t) && t[start-1] == open && t[end] == close
}

var (
	// listChunkSepRe splits a release name into the runs a title list can sit
	// in: brackets and spaced dashes close one, and a colon ends a series or
	// author lead.
	listChunkSepRe   = regexp.MustCompile(`[()\[\]{}:]|\s[-–—]\s`)
	leadingArticleRe = regexp.MustCompile(`^(?:the|a|an) `)
	apostropheDrop   = strings.NewReplacer("'", "", "’", "")
)

func dropApostrophes(s string) string { return apostropheDrop.Replace(s) }

// titleKey folds a title for comparing a list item with a library title:
// lower case, apostrophes dropped, words joined by single spaces, and a
// leading article removed so "The Chamber" equals "Chamber".
func titleKey(s string) string {
	k := strings.Join(packWordRe.FindAllString(strings.ToLower(dropApostrophes(s)), -1), " ")
	return leadingArticleRe.ReplaceAllString(k, "")
}

// titleListMarker matches a semicolon separated list naming two or more of the
// author's titles. See MultiBookPackMarkerFor.
func titleListMarker(title string, want PackContext) string {
	if !strings.Contains(title, ";") {
		return ""
	}
	// known maps a title key to the book it names. Every reading of the
	// wanted title (whole, main title, subtitle) names the wanted book, so a
	// list made of its own parts ("Mistborn; The Final Empire" for "Mistborn:
	// The Final Empire") counts once, even when the library also holds a
	// duplicate record under one of those parts.
	const self = "\x00self"
	known := map[string]string{}
	for _, t := range want.AuthorTitles {
		for _, k := range []string{titleKey(t), titleKey(coreTitle(t))} {
			if k != "" {
				known[k] = k
			}
		}
	}
	main, sub := SplitSubtitle(want.Title)
	for _, k := range []string{titleKey(want.Title), titleKey(main), titleKey(sub)} {
		if k != "" {
			known[k] = self
		}
	}
	if len(known) == 0 {
		return ""
	}
	for _, chunk := range listChunkSepRe.Split(title, -1) {
		if !strings.Contains(chunk, ";") {
			continue
		}
		seen := map[string]bool{}
		for _, item := range strings.Split(chunk, ";") {
			if book, ok := known[titleKey(item)]; ok {
				seen[book] = true
			}
		}
		if len(seen) >= 2 {
			return strings.TrimSpace(chunk)
		}
	}
	return ""
}
