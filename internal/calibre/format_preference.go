package calibre

import "github.com/vavallee/bindery/internal/db"

// lessByFormatPreference orders two paths by the shared ebook format
// preference (db.EbookFormatLess, #2832). The first file makes the Calibre
// record, so it should be the one Calibre reads metadata and a cover from
// best; every later file joins that record as another format.
func lessByFormatPreference(a, b string) bool {
	return db.EbookFormatLess(a, b)
}
