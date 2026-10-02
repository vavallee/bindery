package abs

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// An ABS audiobook stored under the configured default audiobook root folder
// (#2166) is inside Bindery storage, the same as one under the env var dir.
func TestAllowedRootsForBook_IncludesDefaultAudiobookRoot(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	rf := db.NewRootFolderRepo(database)
	settings := db.NewSettingsRepo(database)
	def, err := rf.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, "library.defaultAudiobookRootFolderId", fmt.Sprintf("%d", def.ID)); err != nil {
		t.Fatal(err)
	}

	i := &Importer{settings: settings}
	i.WithStoragePaths("/env/lib", "/env/audiobooks", rf)

	if got := i.effectiveAudiobookDir(ctx, &models.Author{}); got != def.Path {
		t.Fatalf("effectiveAudiobookDir: want %q, got %q", def.Path, got)
	}
	if !i.pathAllowedForBook(ctx, &models.Author{}, models.MediaTypeAudiobook, filepath.Join(def.Path, "Author", "Book")) {
		t.Errorf("audiobook under the default audiobook root should be accepted")
	}
	// The ebook branch must not widen to the audiobook default.
	ebookRoots := i.allowedRootsForBook(ctx, &models.Author{}, models.MediaTypeEbook)
	for _, root := range ebookRoots {
		if root == filepath.Clean(def.Path) {
			t.Errorf("ebook roots must not include the audiobook default: %v", ebookRoots)
		}
	}
}
