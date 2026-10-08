package downloader

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// ClientJobLister finds the download rows that name one job in one download
// client daemon, across every client entry configured against that daemon. *db.DownloadRepo satisfies it; the interface keeps this package free
// of the db import.
type ClientJobLister interface {
	ListByClientTorrentID(ctx context.Context, client *models.DownloadClient, torrentID string) ([]models.Download, error)
	ListByClientNzoID(ctx context.Context, client *models.DownloadClient, nzoID string) ([]models.Download, error)
}

// clientJobRef is the identifier RemoveDownload hands the client for dl, and
// whether it is a torrent id (torrent_id) rather than a usenet job id
// (sabnzbd_nzo_id). The split mirrors RemoveDownload's switch exactly: the two
// must agree, or the sharing check would look at a different field from the
// one the removal acts on.
func clientJobRef(client *models.DownloadClient, dl *models.Download) (ref string, torrent bool) {
	switch client.Type {
	case "transmission", "qbittorrent", "deluge", "rtorrent":
		if dl.TorrentID != nil {
			ref = strings.TrimSpace(*dl.TorrentID)
		}
		return ref, true
	default: // "nzbget", "sabnzbd"
		if dl.SABnzbdNzoID != nil {
			ref = strings.TrimSpace(*dl.SABnzbdNzoID)
		}
		return ref, false
	}
}

// stillUsesClientJob reports whether other, a download row naming the same
// client job as the one being removed, still depends on that job staying in
// the client.
//
// There is no "removed" download state: removing a queue item deletes its row,
// so any row that still exists is a candidate. Two states are not users:
//
//   - failed: the attempt is over and nothing will contact the client for it
//     again. The pollers skip failed rows, so if a failed row held the job, the
//     job would never be removed by anyone. The stall handler relies on this:
//     it marks each stalled row failed after asking for removal, so the last
//     row of a shared stalled torrent is the one that removes it.
//   - imported, when the client is set to remove on import: the user has said
//     an imported download has no further claim on the client, and the import
//     of each row asks for the removal, so counting imported rows here would
//     leave the torrent behind for good once every row has imported.
//
// Every other state still needs the job. In flight and importing rows need the
// payload to import; importFailed, importBlocked, importExternal and importHeld
// rows keep their files in the download directory for a retry, a manual match
// or an external hand off; and an imported row on a client that keeps imported
// torrents is still seeding for its owner.
func stillUsesClientJob(other *models.Download, client *models.DownloadClient) bool {
	switch other.Status {
	case models.StateFailed:
		return false
	case models.StateImported:
		return !client.RemoveOnImport
	default:
		return true
	}
}

// OtherDownloadUsingClientJob returns another download row that still uses
// the client job dl refers to, or nil when nothing else does.
//
// A torrent client adopts a torrent it already holds when a grab sends the
// same info hash again (the duplicate add path), so two grabs of one release
// from two indexers, under two GUIDs or by two users end up as two rows naming
// one torrent. Removing that torrent for one row removes it, and with
// deleteFiles its data, for the other row as well. Every path that removes a
// download from its client asks this first.
//
// An error from the lister is returned as is; callers treat it as "in use",
// since leaving a job in the client is recoverable and removing one another
// row needs is not.
func OtherDownloadUsingClientJob(ctx context.Context, lister ClientJobLister, client *models.DownloadClient, dl *models.Download) (*models.Download, error) {
	if lister == nil || client == nil || dl == nil {
		return nil, nil
	}
	ref, torrent := clientJobRef(client, dl)
	if ref == "" {
		return nil, nil
	}
	var (
		rows []models.Download
		err  error
	)
	if torrent {
		rows, err = lister.ListByClientTorrentID(ctx, client, ref)
	} else {
		rows, err = lister.ListByClientNzoID(ctx, client, ref)
	}
	if err != nil {
		return nil, fmt.Errorf("look up other downloads using %s: %w", ref, err)
	}
	for i := range rows {
		other := &rows[i]
		if other.ID == dl.ID {
			continue
		}
		if stillUsesClientJob(other, client) {
			return other, nil
		}
	}
	return nil, nil
}

// ClientJobShared is OtherDownloadUsingClientJob for callers that only need
// the decision. It logs the skip at Info (and a lookup failure at Warn, which
// also counts as shared), so every removal path reports it the same way.
// action names the caller in the log line.
func ClientJobShared(ctx context.Context, lister ClientJobLister, client *models.DownloadClient, dl *models.Download, action string) bool {
	other, err := OtherDownloadUsingClientJob(ctx, lister, client, dl)
	if err != nil {
		slog.Warn("leaving download in the client: could not check whether another download still uses it",
			"action", action, "download_id", dl.ID, "client", client.Name, "error", err)
		return true
	}
	if other == nil {
		return false
	}
	ref, _ := clientJobRef(client, dl)
	slog.Info("leaving download in the client: another download still uses it",
		"action", action, "download_id", dl.ID, "other_download_id", other.ID,
		"other_status", other.Status, "client", client.Name, "remote_id", ref)
	return true
}

// RemoveDownloadUnlessShared is RemoveDownload behind the ClientJobShared
// check. When another download still uses the job, the client is not
// contacted at all (so deleteFiles is not applied either) and removed is
// false; the caller drops its own row as usual.
func RemoveDownloadUnlessShared(ctx context.Context, lister ClientJobLister, client *models.DownloadClient, dl *models.Download, deleteFiles bool, globalRemap, action string) (removed bool, err error) {
	if ClientJobShared(ctx, lister, client, dl, action) {
		return false, nil
	}
	if err := RemoveDownload(ctx, client, dl, deleteFiles, globalRemap); err != nil {
		return false, err
	}
	return true, nil
}
