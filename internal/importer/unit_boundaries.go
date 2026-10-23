package importer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vavallee/bindery/internal/textutil"
)

// The helpers below decide where one book ends and the next begins. Both the
// folder import scan (internal/api/scan_walk.go) and the library scan's
// unmatched grouping (unmatched_units.go) use them, so they live here once.

// discFolderRe matches the subfolder names ("CD1", "Disc 2", "Part 03",
// "Chapter 4") that split ONE audiobook across several directories. When every
// subdirectory of a folder looks like one of these, the folder is a single
// audiobook rather than a shelf of separate books.
//
// The vocabulary is deliberately restricted to words that can only name a
// PIECE of one recording. It used to also accept "Book N", "Vol N" and a bare
// "1"/"02", and those name a whole book at least as often as a piece of one:
// a series laid out as "Mistborn/Book 1", "Mistborn/Book 2" was offered as a
// single unit and imported three books' audio onto one book row (#2672), which
// loses the distinction between them. Library adoption already drew the line
// the same way (discSetNameRe, unmatched_units.go).
//
// The error in the other direction costs just as much: splitting one recording
// into several units sends each piece to a different book, and a second
// audiobook for a book that already has one is dropped by the idempotency
// guard. That is why "Part", "Pt", "Chapter" and "Ch" stay, since no series
// names its separate books that way, and why AllDiscFolders still requires
// audio beneath every candidate before it merges anything.
var discFolderRe = regexp.MustCompile(`(?i)^(cd|dis[ck]|part|pt|chapter|ch)\s*[._-]?\s*\d+$`)

// titledDiscRe splits a disc folder named after its book, "<title> Disc 1",
// "<title> CD2", "<title> (Disc 03)", into the title and the disc label. Only
// cd/disc labels count here: a title followed by "Part 2" names a part of a
// series as often as a disc.
var titledDiscRe = regexp.MustCompile(`(?i)^(.+?)(?:[\s._-]+[(\[]?|[(\[])\s*(?:cd|dis[ck])\s*[._-]?\s*\d+\s*[)\]]?$`)

// isTitledDisc reports whether name is a disc folder of the book folder
// parentName: the book's own title followed by a cd/disc label, as CD rips
// often name each disc after its album tag (#3327). The title must be the
// parent's, compared the way book titles are, so a disc label after any other
// prefix ("Havgapet Disc 1" under "Fjellvinden (2002)") is a different book.
func isTitledDisc(name, parentName string) bool {
	m := titledDiscRe.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	title := textutil.FoldForTitleMatch(cleanLayoutTitle(parentName))
	return title != "" && textutil.FoldForTitleMatch(cleanLayoutTitle(m[1])) == title
}

// isDiscSetName reports whether name is one disc of the multi-disc audiobook in
// the book folder parentName, by the narrow library rule: a bare cd/disc label
// (discSetNameRe), or the book's own title followed by one (isTitledDisc).
func isDiscSetName(name, parentName string) bool {
	return discSetNameRe.MatchString(name) || isTitledDisc(name, parentName)
}

// IsDiscFolderName reports whether a directory's base name looks like one disc
// or part of a multi-disc audiobook. The name alone; see AllDiscFolders for
// the check that also requires audio beneath it.
func IsDiscFolderName(name string) bool {
	return discFolderRe.MatchString(name)
}

// AllDiscFolders reports whether every directory in dirs is a disc/part folder
// (by name) that actually holds audio somewhere beneath it. With titled set it
// also takes a cd/disc label after the parent folder's own title (#3327). The
// caller sets it only for a parent at book depth, at least two levels under a
// root: one level down the parent is an author folder, and "Kari Nordmann
// Disc 1" there must not make the author folder a book. Both conditions are
// required so a shelf of audiobook folders with numeric-ish names isn't collapsed
// into one book, and an empty "CD1" placeholder doesn't fake a multi-disc set.
func AllDiscFolders(dirs []string, titled bool) bool {
	for _, d := range dirs {
		name := filepath.Base(d)
		isDisc := discFolderRe.MatchString(name) || titled && isTitledDisc(name, filepath.Base(filepath.Dir(d)))
		if !isDisc {
			return false
		}
		if !dirSubtreeHasAudio(d) {
			return false
		}
	}
	return len(dirs) > 0
}

// dirSubtreeHasAudio reports whether dir contains at least one audio file within
// a bounded number of entries, so a deep tree can't stall the disc-folder check.
func dirSubtreeHasAudio(dir string) bool {
	const limit = 2000
	count := 0
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		count++
		if count > limit {
			return filepath.SkipAll
		}
		if !d.IsDir() && IsAudioFile(p) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// SameStem reports whether every file shares one normalised base name (the file
// name without its extension, lower-cased). Title.epub and Title.mobi share the
// stem "title"; Dune.epub and Foundation.epub do not.
func SameStem(files []string) bool {
	stem := func(p string) string {
		b := filepath.Base(p)
		return strings.ToLower(strings.TrimSuffix(b, filepath.Ext(b)))
	}
	if len(files) == 0 {
		return false
	}
	first := stem(files[0])
	for _, f := range files[1:] {
		if stem(f) != first {
			return false
		}
	}
	return true
}
