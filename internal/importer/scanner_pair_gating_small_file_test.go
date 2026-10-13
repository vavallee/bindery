package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestPairGating_ManualTinyEbookHeldThenReleased: a manual import of a tiny
// ebook for a book set to both formats is held (#942), and the audiobook that
// arrives later releases it. The release runs without the manual context, and
// must still place the file (#3224).
func TestPairGating_ManualTinyEbookHeldThenReleased(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	setPairSettings(t, settings, ctx, map[string]string{
		"import.drop_folder":      dropDir,
		"import.drop_pair_gating": "true",
	})
	ebookDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ebookDir, "short.txt"), []byte(strings.Repeat("x", 3000)), 0o644); err != nil {
		t.Fatal(err)
	}
	ebookDL := newPairDownload(t, downloads, book, "ebook", ctx)
	s.ImportFromPath(ctx, ebookDL, ebookDir, "")
	assertStatus(t, downloads, ctx, ebookDL.GUID, models.StateImportHeld)

	audioDL := newPairDownload(t, downloads, book, "audiobook", ctx)
	s.tryImportInternal(ctx, audioDL, writeAudiobookDownload(t), "", "", "", nil, nil)

	assertStatus(t, downloads, ctx, ebookDL.GUID, models.StateImportExternal)
	found := false
	_ = filepath.Walk(dropDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".txt") {
			found = true
		}
		return nil
	})
	if !found {
		t.Error("the held tiny file was not placed in the drop folder")
	}
}

// TestPairGating_HeldAutomaticDownloadStillLeavesNotes: re-reading a held path
// keeps leaving notes out when a real book sits beside them.
func TestPairGating_HeldAutomaticDownloadStillLeavesNotes(t *testing.T) {
	dir := t.TempDir()
	writeEbook(t, filepath.Join(dir, "Title T.epub"), "book")
	writeNote(t, filepath.Join(dir, "notes.txt"))
	if got := discoverHeldBookFiles(dir); len(got) != 1 || !strings.HasSuffix(got[0], ".epub") {
		t.Errorf("held files = %v, want the epub only", got)
	}
	tiny := t.TempDir()
	writeNote(t, filepath.Join(tiny, "short.txt"))
	if got := discoverHeldBookFiles(tiny); len(got) != 1 {
		t.Errorf("held files = %v, want the only file kept", got)
	}
}
