package models

import "time"

// BookFile represents one on-disk file associated with a book.
// A book may have multiple files when a download bundle contains several
// formats (e.g. epub + mobi + pdf). The book_files table is the source of
// truth; the legacy ebook_file_path and audiobook_file_path columns on the
// books table are deprecated computed views derived from this table.
type BookFile struct {
	ID        int64     `json:"id"`
	BookID    int64     `json:"bookId"`
	Format    string    `json:"format"` // "ebook" or "audiobook"
	Path      string    `json:"path"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
}
