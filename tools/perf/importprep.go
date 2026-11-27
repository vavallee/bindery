package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

const importCategory = "bindery-perf"

// stageDownloads writes one finished download per book of an untracked seed
// into dir, each in its own folder the way a torrent client lays them out, and
// returns the torrent list the fake client reports for them. Book i of a seed
// has id i+1 because the seeder inserts in order into an empty table.
func stageDownloads(spec SeedSpec, dir string) ([]FakeTorrent, error) {
	_, plans := planBooks(spec)
	out := make([]FakeTorrent, 0, len(plans))
	for i, p := range plans {
		if !p.onDisk {
			continue
		}
		release := p.author + " - " + p.title
		file := release + filepath.Ext(p.path)
		full := filepath.Join(dir, release, file)
		if err := writeBookFile(full, p.title, p.author, p.audio); err != nil {
			return nil, err
		}
		fi, err := os.Stat(full)
		if err != nil {
			return nil, err
		}
		out = append(out, FakeTorrent{
			Hash: fmt.Sprintf("%040x", i+1), Name: release, ContentPath: filepath.Join(dir, release),
			File: release + "/" + file, Size: fi.Size(), BookID: int64(i + 1), Audio: p.audio,
		})
	}
	return out, nil
}

// prepareImport adds the fake download client and one downloading row per
// torrent to a database copy before the binary boots on it. This is the state
// a grab leaves behind; the grab itself is not part of the import scenario.
func prepareImport(ctx context.Context, dbPath string, clientPort int, torrents []FakeTorrent) error {
	database, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	client := models.DownloadClient{
		Name: "perf-qbittorrent", Type: "qbittorrent", Host: "127.0.0.1", Port: clientPort, Username: "perf", Password: "perf",
		Category: importCategory, CategoryAudiobook: importCategory, Priority: 1, Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true,
	}
	if err := db.NewDownloadClientRepo(database).Create(ctx, &client); err != nil {
		return err
	}
	downloads := db.NewDownloadRepo(database)
	for i := range torrents {
		t := &torrents[i]
		mediaType := models.MediaTypeEbook
		if t.Audio {
			mediaType = models.MediaTypeAudiobook
		}
		d := models.Download{
			GUID: "perf-import-" + t.Hash, BookID: &t.BookID, DownloadClientID: &client.ID, Title: t.Name, Size: t.Size,
			TorrentID: &t.Hash, Status: models.StateDownloading, Protocol: "torrent", Quality: "epub", MediaType: mediaType,
		}
		if err := downloads.Create(ctx, &d); err != nil {
			return fmt.Errorf("download for book %d: %w", t.BookID, err)
		}
	}
	_, err = database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}
