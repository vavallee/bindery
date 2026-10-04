package importer

import "github.com/vavallee/bindery/internal/models"

// MinPlausibleEbookBytes is the size below which an ebook format file cannot
// be a book (#2944). The reporter's library listed two 1008 byte .txt files
// as ebooks and let them be adopted as a book.
//
// 4 KiB is deliberately low. In plain text it is about 650 words, two or
// three pages; the parts every EPUB must carry before its first sentence
// (mimetype, container.xml, the OPF, a nav document) already take over a
// kilobyte of it compressed. Real short books are far above it: a 30 KB
// novella is seven times the floor, and the licence a Project Gutenberg
// short work carries is several times the floor on its own. What falls below
// it is notes, readme and link files.
//
// It is a size and nothing else. A content check was considered and left
// out, because the obvious ones misfire on real books: a word count reads a
// Chinese or Japanese novel, which puts no spaces between words, as nearly
// wordless, and an .epub that is not a zip is often another format under the
// wrong extension, such as a MOBI, which is still a book (formatsniff exists
// for exactly that, #1782).
const MinPlausibleEbookBytes int64 = 4 << 10

// TooSmallToBeABook reports whether the file at path, of size bytes, is too
// small to be a book. Only ebook formats are judged: an audiobook is a folder
// of tracks and a single track can be small.
//
// It gates every path that decides on its own, or offers, that a file in the
// library is a book: the library scan's reconcile tiers and its unmatched
// list, FindExisting on the add author path, and adoption. Manual import is
// deliberately not gated; it is the explicit override for a file that small
// that really is the book.
func TooSmallToBeABook(path string, size int64) bool {
	return IsBookFile(path) &&
		detectDownloadFormat([]string{path}) == models.MediaTypeEbook &&
		size < MinPlausibleEbookBytes
}
