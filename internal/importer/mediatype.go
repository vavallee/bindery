package importer

import "github.com/vavallee/bindery/internal/models"

// IsAudioFile reports whether path carries an audiobook audio extension. The
// set lives in models.IsAudioPath so the book_files format guard shares it.
func IsAudioFile(path string) bool {
	return models.IsAudioPath(path)
}

// IsEbookFile reports whether path is a recognised book file that is NOT audio,
// i.e. a document-format ebook (epub, mobi, pdf, …). Bulk folder import uses the
// audio-vs-ebook split to decide the unit boundary: a folder of audio files is
// one audiobook, a folder of ebooks is many books.
func IsEbookFile(path string) bool {
	return IsBookFile(path) && !IsAudioFile(path)
}
