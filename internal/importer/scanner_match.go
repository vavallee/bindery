package importer

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// FindExisting searches the library directories for a book file that matches
// the given title and author. The mediaType argument selects which roots are
// walked: MediaTypeEbook restricts to libraryDir, MediaTypeAudiobook restricts
// to audiobookDir (falling back to libraryDir when audiobookDir is unset), and
// MediaTypeBoth or an empty/unknown value walks both with libraryDir first.
// Returns the best matching file path, or "" when none matches or two files
// of different titles are too close to call (#2941). Intended to be
// called before auto-searching so books the user already owns are not
// re-downloaded.
func (s *Scanner) FindExisting(ctx context.Context, title, authorName, mediaType string) string {
	// A fresh snapshot per call is exactly the old semantics: one walk of each
	// selected root, now cancellable. Callers with many lookups against the
	// same library state — the author sync creates one lookup per new book —
	// should hold one SnapshotFinder across the batch instead (#1888/#1929).
	return s.SnapshotFinder().FindExisting(ctx, title, authorName, mediaType)
}

// SnapshotFinder returns a LibrarySnapshot over this scanner's roots: a
// FindExisting that walks each library root at most once and answers every
// later query from the parsed entries. See LibrarySnapshot for the staleness
// contract.
func (s *Scanner) SnapshotFinder() *LibrarySnapshot {
	return NewLibrarySnapshot(s.libraryDir, s.audiobookDir).WithExcluded(s.excludedLibraryDirs()...)
}

// normalizeTitle folds a title into the shared title-comparison alphabet
// (textutil.FoldForTitleMatch), converts comma-suffix article form to
// leading-article form, then strips the leading article so that all three
// representations of the same title compare equal:
//
//	"A Darker Shade of Magic"       → "darker shade of magic"
//	"Darker Shade of Magic, A"      → "darker shade of magic"
//	"The Fragile Threads of Power"  → "fragile threads of power"
//	"Fragile Threads of Power, The" → "fragile threads of power"
//
// The fold is what lets a library folder meet its catalogue row when the two
// spell the same title differently: "Enders Game" vs "Ender's Game", "Die
// Hoehle" vs "Die Höhle", "Der Prozess" vs "Der Prozeß" (#1646). It runs AFTER
// the comma-suffix inversion, which needs the comma the fold would remove.
//
// Why this is not indexer.CanonicalDedupKey, despite the #1648 consolidation
// that exists so these alphabets stop drifting: the article handling above is
// deliberate and specific to filesystem names. Library layouts really do carry
// inverted articles as folder and file names, "Darker Shade of Magic, A" being
// the case PR #517 was opened for, and Calibre's own sort-title convention
// produces them by default. CanonicalDedupKey does neither the inversion nor
// the strip, and must not: it is stored verbatim in books.dedup_key and
// compared with =, so every side of every comparison has to compute it the
// same way.
//
// That is also the constraint on this function. The output of normalizeTitle
// MUST NOT be compared against a stored dedup_key, or used to look one up.
// Stripping the article guarantees a miss on any title that starts with one:
// "The Fragile Threads of Power" keys as "the fragile threads of power" in the
// database and normalizes to "fragile threads of power" here. Every caller
// today (scanner.go titleMatch and the scanBook precompute, lookup.go's exact
// title check) compares two in-memory strings that it normalized itself, which
// is the only safe shape. Reach for indexer.CanonicalDedupKey or
// indexer.MainTitleKey when the other side of the comparison came out of the
// database.
func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Invert comma-suffix form: check ", an" before ", a" to avoid prefix collision.
	for _, art := range []string{", the", ", an", ", a"} {
		if strings.HasSuffix(s, art) {
			s = art[2:] + " " + s[:len(s)-len(art)]
			break
		}
	}
	s = textutil.FoldForTitleMatch(s)
	// Strip leading article; check "an " before "a " for the same reason.
	for _, art := range []string{"the ", "an ", "a "} {
		if strings.HasPrefix(s, art) {
			s = s[len(art):]
			break
		}
	}
	return s
}

// titleStopwords are dropped from the significant-token comparison in
// titleMatch. Package-level so the map is not rebuilt on every call — the
// library scan runs titleMatch once per (file, candidate book) pair.
var titleStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true,
	"and": true, "in": true, "to": true, "for": true,
}

// titleSigTokens returns the significant (long enough, non-stopword) tokens of
// a title, reduced through the shared title-comparison alphabet.
//
// The floor is two BYTES of UTF-8, not two characters, and like the three-byte
// one in newznab.SigWords that is deliberate: it scales the character
// requirement by how much a script packs into a character. Do not rewrite it as
// a rune count; see SigWords for what that would break.
//
// This used to keep only [a-z0-9] and treat every other rune as a separator,
// which quietly destroyed non-ASCII titles: "Die Höhle" tokenised to
// [die hle], because the ö split the word and the leftover single-rune "h"
// fell under the 2-char floor (#1646). It also disagreed with normalizeTitle,
// its own neighbour, about apostrophes and diacritics — the two reductions in
// one function using two different alphabets.
func titleSigTokens(s string) []string {
	var out []string
	for _, w := range strings.Fields(textutil.FoldForTitleMatch(s)) {
		if len(w) >= 2 && !titleStopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

// titleTokenSet is the set of distinct titleSigTokens of s.
func titleTokenSet(s string) map[string]bool {
	toks := titleSigTokens(s)
	set := make(map[string]bool, len(toks))
	for _, w := range toks {
		set[w] = true
	}
	return set
}

// titleMatch returns true when bookTitle and parsedTitle refer to the same work.
// It handles numeric titles (1984, 2001), article normalization ("Title, The"),
// and uses dynamic overlap thresholds so short titles still match correctly.
//
// Two volumes of one series are never the same work, however many words they
// share: "Defiance of the Fall 01" and "Defiance of the Fall 17" overlap on
// every significant token but the number, which clears the two-token threshold
// below. seriesmatch.DifferentVolumes is the check the series diff and ABS
// import already apply for exactly this (#1682, #2538); without it here,
// FindExisting bound a newly added volume 17 to volume 1's file (#2810). It
// runs through differentVolumes, which does not let a multi-file audiobook's
// "Part N" stand in for a series position.
func titleMatch(bookTitle, parsedTitle string) bool {
	if parsedTitle == "" || bookTitle == "" {
		return false
	}
	if normalizeTitle(bookTitle) == normalizeTitle(parsedTitle) {
		return true
	}
	if differentVolumes(bookTitle, parsedTitle) {
		return false
	}
	return titleWordsMatch(bookTitle, parsedTitle)
}

// titleWordsMatch is titleMatch without the volume veto: the exact fast path
// and the significant-token overlap. Only a caller that has already settled
// the volume from better evidence may use it. FindExisting does when the book
// folder carries the number, because the filename's numbers are then track
// numbers ("Defiance of the Fall 7/Defiance of the Fall 01.mp3").
func titleWordsMatch(bookTitle, parsedTitle string) bool {
	if parsedTitle == "" || bookTitle == "" {
		return false
	}

	// Fast path: exact match after normalization
	if normalizeTitle(bookTitle) == normalizeTitle(parsedTitle) {
		return true
	}

	// Distinct words on both sides: a word repeated in one title is still one
	// word of evidence. Counting each occurrence let a track file named
	// "11. Track 11.mp3" share two words with any title holding "11" and
	// clear the threshold below on a single number.
	btSet := titleTokenSet(bookTitle)
	ptSet := titleTokenSet(parsedTitle)
	if len(btSet) == 0 || len(ptSet) == 0 {
		return false
	}

	overlap := 0
	for w := range ptSet {
		if btSet[w] {
			overlap++
		}
	}

	// Dynamic threshold: require overlap >= min(len(ptSet), len(btSet), 2).
	// For single-token titles (e.g. "1984") require at least 1 match.
	minLen := len(ptSet)
	if len(btSet) < minLen {
		minLen = len(btSet)
	}
	required := 2
	if minLen < 2 {
		required = minLen
	}
	if required == 0 {
		required = 1
	}

	return overlap >= required
}

// authorNameTokensCache memoises authorNameTokens. ScanLibrary can perform
// thousands of comparisons in one pass over a small set of distinct author
// names, so the normalisation is worth amortising across calls.
var authorNameTokensCache sync.Map // map[string][]string

// authorNameTokens returns the comparison tokens of an author name: the fields
// of textutil.NormalizeAuthorName, which NFD-decomposes, drops combining
// marks, keeps only letters and digits, lowercases and collapses whitespace.
//
// This is the author-identity alphabet (see internal/textutil/fold.go). Both
// sides of every comparison in this file go through it, which is what makes
// the comparison work at all — see authorMatch. The returned slice is shared
// and MUST NOT be mutated.
func authorNameTokens(name string) []string {
	if v, ok := authorNameTokensCache.Load(name); ok {
		return v.([]string)
	}
	toks := strings.Fields(textutil.NormalizeAuthorName(name))
	authorNameTokensCache.Store(name, toks)
	return toks
}

// authorMatch returns true when parsedAuthor is consistent with bookAuthor.
// If parsedAuthor is empty the function returns true (can't disprove).
//
// Significant tokens (>=3 bytes) are extracted from parsedAuthor and each must
// appear as a whole token of bookAuthor. Initials (1-2 char tokens like "R." in
// "George R. R. Martin") are dropped — they are treated as optional, so the
// parsed name "George Martin" can still match "George R. R. Martin".
//
// Both sides are reduced by authorNameTokens first. Before #1646 the parsed
// side was split on whitespace and trimmed of punctuation only at its ENDS,
// while the catalogue side was searched with an ASCII `\b…\b` regex, so two
// separate defects fell out:
//
//   - "J.R.R. Tolkien" yielded the token "j.r.r" — 5 characters, so it survived
//     the >=3 gate meant to discard initials, and no author name can contain
//     it. This is #1608 re-implemented in a second package. It was directional:
//     it fired when the PARSED side (an author folder or an ID3 Artist tag,
//     where audiobook releases overwhelmingly write "J.R.R. Tolkien") had the
//     run-together form and the catalogue had the spaced form that OpenLibrary
//     and DNB return.
//   - Nothing in this package applied Unicode normalisation, so an NFD folder
//     name from macOS never met the NFC catalogue row for the same author.
//
// Both went all the way to unmatched rather than degrading: matchingAuthors
// verifies every index candidate with this predicate and returns an empty
// non-nil set, which the caller reads as "restrict candidates to these",
// leaving nothing for the title tier to consider.
//
// This is stricter than a plain substring check: it eliminates the
// surname-overlap false positives where a co-author shares a token with the
// monitored author (e.g. parsedAuthor="Rachel Reid" should NOT match
// bookAuthor="Rachel Larsen, Adam Reid, and Ozi Akturk" — "rachel" and "reid"
// both appear but not as the same person; we still match because all tokens
// are present, which is a known limitation of word-list matching without
// position. The primary fix is rejecting the inverse case where the
// monitored author's surname coincides with a co-author's surname.)
func authorMatch(bookAuthor, parsedAuthor string) bool {
	if parsedAuthor == "" {
		return true // no author info in filename — don't filter
	}
	tokens := significantAuthorTokens(parsedAuthor)
	if len(tokens) == 0 {
		return true // only initials/punctuation — can't disprove
	}
	have := authorNameTokens(bookAuthor)
	for _, tok := range tokens {
		if !slices.Contains(have, tok) {
			return false
		}
	}
	return true
}

// significantAuthorTokens returns the tokens of name that are long enough to
// carry identity — 3+ bytes, which drops initials and the punctuation they used
// to drag along. Hyphenated names ("Mary-Kate Olsen") split into their parts,
// so both the hyphenated and the spaced spelling of such a name now match.
func significantAuthorTokens(name string) []string {
	var out []string
	for _, w := range authorNameTokens(name) {
		if len(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// pathUnderDir reports whether path is located inside dir (or is dir itself).
func pathUnderDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// bookFormatClaim identifies one (book, media format) slot claimed during a
// single library-scan pass. book_files rows are per-format, so a book can
// legitimately take one ebook file and one audiobook file in the same pass.
//
// Two files of the SAME format claiming one book stays blocked as a matching
// POLICY, not a data-integrity rule: BookFileRepo.Add is INSERT OR IGNORE and
// book_files is UNIQUE on path alone, so a second same-format file appends a
// row rather than overwriting anything (migration 028 exists precisely so
// "multi-file downloads (epub + mobi + pdf) are all recorded"). The guard is
// there because the title tier is fuzzy: at JW >= 0.85 several distinct files
// can match one book, and attaching them all would quietly bury the mistake
// inside a book's file list instead of surfacing it in Unmatched. Deliberate
// multi-format sets still arrive through the import path, which knows the files
// belong together; the scanner is guessing (#1957).
type bookFormatClaim struct {
	bookID int64
	format string
}

// ebookStemOwner is what one library-scan pass knows about the ebook files
// that share one name in one folder (#1357): the book that attached the first
// of them during the pass, or, for a file tracked before the pass, its path
// so the book is looked up only when a sibling actually turns up. The owner
// is set once and never replaced.
type ebookStemOwner struct {
	bookID      int64
	trackedPath string
	// exts are the lower case extensions already known under this name. A
	// file with one of them is a second copy in the same format (perhaps in
	// other letter case), not another format.
	exts map[string]bool
}

// scanBook is the precomputed per-book data the library-scan reconciliation
// loop needs. Hoisting normalizeTitle into this struct makes it run once per
// book instead of once per (file, book) pair — see ScanLibrary.
type scanBook struct {
	book      *models.Book
	normTitle string
	normLen   int
	// reconcilable is isReconcileCandidate for the book: the scan may claim
	// a file for it on its own. Books without it can block title claims
	// and appear as suggestions (#2879).
	reconcilable bool
}

// titleReconcileHit is one catalogue book that cleared every title-tier gate for
// a single reading of a file. reconcileByTitle collects these and then
// chooses; a gate failure is not a hit (#2941).
type titleReconcileHit struct {
	sb      *scanBook
	score   float64
	blocked bool
	// claimed is the part of blocked that means an earlier file of this pass
	// already took the book's slot for this format. Only that sets
	// claimBlocked, which files a supplement-class sidecar with its container
	// (#2188); a book the scan may not claim at all (Skipped, or already
	// holding a file) blocks the hit without making the file a sidecar.
	claimed bool
}

func newScanBook(b *models.Book, reconcilable bool) scanBook {
	sb := scanBook{book: b, normTitle: normalizeTitle(b.Title), reconcilable: reconcilable}
	sb.normLen = len(sb.normTitle)
	return sb
}

// cleanLayoutTitle strips bracket/paren annotations from a book-folder name —
// Calibre's " (id)", Readarr's " (year)" — so it matches the stored title.
// It also strips a leading Readarr "{Series} #{N} - " prefix
// ("Discworld #8 - Guards! Guards!" → "Guards! Guards!", issue #1234): without
// this the whole folder name, series tag and all, leaks through as the title
// and only series openers (where book title == series title) reconcile.
// A bare position prefix goes the same way ("01 - The Eye of the World
// (1990)" → "The Eye of the World", #2171): the {series}/{seriesIndex} -
// {title} layout writes the number without the "#" that parseSeriesFolder
// looks for, and the number then blocked every title match.
func cleanLayoutTitle(dir string) string {
	if _, _, title, ok := parseSeriesFolder(dir); ok {
		dir = title
	}
	s := cleanRe.ReplaceAllString(dir, "")
	s = multiSp.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	stripped, _ := stripLeadingPosition(dashNormalizer.Replace(s))
	return stripped
}

// bookFolderFromLayout returns the author and the folder that names the book
// for a library file. A file under <root>/<Author>/<Book>/<file> names both
// unambiguously and is dash-safe, unlike splitting an "Author - Title" or
// "Title - Author" filename (#754): the first directory under the root is the
// author, and the file's parent is the book folder.
//
// When that parent is one disc of a multi-disc audiobook ("CD1", "Disc 2", or
// the book's own title in front, "Fjellvinden Disc 1", #3327) it names a part
// rather than the book, so the folder above it names the book instead (#2723),
// by the cd/disc rule the library scan's unmatched grouping already uses
// (isDiscSetName, #2672): "Book 1", "Vol 1" and bare numbers are
// left alone, because in a library those name separate books of a series as
// often as they name discs. The folder above counts only when a book folder
// separates it from the author: in <root>/<Author>/<disc>/<file> the parent is
// the author folder, which is shared with every other book and never names one.
//
// bookFolder is "" for a file with no book folder of its own — directly under a
// root, or one level down in an author folder. ok is false when the file is
// not nested under any root.
func bookFolderFromLayout(path string, roots ...string) (author, bookFolder string, ok bool) {
	for _, root := range roots {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		switch parts := strings.Split(rel, string(filepath.Separator)); {
		case len(parts) >= 3:
			// <root>/<Author>/…/<Book>/<file>: first dir is the author, the
			// file's parent names the book — unless it is a disc folder with a
			// book folder above it.
			idx := len(parts) - 2
			if idx > 1 && isDiscSetName(parts[idx], parts[idx-1]) {
				idx--
			}
			folder := filepath.Dir(path)
			if idx < len(parts)-2 {
				folder = filepath.Dir(folder)
			}
			return strings.TrimSpace(parts[0]), filepath.Clean(folder), true
		case len(parts) == 2:
			// <root>/<Author>/<file>: only the author is unambiguous.
			return strings.TrimSpace(parts[0]), "", true
		}
	}
	return "", "", false
}

// authorTitleFromLayout derives author and title from a library file's folder
// hierarchy: the author folder, and the book folder bookFolderFromLayout finds.
// title is "" when the file has no book folder of its own; ok is false when the
// file is not nested under any root.
func authorTitleFromLayout(path string, roots ...string) (author, title string, ok bool) {
	author, folder, ok := bookFolderFromLayout(path, roots...)
	if !ok || folder == "" {
		return author, "", ok
	}
	return author, cleanLayoutTitle(filepath.Base(folder)), true
}

// flatAudioDir reports whether the audio file at path has no book folder of
// its own: it sits directly in a library root, or directly in an author folder
// beneath one (the flat Author/Title.mp3 layout). Its parent then holds other
// books as well, so it cannot stand for "this audiobook's tracks" (#1985).
func flatAudioDir(path string, roots ...string) bool {
	dir := filepath.Clean(filepath.Dir(path))
	for _, root := range roots {
		if root != "" && dir == filepath.Clean(root) {
			return true
		}
	}
	_, folder, ok := bookFolderFromLayout(path, roots...)
	return ok && folder == ""
}

// trackWordGlueRe splits a track label from a number glued onto it, so
// "CD1" and "Part02" read as "cd 1" and "part 02".
var trackWordGlueRe = regexp.MustCompile(`(?i)\b(part|pt|track|trk|chapter|chap|ch|disc|disk|cd)(\d)`)

// trackNumberRe finds where a track's numbering starts: the first digit run,
// or a spelled number after a track label ("Part One", "Chapter Twelve").
var trackNumberRe = regexp.MustCompile(`(?i)\d|\b(?:part|pt|track|trk|chapter|chap|ch|disc|disk|cd)\s+(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth)\b`)

// trackWordRe matches the track labels left in front of a number.
var trackWordRe = regexp.MustCompile(`(?i)\b(?:part|pt|track|trk|chapter|chap|ch|disc|disk|cd)\b`)

// audioTrackStem is the part of an audio file's name that names the book, for
// telling another track of a tracked audiobook apart from a different book in
// the same flat folder (#1985). Everything from the first track number on is
// dropped, since what follows a number is usually a chapter title ("01 -
// Chapter One", "Alpha - 02 - The Middle", "Alpha (2 of 12)"), and track labels
// in front of it go too. So "Alpha - Part 01.mp3", "Alpha CD2.mp3", "Alpha Part
// Two.mp3" and "Alpha.mp3" all share the stem "alpha" while "Beta.mp3" does
// not. A name that starts with its number has the empty stem, which callers
// treat as a bare track. Titles that differ only by a number ("Saga 1", "Saga
// 2") share a stem too; that keeps the pre-#1985 behaviour for them rather
// than splitting one audiobook's tracks into separate books.
func audioTrackStem(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	stem = trackWordGlueRe.ReplaceAllString(stem, "$1 $2")
	if loc := trackNumberRe.FindStringIndex(stem); loc != nil {
		if stem[loc[0]] >= '0' && stem[loc[0]] <= '9' {
			stem = stem[:loc[0]]
		} else {
			// A spelled number: keep the label, trackWordRe removes it below.
			stem = stem[:loc[1]]
			stem = stem[:strings.LastIndexAny(stem, " \t")]
		}
	}
	stem = trackWordRe.ReplaceAllString(stem, " ")
	stem = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, stem)
	return strings.Join(strings.Fields(stem), " ")
}

// reconciledAudiobookPath returns what a reconciled audiobook file should be
// recorded as in book_files. A track that sits in a book folder of its own is
// recorded as that folder — the shape the importer writes for an audiobook
// (SetFormatFilePath with the destination folder) and the shape the
// unmatched-adoption path registers for a folder unit. The folder is the
// audiobook, so every track inside it moves and deletes with the book instead
// of only the one track that happened to match first (#2716). For a disc-split
// audiobook that is the book folder above CD1/CD2, never the disc folder
// (#2723).
//
// A track with no book folder of its own keeps its own path: directly under the
// library or audiobook root, or one level down in an author folder, its parent
// is shared with other books and must not be handed to this one.
func reconciledAudiobookPath(path string, roots ...string) string {
	_, folder, ok := bookFolderFromLayout(path, roots...)
	if !ok || folder == "" || cleanLayoutTitle(filepath.Base(folder)) == "" {
		return path
	}
	return folder
}

// flipByLayout returns the other reading of a two sided filename whose title
// side names the author folder it sits in. ParseFilename reads a bare "X - Y"
// as "Title - Author", so a Readarr named "Christopher Pike - Evil Thirst.epub"
// in Christopher Pike/ parses with author and title swapped (#754, #2331). The
// flipped reading takes the other side as the title and the folder as the
// author.
//
// ok only says a flip is possible, never that it is right. A book folder
// always names the title side of a correct "Title - Author" name
// (It/It - Stephen King.epub), so the folder alone cannot tell the two apart.
// Every caller therefore matches the parse as it is first, and takes this
// reading only when that finds nothing in the catalogue and this one finds a
// book. The name comparison is confidentAuthorMatch because the flip is an
// automatic decision, and an ambiguous author match never auto matches.
func flipByLayout(parsed ParsedFile, layoutAuthor string) (ParsedFile, bool) {
	if layoutAuthor == "" || parsed.Title == "" || parsed.Author == "" {
		return parsed, false
	}
	if !namesAuthorConfidently(parsed.Title, layoutAuthor) || namesAuthorConfidently(parsed.Author, layoutAuthor) {
		return parsed, false
	}
	parsed.Title, parsed.Author = parsed.Author, layoutAuthor
	return parsed, true
}

// namesAuthorConfidently reports whether one side of a filename names the
// author folder: the whole side matches it under confidentAuthorMatch, or, for
// a multi author side ("Stephen King, Peter Straub", "King & Straub"), one of
// the names in it does (#3494).
func namesAuthorConfidently(side, layoutAuthor string) bool {
	if confidentAuthorMatch(side, layoutAuthor) {
		return true
	}
	names := splitAuthorNames(side)
	if len(names) < 2 {
		return false
	}
	// A list counts only when every element reads as a person's name. A title
	// that happens to start with the author ("Tom Clancy and Steve Pieczenik's
	// Net Force", "Stephen King, A Life") is a forward file and must keep the
	// plain reading it always had.
	found := false
	for _, name := range names {
		if !looksLikePersonName(name) {
			return false
		}
		if confidentAuthorMatch(name, layoutAuthor) {
			found = true
		}
	}
	return found
}

var authorNameParticles = map[string]bool{
	"van": true, "von": true, "de": true, "der": true, "den": true, "la": true, "le": true,
	"du": true, "di": true, "da": true, "del": true, "bin": true, "al": true, "ibn": true,
}

// looksLikePersonName is a cheap shape test for one element of an author list:
// one to four words, each a capitalised name, an initial or a particle, with no
// digits, possessive or leading article.
func looksLikePersonName(name string) bool {
	words := strings.Fields(name)
	if len(words) == 0 || len(words) > 4 {
		return false
	}
	switch strings.ToLower(words[0]) {
	case "the", "a", "an":
		return false
	}
	for _, w := range words {
		lw := strings.ToLower(w)
		if strings.HasSuffix(lw, "'s") || strings.HasSuffix(lw, "\u2019s") {
			return false
		}
		if authorNameParticles[lw] {
			continue
		}
		runes := []rune(w)
		for _, r := range runes {
			if !unicode.IsLetter(r) && r != '.' && r != '-' && r != '\'' && r != '\u2019' {
				return false
			}
		}
		if !unicode.IsUpper(runes[0]) || (len(runes) == 1) {
			return false
		}
	}
	return true
}

var authorListSepRe = regexp.MustCompile(`(?i)\s*(?:[,;&+]|\band\b)\s*`)

// splitAuthorNames splits a multi author side on commas, semicolons, ampersands,
// plus signs and "and". A side with no separator comes back as it is.
func splitAuthorNames(side string) []string {
	var names []string
	for _, n := range authorListSepRe.Split(side, -1) {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// namesAuthorAmbiguously is the title side of an "Author - Title" filename the
// flip refuses: a variant spelling of the folder author ("Dostoyevsky" in a
// Dostoevsky folder) that lookupAuthorMatch accepts and confidentAuthorMatch
// does not. The flip never auto matches on it, but the title side is still the
// author's name, so the plain reading must not fuzzy-match it as a title
// (#3494).
func namesAuthorAmbiguously(parsed ParsedFile, layoutAuthor string) bool {
	if layoutAuthor == "" || parsed.Title == "" || parsed.Author == "" {
		return false
	}
	if namesAuthorConfidently(parsed.Title, layoutAuthor) || namesAuthorConfidently(parsed.Author, layoutAuthor) {
		return false
	}
	return lookupAuthorMatch(parsed.Title, layoutAuthor) || echoesAuthorSurname(parsed.Title, layoutAuthor)
}

// echoesAuthorSurname is the looser test for a lone surname spelled a little
// differently from the folder author's ("Dostoyevsky" in a Dostoevsky folder).
// MatchAuthorName gives such a pair no band at all, so lookupAuthorMatch cannot
// see it, yet the scan still fuzzy-matched the side as a title against
// "Dostoevsky 8" (#3494). It is only ever used to withhold that fuzzy title
// match, never to claim a book, so a wrong answer costs a book that is named
// exactly or by its other side, not a wrong link. Both words must be longer
// than the short-surname cut ("Jones" and "Jonas" are different people).
func echoesAuthorSurname(side, layoutAuthor string) bool {
	if len(strings.Fields(side)) != 1 {
		return false
	}
	fields := strings.Fields(layoutAuthor)
	if len(fields) == 0 {
		return false
	}
	surname := strings.ToLower(fields[len(fields)-1])
	word := strings.ToLower(strings.Trim(side, " .,;"))
	if len([]rune(word)) < 6 || len([]rune(surname)) < 6 {
		return false
	}
	return textutil.JaroWinkler(word, surname) >= 0.88
}
