package importer

import (
	"path/filepath"
	"strings"
)

// audioExtensions lists the audio container extensions that mark a file as part
// of an audiobook. Kept in sync with the audio arm of detectDownloadFormat so
// the two agree on what counts as audio.
var audioExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".m4b": true, ".aac": true,
	".flac": true, ".ogg": true, ".opus": true,
}

// IsAudioFile reports whether path carries an audiobook audio extension.
func IsAudioFile(path string) bool {
	return audioExtensions[strings.ToLower(filepath.Ext(path))]
}

// IsEbookFile reports whether path is a recognised book file that is NOT audio,
// i.e. a document-format ebook (epub, mobi, pdf, …). Bulk folder import uses the
// audio-vs-ebook split to decide the unit boundary: a folder of audio files is
// one audiobook, a folder of ebooks is many books.
func IsEbookFile(path string) bool {
	return IsBookFile(path) && !IsAudioFile(path)
}
