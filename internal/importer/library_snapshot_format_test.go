package importer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestLibrarySnapshot_FindExistingKeepsToTheFormatAsked pins #3896: in one
// combined root a book's folder holds both formats, and an audiobook lookup
// answered with the epub because its name sorts before the m4b. Each format's
// lookup must answer with a file of that format only.
func TestLibrarySnapshot_FindExistingKeepsToTheFormatAsked(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Frank Herbert", "Dune Messiah B002V8H660")
	epub := filepath.Join(folder, "Dune Messiah by Frank Herbert.epub")
	m4b := filepath.Join(folder, "Dune Messiah.m4b")
	writeFile(t, epub)
	writeFile(t, m4b)

	for name, tc := range map[string]struct {
		mediaType, want string
	}{
		"audiobook gets the m4b": {models.MediaTypeAudiobook, m4b},
		"ebook gets the epub":    {models.MediaTypeEbook, epub},
	} {
		t.Run(name, func(t *testing.T) {
			snap := NewLibrarySnapshot(root, root)
			if got := snap.FindExisting(context.Background(), "Dune Messiah", "Frank Herbert", tc.mediaType); got != tc.want {
				t.Errorf("FindExisting(%s) = %q, want %q", tc.mediaType, got, tc.want)
			}
		})
	}

	t.Run("audiobook lookup never answers with an ebook alone", func(t *testing.T) {
		only := t.TempDir()
		writeFile(t, filepath.Join(only, "Frank Herbert", "Dune Messiah", "Dune Messiah.epub"))
		snap := NewLibrarySnapshot(only, only)
		if got := snap.FindExisting(context.Background(), "Dune Messiah", "Frank Herbert", models.MediaTypeAudiobook); got != "" {
			t.Errorf("audiobook lookup = %q, want no match", got)
		}
	})
}
