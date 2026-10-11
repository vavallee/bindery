package api

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A metadata map replaces the cover with the new record's, but a cover read
// from the user's own file survives a record that has none.
func TestPreserveBookStateForMetadataMap_Cover(t *testing.T) {
	const fileCover = "bindery-cover:0123abcd.jpg"
	const providerCover = "https://covers.example.invalid/1.jpg"
	for name, tc := range map[string]struct{ had, target, want string }{
		"file cover kept when target has none":  {fileCover, "", fileCover},
		"target cover replaces a file cover":    {fileCover, providerCover, providerCover},
		"provider cover follows the new record": {"https://old.example.invalid/c.jpg", "", ""},
	} {
		book := &models.Book{ImageURL: tc.had}
		preserveBookStateForMetadataMap(book, &models.Book{ImageURL: tc.target})
		if book.ImageURL != tc.want {
			t.Errorf("%s: image_url = %q, want %q", name, book.ImageURL, tc.want)
		}
	}
}
