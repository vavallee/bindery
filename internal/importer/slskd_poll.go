package importer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/models"
)

// Stall limits for a slskd download. A peer's upload queue can hold a request
// for a long time, so waiting is allowed far longer than a stopped transfer.
// Both are defaults a Scanner can override (slskdQueueStall, slskdBytesStall).
const (
	// SlskdQueueStall is how long a download may sit queued or starting, with
	// nothing moving, before it is cancelled and failed.
	SlskdQueueStall = 30 * time.Minute
	// SlskdBytesStall is how long a download with bytes flowing may go without
	// a single new byte before it is cancelled and failed.
	SlskdBytesStall = 10 * time.Minute
)

// slskdProgress is what the poller remembers about one download between
// cycles: the last point at which anything moved.
type slskdProgress struct {
	done    int64
	group   slskd.Group
	files   int
	changed time.Time
}

func (s *Scanner) slskdClock() time.Time {
	if s.slskdNow != nil {
		return s.slskdNow()
	}
	return time.Now()
}

func (s *Scanner) slskdLimits() (queue, bytes time.Duration) {
	queue, bytes = s.slskdQueueStall, s.slskdBytesStall
	if queue <= 0 {
		queue = SlskdQueueStall
	}
	if bytes <= 0 {
		bytes = SlskdBytesStall
	}
	return queue, bytes
}

// slskdStalled records this cycle's progress for a download and reports whether
// it has gone without any for longer than its limit. Progress is more bytes,
// another finished file or a new state.
func (s *Scanner) slskdStalled(dl *models.Download, p slskd.Progress) (bool, time.Duration) {
	now := s.slskdClock()
	s.slskdMu.Lock()
	defer s.slskdMu.Unlock()
	if s.slskdProgress == nil {
		s.slskdProgress = map[int64]*slskdProgress{}
	}
	prev := s.slskdProgress[dl.ID]
	if prev == nil {
		// First sight after a start: count from the grab, so a restart does not
		// give a long dead download a fresh window.
		since := dl.AddedAt
		if dl.GrabbedAt != nil {
			since = *dl.GrabbedAt
		}
		if since.IsZero() || since.After(now) {
			since = now
		}
		prev = &slskdProgress{done: p.Done, group: p.Group, files: p.Succeeded, changed: since}
		s.slskdProgress[dl.ID] = prev
	}
	if p.Done > prev.done || p.Group != prev.group || p.Succeeded != prev.files {
		prev.changed = now
	}
	prev.done, prev.group, prev.files = p.Done, p.Group, p.Succeeded
	queueLimit, bytesLimit := s.slskdLimits()
	limit := queueLimit
	if p.Group == slskd.GroupActive {
		limit = bytesLimit
	}
	idle := now.Sub(prev.changed)
	return idle > limit, limit
}

func (s *Scanner) forgetSlskdProgress(keep map[int64]bool) {
	s.slskdMu.Lock()
	defer s.slskdMu.Unlock()
	for id := range s.slskdProgress {
		if !keep[id] {
			delete(s.slskdProgress, id)
		}
	}
}

// checkSlskdDownloads polls slskd for the downloads Bindery started there. A
// download completes only when every file it asked for succeeded; any failed
// file, or a stall, fails the whole download and takes its transfers and files
// back, because a half download is never imported.
func (s *Scanner) checkSlskdDownloads(ctx context.Context, client *models.DownloadClient) {
	rows, err := s.downloads.ListByStatuses(ctx, models.StateGrabbed, models.StateDownloading, models.StateImportFailed)
	if err != nil {
		slog.Warn("download poll: failed to list slskd downloads", "client", client.Name, "error", err)
		return
	}
	var mine []models.Download
	for _, d := range rows {
		if d.DownloadClientID != nil && *d.DownloadClientID == client.ID &&
			d.Protocol == downloader.ProtocolSoulseek && d.SABnzbdNzoID != nil && *d.SABnzbdNzoID != "" {
			mine = append(mine, d)
		}
	}
	if len(mine) == 0 {
		s.forgetSlskdProgress(nil)
		return
	}
	batches, err := downloader.SlskdFor(client).ListBatches(ctx)
	if err != nil {
		slog.Warn("download poll: failed to read slskd's downloads, so they will not be imported",
			"client", client.Name, "error", err)
		return
	}
	live := map[int64]bool{}
	for i := range mine {
		dl := &mine[i]
		live[dl.ID] = true
		batchID := *dl.SABnzbdNzoID
		if dl.Status == models.StateImportFailed {
			s.retrySlskdImport(ctx, client, dl, batchID)
			continue
		}
		p := slskd.Summarize(batches[strings.ToLower(batchID)])
		switch p.Group {
		case slskd.GroupSucceeded:
			if want := expectedSlskdFiles(dl); want > 0 && p.Files < want {
				s.failSlskd(ctx, client, dl, fmt.Sprintf("slskd reports %d of the %d files of this download", p.Files, want))
				continue
			}
			s.completeSlskd(ctx, client, dl, batchID)
		case slskd.GroupFailed:
			s.failSlskd(ctx, client, dl, slskdFailureMessage(p))
		default:
			if stalled, limit := s.slskdStalled(dl, p); stalled {
				s.failSlskd(ctx, client, dl, slskdStallMessage(p, limit))
			}
		}
	}
	s.forgetSlskdProgress(live)
	// Rows slskd no longer lists are failed above (an empty batch summarises as
	// failed), so there is nothing for the never-arrived sweep to add.
}

func expectedSlskdFiles(dl *models.Download) int {
	g, err := slskd.DecodeGrab(dl.NZBURL)
	if err != nil {
		return 0
	}
	return len(g.Files)
}

func slskdFailureMessage(p slskd.Progress) string {
	if p.Files == 0 {
		return "slskd no longer has this download, so it was cancelled. Pick another result"
	}
	return fmt.Sprintf("%d of the %d files %s on Soulseek, so nothing was imported. Pick another result", p.Failed, p.Files, p.FailureWord)
}

func slskdStallMessage(p slskd.Progress, limit time.Duration) string {
	if p.Group == slskd.GroupActive {
		return fmt.Sprintf("the transfer stopped moving for %d minutes, so it was cancelled. Pick another result", int(limit.Minutes()))
	}
	return fmt.Sprintf("the peer did not start sending within %d minutes, so it was cancelled. Pick another result", int(limit.Minutes()))
}

// failSlskd cancels the download in slskd, deletes what it left on disk and
// marks it failed.
func (s *Scanner) failSlskd(ctx context.Context, client *models.DownloadClient, dl *models.Download, message string) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := downloader.RemoveDownload(cancelCtx, client, dl, true, s.downloadPathRemap); err != nil {
		slog.Warn("slskd: could not cancel a failed download in slskd", "download_id", dl.ID, "error", err)
	}
	slog.Warn("download failed", "title", dl.Title, "message", message)
	s.setDownloadError(ctx, dl.ID, message)
	s.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, dl.Title, dl.BookID, dl.OwnerUserID, map[string]string{"guid": dl.GUID, "message": message})
	s.notify(ctx, notifierEventDownloadFailed, map[string]interface{}{"title": dl.Title, "message": message})
}

// slskdFolder resolves the download's folder on Bindery's filesystem and
// refuses a symlink in its place.
func (s *Scanner) slskdFolder(ctx context.Context, client *models.DownloadClient, dl *models.Download) (string, string) {
	folder, reason := downloader.SlskdLocalFolder(ctx, client, dl.ID, s.downloadPathRemap)
	if folder == "" {
		return "", reason
	}
	if fi, err := os.Lstat(folder); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", "the download's folder is a symbolic link"
	}
	return folder, ""
}

func (s *Scanner) completeSlskd(ctx context.Context, client *models.DownloadClient, dl *models.Download, batchID string) {
	// slskd reports it complete, so it is: walk through completed first, since
	// the state machine has no downloading to importFailed edge. From
	// importFailed the row is retried each cycle while the files may still
	// appear, and skipImportRetry blocks it once the skip streak runs out.
	s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
	folder, reason := s.slskdFolder(ctx, client, dl)
	if folder == "" {
		s.failImport(ctx, dl, models.StateImportFailed, fmt.Sprintf(
			"slskd finished this download but Bindery cannot read its folder (%s). slskd and Bindery must share the downloads volume, or the client's path remap must reach it", reason))
		return
	}
	if !importSourcePresent(folder, nil) {
		s.failImport(ctx, dl, models.StateImportFailed, importContentMissingReason(folder))
		return
	}
	slog.Info("download completed", "title", dl.Title, "path", folder)
	s.tryImportSlskd(ctx, client, dl, batchID, folder)
}

func (s *Scanner) retrySlskdImport(ctx context.Context, client *models.DownloadClient, dl *models.Download, batchID string) {
	if dl.ImportRetryCount >= importRetryLimit {
		return
	}
	folder, _ := s.slskdFolder(ctx, client, dl)
	if folder == "" || !importSourcePresent(folder, nil) {
		s.skipImportRetry(ctx, dl, folder)
		return
	}
	slog.Info("retrying failed import", "title", dl.Title, "path", folder,
		"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit)
	if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
		slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
	}
	s.tryImportSlskd(ctx, client, dl, batchID, folder)
}

// tryImportSlskd runs the normal import over the download's own folder. On
// success the finished transfers are removed from slskd's list (the files stay,
// since slskd may be sharing them back).
func (s *Scanner) tryImportSlskd(ctx context.Context, client *models.DownloadClient, dl *models.Download, batchID, folder string) {
	cleanup := func() error {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return downloader.SlskdFor(client).RemoveBatch(cleanCtx, batchID)
	}
	s.tryImportInternal(ctx, dl, folder, "slskd", batchID, "", cleanup, nil)
}
