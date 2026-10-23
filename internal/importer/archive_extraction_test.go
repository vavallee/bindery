package importer

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A release packed as a RAR set waits for an unpacker instead of spending its
// import retries, and once it is extracted the archive stays out of the
// library (#3203).

// rarRelease lays out a torrent folder holding a RAR set and an .nfo, as a
// scene audiobook release ships, and returns the folder and its file names.
func rarRelease(t *testing.T) (string, []string) {
	t.Helper()
	folder := filepath.Join(t.TempDir(), "Fjellvinden")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	names := []string{"Fjellvinden.part1.rar", "Fjellvinden.part2.rar", "Fjellvinden.nfo"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return folder, names
}

func TestIsArchivePart(t *testing.T) {
	for name, want := range map[string]bool{
		"Fjellvinden.rar": true, "Fjellvinden.part1.rar": true, "Fjellvinden.part12.RAR": true,
		"Fjellvinden.r00": true, "Fjellvinden.r99": true, "Fjellvinden.s00": true,
		// Book files and sidecars, including the RAR-based comic format.
		"Fjellvinden.cbr": false, "Fjellvinden.mp3": false, "Fjellvinden.epub": false, "Fjellvinden.nfo": false,
		"Fjellvinden.r": false, "Fjellvinden.r100": false, "Fjellvinden.rtf": false, "rar": false,
	} {
		if got := isArchivePart(name); got != want {
			t.Errorf("isArchivePart(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestArchiveAwaitingExtraction(t *testing.T) {
	folder, _ := rarRelease(t)
	if got := archiveAwaitingExtraction(folder); filepath.Base(got) != "Fjellvinden.part1.rar" {
		t.Errorf("archive-only folder: got %q, want its first volume", got)
	}

	// Extracted: a book file next to the archive means there is something to
	// import.
	if err := os.WriteFile(filepath.Join(folder, "01 Spor 1.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := archiveAwaitingExtraction(folder); got != "" {
		t.Errorf("extracted folder: got %q, want none", got)
	}

	// The RAR 4 naming: the first volume is x.rar, although x.r00 sorts first.
	old := filepath.Join(t.TempDir(), "Havgapet")
	if err := os.MkdirAll(old, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Havgapet.r00", "Havgapet.r01", "Havgapet.rar"} {
		if err := os.WriteFile(filepath.Join(old, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := archiveAwaitingExtraction(old); filepath.Base(got) != "Havgapet.rar" {
		t.Errorf("RAR 4 set: got %q, want Havgapet.rar", got)
	}

	// No archive at all, and a folder that does not exist.
	if got := archiveAwaitingExtraction(t.TempDir()); got != "" {
		t.Errorf("empty folder: got %q, want none", got)
	}
	if got := archiveAwaitingExtraction(filepath.Join(t.TempDir(), "gone")); got != "" {
		t.Errorf("missing folder: got %q, want none", got)
	}
}

// A notes file beside the volumes is not the extracted book, so the folder
// still waits for an unpacker (#3232). An ebook of a plausible size does end
// the wait, and so does a small audio track, since only ebook formats are
// judged by size.
func TestArchiveAwaitingExtraction_IgnoresTinyEbookFiles(t *testing.T) {
	small := []byte("visit www.example-site.com")
	big := make([]byte, MinPlausibleEbookBytes)
	for _, tc := range []struct {
		name    string
		file    string
		content []byte
		waits   bool
	}{
		{"tiny txt notes file", "www.example-site.com.txt", small, true},
		{"tiny extracted epub", "Fjellvinden.epub", small, true},
		{"epub at the size floor", "Fjellvinden.epub", big, false},
		{"tiny audio track", "01 Spor 1.mp3", small, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder, _ := rarRelease(t)
			if err := os.WriteFile(filepath.Join(folder, tc.file), tc.content, 0o644); err != nil {
				t.Fatal(err)
			}
			got := archiveAwaitingExtraction(folder)
			if tc.waits && filepath.Base(got) != "Fjellvinden.part1.rar" {
				t.Errorf("got %q, want the first volume", got)
			}
			if !tc.waits && got != "" {
				t.Errorf("got %q, want none", got)
			}
		})
	}
}

// Counted as present, an archive-only folder spent a retry on every poll.
func TestImportSourcePresent_WaitsForArchiveExtraction(t *testing.T) {
	folder, _ := rarRelease(t)
	if importSourcePresent(folder, nil) {
		t.Error("an archive-only folder counts as present, so each poll spends a retry")
	}
	if err := os.WriteFile(filepath.Join(folder, "01 Spor 1.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !importSourcePresent(folder, nil) {
		t.Error("once extracted, the folder must count as present")
	}
}

// After the wait the download is blocked with a reason that names the archive
// and says what to do, not the "files moved or PathRemap" one.
func TestImportSourceGoneReason_NamesTheArchive(t *testing.T) {
	folder, _ := rarRelease(t)
	reason := importSourceGoneReason(folder)
	for _, want := range []string{"Fjellvinden.part1.rar", "does not unpack archives", "unpackerr"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention %q", reason, want)
		}
	}
	if gone := importSourceGoneReason(t.TempDir()); strings.Contains(gone, "archive") {
		t.Errorf("a folder without an archive got the archive reason: %q", gone)
	}
}

// End to end through the qBittorrent poller, as the issue reproduced it: the
// first attempt says it is waiting for the archive, the polls after it spend
// no retry, and once the tracks are extracted into the torrent's folder they
// import, without the archive volumes.
func TestQbittorrentRARRelease_WaitsThenImportsWithoutTheArchive(t *testing.T) {
	s, book, dlRepo, _, ctx := multiDiscFixture(t, "hardlink")
	folder, names := rarRelease(t)

	const hash = "0ddba11e0ddba11e0ddba11e0ddba11e0ddba11e"
	var files []map[string]any
	for _, name := range names {
		files = append(files, map[string]any{"name": "Fjellvinden/" + name, "size": len(name)})
	}
	srv := httptest.NewServer(qbittorrentMatrixHandler(t, map[string]any{
		"hash": hash, "name": "Fjellvinden", "progress": 1.0, "state": "uploading",
		"category": "audiobooks", "amount_left": 0, "save_path": filepath.Dir(folder), "content_path": folder,
	}, files))
	t.Cleanup(srv.Close)
	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: host, Port: port,
		Username: "admin", Password: "secret", Category: "audiobooks", Enabled: true}
	if err := s.clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	torrentHash := hash
	dl := &models.Download{GUID: "guid-fjellvinden", Title: "Fjellvinden", NZBURL: "magnet:?xt=urn:btih:" + hash,
		Status: models.StateDownloading, Protocol: "torrent", TorrentID: &torrentHash,
		DownloadClientID: &client.ID, BookID: book.BookID}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	poll := func() *models.Download {
		t.Helper()
		s.checkQbittorrentDownloads(ctx, client)
		got, err := dlRepo.GetByID(ctx, dl.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := poll()
	if got.Status != models.StateImportFailed || !strings.Contains(got.ErrorMessage, "waiting for Fjellvinden.part1.rar to be extracted") {
		t.Fatalf("first attempt: status %q, message %q; want importFailed waiting for the archive", got.Status, got.ErrorMessage)
	}
	// Before #3203 these spent the five retries and blocked the download.
	for i := range importRetryLimit + 1 {
		if got = poll(); got.Status != models.StateImportFailed || got.ImportRetryCount != 0 {
			t.Fatalf("poll %d while waiting: status %q, retries %d; want importFailed with no retry spent", i+1, got.Status, got.ImportRetryCount)
		}
	}

	// unpackerr with move_back = true: the tracks land in the torrent's own
	// folder, and the archive stays for seeding.
	for _, track := range []string{"01 Spor 1.mp3", "02 Spor 2.mp3"} {
		if err := os.WriteFile(filepath.Join(folder, track), []byte(track), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got = poll(); got.Status != models.StateImported {
		t.Fatalf("after extraction: status %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	placed := relFiles(t, importedAudiobookDir(t, s))
	for _, want := range []string{"01 Spor 1.mp3", "02 Spor 2.mp3", "Fjellvinden.nfo"} {
		if _, ok := placed[want]; !ok {
			t.Errorf("%s not placed (placed: %v)", want, placed)
		}
	}
	for name := range placed {
		if isArchivePart(name) {
			t.Errorf("archive volume %s placed in the library", name)
		}
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Errorf("%s gone from the torrent folder: %v", name, err)
		}
	}
}

// In move mode on one filesystem the torrent folder is renamed into the
// library wholesale, and the sweep that follows is there for the receipt and
// repair clutter. It must leave archive volumes alone: the move already took
// the folder from the client, and deleting the volumes would destroy the
// archive for good.
func TestMoveMode_SweepKeepsArchiveVolumes(t *testing.T) {
	s, dl, dlRepo, _, ctx := multiDiscFixture(t, "move")
	folder := filepath.Join(t.TempDir(), "Fjellvinden")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	var audio []string
	for _, name := range []string{
		"01 Spor 01.mp3", "02 Spor 02.mp3", "Fjellvinden.part1.rar", "Fjellvinden.r00", "Fjellvinden.nzb", "Fjellvinden.vol01+02.par2",
	} {
		p := filepath.Join(folder, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(name) == ".mp3" {
			audio = append(audio, p)
		}
	}
	sort.Strings(audio)

	s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, folder, "", "", "", nil, audio)

	if got, _ := dlRepo.GetByID(ctx, dl.ID); got.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	placed := relFiles(t, importedAudiobookDir(t, s))
	for _, want := range []string{"01 Spor 01.mp3", "Fjellvinden.part1.rar", "Fjellvinden.r00"} {
		if _, ok := placed[want]; !ok {
			t.Errorf("%s missing after the move (placed: %v)", want, placed)
		}
	}
	for _, clutter := range []string{"Fjellvinden.nzb", "Fjellvinden.vol01+02.par2"} {
		if _, ok := placed[clutter]; ok {
			t.Errorf("%s was not swept from the moved folder", clutter)
		}
	}
}

// archiveMoveSource builds a torrent folder holding a track, a RAR set and
// receipt clutter, one level down as well as at the top.
func archiveMoveSource(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "Fjellvinden")
	if err := os.MkdirAll(filepath.Join(src, "extra"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"01 Spor 01.mp3", "Fjellvinden.part1.rar", "Fjellvinden.r00", "Fjellvinden.nzb",
		"extra/Fjellvinden.s00", "extra/Fjellvinden.vol01+02.par2",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func checkArchiveMoved(t *testing.T, src, dst string) {
	t.Helper()
	placed := relFiles(t, dst)
	for _, want := range []string{"01 Spor 01.mp3", "Fjellvinden.part1.rar", "Fjellvinden.r00", "extra/Fjellvinden.s00"} {
		if _, ok := placed[want]; !ok {
			t.Errorf("%s did not arrive (placed: %v)", want, placed)
		}
	}
	for _, clutter := range []string{"Fjellvinden.nzb", "extra/Fjellvinden.vol01+02.par2"} {
		if _, ok := placed[clutter]; ok {
			t.Errorf("%s was moved into the library", clutter)
		}
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source folder still there after a clean move (err=%v)", err)
	}
}

// The copy fallback of a move (a different filesystem, forced here by turning
// the rename off) deletes the source once the copy is done, so it must carry
// the archive volumes along rather than skip them like a copy-mode import.
func TestMoveDir_CopyFallbackKeepsArchiveVolumes(t *testing.T) {
	src := archiveMoveSource(t)
	dst := filepath.Join(t.TempDir(), "lib", "Fjellvinden")
	if err := moveDir(context.Background(), src, dst, false); err != nil {
		t.Fatal(err)
	}
	checkArchiveMoved(t, src, dst)
}

// Same for a move that merges into an existing folder.
func TestMoveDirMerge_KeepsArchiveVolumes(t *testing.T) {
	src := archiveMoveSource(t)
	dst := filepath.Join(t.TempDir(), "lib", "Fjellvinden")
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	skipped, err := MoveDirMergeCtx(context.Background(), src, dst)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("merge: skipped %v, err %v", skipped, err)
	}
	checkArchiveMoved(t, src, dst)
}

// Copy mode leaves the source alone, so the archive volumes stay out of the
// library copy and the client keeps them (#3203).
func TestCopyDir_StillSkipsArchiveVolumes(t *testing.T) {
	src := archiveMoveSource(t)
	dst := filepath.Join(t.TempDir(), "lib", "Fjellvinden")
	if err := CopyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	placed := relFiles(t, dst)
	if len(placed) != 1 || placed["01 Spor 01.mp3"] == "" {
		t.Errorf("copy placed %v, want only the track", placed)
	}
}

// A flatten (multi-disc, or the per-file naming template) in move mode copies
// the tracks and root sidecars, then deletes the source folder. It must carry
// the archive volumes along, or the delete destroys them; copy mode still
// leaves them with the client.
func TestFlatten_MoveModeKeepsArchiveVolumes(t *testing.T) {
	for _, tc := range []struct {
		name, mode, key, value string
		wantArchive            bool
	}{
		{"multi-disc move", "move", "import.audiobook.flatten_multi_disc", "true", true},
		{"naming template move", "move", "naming.audiobook_file_template", "{Title} - Part {Part:3}.{ext}", true},
		{"multi-disc copy", "copy", "import.audiobook.flatten_multi_disc", "true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dl, dlRepo, settings, ctx := multiDiscFixture(t, tc.mode)
			if err := settings.Set(ctx, tc.key, tc.value); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(t.TempDir(), "Fjellvinden")
			var audio []string
			for _, name := range []string{
				"CD1/01 Spor 01.mp3", "CD2/01 Spor 01.mp3", "Fjellvinden.part1.rar", "Fjellvinden.r00", "Fjellvinden.nzb",
			} {
				p := filepath.Join(src, name)
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

			s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, src, "", "", "", nil, audio)

			if got, _ := dlRepo.GetByID(ctx, dl.ID); got.Status != models.StateImported {
				t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}
			placed := relFiles(t, importedAudiobookDir(t, s))
			for _, vol := range []string{"Fjellvinden.part1.rar", "Fjellvinden.r00"} {
				_, inLibrary := placed[vol]
				_, srcErr := os.Stat(filepath.Join(src, vol))
				if tc.wantArchive && !inLibrary {
					t.Errorf("%s lost: not carried into the library (placed: %v) before the source was deleted", vol, placed)
				}
				if !tc.wantArchive && (inLibrary || srcErr != nil) {
					t.Errorf("%s: in library %v, source err %v; copy mode leaves it with the client", vol, inLibrary, srcErr)
				}
			}
			if _, ok := placed["Fjellvinden.nzb"]; ok {
				t.Error("the .nzb receipt was carried into the library")
			}
		})
	}
}

// A move whose copy fallback fails part-way, here on an unreadable file in a
// subfolder, must leave the source whole, archive volumes included: the
// source is only removed once the copy has completed.
func TestMoveDir_FailedCopyKeepsTheSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	src := archiveMoveSource(t)
	locked := filepath.Join(src, "extra", "Fjellvinden.s00")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })
	dst := filepath.Join(t.TempDir(), "lib", "Fjellvinden")

	if err := moveDir(context.Background(), src, dst, false); err == nil {
		t.Fatal("the move succeeded although a file could not be read")
	}
	for _, name := range []string{"01 Spor 01.mp3", "Fjellvinden.part1.rar", "Fjellvinden.r00", "extra/Fjellvinden.s00"} {
		if _, err := os.Lstat(filepath.Join(src, name)); err != nil {
			t.Errorf("%s gone from the source after a failed move: %v", name, err)
		}
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a half-copied destination was left behind (err=%v)", err)
	}
}

// A move-mode flatten deletes its source once the copy is done. A root level
// archive volume that cannot be read must therefore fail the flatten and leave
// the source whole, instead of being swallowed like an unreadable cover and
// destroyed with the folder. Copy mode keeps the source anyway, never carries
// the volume, and so is not affected.
func TestFlatten_UnreadableArchiveVolumeKeepsTheSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	for _, tc := range []struct {
		name, mode, key, value string
		wantFailure            bool
	}{
		{"multi-disc move", "move", "import.audiobook.flatten_multi_disc", "true", true},
		{"naming template move", "move", "naming.audiobook_file_template", "{Title} - Part {Part:3}.{ext}", true},
		{"multi-disc copy", "copy", "import.audiobook.flatten_multi_disc", "true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dl, dlRepo, settings, ctx := multiDiscFixture(t, tc.mode)
			if err := settings.Set(ctx, tc.key, tc.value); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(t.TempDir(), "Fjellvinden")
			var audio []string
			for _, name := range []string{"CD1/01 Spor 01.mp3", "CD2/01 Spor 01.mp3", "Fjellvinden.part1.rar", "cover.jpg"} {
				p := filepath.Join(src, name)
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
			locked := filepath.Join(src, "Fjellvinden.part1.rar")
			if err := os.Chmod(locked, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

			s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, src, "", "", "", nil, audio)

			got, _ := dlRepo.GetByID(ctx, dl.ID)
			if tc.wantFailure {
				if got.Status == models.StateImported {
					t.Errorf("imported although the archive volume could not be carried")
				}
				for _, name := range []string{"CD1/01 Spor 01.mp3", "CD2/01 Spor 01.mp3", "Fjellvinden.part1.rar", "cover.jpg"} {
					if _, err := os.Lstat(filepath.Join(src, name)); err != nil {
						t.Errorf("%s gone from the source after a failed move flatten: %v", name, err)
					}
				}
				return
			}
			if got.Status != models.StateImported {
				t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}
			if _, err := os.Lstat(locked); err != nil {
				t.Errorf("copy mode touched the volume in the source: %v", err)
			}
		})
	}
}
