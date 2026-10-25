package importer

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
	"github.com/vavallee/bindery/internal/seriesmatch"
	"github.com/vavallee/bindery/internal/textutil"
)

// Post-download content check (#3015, #3246).
//
// The grab time filters judge a release by its NAME, and the import path before
// this looked at the file's format (#1782), a multi-book pack's name (#2276) and
// an EPUB's declared language (#2998), but never at whether the file is the book
// that was grabbed for. A release named for the wanted book can hold another one,
// and then the wrong book sits in the library as imported and is never searched
// for again.
//
// So the importer now compares what the files say about themselves with the
// book, using only metadata already inside them: an EPUB's dc:title and
// dc:creator, an audiobook's album and track tags. It never reads text or
// pages and never writes anything into a file.
//
// Each media type has its own setting, off, warn or block:
//
//   - off: the check does not run, and no file is read for it.
//   - warn (the default): the import proceeds and the bookImported event and
//     webhook carry a contentCheck object saying what was found. A check that
//     is wrong in warn mode costs a note in History, not a refused download.
//   - block: the same path the language and format checks take. The release is
//     blocklisted, the download is blocked with a message naming what was found
//     and what was wanted, and the book stays Wanted. Match to book and manual
//     import skip the check, and Retry import runs it again.
//
// Only a download tied to a book is checked, only on the download import path
// (tryImportInternal), and never for a library scan, a manual import or Match
// to book: a person who picks the files has already decided.

// The setting keys. The api package declares the same literals as
// SettingImportEbookContentCheck and SettingImportAudiobookContentCheck; the
// importer reads them as strings to avoid an import cycle, so keep them in sync.
const settingImportEbookContentCheck = "import.ebook.content_check"

// Values of the content check settings.
const (
	contentCheckOff   = "off"
	contentCheckWarn  = "warn"
	contentCheckBlock = "block"
)

// contentCheckMode reads a content check setting. Anything other than "off" or
// "block", including a value nobody set, is warn: the setting is validated when
// saved, so an odd value here is a stale row, and the safe reading of it is the
// one that never refuses a download.
func (s *Scanner) contentCheckMode(ctx context.Context, key string) string {
	if s.settings == nil {
		return contentCheckWarn
	}
	setting, err := s.settings.Get(ctx, key)
	if err != nil || setting == nil {
		return contentCheckWarn
	}
	switch v := strings.ToLower(strings.TrimSpace(setting.Value)); v {
	case contentCheckOff, contentCheckBlock:
		return v
	}
	return contentCheckWarn
}

// WithAuthorAliases wires the author alias repo the ebook content check uses to
// accept a pen name or house name as the author ("Franklin W. Dixon" for Leslie
// McFarlane). Without it the check compares the author's own name only.
func (s *Scanner) WithAuthorAliases(aliases *db.AuthorAliasRepo) *Scanner {
	s.authorAliases = aliases
	return s
}

// contentCheckResult is what a content check found. The zero value means the
// check did not run or found nothing wrong.
type contentCheckResult struct {
	mode string
	// flagged is true when the files look like another book.
	flagged bool
	// data is the contentCheck object attached to the bookImported event and
	// webhook in warn mode, and to the failure event and webhook in block mode.
	data map[string]any
	// reason is the message a blocked download shows.
	reason string
	// uncertain is true when a flagged file is only probably another book (see
	// otherWorkLikely). It is noted like any warn finding and never blocks.
	uncertain bool
}

// blocks reports whether the result refuses the download.
func (r contentCheckResult) blocks() bool {
	return r.flagged && !r.uncertain && r.mode == contentCheckBlock
}

// maxContentCheckEpubReads bounds how many EPUBs of one release the ebook check
// judges. A release with more is a pack; it is not read at all, because the
// verdict "every EPUB disagrees" cannot be reached on a prefix.
const maxContentCheckEpubReads = 20

// compilationRe matches the wording a compilation names itself with that the
// catalogue's bundle detection does not catch: an omnibus or anthology, collected
// or complete works, novels or short stories, a counted set ("Four Novels"), a
// trilogy, duology or quartet, "The Complete X Series", and a series or saga as
// the last word ("The Dark Tower Series", "Foundation Saga").
//
// Bare words that are also ordinary title words are deliberately absent:
// complete, collection, treasury, best of. "The Best of Me", "Complete Control",
// "The Complete Maus" and "A Collection of Curses" are single books, and skipping
// them would silently turn the check off for them.
var compilationRe = regexp.MustCompile(`(?i)` +
	`\b(?:omnibus|anthology|anthologies` +
	`|short\s+stories` +
	`|collected\s+\S+` +
	`|complete\s+(?:works|novels|stories|poems|collection)` +
	`|box(?:ed)?\s+set` +
	`|(?:\d+|two|three|four|five|six|seven|eight|nine|ten)\s+(?:novels|books|volumes)` +
	`|trilogy|duology|quartet|quintet|tetralogy|pentalogy)\b` +
	// "The Complete Chronicles of Conan", "The Complete Tales of Poe": complete,
	// then a collective noun somewhere after it.
	`|\bcomplete\b.*\b(?:chronicles|stories|tales|works|novels|adventures|poems|cases|strips|plays|essays|letters|set)\b` +
	`|^the\s+complete\s+.+\s+series\b` +
	// "The Complete Calvin and Hobbes", "The Complete Sherlock Holmes": the
	// complete, then two or three words, and not a guide of some kind.
	`|^(?:the\s+)?complete\s+(?:[^\s]+\s+){1,2}[^\s]+$` +
	// "Treasury of Fairy Tales".
	`|\btreasury\s+of\b.*\b(?:tales|stories|poems|verse|fables|songs|myths|legends|essays|plays)$` +
	`|\S\s+(?:saga|series)\s*$`)

// collectionShape reports a title that ends in Collection after at least two
// other words, not counting a leading article: "Harry Potter Collection", "The
// Dune Saga Collection". "The Memory Collection" has one word before it and "A
// Collection of Curses" does not end in it, so both stay single books.
func collectionShape(title string) bool {
	words := strings.Fields(textutil.FoldForSearch(title))
	if len(words) > 0 {
		switch words[0] {
		case "the", "a", "an":
			words = words[1:]
		}
	}
	return len(words) >= 3 && words[len(words)-1] == "collection"
}

// completeGuideRe cancels the "complete, then two or three words" rule for a
// guide, handbook or course, which is one book.
var completeGuideRe = regexp.MustCompile(`(?i)\b(?:guide|handbook|manual|course|cookbook|idiot'?s|dummies|reference)\b`)

// numberRangeRe matches a numeric range after a title word: "Dune Chronicles
// 1-3", "Harry Potter 1-7". The second number must be larger than the first.
var numberRangeRe = regexp.MustCompile(`[A-Za-z)]\s+(\d{1,2})\s*-\s*(\d{1,2})\s*$`)

func hasNumberRange(title string) bool {
	m := numberRangeRe.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return false
	}
	a, errA := strconv.Atoi(m[1])
	b, errB := strconv.Atoi(m[2])
	return errA == nil && errB == nil && b > a
}

// bookIsCompilation reports whether a wanted book is a box set, omnibus,
// anthology, trilogy or other pack. Its files legitimately carry the titles of
// other works, so there is nothing to compare. Skipping on a title too many only
// costs a missed mismatch, which is the safe direction, but a wrong skip also
// switches the check off for a book that deserved it, so the list is strict.
// A single book that is really several parts ("The Lord of the Rings", "The
// Chronicles of Narnia") is a documented limit.
func bookIsCompilation(title string) bool {
	if metadata.IsBundleTitle(title) || indexer.MultiBookPackMarker(title) != "" || hasNumberRange(title) {
		return true
	}
	if collectionShape(title) {
		return true
	}
	if !compilationRe.MatchString(title) {
		return false
	}
	// The "complete X Y" shape alone is not enough for a guide.
	if m := compilationRe.FindString(title); strings.EqualFold(strings.TrimSpace(m), strings.TrimSpace(title)) && completeGuideRe.MatchString(title) {
		return false
	}
	return true
}

// bookIsCompilationBy is bookIsCompilation that also knows the book's author:
// "The Best of Poe" by Edgar Allan Poe is a compilation, and "The Best of Me" and
// "Best of Friends" are not, because the name after "best of" is the author's.
func bookIsCompilationBy(title, author string) bool {
	if bookIsCompilation(title) {
		return true
	}
	if !bestOfRe.MatchString(title) {
		return false
	}
	fields := strings.Fields(textutil.FoldForSearch(author))
	if len(fields) == 0 {
		return false
	}
	surname := fields[len(fields)-1]
	if len(surname) < 3 {
		return false
	}
	return containsWholeWords(textutil.FoldForSearch(title), surname)
}

var bestOfRe = regexp.MustCompile(`(?i)\bbest\s+of\b`)

// splitCreators breaks a dc:creator string that credits several people into one
// entry per person where it can tell, and always keeps the whole string as an
// entry too, so a "Last, First" name stays readable.
func splitCreators(creator string) []string {
	creator = strings.TrimSpace(creator)
	if creator == "" {
		return nil
	}
	out := []string{creator}
	repl := strings.NewReplacer(";", "\n", " and ", "\n", " & ", "\n", " | ", "\n", " / ", "\n")
	for _, part := range strings.Split(repl.Replace(creator), "\n") {
		if p := strings.TrimSpace(part); p != "" && p != creator {
			out = append(out, p)
		}
	}
	return out
}

// ebookTitleMatches reports whether an EPUB's dc:title is compatible with the
// book's title (see contentTitleSupports). An empty title on either side is
// compatible: a file with no title has not said it is another book.
func ebookTitleMatches(bookTitle, fileTitle string) bool {
	if strings.TrimSpace(fileTitle) == "" || strings.TrimSpace(bookTitle) == "" {
		return true
	}
	if nonLatinAgainstLatin(bookTitle, fileTitle) {
		// A Russian, Japanese or Arabic edition of a Latin catalogued book is
		// the same book. No folding makes the two comparable, so say nothing.
		return true
	}
	return contentTitleSupports(bookTitle, fileTitle, true)
}

// ebookAuthorMatches reports whether an EPUB's dc:creator names one of the
// names the book's author goes by. It uses the importer's author matcher in both
// directions, so "Tolkien, J. R. R." meets "J.R.R. Tolkien" and a credit of
// "James Patterson and Maxine Paetro" meets "James Patterson". An empty creator,
// or a book with no author on record, is compatible: nothing was said.
func ebookAuthorMatches(names []string, creator string) bool {
	agrees, _ := ebookAuthorAgreement(names, creator)
	return agrees
}

// ebookAuthorAgreement is ebookAuthorMatches that also says whether the file
// actually named the author: named is true only when the creator holds a real
// person, there is a name on record in the creator's script, and one of the
// creators matched it. A file that said nothing (no creator, a placeholder, a
// script the author has no name in) agrees but is not named, so a rule that
// needs positive evidence can tell the two apart.
func ebookAuthorAgreement(names []string, creator string) (agrees, named bool) {
	creators := splitCreators(creator)
	if len(creators) == 0 || len(names) == 0 {
		return true, false
	}
	if creatorIsPlaceholder(creators) {
		return true, false
	}
	// A creator is compared with the names in its own script. A name in another
	// script is no evidence either way, and an author with one Cyrillic alias
	// must still be able to disagree with a Latin creator. When the author has
	// no name in the creator's script at all, nothing can be said.
	var latin, other []string
	for _, n := range names {
		if strings.TrimSpace(n) == "" {
			continue
		}
		if l, lat := scriptOf(n); l && !lat {
			other = append(other, n)
		} else {
			latin = append(latin, n)
		}
	}
	for _, c := range creators {
		if placeholderCreator(c) {
			continue
		}
		pool := latin
		if l, lat := scriptOf(c); l && !lat {
			pool = other
		}
		if len(pool) == 0 {
			return true, false
		}
		for _, n := range pool {
			if authorMatch(n, c) || authorMatch(c, n) {
				return true, true
			}
		}
	}
	return false, false
}

// placeholderCreators are dc:creator values that name nobody: what a converter,
// a library tool or a scraper writes when it has no author.
var placeholderCreators = map[string]bool{
	"unknown": true, "unknown author": true, "anonymous": true, "anon": true, "various": true,
	"various authors": true, "n a": true, "none": true, "author": true, "admin": true,
	"calibre": true, "project gutenberg": true, "gutenberg": true,
}

// publisherCreatorRe matches a creator that is a publisher or an imprint by its
// corporate ending: "Scholastic Inc.", "Penguin Group", "Acme Publishing",
// "Springer Verlag GmbH". Bare words that are also surnames (House, Press, Books,
// Media, Company) are deliberately not here: "Stephen House" is an author.
var publisherCreatorRe = regexp.MustCompile(`(?i)\b(?:group|inc|llc|ltd|gmbh|publishing|publishers?)\.?$`)

// publisherCreators are well known publishers that carry no such ending.
var publisherCreators = map[string]bool{
	"penguin": true, "penguin random house": true, "random house": true, "scholastic": true,
	"harpercollins": true, "harper collins": true, "hachette": true, "macmillan": true,
	"simon and schuster": true, "bloomsbury": true, "tor books": true, "del rey": true,
	"oxford university press": true, "cambridge university press": true, "orbit": true,
}

// emailRe matches a creator that is an email address and nothing else, bare or
// inside one pair of brackets.
var emailRe = regexp.MustCompile(`^[(\[<]?[^\s@]+@[^\s@]+[)\]>]?$`)

// placeholderCreator reports whether one creator entry names nobody: a listed
// placeholder, a publisher name, or an email address on its own. Such an entry
// is treated as if the file had no creator. An address inside a real name
// ("Jane Austen (jane@x.org)") leaves the name standing.
func placeholderCreator(c string) bool {
	c = strings.TrimSpace(c)
	if c == "" || emailRe.MatchString(c) {
		return true
	}
	folded := textutil.FoldForSearch(c)
	if placeholderCreators[folded] || publisherCreators[folded] {
		return true
	}
	return publisherCreatorRe.MatchString(c)
}

// creatorIsPlaceholder reports whether every person a creator string credits is
// a placeholder. creators is splitCreators output: the whole string first, then
// its parts when it had several.
func creatorIsPlaceholder(creators []string) bool {
	parts := creators
	if len(creators) > 1 {
		parts = creators[1:]
	}
	for _, p := range parts {
		if !placeholderCreator(p) {
			return false
		}
	}
	return true
}

// ebookDefiniteMismatch reports whether an EPUB is plausibly another book: its
// title does not match the book's AND its author does not match any name the
// book's author goes by. Requiring both keeps false positives low, since a real
// book with odd metadata usually still gets one of the two right. Missing
// metadata never counts.
func ebookDefiniteMismatch(bookTitle string, names []string, fileTitle, fileCreator string) bool {
	return !ebookTitleMatches(bookTitle, fileTitle) && !ebookAuthorMatches(names, fileCreator)
}

// seriesSlot is one series membership with its position, "" when unknown.
type seriesSlot struct {
	seriesID int64
	position string
}

// knownPosition reports whether a series position is a single number. A range
// ("1-3", an omnibus) or any other text is unknown: providers write those for
// bundles and for bad data, and they say nothing about which book this is.
func knownPosition(p string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
	return err == nil
}

// seriesPositionsDiffer reports whether a and b share a series and hold
// different known positions in it. Positions compare as numbers
// (seriesmatch.SamePosition), so "1", "1.0" and "01" are one position, and a
// position that is a range or not a number is unknown and never differs.
func seriesPositionsDiffer(a, b []seriesSlot) bool {
	for _, x := range a {
		for _, y := range b {
			if x.seriesID == y.seriesID && knownPosition(x.position) && knownPosition(y.position) &&
				!seriesmatch.SamePosition(x.position, y.position) {
				return true
			}
		}
	}
	return false
}

// seriesPositionsSame reports whether a and b share a series and hold the same
// position in it, as two editions of one book do.
func seriesPositionsSame(a, b []seriesSlot) bool {
	for _, x := range a {
		for _, y := range b {
			if x.seriesID == y.seriesID && seriesmatch.SamePosition(x.position, y.position) {
				return true
			}
		}
	}
	return false
}

// catalogueBook is what the other-book rule reads about one book of the
// author's catalogue: its titles, every id it is known by, and its series slots.
type catalogueBook struct {
	id            int64
	title         string
	originalTitle string
	ids           []string
	slots         []seriesSlot
}

// otherWork is how sure the content check is that a file is another book.
type otherWork int

const (
	// otherWorkNo: the file is not shown to be another book.
	otherWorkNo otherWork = iota
	// otherWorkLikely: the file's title is that of another catalogue book and
	// nothing says the two are editions of one work, but nothing says they are
	// distinct works either. It is noted and never refuses a download.
	otherWorkLikely
	// otherWorkConfident: the file is the title of a book that sits at a
	// different position in a series the wanted book is also in.
	otherWorkConfident
)

// ebookIsAnotherBookOfTheAuthor judges an EPUB whose author agrees but whose
// title differs (#3461). A series author's wrong grab is usually a neighbouring
// volume, and the author side of ebookDefiniteMismatch cannot see it.
//
// Title difference alone is not evidence: a translated or alternate title
// ("Voyna i mir" for "War and Peace", "The Golden Compass" for "Northern
// Lights") reads as a different work to titlesNameDifferentWorks. So the file's
// title must also be a different catalogue book (a row whose title the file
// matches, that is a different work than the wanted book by title), and that row
// must not look like another edition of the wanted book. A row is set aside as
// an edition when it shares any id with the wanted book, one's original title is
// the other's title, or they sit
// at the same position of one series. Such rows are common: the catalogue
// can hold both editions as separate rows, even excluded ones.
//
// What is left is otherWorkConfident when the row and the wanted book are at
// different positions of one series, the one fact that says two titles are
// distinct books, and otherWorkLikely otherwise. The author must be named in the
// file: a missing or placeholder creator says nothing.
func ebookIsAnotherBookOfTheAuthor(wanted catalogueBook, names []string, fileTitle, fileCreator string, catalogue []catalogueBook) otherWork {
	if ebookTitleMatches(wanted.title, fileTitle) || !titlesNameDifferentWorks(wanted.title, fileTitle) {
		return otherWorkNo
	}
	if _, named := ebookAuthorAgreement(names, fileCreator); !named {
		return otherWorkNo
	}
	verdict := otherWorkNo
	for _, t := range catalogue {
		if t.id == wanted.id && wanted.id != 0 || strings.TrimSpace(t.title) == "" || bookIsCompilation(t.title) {
			continue
		}
		// The row must be a different work than the wanted book by title, and
		// the file must be that row.
		if ebookTitleMatches(wanted.title, t.title) || !titlesNameDifferentWorks(wanted.title, t.title) {
			continue
		}
		if !ebookTitleMatches(t.title, fileTitle) || titlesNameDifferentWorks(t.title, fileTitle) {
			continue
		}
		if looksLikeAnEdition(wanted, t) {
			continue
		}
		if seriesPositionsDiffer(wanted.slots, t.slots) {
			return otherWorkConfident
		}
		verdict = otherWorkLikely
	}
	return verdict
}

// looksLikeAnEdition reports whether two catalogue rows may be one work.
func looksLikeAnEdition(a, b catalogueBook) bool {
	for _, x := range a.ids {
		for _, y := range b.ids {
			if x != "" && x == y {
				return true
			}
		}
	}
	if a.originalTitle != "" && ebookTitleMatches(a.originalTitle, b.title) {
		return true
	}
	if b.originalTitle != "" && ebookTitleMatches(b.originalTitle, a.title) {
		return true
	}
	return seriesPositionsSame(a.slots, b.slots)
}

// authorCatalogue loads the author's books (excluded ones too) with their ids
// and series slots; nil when they cannot be read.
func (s *Scanner) authorCatalogue(ctx context.Context, author *models.Author) []catalogueBook {
	if author == nil || s.books == nil {
		return nil
	}
	books, err := s.books.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		slog.Warn("content check: could not load the author's books", "authorID", author.ID, "error", err)
		return nil
	}
	identifiers, err := s.books.ListBookIdentifiersByAuthor(ctx, author.ID)
	if err != nil {
		slog.Warn("content check: could not load the author's book ids", "authorID", author.ID, "error", err)
		identifiers = nil
	}
	var memberships map[int64][]db.BookSeriesMembership
	if s.series != nil {
		if memberships, err = s.series.ListBookSeriesMembershipsByAuthor(ctx, author.ID); err != nil {
			slog.Warn("content check: could not load the author's series", "authorID", author.ID, "error", err)
			memberships = nil
		}
	}
	out := make([]catalogueBook, 0, len(books))
	for _, b := range books {
		c := catalogueBook{id: b.ID, title: b.Title, originalTitle: b.OriginalTitle}
		for _, id := range []string{b.ForeignID, b.HardcoverForeignID, b.ASIN} {
			if id != "" {
				c.ids = append(c.ids, id)
			}
		}
		for _, bi := range identifiers[b.ID] {
			c.ids = append(c.ids, bi.ForeignID)
		}
		for _, m := range memberships[b.ID] {
			c.slots = append(c.slots, seriesSlot{seriesID: m.SeriesID, position: m.Position})
		}
		out = append(out, c)
	}
	return out
}

// authorNamesFor returns the names the book's author is known by: the name on
// record plus every alias.
func (s *Scanner) authorNamesFor(ctx context.Context, author *models.Author) []string {
	if author == nil {
		return nil
	}
	names := []string{author.Name}
	if s.authorAliases == nil {
		return names
	}
	aliases, err := s.authorAliases.ListByAuthor(ctx, author.ID)
	if err != nil {
		slog.Warn("content check: could not load author aliases", "authorID", author.ID, "error", err)
		return names
	}
	for _, a := range aliases {
		names = append(names, a.Name)
	}
	return names
}

// withContentCheck returns a bookImported history payload with the contentCheck
// object added when there is one. With none it carries exactly the input, so an
// import nobody flagged writes the same row as before.
func withContentCheck(data map[string]string, contentCheck map[string]any) map[string]any {
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	if contentCheck != nil {
		out["contentCheck"] = contentCheck
	}
	return out
}

// withContentCheckPayload adds the contentCheck object to a webhook payload.
func withContentCheckPayload(payload map[string]interface{}, contentCheck map[string]any) map[string]interface{} {
	if contentCheck != nil {
		payload["contentCheck"] = contentCheck
	}
	return payload
}

// safeFileText cleans text that came out of a downloaded file or a metadata
// provider before it goes into a message, a History row or a webhook: control
// and invisible characters are removed, the length is capped, and chat markup
// (@everyone, <!channel>, [text](url), bare URLs) is neutralised, since the
// failure message is sent to chat webhooks as is. It changes nothing a person
// reads in an ordinary title.
func safeFileText(s string) string { return notifier.SafeText(s, 200) }

// describeEpub renders what a file says about itself for a message:
// `"Mary and Mr Eliot" by Mary Trevelyan`.
func describeEpub(title, author string) string {
	title, author = strings.TrimSpace(title), strings.TrimSpace(author)
	switch {
	case title != "" && author != "":
		return fmt.Sprintf("%q by %s", title, author)
	case title != "":
		return fmt.Sprintf("%q", title)
	case author != "":
		return "a book by " + author
	}
	return "no title or author"
}

// checkEbookContent compares the title and author inside each EPUB of a
// download with the book it was grabbed for (#3015).
//
// A release is flagged only when every EPUB it holds that could be read is a
// definite mismatch: one EPUB that agrees, or that carries no metadata to
// disagree with, keeps the release, since a stray extra file should not condemn
// the real one. Only EPUB is read; every other format is unchanged.
//
// A wanted box set or omnibus is not compared, and the check does nothing when
// the setting is off.
func (s *Scanner) checkEbookContent(ctx context.Context, book *models.Book, author *models.Author, files []string) contentCheckResult {
	mode := s.contentCheckMode(ctx, settingImportEbookContentCheck)
	if mode == contentCheckOff || book == nil || bookIsCompilationBy(book.Title, authorName(author)) {
		return contentCheckResult{}
	}
	// Kindle files carry the same title and author in their EXTH block
	// (#3016), so they are compared alongside EPUBs.
	var epubs []string
	for _, f := range files {
		if HasEmbeddedMetadata(f) {
			epubs = append(epubs, f)
		}
	}
	if len(epubs) == 0 {
		return contentCheckResult{}
	}
	if s.onContentCheck != nil {
		s.onContentCheck("ebook")
	}
	names := s.authorNamesFor(ctx, author)
	// The author's other books, read once and only if a file needs them.
	var catalogue []catalogueBook
	var catalogueRead bool
	var wantedEntry catalogueBook
	allUncertain := true

	// A release with more EPUBs than the cap is not judged: a prefix of it could
	// be all wrong books while the right one sits later in the list, and the
	// importer picks the file for the book itself. Fail open, and say so.
	if len(epubs) > maxContentCheckEpubReads {
		return contentCheckResult{mode: mode, data: map[string]any{
			"kind":       "ebook",
			"bookTitle":  safeFileText(book.Title),
			"bookAuthor": safeFileText(authorName(author)),
			"truncated":  true,
			"filesRead":  0,
			"filesTotal": len(epubs),
		}}
	}

	type found struct{ file, title, author string }
	var read int
	var mismatches []found
	for _, f := range epubs {
		if ctx.Err() != nil {
			break
		}
		meta, err := ReadEbookMetadata(f)
		if err != nil {
			continue
		}
		read++
		mismatch := ebookDefiniteMismatch(book.Title, names, meta.Title, meta.Author)
		fileUncertain := false
		if !mismatch && !ebookTitleMatches(book.Title, meta.Title) {
			// Title differs and the author agrees: another book by the author?
			if !catalogueRead {
				catalogue, catalogueRead = s.authorCatalogue(ctx, author), true
				wantedEntry = catalogueBook{id: book.ID, title: book.Title, originalTitle: book.OriginalTitle, ids: []string{book.ForeignID}}
				for _, c := range catalogue {
					if c.id == book.ID {
						wantedEntry = c
					}
				}
			}
			switch ebookIsAnotherBookOfTheAuthor(wantedEntry, names, meta.Title, meta.Author, catalogue) {
			case otherWorkConfident:
				mismatch = true
			case otherWorkLikely:
				mismatch, fileUncertain = true, true
			}
		}
		if mismatch {
			allUncertain = allUncertain && fileUncertain
			mismatches = append(mismatches, found{safeFileText(filepath.Base(f)), safeFileText(meta.Title), safeFileText(meta.Author)})
		}
	}
	// A context that ended mid read means the answer is incomplete, and an
	// incomplete answer never refuses a download (and could not write it down).
	if ctx.Err() != nil || read == 0 || len(mismatches) < read {
		return contentCheckResult{mode: mode}
	}

	bookAuthor := ""
	if author != nil {
		bookAuthor = safeFileText(author.Name)
	}
	bookTitle := safeFileText(book.Title)
	listed := make([]map[string]string, 0, maxContentCheckTitles)
	for i, m := range mismatches {
		if i >= maxContentCheckTitles {
			break
		}
		listed = append(listed, map[string]string{"file": m.file, "title": m.title, "author": m.author})
	}
	first := mismatches[0]
	extra := ""
	if len(mismatches) > 1 {
		extra = fmt.Sprintf(" (and %d more file(s) like it)", len(mismatches)-1)
	}
	data := ebookCheckData(bookTitle, bookAuthor, listed, len(mismatches), read)
	uncertain := allUncertain
	if uncertain {
		data["uncertain"] = true
	}
	return contentCheckResult{
		mode:      mode,
		flagged:   true,
		uncertain: uncertain,
		data:      data,
		reason: fmt.Sprintf("the file says it is %s%s, but the book is %q by %s. "+
			"Not imported, and the release was blocklisted so the next search picks another. "+
			"To keep this file anyway, use Match to book in the Queue or manual import; "+
			"to stop this check, set import.ebook.content_check to warn or off",
			describeEpub(first.title, first.author), extra, bookTitle, bookAuthor),
	}
}

// ebookCheckData builds the contentCheck object of an ebook: the listed
// mismatches (at most maxContentCheckTitles), how many more there were, and how
// many EPUBs were read.
func ebookCheckData(bookTitle, bookAuthor string, listed []map[string]string, total, read int) map[string]any {
	d := map[string]any{
		"kind":       "ebook",
		"bookTitle":  bookTitle,
		"bookAuthor": bookAuthor,
		"mismatches": listed,
		"filesRead":  read,
	}
	if more := total - len(listed); more > 0 {
		d["moreTitles"] = more
	}
	return d
}

// authorName is the author's name, or "" for no author.
func authorName(a *models.Author) string {
	if a == nil {
		return ""
	}
	return a.Name
}
