package models

import (
	"path/filepath"
	"strings"
)

// audioExtensions lists the audio container extensions that mark a file as part
// of an audiobook. The importer's scan, its download-format detection and the
// book_files format guard all read this one set, so they cannot disagree on
// what counts as audio.
var audioExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".m4b": true, ".aac": true,
	".flac": true, ".ogg": true, ".opus": true,
}

// .wma is intentionally not in the set: the scan, manual import and library
// finder ignore it today, and listing it here would start tracking .wma files
// as audiobook tracks, which is a separate decision from #3474.

// IsAudioPath reports whether path carries an audiobook audio extension.
func IsAudioPath(path string) bool {
	return audioExtensions[strings.ToLower(filepath.Ext(path))]
}

// AudioExtensions returns the audio extensions in lower case, dot included.
func AudioExtensions() []string {
	out := make([]string, 0, len(audioExtensions))
	for ext := range audioExtensions {
		out = append(out, ext)
	}
	return out
}

// BookFileFormatFor returns the book_files format a file at path belongs to.
// An audio extension is always an audiobook file, whatever the book's declared
// media type or the caller's guess says: media_type records acquisition intent
// ("both" is a legal value), not the nature of a file (#3474). A path with no
// audio extension keeps the format the caller supplied, because an audiobook is
// also recorded by its folder, which has no extension to read.
func BookFileFormatFor(path, format string) string {
	if IsAudioPath(path) {
		return MediaTypeAudiobook
	}
	return format
}
