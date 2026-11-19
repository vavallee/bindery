package api

// validASIN reports whether s is a normalized Amazon ASIN: ten characters,
// each a digit or an upper-case letter. Callers normalize with
// isbnutil.NormalizeASIN first. The importer's tag reader insists on a
// leading B because Audible ASINs start with one; the API accepts any
// ten-character form so an ISBN-10-shaped ASIN pasted from a product page
// is not refused.
func validASIN(s string) bool {
	if len(s) != 10 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
