package models

import (
	"path/filepath"
	"strings"
)

// bookFileExtensions lists common ebook file extensions, plus the audio ones
// the parser has always accepted as book files. It is the one allowlist for
// what Bindery tracks as a book file: the library scan, folder and manual
// imports, and the Calibre library import all read it, so a file one of them
// ignores the others ignore too (#3630).
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
