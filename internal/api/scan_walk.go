package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/importer"
)

// scanUnit is one importable book unit discovered by enumerateImportUnits. A
// unit is either a single ebook FILE or a DIRECTORY that stands for one book
// (a folder-based audiobook, or an ebook present in several formats).
type scanUnit struct {
	path  string
	name  string
	isDir bool
}

// discFolderRe matches the disc/part subfolder names ("CD1", "Disc 2",
// "Part 03", "Vol. 1", or a bare "1"/"02") that split ONE audiobook across
// several directories. When every subdirectory of a folder looks like a disc,
// the folder is a single multi-disc audiobook rather than a shelf of separate
// books.
var discFolderRe = regexp.MustCompile(`(?i)^(cd|dis[ck]|part|pt|vol|volume|book|chapter|ch)\s*[._-]?\s*\d+$|^\d{1,2}$`)

// walkUnitLimits bounds the recursive enumeration so pointing the scan at a
// pathological tree can't stall the request or allocate without bound.
const (
	// maxWalkEntries caps how many directory entries the walk inspects overall.
	maxWalkEntries = 50000
	// maxWalkDepth caps how deep the walk descends below the scan root.
	maxWalkDepth = 12
)

// enumerateImportUnits walks root recursively and returns the individual book
// units beneath it, deciding at each directory whether the directory is ONE
// unit or a container of many (issue #1434). The unit-boundary heuristic:
//
//   - A directory that directly contains audio files is ONE audiobook unit
//     (loose disc tracks belong to a single book). The walk does not descend.
//   - A directory whose subdirectories all look like disc folders (CD1, CD2…)
//     is ONE multi-disc audiobook unit.
//   - A leaf directory holding two or more ebook files that share a base name
//     (Title.epub + Title.mobi) is ONE unit — the same book in several formats.
//   - Any other directory is a container (a library root or an author folder):
//     each ebook file directly inside it is its own unit, and every
//     subdirectory is recursed into.
//
// Enumeration stops once `limit` units are collected (truncated=true) or the
// entry/depth guards trip. Units are returned in a stable, name-sorted order.
func enumerateImportUnits(root string, limit int) (units []scanUnit, truncated bool) {
	entriesSeen := 0
	var walk func(dir string, depth int, isRoot bool)
	walk = func(dir string, depth int, isRoot bool) {
		if truncated {
			return
		}
		if len(units) >= limit || depth > maxWalkDepth || entriesSeen > maxWalkEntries {
			truncated = true
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return // skip unreadable directories rather than abort the whole scan
		}
		entriesSeen += len(entries)

		var subdirs, audioFiles, ebookFiles []string
		for _, e := range entries {
			name := e.Name()
			full := filepath.Join(dir, name)
			switch {
			case e.IsDir():
				subdirs = append(subdirs, full)
			case importer.IsAudioFile(full):
				audioFiles = append(audioFiles, full)
			case importer.IsEbookFile(full):
				ebookFiles = append(ebookFiles, full)
			}
		}
		sort.Strings(subdirs)
		sort.Strings(audioFiles)
		sort.Strings(ebookFiles)

		emit := func(path string, isDir bool) {
			if len(units) >= limit {
				truncated = true
				return
			}
			units = append(units, scanUnit{path: path, name: filepath.Base(path), isDir: isDir})
		}

		// A directory (never the scan root itself) can BE a single book unit.
		if !isRoot {
			// Loose audio tracks → one audiobook folder.
			if len(audioFiles) > 0 {
				emit(dir, true)
				return
			}
			// Every subdir a disc folder → one multi-disc audiobook.
			if len(subdirs) > 0 && allDiscFolders(subdirs) {
				emit(dir, true)
				return
			}
			// A leaf folder of same-named ebooks → one book in several formats.
			if len(subdirs) == 0 && len(ebookFiles) >= 2 && sameStem(ebookFiles) {
				emit(dir, true)
				return
			}
		}

		// Container: each ebook file is its own unit; recurse into subdirs.
		for _, f := range ebookFiles {
			emit(f, false)
			if truncated {
				return
			}
		}
		// A container that ALSO has loose audio at the root (audio files never
		// classified the root above) surfaces each audio file as its own unit.
		if isRoot {
			for _, f := range audioFiles {
				emit(f, false)
				if truncated {
					return
				}
			}
		}
		for _, sd := range subdirs {
			walk(sd, depth+1, false)
			if truncated {
				return
			}
		}
	}
	walk(root, 0, true)
	return units, truncated
}

// allDiscFolders reports whether every directory in dirs is a disc/part folder
// (by name) that actually holds audio somewhere beneath it. Both conditions are
// required so a shelf of audiobook folders with numeric-ish names isn't collapsed
// into one book, and an empty "CD1" placeholder doesn't fake a multi-disc set.
func allDiscFolders(dirs []string) bool {
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
		if !d.IsDir() && importer.IsAudioFile(p) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// sameStem reports whether every file shares one normalised base name (the file
// name without its extension, lower-cased). Title.epub and Title.mobi share the
// stem "title"; Dune.epub and Foundation.epub do not.
func sameStem(files []string) bool {
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
