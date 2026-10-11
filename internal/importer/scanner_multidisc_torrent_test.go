package importer

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/models"
)

// A multi-disc audiobook torrent ("Fjellvinden/Fjellvinden CD1/01 Spor
// 01.mp3", "…/Fjellvinden CD2/01 Spor 01.mp3") from qBittorrent, Deluge or
// rTorrent was blocked with "two files in this download would be placed at
// the same path". Those clients report the torrent's own folder, and the
// importer took a common directory equal to it for the client's shared save
// path (#903), so it placed each file by its base name. The same download
// from Transmission imported with its disc folders kept.

// multiDiscTorrent builds the reported shape under its own torrent folder and
// returns the folder and the client's file list, CD2 first as a client may
// list them.
func multiDiscTorrent(t *testing.T) (folder string, files []string) {
	t.Helper()
	return multiDiscTorrentNamed(t, "Fjellvinden CD")
}

// multiDiscTorrentNamed is multiDiscTorrent with the disc folders named
// discPrefix + number, e.g. "Fjellvinden_Cd 0" for "Fjellvinden_Cd 01".
func multiDiscTorrentNamed(t *testing.T, discPrefix string) (folder string, files []string) {
	t.Helper()
	folder = filepath.Join(t.TempDir(), "Fjellvinden")
	for _, f := range []struct{ rel, body string }{
		{discPrefix + "2/01 Spor 01.mp3", "disc two track one"},
		{discPrefix + "2/02 Spor 02.mp3", "disc two track two"},
		{discPrefix + "1/01 Spor 01.mp3", "disc one track one"},
		{discPrefix + "1/02 Spor 02.mp3", "disc one track two"},
	} {
		p := filepath.Join(folder, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	return folder, files
}

func multiDiscFixture(t *testing.T, mode string) (*Scanner, *models.Download, *db.DownloadRepo, *db.SettingsRepo, context.Context) {
	t.Helper()
	s, dl, dlRepo, bookRepo, ctx := dataLossFixture(t, t.TempDir(), mode)
	book, err := bookRepo.GetByID(ctx, *dl.BookID)
	if err != nil {
		t.Fatal(err)
	}
	book.MediaType = models.MediaTypeAudiobook
	if err := bookRepo.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	if s.settings == nil {
		t.Fatal("scanner has no settings repo")
	}
	return s, dl, dlRepo, s.settings, ctx
}

// relFiles lists the files under dir, relative to it, with their contents.
func relFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		body, _ := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	return out
}

func TestMultiDiscTorrent_ImportsWithDiscFoldersKept(t *testing.T) {
	for _, mode := range []string{"hardlink", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			s, dl, dlRepo, _, ctx := multiDiscFixture(t, mode)
			folder, files := multiDiscTorrent(t)

			s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, folder, "", "", "", nil, files)

			got, err := dlRepo.GetByID(ctx, dl.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != models.StateImported {
				t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}
			placed := relFiles(t, importedAudiobookDir(t, s))
			for rel, body := range map[string]string{
				"Fjellvinden CD1/01 Spor 01.mp3": "disc one track one",
				"Fjellvinden CD1/02 Spor 02.mp3": "disc one track two",
				"Fjellvinden CD2/01 Spor 01.mp3": "disc two track one",
				"Fjellvinden CD2/02 Spor 02.mp3": "disc two track two",
			} {
				if placed[rel] != body {
					t.Errorf("%s = %q, want %q (placed: %v)", rel, placed[rel], body, placed)
				}
			}
		})
	}
}

// With multi-disc flattening on, and with a per-track template, the tracks
// are numbered across discs in disc order.
func TestMultiDiscTorrent_FlattensAcrossDiscsInOrder(t *testing.T) {
	for name, setting := range map[string][2]string{
		"flatten setting":    {"import.audiobook.flatten_multi_disc", "true"},
		"per-track template": {"naming.audiobook_file_template", "{Title} - Part {Part:3}.{ext}"},
	} {
		// "Title_Cd 01" is a common release layout; \b did not see a disc
		// there, so tracks interleaved across the discs.
		for _, discPrefix := range []string{"Fjellvinden CD", "Fjellvinden_Cd 0"} {
			t.Run(name+"/"+discPrefix, func(t *testing.T) { flattensInOrder(t, setting, discPrefix) })
		}
	}
}

func flattensInOrder(t *testing.T, setting [2]string, discPrefix string) {
	{
		{
			s, dl, dlRepo, settings, ctx := multiDiscFixture(t, "copy")
			if err := settings.Set(ctx, setting[0], setting[1]); err != nil {
				t.Fatal(err)
			}
			folder, files := multiDiscTorrentNamed(t, discPrefix)

			s.tryImportInternal(withTorrentOwnFolder(ctx, true), dl, folder, "", "", "", nil, files)

			if got, _ := dlRepo.GetByID(ctx, dl.ID); got.Status != models.StateImported {
				t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}
			placed := relFiles(t, importedAudiobookDir(t, s))
			names := make([]string, 0, len(placed))
			for rel := range placed {
				names = append(names, rel)
			}
			sort.Strings(names)
			want := []string{"disc one track one", "disc one track two", "disc two track one", "disc two track two"}
			if len(names) != len(want) {
				t.Fatalf("placed %v, want %d flat tracks", names, len(want))
			}
			for i, n := range names {
				if filepath.Dir(n) != "." {
					t.Errorf("%s is not flat", n)
				}
				if placed[n] != want[i] {
					t.Errorf("track %d (%s) = %q, want %q: numbering must run across discs in disc order", i+1, n, placed[n], want[i])
				}
			}
		}
	}
}

// A download blocked by the collision imports once Retry import resets it:
// the retry goes through the same poller path, which now marks the torrent's
// own folder. Nothing was written by the blocked attempt.
func TestMultiDiscTorrent_RetryImportSucceedsAfterBlock(t *testing.T) {
	s, dl, dlRepo, _, ctx := multiDiscFixture(t, "hardlink")
	folder, files := multiDiscTorrent(t)

	// What happened before: the folder taken for a shared save path.
	s.tryImportInternal(ctx, dl, folder, "", "", "", nil, files)
	got, err := dlRepo.GetByID(ctx, dl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want the old collision block to set up the retry", got.Status)
	}

	if accepted, _, err := dlRepo.ResetImportRetry(ctx, dl.ID); err != nil || !accepted {
		t.Fatalf("ResetImportRetry = %v, %v", accepted, err)
	}
	got, _ = dlRepo.GetByID(ctx, dl.ID)
	s.tryImportInternal(withTorrentOwnFolder(ctx, true), got, folder, "", "", "", nil, files)

	if got, _ := dlRepo.GetByID(ctx, dl.ID); got.Status != models.StateImported {
		t.Fatalf("status after retry = %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	if n := len(relFiles(t, importedAudiobookDir(t, s))); n != 4 {
		t.Errorf("placed %d files after retry, want 4", n)
	}
}

func TestResolveAudiobookSource_OwnFolderIsPlacedWhole(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	folder := "/data/downloads/Fjellvinden"
	files := []string{folder + "/Fjellvinden CD1/01 Spor 01.mp3", folder + "/Fjellvinden CD2/01 Spor 01.mp3"}
	if got, perFile := s.resolveAudiobookSource(folder, files, true); perFile || got != folder {
		t.Errorf("own folder: got (%q, perFile=%v), want the folder placed whole", got, perFile)
	}
	// The shared save path keeps per-file placement (#903).
	if _, perFile := s.resolveAudiobookSource(folder, files, false); !perFile {
		t.Error("shared save path: want per-file placement")
	}
}

func TestQbitHasOwnFolder(t *testing.T) {
	for _, tc := range []struct {
		content, save string
		want          bool
	}{
		{"/downloads/Fjellvinden", "/downloads", true},
		{"/downloads/Fjellvinden/", "/downloads/", true},
		{`D:\torrents\Fjellvinden`, `D:\torrents\`, true},
		// A torrent added without a root folder reports its save path.
		{"/downloads", "/downloads/", false},
		{`D:\torrents`, `D:\torrents\`, false},
		// No content path: the poller falls back to save path + name.
		{"", "/downloads", true},
	} {
		if got := qbitHasOwnFolder(qbittorrent.Torrent{ContentPath: tc.content, SavePath: tc.save}); got != tc.want {
			t.Errorf("qbitHasOwnFolder(%q, %q) = %v, want %v", tc.content, tc.save, got, tc.want)
		}
	}
}

// End to end through the qBittorrent poller: a multi-disc torrent finishing
// downloading, and one where Retry import leaves a blocked download
// (importFailed with no retries spent), both import with the disc folders kept.
func TestCheckQbittorrentDownloads_ImportsMultiDiscTorrent(t *testing.T) {
	for _, state := range []models.DownloadState{models.StateDownloading, models.StateImportFailed} {
		t.Run(string(state), func(t *testing.T) { checkQbitMultiDisc(t, state) })
	}
}

func checkQbitMultiDisc(t *testing.T, state models.DownloadState) {
	s, dl, dlRepo, _, ctx := multiDiscFixture(t, "hardlink")
	folder, _ := multiDiscTorrent(t)
	savePath := filepath.Dir(folder)

	const hash = "0ddba11e0ddba11e0ddba11e0ddba11e0ddba11e"
	var files []map[string]any
	for _, rel := range []string{
		"Fjellvinden/Fjellvinden CD1/01 Spor 01.mp3", "Fjellvinden/Fjellvinden CD1/02 Spor 02.mp3",
		"Fjellvinden/Fjellvinden CD2/01 Spor 01.mp3", "Fjellvinden/Fjellvinden CD2/02 Spor 02.mp3",
	} {
		files = append(files, map[string]any{"name": rel, "size": 18})
	}
	srv := httptest.NewServer(qbittorrentMatrixHandler(t, map[string]any{
		"hash": hash, "name": "Fjellvinden", "progress": 1.0, "state": "uploading",
		"category": "audiobooks", "amount_left": 0, "save_path": savePath, "content_path": folder,
	}, files))
	t.Cleanup(srv.Close)

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: host, Port: port,
		Username: "admin", Password: "secret", Category: "audiobooks", Enabled: true}
	if err := s.clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	torrentHash := hash
	dl = &models.Download{GUID: "guid-fjellvinden", Title: "Fjellvinden", NZBURL: "magnet:?xt=urn:btih:" + hash,
		Status: state, Protocol: "torrent", TorrentID: &torrentHash,
		DownloadClientID: &client.ID, BookID: dl.BookID}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByID(ctx, dl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	placed := relFiles(t, importedAudiobookDir(t, s))
	if placed["Fjellvinden CD1/01 Spor 01.mp3"] != "disc one track one" || placed["Fjellvinden CD2/01 Spor 01.mp3"] != "disc two track one" {
		t.Errorf("placed %v, want both discs' tracks under their disc folders", placed)
	}
}
