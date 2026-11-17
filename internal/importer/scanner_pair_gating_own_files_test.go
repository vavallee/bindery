package importer

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// A Transmission download reports the client's shared category folder as its
// path and passes the torrent's own files separately. Pair gating used to hold
// the folder, so the release rediscovered everything in it. These tests pin
// that a hold records, and a release drops, only the download's own files.

func writeHeldFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, bookSized(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// dropListing returns every file under dir, relative, sorted.
func dropListing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func gatingSettings() map[string]string {
	return map[string]string{"import.drop_pair_gating": "true"}
}

func TestPairGating_Transmission_EbookHeldAndReleasedOwnFilesOnly(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	kv := gatingSettings()
	kv["import.drop_folder"] = dropDir
	setPairSettings(t, settings, ctx, kv)

	shared := t.TempDir()
	writeHeldFiles(t, shared, map[string]string{
		"book.epub":                 "own epub",
		"unrelated.epub":            "unrelated epub",
		"Unrelated Audio/01.m4b":    "unrelated track",
		"Unrelated Audio/02.m4b":    "unrelated track two",
		"unrelated-audio-loose.mp3": "loose",
	})
	own := filepath.Join(shared, "book.epub")

	ebookDL := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(ctx, ebookDL, shared, "transmission", "", "", nil, []string{own})
	assertStatus(t, downloads, ctx, ebookDL.GUID, models.StateImportHeld)

	held, err := downloads.GetByGUID(ctx, ebookDL.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if held.ImportPath != own {
		t.Fatalf("held import_path = %q, want only the torrent's own file %q", held.ImportPath, own)
	}

	audioDL := newPairDownload(t, downloads, book, "audiobook", ctx)
	s.tryImportInternal(ctx, audioDL, writeAudiobookDownload(t), "", "", "", nil, nil)

	want := []string{"Title T - Author A.epub", "Title T - Author A/audiobook.m4b"}
	if got := dropListing(t, dropDir); !equalStrings(got, want) {
		t.Fatalf("drop folder = %v, want %v", got, want)
	}
	body, _ := os.ReadFile(filepath.Join(dropDir, "Title T - Author A.epub"))
	if string(body[:8]) != "own epub" {
		t.Errorf("dropped epub is not the torrent's own file: %q", body[:20])
	}
	assertStatus(t, downloads, ctx, ebookDL.GUID, models.StateImportExternal)
	assertStatus(t, downloads, ctx, audioDL.GUID, models.StateImportExternal)

	done, _ := downloads.GetByGUID(ctx, ebookDL.GUID)
	if srcs := done.HandedOffSources(); len(srcs) != 1 || srcs[0] != own {
		t.Errorf("recorded hand off = %v, want only %q", srcs, own)
	}
	for _, p := range []string{"unrelated.epub", "Unrelated Audio/01.m4b", "unrelated-audio-loose.mp3"} {
		if _, err := os.Stat(filepath.Join(shared, p)); err != nil {
			t.Errorf("unrelated file %s was touched: %v", p, err)
		}
	}
}

// The audiobook arrives first and is held; the ebook then releases it. The
// torrent's tracks sit in a folder of their own inside the shared folder, and
// an unrelated audiobook and epub sit beside it.
func TestPairGating_Transmission_AudiobookHeldFirstReleasedByEbook(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	kv := gatingSettings()
	kv["import.drop_folder"] = dropDir
	setPairSettings(t, settings, ctx, kv)

	shared := t.TempDir()
	writeHeldFiles(t, shared, map[string]string{
		"Title T/01.m4b":     "t1",
		"Title T/02.m4b":     "t2",
		"Other Audio/01.m4b": "o1",
		"unrelated.epub":     "unrelated epub",
	})
	tracks := []string{filepath.Join(shared, "Title T/01.m4b"), filepath.Join(shared, "Title T/02.m4b")}

	audioDL := newPairDownload(t, downloads, book, "audiobook", ctx)
	s.tryImportInternal(ctx, audioDL, shared, "transmission", "", "", nil, tracks)
	assertStatus(t, downloads, ctx, audioDL.GUID, models.StateImportHeld)
	held, _ := downloads.GetByGUID(ctx, audioDL.GUID)
	if held.ImportPath != filepath.Join(shared, "Title T") {
		t.Fatalf("held import_path = %q, want the torrent's own folder", held.ImportPath)
	}

	ebookDL := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(ctx, ebookDL, writeEbookDownload(t), "", "", "", nil, nil)

	want := []string{"Title T - Author A.epub", "Title T - Author A/01.m4b", "Title T - Author A/02.m4b"}
	if got := dropListing(t, dropDir); !equalStrings(got, want) {
		t.Fatalf("drop folder = %v, want %v", got, want)
	}
	assertStatus(t, downloads, ctx, audioDL.GUID, models.StateImportExternal)
	assertStatus(t, downloads, ctx, ebookDL.GUID, models.StateImportExternal)
}

// Loose tracks at the shared root have no folder of their own, so the hold
// records the tracks themselves and the release places them one by one.
func TestPairGating_Transmission_LooseTracksHeldAsFileList(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	kv := gatingSettings()
	kv["import.drop_folder"] = dropDir
	setPairSettings(t, settings, ctx, kv)

	shared := t.TempDir()
	writeHeldFiles(t, shared, map[string]string{
		"a.m4b":              "a",
		"b.m4b":              "b",
		"Other Audio/01.m4b": "o1",
		"unrelated.epub":     "unrelated epub",
	})
	tracks := []string{filepath.Join(shared, "a.m4b"), filepath.Join(shared, "b.m4b")}

	audioDL := newPairDownload(t, downloads, book, "audiobook", ctx)
	s.tryImportInternal(ctx, audioDL, shared, "transmission", "", "", nil, tracks)
	assertStatus(t, downloads, ctx, audioDL.GUID, models.StateImportHeld)
	held, _ := downloads.GetByGUID(ctx, audioDL.GUID)
	if got := held.HandedOffSources(); len(got) != 2 {
		t.Fatalf("held sources = %v, want the two tracks", got)
	}

	ebookDL := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(ctx, ebookDL, writeEbookDownload(t), "", "", "", nil, nil)

	want := []string{"Title T - Author A.epub", "Title T - Author A/a.m4b", "Title T - Author A/b.m4b"}
	if got := dropListing(t, dropDir); !equalStrings(got, want) {
		t.Fatalf("drop folder = %v, want %v", got, want)
	}
}

// A row held before this change recorded a folder. It still releases from the
// folder, so a hold in flight survives the upgrade.
func TestPairGating_LegacyFolderHoldStillReleases(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	kv := gatingSettings()
	kv["import.drop_folder"] = dropDir
	setPairSettings(t, settings, ctx, kv)

	ebookDir := writeEbookDownload(t)
	legacy := newPairDownload(t, downloads, book, "ebook", ctx)
	if err := downloads.SetImportPath(ctx, legacy.ID, ebookDir); err != nil {
		t.Fatal(err)
	}
	s.updateDownloadStatus(ctx, legacy.ID, models.StateImporting)
	s.updateDownloadStatus(ctx, legacy.ID, models.StateImportPending)
	s.updateDownloadStatus(ctx, legacy.ID, models.StateImportHeld)
	assertStatus(t, downloads, ctx, legacy.GUID, models.StateImportHeld)

	audioDL := newPairDownload(t, downloads, book, "audiobook", ctx)
	s.tryImportInternal(ctx, audioDL, writeAudiobookDownload(t), "", "", "", nil, nil)

	want := []string{"Title T - Author A.epub", "Title T - Author A/audiobook.m4b"}
	if got := dropListing(t, dropDir); !equalStrings(got, want) {
		t.Fatalf("drop folder = %v, want %v", got, want)
	}
	assertStatus(t, downloads, ctx, legacy.GUID, models.StateImportExternal)
}

// The timeout drops the held format alone, and only its own files.
func TestPairGating_TimeoutReleasesOnlyOwnFiles(t *testing.T) {
	s, settings, downloads, _, database, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	kv := gatingSettings()
	kv["import.drop_folder"] = dropDir
	setPairSettings(t, settings, ctx, kv)

	shared := t.TempDir()
	writeHeldFiles(t, shared, map[string]string{
		"book.epub":             "own epub",
		"unrelated.epub":        "unrelated epub",
		"Unrelated Audio/1.mp3": "unrelated track",
	})
	own := filepath.Join(shared, "book.epub")
	dl := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(ctx, dl, shared, "transmission", "", "", nil, []string{own})
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportHeld)

	old := time.Now().Add(-100 * time.Hour).UTC()
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET completed_at=? WHERE id=?", old, dl.ID); err != nil {
		t.Fatal(err)
	}
	s.sweepHeldPairGating(ctx)

	want := []string{"Title T - Author A.epub"}
	if got := dropListing(t, dropDir); !equalStrings(got, want) {
		t.Fatalf("drop folder = %v, want %v", got, want)
	}
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
}

// A download with a folder of its own (SABnzbd, or a qBittorrent torrent with a
// root folder) holds that folder, as before.
func TestPairGating_DedicatedFolderRecordsFolder(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	kv := gatingSettings()
	kv["import.drop_folder"] = dropDir
	setPairSettings(t, settings, ctx, kv)

	sab := writeEbookDownload(t)
	sabDL := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(ctx, sabDL, sab, "", "", "", nil, nil)
	got, _ := downloads.GetByGUID(ctx, sabDL.GUID)
	if got.ImportPath != sab {
		t.Errorf("usenet held import_path = %q, want folder %q", got.ImportPath, sab)
	}

}

func TestPairGating_QbittorrentOwnFolderRecordsFolder(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	kv := gatingSettings()
	kv["import.drop_folder"] = dropDir
	setPairSettings(t, settings, ctx, kv)

	qbitDir := writeEbookDownload(t)
	dl := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, qbitDir, "qbittorrent", "", "", nil, []string{filepath.Join(qbitDir, "book.epub")})
	got, _ := downloads.GetByGUID(ctx, dl.GUID)
	if got.ImportPath != qbitDir {
		t.Errorf("qbittorrent held import_path = %q, want folder %q", got.ImportPath, qbitDir)
	}
}
