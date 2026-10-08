package importer

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Library adoption's evidence about which book a unit is (#2942). A unit's
// suggestions used to come only from its author folder's catalogue, scored on
// the unit's title. When the files are another author's book left in the wrong
// folder, every suggestion is then a look alike by the wrong author: seven
// "Katy Evans - Tycoon N-7.mp3" tracks tagged artist "Katy Evans", album
// "Tycoon", in a James Patterson folder, were offered "Katt vs. Dogg" at 80%,
// because the track title "Katy Evans - Tycoon" shares its "Kat" opening.
//
// The files themselves say more than the folder does here. Their tags name an
// author and an album, and a run of tracks that all read "<Author> - <Title>
// <n>" names both. When that author is not the folder's, the unit records the
// author and title the files name, its suggestions are searched for that
// author first and scored on that title, and the folder author's books follow
// as plain look alikes the page will not preselect.

// unitEvidence is what a unit's files say about their own author and title,
// when that disagrees with the folder they sit in. The zero value means the
// files agree with the folder or say nothing, and the unit is ranked exactly
// as before.
type unitEvidence struct {
	// author is the author the files name.
	author string
	// title is the book the files name, "" when they name none. Candidates
	// are scored on it instead of the folder's or a track's title.
	title string
	// folder is the author folder the files disagree with.
	folder string
}

func (e unitEvidence) conflict() bool { return e.author != "" }

// AuthorNamesAgree reports whether two author strings can be the same author:
// by the import lookup's matcher (comma inversion, transliteration, the fuzzy
// bands of textutil.MatchAuthorName, ambiguous included), by the library
// scan's token subset rule in either direction ("Tolkien" and "J.R.R.
// Tolkien"), or through any name credited in a contributor list ("James
// Patterson, Peter Hermes - narrator"). It errs towards agreeing: a conflict is
// only worth raising when no reading of the two names can be the same person.
// A side with no significant name token cannot be disproved and agrees.
func AuthorNamesAgree(a, b string) bool {
	if lookupAuthorMatch(a, b) || authorMatch(a, b) || authorMatch(b, a) {
		return true
	}
	for _, c := range contributorCandidates(a) {
		if lookupAuthorMatch(c, b) || authorMatch(b, c) {
			return true
		}
	}
	for _, c := range contributorCandidates(b) {
		if lookupAuthorMatch(c, a) || authorMatch(a, c) {
			return true
		}
	}
	return false
}

// authorEvidenceConflict reports whether the author a unit's files name is
// someone other than its author folder. Both empty sides, a placeholder credit
// and agreeing names are no conflict.
func authorEvidenceConflict(filesAuthor, folderAuthor string) bool {
	filesAuthor, folderAuthor = strings.TrimSpace(filesAuthor), strings.TrimSpace(folderAuthor)
	if filesAuthor == "" || folderAuthor == "" || isPlaceholderAuthor(filesAuthor) {
		return false
	}
	return !AuthorNamesAgree(filesAuthor, folderAuthor)
}

// placeholderAuthorNames are credits that name nobody: compilations, unknowns,
// productions and publishers that rips put in an author tag. Such a tag is no
// evidence of an author, so it never raises a conflict or an Add author.
// Keys are in the form placeholderKey produces, so "V.A.", "V/A" and "VA" are
// all "va", "Unknown Author(s)" is "unknown authors" and "Full-Cast" is "full
// cast". The codebase had no such list; "Unknown Author" is only ever written
// as a default (renamer.go), never recognised.
var placeholderAuthorNames = map[string]bool{
	// Compilations and unknowns.
	"various": true, "various authors": true, "various artists": true, "various narrators": true,
	"va": true, "na": true, "none": true, "author": true, "artist": true,
	"unknown": true, "unknown artist": true, "unknown artists": true,
	"unknown author": true, "unknown authors": true, "author unknown": true, "artist unknown": true,
	"anonymous": true, "anon": true,
	// Productions.
	"full cast": true, "a full cast": true, "full cast production": true, "full cast drama": true,
	"librivox": true, "librivox volunteers": true, "audiobook": true, "audiobooks": true, "audio book": true,
	"bbc radio": true, "bbc radio 4": true, "bbc audio": true,
	// Audiobook publishers and imprints.
	"audible studios": true, "audible": true, "audible originals": true, "audible original": true,
	"brilliance audio": true, "brilliance publishing": true, "recorded books": true,
	"tantor audio": true, "tantor media": true, "macmillan audio": true,
	"blackstone audio": true, "blackstone publishing": true, "random house audio": true,
	"penguin audio": true, "penguin random house audio": true,
	"harperaudio": true, "harper audio": true, "podium audio": true, "podium publishing": true,
}

// placeholderKey folds an author tag for comparison with
// placeholderAuthorNames: lower case, brackets, dots, slashes and apostrophes
// dropped, any other punctuation read as a space, spacing collapsed.
func placeholderKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case strings.ContainsRune("()[]{}<>./'’", r):
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// isPlaceholderAuthor reports whether an author tag names nobody in
// particular ("Various Artists.", "[Unknown]", "Full-Cast", "Brilliance Audio").
func isPlaceholderAuthor(s string) bool {
	return placeholderAuthorNames[placeholderKey(s)]
}

// tagAuthorNames returns a file's usable author tags, Album Artist first:
// audiobook tooling puts the narrator in Artist often enough that Album
// Artist is the better witness for the author. Empty values, narrator credits
// and placeholders are left out, and a name repeated across tags appears once.
func tagAuthorNames(t AudioTags) []string {
	var out []string
	for _, v := range []string{t.AlbumArtist, t.Artist, t.Composer} {
		v = strings.TrimSpace(v)
		if v == "" || isNarratorCredit(v) || isPlaceholderAuthor(v) {
			continue
		}
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, v) }) {
			out = append(out, v)
		}
	}
	return out
}

// tagsNameAnotherAuthor reports whether a file's author tags name someone and
// none of them is the folder's author. Artist, Album Artist and Composer are
// all checked, so a narrator in Artist beside the author in Album Artist is no
// conflict. Tags whose album or title names the folder's own book agree with
// the folder about the book, and the odd author tag is then more likely a
// narrator than another author's book, so they do not count either.
func tagsNameAnotherAuthor(t AudioTags, folder, layoutTitle string) bool {
	folder = strings.TrimSpace(folder)
	names := tagAuthorNames(t)
	if folder == "" || len(names) == 0 {
		return false
	}
	for _, n := range names {
		if AuthorNamesAgree(n, folder) {
			return false
		}
	}
	return !sameBookTitle(t.Album, layoutTitle) && !sameBookTitle(t.Title, layoutTitle)
}

// evidenceFor reads what a unit's members say about their author and title,
// against the unit's author folder. Tags lead: the Artist, Album Artist and
// Composer values most members carry, judged by tagsNameAnotherAuthor, with
// Album Artist preferred as the author named. Without author tags, a filename
// pattern every member shares; that is weaker, since "<Series> - <Title>
// 01.mp3" has the same shape, so it counts only when the title side also
// names a different book than the folder does.
func evidenceFor(members []unmatchedScanFile, folder, layoutTitle string) unitEvidence {
	folder = strings.TrimSpace(folder)
	if folder == "" || len(members) == 0 {
		return unitEvidence{}
	}
	tags := AudioTags{
		Artist:      consensus(members, func(m unmatchedScanFile) string { return m.tags.Artist }),
		AlbumArtist: consensus(members, func(m unmatchedScanFile) string { return m.tags.AlbumArtist }),
		Composer:    consensus(members, func(m unmatchedScanFile) string { return m.tags.Composer }),
		Album:       consensus(members, func(m unmatchedScanFile) string { return m.tags.Album }),
		Title:       consensus(members, func(m unmatchedScanFile) string { return m.tags.Title }),
	}
	fnAuthor, fnTitle := filenameAuthorTitle(members, layoutTitle)
	var author string
	if names := tagAuthorNames(tags); len(names) > 0 {
		if !tagsNameAnotherAuthor(tags, folder, layoutTitle) {
			return unitEvidence{}
		}
		author = names[0]
	} else {
		if !authorEvidenceConflict(fnAuthor, folder) || sameBookTitle(fnTitle, layoutTitle) {
			return unitEvidence{}
		}
		author = fnAuthor
	}
	ev := unitEvidence{author: author, folder: folder}
	// The title the files name, best evidence first: the album tag, then a
	// title tag that is "<author> - <title>", then the shared filename title
	// when the file names credit the same author.
	if tags.Album != "" && !AuthorNamesAgree(tags.Album, author) {
		ev.title = tags.Album
	} else if tags.Title != "" {
		if rest, ok := stripAuthorPrefix(tags.Title, author); ok {
			ev.title = rest
		}
	}
	if ev.title == "" && fnAuthor != "" && AuthorNamesAgree(fnAuthor, author) {
		ev.title = fnTitle
	}
	return ev
}

// consensus returns the value of field most members carry, compared without
// case, when more than half of them carry it; else "". Ties go to the first
// member, and members arrive sorted by path.
func consensus(members []unmatchedScanFile, field func(unmatchedScanFile) string) string {
	counts := make(map[string]int, 1)
	best, bestKey := "", ""
	for _, m := range members {
		v := strings.TrimSpace(field(m))
		if v == "" {
			continue
		}
		k := strings.ToLower(v)
		counts[k]++
		if counts[k] > counts[bestKey] {
			best, bestKey = v, k
		}
	}
	if best == "" || counts[bestKey]*2 <= len(members) {
		return ""
	}
	return best
}

// stripAuthorPrefix returns what follows "<author> - " in s, and whether s
// had that shape with that author.
func stripAuthorPrefix(s, author string) (string, bool) {
	left, right, ok := strings.Cut(dashNormalizer.Replace(s), " - ")
	if !ok || strings.TrimSpace(right) == "" || !AuthorNamesAgree(strings.TrimSpace(left), author) {
		return "", false
	}
	return strings.TrimSpace(right), true
}

// trackMarkerWords are the words a track name puts between the book title and
// its number ("Tycoon Part 01"), dropped from the end of the shared title.
var trackMarkerWords = map[string]bool{
	"part": true, "pt": true, "pt.": true, "disc": true, "disk": true, "cd": true, "track": true,
	"chapter": true, "ch": true, "ch.": true, "of": true,
}

// filenameAuthorTitle reads the "<Author> - <Title> <n>" pattern from an
// audiobook unit's track names: at least two files, every one with the same
// text before its first " - ", and a title made of the words every file's
// remainder starts with ("Tycoon 1-7", "Tycoon 2-7" give "Tycoon"). The left
// side counts as an author only when it is shaped like a name: a letter, no
// digit, at most five words, none of them a title stopword, and not the book
// folder's own title. A title that is a chapter heading ("Chapter 01") means
// the left side was the book, not an author. Either side failing returns "".
func filenameAuthorTitle(members []unmatchedScanFile, layoutTitle string) (author, title string) {
	if len(members) < 2 || members[0].format != models.MediaTypeAudiobook {
		return "", ""
	}
	var rests [][]string
	for _, m := range members {
		base := dashNormalizer.Replace(strings.TrimSuffix(filepath.Base(m.path), filepath.Ext(m.path)))
		left, right, ok := strings.Cut(base, " - ")
		left = strings.TrimSpace(left)
		if !ok || left == "" {
			return "", ""
		}
		if author == "" {
			author = left
		} else if !strings.EqualFold(author, left) {
			return "", ""
		}
		rests = append(rests, strings.Fields(right))
	}
	words := commonLeadingWords(rests)
	for len(words) > 0 && (trackMarkerWords[strings.ToLower(words[len(words)-1])] || !hasLetterOrDigit(words[len(words)-1])) {
		words = words[:len(words)-1]
	}
	title = strings.Join(words, " ")
	if !nameShaped(author) || !hasLetter(title) || looksLikeChapterTitle(title) {
		return "", ""
	}
	if na, nl := normalizeTitle(author), normalizeTitle(layoutTitle); nl != "" && (strings.Contains(nl, na) || strings.Contains(na, nl)) {
		return "", ""
	}
	return author, title
}

// commonLeadingWords returns the words every list starts with, compared
// without case, spelled as the first list spells them.
func commonLeadingWords(lists [][]string) []string {
	if len(lists) == 0 {
		return nil
	}
	n := len(lists[0])
	for _, l := range lists[1:] {
		i := 0
		for i < n && i < len(l) && strings.EqualFold(lists[0][i], l[i]) {
			i++
		}
		n = i
	}
	return lists[0][:n]
}

// nameShaped reports whether s could be a person's name read from a file
// name: a significant name token, no digit, at most five words, and no title
// stopword ("Wheel of Time" is a series, not an author).
func nameShaped(s string) bool {
	words := strings.Fields(s)
	if len(words) == 0 || len(words) > 5 || len(significantAuthorTokens(s)) == 0 {
		return false
	}
	for _, r := range s {
		if unicode.IsDigit(r) {
			return false
		}
	}
	for _, w := range words {
		if titleStopwords[strings.ToLower(w)] {
			return false
		}
	}
	return true
}

func hasLetter(s string) bool {
	return strings.IndexFunc(s, unicode.IsLetter) >= 0
}

func hasLetterOrDigit(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// sameBookTitle reports whether two titles name the same book by the scan's
// own title rule, or one contains the other.
func sameBookTitle(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if titleMatch(a, b) {
		return true
	}
	na, nb := normalizeTitle(a), normalizeTitle(b)
	return na != "" && nb != "" && (strings.Contains(na, nb) || strings.Contains(nb, na))
}

// rankConflictCandidates ranks suggestions for a unit whose files name another
// author than its folder. Books of the author the files name come first,
// scored on the title the files name; the folder author's books follow, scored
// on the same title, so a look alike of an unrelated title falls below the
// threshold instead of riding a shared opening. No volume rule reads the book
// folder's title: it is the folder's book, not the files'.
func rankConflictCandidates(title string, catalogue []scanBook, catalogueByAuthor map[int64][]int, filesSet, folderSet map[int64]bool) []db.UnmatchedCandidate {
	var out []db.UnmatchedCandidate
	if len(filesSet) > 0 {
		out = append(out, rankCandidates(title, "", nil, catalogue, catalogueByAuthor, filesSet)...)
	}
	rest := make(map[int64]bool, len(folderSet))
	for id := range folderSet {
		if !filesSet[id] {
			rest[id] = true
		}
	}
	if len(rest) > 0 {
		for _, c := range rankCandidates(title, "", nil, catalogue, catalogueByAuthor, rest) {
			c.FolderAuthorOnly = true
			out = append(out, c)
		}
	}
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// conflictReason is the unit's reason when its files name another author:
// what is true of that author, not of the folder's.
func conflictReason(title string, filesSet map[int64]bool, booksOf func(int64) int) string {
	switch {
	case filesSet == nil:
		return ""
	case len(filesSet) == 0:
		return unmatchedReasonAuthorNotInLibrary
	}
	n := 0
	for id := range filesSet {
		n += booksOf(id)
	}
	switch {
	case n == 0:
		return unmatchedReasonNoCandidateBooks
	case title == "":
		return unmatchedReasonNoTitleParsed
	}
	return unmatchedReasonNoTitleMatch
}
