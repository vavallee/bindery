package importer

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// flattenBranches are the two audiobook import branches that flatten a
// folder: the multi-disc flatten and the per-file naming template.
var flattenBranches = []struct{ name, key, value string }{
	{"multi-disc", "import.audiobook.flatten_multi_disc", "true"},
	{"naming template", "naming.audiobook_file_template", "{Title} - Part {Part:3}.{ext}"},
}

// writeFlattenSource builds a download folder from rel path to content and
// returns it with its audio files, sorted.
func writeFlattenSource(t *testing.T, files []string) (string, []string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "Fjellvinden")
	var audio []string
	for _, name := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(name) == ".mp3" {
			audio = append(audio, p)
		}
	}
	sort.Strings(audio)
	return src, audio
}

// A flatten places the tracks and the root sidecars only. In move mode the
// source folder used to be deleted whole afterwards, which destroyed every
// file that is not audio inside a subfolder: a booklet, the cover of each
// disc, an archive volume. Those must survive in the source. What was placed,
// and the receipts a move drops on purpose, still go. Copy mode keeps its
// source untouched and places exactly what it always did.
func TestFlatten_MoveModeKeepsNestedFilesItDidNotPlace(t *testing.T) {
	all := []string{
		"CD1/01 Spor 01.mp3", "CD2/01 Spor 01.mp3",
		"CD1/cover.jpg", "CD2/cover.jpg",
		"Extras/booklet.pdf", "Extras/Fjellvinden.part1.rar",
		"CD1/Fjellvinden.nzb", "cover.jpg",
	}
	unplaced := []string{"CD1/cover.jpg", "CD2/cover.jpg", "Extras/booklet.pdf", "Extras/Fjellvinden.part1.rar"}
	consumed := []string{"CD1/01 Spor 01.mp3", "CD2/01 Spor 01.mp3", "CD1/Fjellvinden.nzb", "cover.jpg"}

	for _, br := range flattenBranches {
		for _, mode := range []string{"move", "copy"} {
			t.Run(br.name+" "+mode, func(t *testing.T) {
				s, dl, dlRepo, settings, ctx := multiDiscFixture(t, mode)
				if err := settings.Set(ctx, br.key, br.value); err != nil {
					t.Fatal(err)
				}
				src, audio := writeFlattenSource(t, all)

				s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, src, "", "", "", nil, audio)

				if got, _ := dlRepo.GetByID(ctx, dl.ID); got.Status != models.StateImported {
					t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
				}
				// The library folder is the same in both modes: two tracks
				// and the root cover, nothing from the subfolders.
				placed := relFiles(t, importedAudiobookDir(t, s))
				if len(placed) != 3 || placed["cover.jpg"] != "cover.jpg" {
					t.Errorf("library holds %v, want the two tracks and the root cover", placed)
				}

				left := relFiles(t, src)
				if mode == "copy" {
					if len(left) != len(all) {
						t.Errorf("copy mode changed its source: %v", left)
					}
					return
				}
				for _, name := range unplaced {
					if left[name] != name {
						t.Errorf("%s never reached the library and is gone from the source (source now: %v)", name, left)
					}
				}
				for _, name := range consumed {
					if _, ok := left[name]; ok {
						t.Errorf("%s still in the source after the move", name)
					}
				}
			})
		}
	}
}

// The root sidecar carry is best effort: a cover that cannot be copied does
// not fail the import. In move mode that swallowed failure used to end in the
// source folder being deleted, cover included. The import still succeeds, and
// the cover stays in the source.
func TestFlatten_MoveModeKeepsRootSidecarThatFailedToCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	for _, br := range flattenBranches {
		t.Run(br.name, func(t *testing.T) {
			s, dl, dlRepo, settings, ctx := multiDiscFixture(t, "move")
			if err := settings.Set(ctx, br.key, br.value); err != nil {
				t.Fatal(err)
			}
			src, audio := writeFlattenSource(t, []string{"CD1/01 Spor 01.mp3", "CD2/01 Spor 01.mp3", "cover.jpg", "notes.nfo"})
			locked := filepath.Join(src, "cover.jpg")
			if err := os.Chmod(locked, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

			s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, src, "", "", "", nil, audio)

			if got, _ := dlRepo.GetByID(ctx, dl.ID); got.Status != models.StateImported {
				t.Fatalf("status = %q (%s), want imported: a cover must not block the import", got.Status, got.ErrorMessage)
			}
			placed := relFiles(t, importedAudiobookDir(t, s))
			if _, ok := placed["cover.jpg"]; ok {
				t.Fatalf("the unreadable cover was placed (%v); the test no longer forces a copy failure", placed)
			}
			if placed["notes.nfo"] != "notes.nfo" {
				t.Errorf("the readable sidecar was not carried: %v", placed)
			}
			if _, err := os.Lstat(locked); err != nil {
				t.Errorf("cover.jpg could not be copied and was deleted with the source: %v", err)
			}
			entries, err := os.ReadDir(src)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("source should hold only the cover that was not placed, got %v", entries)
			}
		})
	}
}

// With nothing left over, a move flatten still removes its source folder
// whole, symlinks and receipts in subfolders included.
func TestRemoveFlattenedSource_RemovesFolderWhenEverythingWasPlaced(t *testing.T) {
	src, audio := writeFlattenSource(t, []string{"CD1/01.mp3", "CD2/01.mp3", "CD2/job.par2", "cover.jpg"})
	if err := os.Symlink(audio[0], filepath.Join(src, "CD1", "link.mp3")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	placed := append([]string{filepath.Join(src, "cover.jpg")}, audio...)

	left, err := removeFlattenedSource(src, placed, isReceiptOrRepairFile)
	if err != nil || len(left) != 0 {
		t.Fatalf("left %v, err %v", left, err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Errorf("source folder still there: %v", err)
	}
}
