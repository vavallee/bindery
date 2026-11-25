package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A download client reports the folder an audiobook sits in. One that names a
// library folder, a folder holding one, or the shared download folder must not
// have the whole of it placed as a book (and, in move mode, removed).
func TestImport_AudiobookSourceThatIsOrHoldsStorageIsRefused(t *testing.T) {
	for _, mode := range []string{"move", "copy", "hardlink"} {
		for _, which := range []string{"library root", "above a library", "download root"} {
			t.Run(mode+"/"+which, func(t *testing.T) {
				f := newRoutingFixture(t, models.MediaTypeAudiobook, map[string]string{"import.mode": mode})
				dlRoot := t.TempDir()
				f.s.downloadDir = dlRoot
				f.s.audiobookDownloadDir = dlRoot
				mustWrite(t, filepath.Join(dlRoot, "other job", "part1.mp3"), "someone else's track")
				mustWrite(t, filepath.Join(f.abDir, "Existing Author", "Existing Book", "part1.mp3"), "library track")
				src := map[string]string{
					"library root":    f.abDir,
					"above a library": filepath.Dir(f.abDir),
					"download root":   dlRoot,
				}[which]

				dl := newPairDownload(t, f.downloads, f.book, models.MediaTypeAudiobook, f.ctx)
				f.s.tryImportInternal(f.ctx, dl, src, "", "", "", nil, nil)

				assertStatus(t, f.downloads, f.ctx, dl.GUID, models.StateImportBlocked)
				if _, err := os.Stat(filepath.Join(f.abDir, "Existing Author", "Existing Book", "part1.mp3")); err != nil {
					t.Fatalf("library file disturbed: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dlRoot, "other job", "part1.mp3")); err != nil {
					t.Fatalf("another job's file disturbed: %v", err)
				}
			})
		}
	}
}

// A job folder under the download folder is still imported.
func TestImport_AudiobookJobFolderUnderDownloadRootStillImports(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeAudiobook, map[string]string{"import.mode": "copy"})
	dlRoot := t.TempDir()
	f.s.downloadDir = dlRoot
	f.s.audiobookDownloadDir = dlRoot
	job := filepath.Join(dlRoot, "Author A - Title T")
	mustWrite(t, filepath.Join(job, "part1.mp3"), "track one")
	dl := newPairDownload(t, f.downloads, f.book, models.MediaTypeAudiobook, f.ctx)
	f.s.tryImportInternal(f.ctx, dl, job, "", "", "", nil, nil)
	assertHasFileNamed(t, f.abDir, "part1.mp3", "audiobook library")
}
