package api

import (
	"os"
	"path/filepath"
	"slices"
	"sort"

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
//
// skip, when non-nil, is consulted for every unit the walk would otherwise
// emit; a unit it reports true for is dropped without counting toward limit
// or truncation, so the walk keeps descending past it in search of `limit`
// units skip accepts. Callers use this to filter out already-tracked units
// during the walk itself, rather than capping first and filtering the capped
// result — the latter can starve the cap entirely when most of a large,
// already-imported folder sits ahead of any new files in walk order.
//
// excluded lists folders the walk does not enter, as the library scan does
// for the download folders: a path equal to one of them is left out (the root
// itself never is). Callers pass them cleaned and symlink-resolved once, at
// setup, so the walk compares strings and makes no filesystem call for them.
// Junk folders are left out as in the library scan, through the same
// deny-list (importer.JunkLibraryDirName) so the rules cannot drift.
func enumerateImportUnits(root string, limit int, skip func(path string, isDir bool) bool, excluded []string) (units []scanUnit, truncated bool) {
	excludedSet := make(map[string]bool, len(excluded))
	for _, dir := range excluded {
		excludedSet[dir] = true
	}
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
		// A .binderyignore folder is left out, as in the library scan
		// (importer.SkipLibraryDir). It is found among the entries already
		// read rather than with a second filesystem lookup per folder.
		if !isRoot && slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.Name() == importer.LibraryIgnoreFile }) {
			return
		}

		var subdirs, audioFiles, ebookFiles []string
		for _, e := range entries {
			name := e.Name()
			full := filepath.Join(dir, name)
			switch {
			case e.IsDir():
				// Junk folders (a torrent client's ".torrents", a trash
				// folder) and the download folders are not library content,
				// as in the library scan.
				if !importer.JunkLibraryDirName(name) && !excludedSet[full] {
					subdirs = append(subdirs, full)
				}
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
			if skip != nil && skip(path, isDir) {
				return
			}
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
			// Every subdir a disc folder → one multi-disc audiobook. A disc
			// named after its book counts only from book depth down, two
			// levels under the root, so an author folder never becomes a book
			// (#3327).
			if len(subdirs) > 0 && importer.AllDiscFolders(subdirs, depth >= 2) {
				emit(dir, true)
				return
			}
			// A leaf folder of same-named ebooks → one book in several formats.
			if len(subdirs) == 0 && len(ebookFiles) >= 2 && importer.SameStem(ebookFiles) {
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
