package models

import (
	"path/filepath"
	"strings"
)

// bookFileExtensions lists common ebook file extensions, plus the audio ones
// the parser has always accepted as book files. It is the one base allowlist
// for what Bindery tracks as a book file: the library scan and folder and
// manual imports read it as is, and the Calibre library import reads it plus
// the documented calibreOnlyExtensions (#3630).
//
// The audio part is NOT the audioExtensions set on purpose: it leaves out .aac
// and .wma, and widening it would make IsBookFilePath accept files the
// importer never treated as books.
var bookFileExtensions = map[string]bool{
	".epub": true, ".mobi": true, ".azw3": true, ".azw": true,
	".pdf": true, ".djvu": true, ".cbr": true, ".cbz": true,
	".fb2": true, ".lit": true, ".txt": true, ".rtf": true,
	".mp3": true, ".m4a": true, ".m4b": true, ".flac": true, ".ogg": true, ".opus": true,
}

// IsBookFilePath reports whether path has an extension Bindery tracks as a
// book file. Only the last extension counts, so a Kobo "x.kepub.epub" passes
// and Calibre's "x.original_epub" backup does not.
func IsBookFilePath(path string) bool {
	return bookFileExtensions[strings.ToLower(filepath.Ext(path))]
}

// calibreOnlyExtensions are the formats the Calibre library import tracks on
// top of bookFileExtensions. Each is a real reading format Calibre stores and
// earlier imports always tracked, but the scan never picked up:
//   - .prc: Mobipocket, common in older Calibre libraries.
//   - .kepub: a Kobo KEPUB format Calibre stores bare, not as .kepub.epub.
//   - .aac: audio, always in the Calibre import's audio format map.
//
// They are not added to bookFileExtensions because that would widen what the
// library scan and folder imports track, which is a separate decision.
var calibreOnlyExtensions = map[string]bool{
	".prc": true, ".kepub": true, ".aac": true,
}

// IsCalibreBookFilePath reports whether the Calibre library import tracks a
// format stored at path: anything IsBookFilePath accepts, plus
// calibreOnlyExtensions. A Polish backup (.original_epub) or KFX does not
// pass (#3630).
func IsCalibreBookFilePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return bookFileExtensions[ext] || calibreOnlyExtensions[ext]
}
