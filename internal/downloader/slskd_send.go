package downloader

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/models"
)

// newBatchID makes the GUID slskd takes as a batch id. It is random, so a retry
// of a failed download never collides with the batch the first attempt left.
func newBatchID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// sendSlskd asks slskd for the files a Soulseek result names. sourceURL is the
// result's slskd:// address. The batch id is the download's remote id.
func sendSlskd(ctx context.Context, client *models.DownloadClient, sourceURL string, opts SendOptions, res *SendResult) (*SendResult, error) {
	grab, err := slskd.DecodeGrab(sourceURL)
	if err != nil {
		return nil, err
	}
	if opts.DownloadID <= 0 {
		return nil, errors.New("a Soulseek download needs its download id")
	}
	// A retry reuses the download's folder. Whatever an earlier attempt left in
	// it is this download's own and would be imported next to the new files.
	deleteSlskdFolder(ctx, client, opts.DownloadID, opts.GlobalRemap)
	id, err := newBatchID()
	if err != nil {
		return nil, err
	}
	if _, err := SlskdFor(client).EnqueueBatch(ctx, id, grab.User, grab.Files, slskd.DownloadFolder(opts.DownloadID)); err != nil {
		return nil, err
	}
	res.RemoteID = id
	res.UsesTorrentID = false
	return res, nil
}

// removeSlskdDownload cancels and removes the download's transfers in slskd and,
// when asked, deletes the folder its files landed in. The folder belongs to this
// download alone (bindery/<id>), so no other download's files are touched.
func removeSlskdDownload(ctx context.Context, client *models.DownloadClient, dl *models.Download, deleteFiles bool, globalRemap string) error {
	var err error
	if dl.SABnzbdNzoID != nil && *dl.SABnzbdNzoID != "" {
		err = SlskdFor(client).RemoveBatch(ctx, *dl.SABnzbdNzoID)
	}
	if deleteFiles && dl.ID > 0 {
		deleteSlskdFolder(ctx, client, dl.ID, globalRemap)
	}
	return err
}

// SlskdLocalFolder is where a download's files are on Bindery's filesystem, the
// slskd downloads folder read through the client's path remap. "" with a reason
// when it cannot be worked out safely.
func SlskdLocalFolder(ctx context.Context, client *models.DownloadClient, downloadID int64, globalRemap string) (string, string) {
	root, err := SlskdFor(client).DownloadsDir(ctx)
	if err != nil {
		return "", "slskd did not report its downloads folder"
	}
	local := filepath.Clean(remapClientPath(client, filepath.ToSlash(root)+"/"+slskd.DownloadFolder(downloadID), globalRemap))
	if !filepath.IsAbs(local) {
		return "", fmt.Sprintf("resolved path %q is not absolute", local)
	}
	if filepath.Base(local) != fmt.Sprint(downloadID) || filepath.Base(filepath.Dir(local)) != "bindery" {
		return "", "the resolved path is not this download's folder"
	}
	return local, ""
}

func deleteSlskdFolder(ctx context.Context, client *models.DownloadClient, id int64, globalRemap string) {
	local, reason := SlskdLocalFolder(ctx, client, id, globalRemap)
	if local == "" {
		slog.Warn("slskd: not deleting the download's files", "download_id", id, "reason", reason)
		return
	}
	// A symlink is removed as the link, never followed.
	fi, err := os.Lstat(local)
	if err != nil || !fi.IsDir() {
		return
	}
	if err := os.RemoveAll(local); err != nil {
		slog.Warn("slskd: failed to delete the download's files", "download_id", id, "error", err)
	}
}

// slskdLiveState names a rolled up state for the queue.
func slskdLiveState(g slskd.Group) string {
	switch g {
	case slskd.GroupStarting:
		return "starting"
	case slskd.GroupActive:
		return "downloading"
	case slskd.GroupSucceeded:
		return "completed"
	case slskd.GroupFailed:
		return "failed"
	}
	return "waiting"
}

// getSlskdLiveStatuses reads every batch slskd holds, keyed by batch id, which
// is what a slskd download stores as its remote id.
func getSlskdLiveStatuses(ctx context.Context, client *models.DownloadClient) (map[string]LiveStatus, error) {
	batches, err := SlskdFor(client).ListBatches(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]LiveStatus, len(batches))
	for id, transfers := range batches {
		p := slskd.Summarize(transfers)
		pct := "0"
		if p.Total > 0 {
			pct = fmt.Sprintf("%.1f", float64(p.Done)/float64(p.Total)*100)
		}
		out[id] = LiveStatus{
			Percentage: pct,
			Speed:      bytesPerSecondToString(int64(p.Speed)),
			Size:       p.Total,
			SizeLeft:   max(p.Total-p.Done, 0),
			Status:     strings.ToLower(slskdLiveState(p.Group)),
			Soulseek: &SoulseekLive{
				State:         slskdLiveState(p.Group),
				Files:         p.Files,
				FilesDone:     p.Succeeded,
				QueuePosition: p.QueuePosition,
			},
		}
	}
	return out, nil
}
