package importer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// IsDiscFolderName reports whether a directory's base name looks like one disc
// or part of a multi-disc audiobook. The name alone; see AllDiscFolders for
// the check that also requires audio beneath it.
func IsDiscFolderName(name string) bool {
	return discFolderRe.MatchString(name)
}

// AllDiscFolders reports whether every directory in dirs is a disc/part folder
// (by name) that actually holds audio somewhere beneath it. Both conditions are
// required so a shelf of audiobook folders with numeric-ish names isn't collapsed
// into one book, and an empty "CD1" placeholder doesn't fake a multi-disc set.
func AllDiscFolders(dirs []string) bool {
	for _, d := range dirs {
		if !discFolderRe.MatchString(filepath.Base(d)) {
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
