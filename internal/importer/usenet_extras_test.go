package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Tests for #3647: a usenet job folder holding only release extras after a
// clean move import must go, and every guard around that must hold.

// usenetJob makes completeDir/<name> holding the given files (relative path to
// contents) and returns completeDir and the job folder.
func usenetJob(t *testing.T, files map[string][]byte) (completeDir, jobDir string) {
	t.Helper()
	completeDir = t.TempDir()
	jobDir = filepath.Join(completeDir, "Scott.Lynch.-.The.Lies.Of.Locke.Lamora.2006.RETAIL.EPUB.eBook-CTO")
	for rel, data := range files {
		p := filepath.Join(jobDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return completeDir, jobDir
}

// clientRoots is a stand in for the SABnzbd or NZBGet completed folder lookup.
func clientRoots(dirs ...string) downloadRootsFunc {
	return func(context.Context) ([]string, error) { return dirs, nil }
}

func assertGone(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s should have been removed: %s (stat err %v); left: %v", what, path, err, filesUnder(t, path))
	}
}

func assertKept(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s must be kept: %s: %v", what, path, err)
	}
}

func assertImported(t *testing.T, ctx context.Context, s *Scanner, dl *models.Download) {
	t.Helper()
	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("download status = %q, want imported", got.Status)
	}
}

func extrasJob(t *testing.T) (string, string) {
	return usenetJob(t, map[string][]byte{
		"The Lies of Locke Lamora.epub": bookSized("epub"),
		"cto.nfo":                       []byte("nfo"),
		"cover.jpg":                     []byte("jpg"),
		"file_id.diz":                   []byte("diz"),
		"Sample/scan.png":               []byte("png"),
		"Thumbs.db":                     []byte("db"),
	})
}

func TestUsenetJobExtras_SABnzbdCleanMoveImportRemovesFolder(t *testing.T) {
	completeDir, jobDir := extrasJob(t)
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))

	s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertGone(t, jobDir, "SABnzbd job folder holding only release extras")
	assertKept(t, completeDir, "completed folder")
}

// Auto mode resolves to move for usenet (#1542), so it gets the same sweep.
func TestUsenetJobExtras_NZBGetAutoModeRemovesFolder(t *testing.T) {
	completeDir, jobDir := extrasJob(t)
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "")
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))

	s.tryImportInternal(ctx, dl, jobDir, "nzbget", "42", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertGone(t, jobDir, "NZBGet job folder holding only release extras")
	assertKept(t, completeDir, "completed folder")
}

// A single file job reports the file as its path; the sweep works on the
// file's job folder.
func TestUsenetJobExtras_SingleFileJobRemovesFolder(t *testing.T) {
	completeDir, jobDir := usenetJob(t, map[string][]byte{
		"book.epub": bookSized("epub"),
		"cto.nfo":   []byte("nfo"),
		"book.url":  []byte("url"),
	})
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))

	s.tryImportInternal(ctx, dl, filepath.Join(jobDir, "book.epub"), "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertGone(t, jobDir, "single file job folder")
	assertKept(t, completeDir, "completed folder")
}

// A single file audiobook job moves the lone file, not the folder, so the
// folder and its extras were left on main.
func TestUsenetJobExtras_SingleFileAudiobookJobRemovesFolder(t *testing.T) {
	s, settings, _, _, _, _, book, ctx := dropFixture(t, models.MediaTypeAudiobook)
	s.audiobookDir = t.TempDir()
	if err := settings.Set(ctx, "import.mode", "move"); err != nil {
		t.Fatal(err)
	}
	completeDir, jobDir := usenetJob(t, map[string][]byte{
		"audiobook.m4b": []byte("m4b bytes"),
		"release.nfo":   []byte("nfo"),
		"folder.jpg":    []byte("jpg"),
	})
	dl := newPairDownload(t, s.downloads, book, models.MediaTypeAudiobook, ctx)
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))

	s.tryImportInternal(ctx, dl, filepath.Join(jobDir, "audiobook.m4b"), "sabnzbd", "nzo-ab", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertHasFileNamed(t, s.audiobookDir, "audiobook.m4b", "audiobook library")
	assertGone(t, jobDir, "single file audiobook job folder")
	assertKept(t, completeDir, "completed folder")
}

// Anything that is not a release extra keeps the whole folder, extras and
// all: an unimported book file (here a .mobi too small to be imported) or a
// file of an unknown type.
func TestUsenetJobExtras_UnimportedOrUnknownFileKeepsFolder(t *testing.T) {
	for name, leftover := range map[string]string{
		"unimported mobi": "tiny.mobi",
		"unknown type":    "installer.exe",
	} {
		t.Run(name, func(t *testing.T) {
			completeDir, jobDir := usenetJob(t, map[string][]byte{
				"book.epub": bookSized("epub"),
				"cto.nfo":   []byte("nfo"),
				leftover:    []byte("x"),
			})
			s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
			ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))

			s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)

			assertImported(t, ctx, s, dl)
			assertKept(t, filepath.Join(jobDir, leftover), "the file that is not an extra")
			assertKept(t, filepath.Join(jobDir, "cto.nfo"), "the extras of a folder that stays")
		})
	}
}

// A torrent's folder may still be seeding: extras stay whatever the mode.
func TestUsenetJobExtras_TorrentFolderKept(t *testing.T) {
	completeDir, jobDir := extrasJob(t)
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))

	s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, jobDir, "qbittorrent", "hash", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(jobDir, "cto.nfo"), "torrent folder extras")
}

// The download root itself is never swept, even when it holds nothing but
// extras after the import.
func TestUsenetJobExtras_DownloadRootNeverPruned(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string][]byte{"book.epub": bookSized("epub"), "cto.nfo": []byte("nfo")} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	s.WithDownloadDir(root)
	ctx = withClientDownloadRoots(ctx, clientRoots(filepath.Join(t.TempDir(), "elsewhere")))

	s.tryImportInternal(ctx, dl, root, "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(root, "cto.nfo"), "extras in the download root")
}

// A SABnzbd category ending in an asterisk puts jobs straight into the
// category folder. A single file job's parent is then that shared folder,
// which the client reports as its completed folder: refused.
func TestUsenetJobExtras_ClientCategoryFolderNeverPruned(t *testing.T) {
	category := t.TempDir()
	for name, data := range map[string][]byte{"book.epub": bookSized("epub"), "other-job.nfo": []byte("nfo")} {
		if err := os.WriteFile(filepath.Join(category, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, clientRoots(category))

	s.tryImportInternal(ctx, dl, filepath.Join(category, "book.epub"), "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(category, "other-job.nfo"), "another job's extra in the category folder")
}

// When the client's completed folder cannot be read (an NZB only SABnzbd
// key, say), the sweep refuses rather than guess.
func TestUsenetJobExtras_UnknownClientFolderKeepsFolder(t *testing.T) {
	for name, roots := range map[string]downloadRootsFunc{
		"no lookup":     nil,
		"lookup failed": func(context.Context) ([]string, error) { return nil, errors.New("refused get_config") },
	} {
		t.Run(name, func(t *testing.T) {
			_, jobDir := extrasJob(t)
			s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
			if roots != nil {
				ctx = withClientDownloadRoots(ctx, roots)
			}
			s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)

			assertImported(t, ctx, s, dl)
			assertKept(t, filepath.Join(jobDir, "cto.nfo"), "extras when the client folder is unknown")
		})
	}
}

// A partial import is not clean: the folder and everything in it stay.
func TestUsenetJobExtras_PartialImportKeepsFolder(t *testing.T) {
	completeDir, jobDir := usenetJob(t, map[string][]byte{
		"book.epub": bookSized("epub"),
		"book.mobi": bookSized("mobi"),
		"cto.nfo":   []byte("nfo"),
	})
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))
	refused := failCommitFor(t, ".mobi")

	s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)

	if *refused == 0 {
		t.Fatal("the mobi commit was never attempted, so this test exercised nothing")
	}
	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == models.StateImported {
		t.Fatal("a partial import must not be marked imported")
	}
	assertKept(t, filepath.Join(jobDir, "book.mobi"), "the unimported mobi")
	assertKept(t, filepath.Join(jobDir, "cto.nfo"), "extras after a partial import")
}

// A symlink in the job folder is never followed or removed, so it keeps the
// folder, and the file it points at is untouched.
func TestUsenetJobExtras_SymlinkKeepsFolder(t *testing.T) {
	completeDir, jobDir := extrasJob(t)
	outside := filepath.Join(t.TempDir(), "precious.nfo")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(jobDir, "link.nfo")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	ctx = withClientDownloadRoots(ctx, clientRoots(completeDir))

	s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)

	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(jobDir, "link.nfo"), "the symlink")
	assertKept(t, outside, "the symlink's target")
}

func TestIsReleaseExtra(t *testing.T) {
	for _, name := range []string{"a.NFO", "x.diz", "x.vol01+02.par2", "Thumbs.db", ".DS_Store", "desktop.ini", "cover.JPEG", "metadata.opf"} {
		if !isReleaseExtra(name) {
			t.Errorf("isReleaseExtra(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"book.epub", "book.mobi", "book.pdf", "a.m4b", "a.mp3", "x.rar", "x.exe", "noext"} {
		if isReleaseExtra(name) {
			t.Errorf("isReleaseExtra(%q) = true, want false", name)
		}
	}
}

// A book file the import found but did not place keeps the folder even when
// its extension is on the extras list: .txt is also an ebook format.
func TestSweepUsenetJobExtras_UnplacedBookFileKeepsFolder(t *testing.T) {
	completeDir, jobDir := usenetJob(t, map[string][]byte{
		"book.txt": bookSized("a text edition the profile did not allow"),
		"cto.nfo":  []byte("nfo"),
	})
	s := &Scanner{libraryDir: t.TempDir()}
	ctx := withClientDownloadRoots(context.Background(), clientRoots(completeDir))
	moved := filepath.Join(jobDir, "book.epub") // already moved away

	s.sweepUsenetJobExtras(ctx, jobDir, []string{moved}, []string{moved, filepath.Join(jobDir, "book.txt")})
	assertKept(t, filepath.Join(jobDir, "book.txt"), "the unplaced .txt book")
	assertKept(t, filepath.Join(jobDir, "cto.nfo"), "extras beside it")

	// The same folder without the .txt in the book list is all extras.
	s.sweepUsenetJobExtras(ctx, jobDir, []string{moved}, []string{moved})
	assertGone(t, jobDir, "job folder of extras only")
}
