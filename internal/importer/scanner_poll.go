package importer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/downloader/transmission"
	"github.com/vavallee/bindery/internal/models"
)

// checkSABnzbdDownloads polls SABnzbd for status changes.
func (s *Scanner) checkSABnzbdDownloads(ctx context.Context, client *models.DownloadClient) {
	sab := downloader.SabnzbdFor(client)

	// Check history for completed downloads (no category filter — match by NZO ID)
	history, err := sab.GetHistory(ctx, "", 50)
	if err != nil {
		slog.Debug("failed to fetch SABnzbd history", "error", err)
		return
	}

	// Track which downloads' sources we observed this cycle so stale
	// StateImportFailed downloads (history entry cleared / aged out) can be
	// terminally blocked rather than left stuck (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, slot := range history.Slots {
		dl, err := s.downloads.GetByNzoID(ctx, slot.NzoID)
		if err != nil || dl == nil {
			continue
		}
		seenSourceIDs[dl.ID] = true

		switch slot.Status {
		case "Completed":
			if dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed {
				localPath := s.remapDownloadClientPath(client, slot.Path)
				if localPath != slot.Path {
					slog.Debug("remapped download path", "sab", slot.Path, "local", localPath)
				}
				slog.Info("download completed", "title", dl.Title, "path", localPath)
				s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
				s.tryImportSABnzbd(ctx, sab, dl, slot.NzoID, localPath)
			} else if dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
				// Bug #7: retry a previously failed import.
				localPath := s.remapDownloadClientPath(client, slot.Path)
				slog.Info("retrying failed import", "title", dl.Title, "path", localPath,
					"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit)
				if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
					slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
				}
				s.tryImportSABnzbd(ctx, sab, dl, slot.NzoID, localPath)
			}
		case "Failed":
			if dl.Status != models.StateFailed {
				slog.Warn("download failed", "title", dl.Title, "message", slot.FailMessage)
				s.setDownloadError(ctx, dl.ID, slot.FailMessage)
				s.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, dl.Title, dl.BookID, map[string]string{"guid": dl.GUID, "message": slot.FailMessage})
				s.notify(ctx, notifierEventDownloadFailed, map[string]interface{}{
					"title":   dl.Title,
					"message": slot.FailMessage,
				})
			}
		}
	}

	// Terminally block StateImportFailed downloads whose retry budget is spent
	// (issue #706 finding 4). sourceListIsComplete is false: SABnzbd history is
	// paginated (capped at 50 slots above), so a download missing from this poll
	// may simply have aged out while still healthy — only retry-exhaustion is a
	// safe, definitive signal here.
	s.blockStaleImportFailures(ctx, seenSourceIDs, false, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == client.ID
	})
}

// checkNZBGetDownloads polls NZBGet for status changes using its JSON-RPC API.
func (s *Scanner) checkNZBGetDownloads(ctx context.Context, client *models.DownloadClient) {
	ng := downloader.NzbgetFor(client)

	// Check history for completed/failed downloads (matched by NZBID stored as sabnzbd_nzo_id).
	history, err := ng.GetHistory(ctx)
	if err != nil {
		slog.Debug("failed to fetch NZBGet history", "error", err)
		return
	}

	// Track which downloads' sources we observed this cycle (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, item := range history {
		nzbIDStr := strconv.Itoa(item.NZBID)
		dl, err := s.downloads.GetByNzoID(ctx, nzbIDStr)
		if err != nil || dl == nil {
			continue
		}
		seenSourceIDs[dl.ID] = true

		if nzbget.IsSuccess(item.Status) {
			if dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed {
				localPath := s.remapDownloadClientPath(client, item.DestDir)
				if localPath != item.DestDir {
					slog.Debug("remapped download path", "nzbget", item.DestDir, "local", localPath)
				}
				slog.Info("download completed", "title", dl.Title, "path", localPath)
				s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
				s.tryImportNZBGet(ctx, ng, dl, item.NZBID, localPath)
			} else if dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
				// Bug #7: retry a previously failed import.
				localPath := s.remapDownloadClientPath(client, item.DestDir)
				slog.Info("retrying failed import", "title", dl.Title, "path", localPath,
					"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit)
				if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
					slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
				}
				s.tryImportNZBGet(ctx, ng, dl, item.NZBID, localPath)
			}
		} else if nzbget.IsFailure(item.Status) {
			if dl.Status != models.StateFailed {
				msg := fmt.Sprintf("NZBGet reported status: %s", item.Status)
				slog.Warn("download failed", "title", dl.Title, "status", item.Status)
				s.setDownloadError(ctx, dl.ID, msg)
				s.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, dl.Title, dl.BookID, map[string]string{"guid": dl.GUID, "message": msg})
				s.notify(ctx, notifierEventDownloadFailed, map[string]interface{}{
					"title":   dl.Title,
					"message": msg,
				})
			}
		}
	}

	// Terminally block StateImportFailed downloads whose retry budget is spent
	// (issue #706 finding 4). sourceListIsComplete is false: the NZBGet history
	// response is not a guaranteed-complete enumeration of every source we might
	// still retry, so only retry-exhaustion is acted on here.
	s.blockStaleImportFailures(ctx, seenSourceIDs, false, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == client.ID
	})
}

// tryImportNZBGet attempts to import a completed NZBGet download into the library.
// ng is used to clean up the NZBGet history entry once bindery has taken ownership.
//
// NZBGet always lands a job inside a per-job DestDir, so walking that path is
// safe; the issue #903 file-list API addition does not apply here.
func (s *Scanner) tryImportNZBGet(ctx context.Context, ng *nzbget.Client, dl *models.Download, nzbID int, downloadPath string) {
	nzbIDStr := strconv.Itoa(nzbID)
	s.tryImportInternal(ctx, dl, downloadPath, "nzbget", nzbIDStr, "", func() error {
		return ng.RemoveHistory(ctx, nzbID)
	}, nil)
}

// checkTransmissionDownloads polls Transmission for status changes.
func (s *Scanner) checkTransmissionDownloads(ctx context.Context, client *models.DownloadClient) {
	trans := downloader.TransmissionFor(client)

	// Get all torrents — Category is used as the download directory filter so
	// Bindery only sees its own torrents on a shared Transmission instance.
	torrents, err := trans.GetTorrents(ctx, client.Category)
	if err != nil {
		slog.Debug("failed to fetch Transmission torrents", "error", err)
		return
	}

	// Get all active downloads from DB (not yet completed/imported)
	allDownloads, err := s.downloads.List(ctx)
	if err != nil {
		slog.Debug("failed to list downloads", "error", err)
		return
	}
	torrentsMap := make(map[string]transmission.Torrent)
	for _, t := range torrents {
		torrentsMap[fmt.Sprintf("%d", t.ID)] = t
	}

	// Track which downloads' sources we observed this cycle so stale
	// StateImportFailed downloads (torrent removed) can be terminally blocked
	// rather than left stuck below the retry limit (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, dl := range allDownloads {
		if dl.DownloadClientID == nil || *dl.DownloadClientID != client.ID || dl.TorrentID == nil {
			continue
		}
		torrent, ok := torrentsMap[*dl.TorrentID]
		if !ok {
			continue
		}
		seenSourceIDs[dl.ID] = true

		if dl.Status == models.StateImported || dl.Status == models.StateFailed {
			continue
		}

		// Status codes: 0=stopped, 1=checking, 2=downloading, 3=seeding, 4=allocating, 5=checking, 6=stopped
		isComplete := torrent.Status == 3 || (torrent.PercentDone >= 1.0)
		isStopped := torrent.Status == 0 || torrent.Status == 6
		stopError := strings.TrimSpace(torrent.ErrorString)

		if isComplete && (dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed) {
			// Download is complete
			downloadPath := s.remapDownloadClientPath(client, torrent.DownloadDir)
			// Issue #903: ask Transmission for the authoritative file list so
			// the importer only touches files belonging to THIS torrent rather
			// than walking the shared download root. A nil return signals
			// transmissionFilesFor to fall back to the legacy dir walk (a WARN
			// is already emitted inside the helper).
			bookFiles := s.transmissionFilesFor(ctx, trans, client, torrent)
			slog.Info("download completed", "title", dl.Title, "path", downloadPath, "files", len(bookFiles))
			s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
			s.tryImportTransmission(ctx, &dl, downloadPath, bookFiles)
		} else if isComplete && dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
			// Bug #7: retry a previously failed import.
			downloadPath := s.remapDownloadClientPath(client, torrent.DownloadDir)
			bookFiles := s.transmissionFilesFor(ctx, trans, client, torrent)
			slog.Info("retrying failed import", "title", dl.Title, "path", downloadPath,
				"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit, "files", len(bookFiles))
			if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
				slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
			}
			s.tryImportTransmission(ctx, &dl, downloadPath, bookFiles)
		} else if isStopped && !isComplete && dl.Status != models.StateFailed {
			if stopError == "" {
				// Transmission also reports user-paused torrents as stopped.
				continue
			}
			slog.Warn("download failed", "title", dl.Title, "error", stopError)
			s.markDownloadFailed(ctx, &dl, stopError)
		}
	}

	// Terminally block StateImportFailed downloads whose torrent has been
	// removed from Transmission, or whose retry budget is spent (issue #706
	// finding 4). sourceListIsComplete is true: GetTorrents returns every
	// torrent, so a missing entry definitively means the source is gone.
	s.blockStaleImportFailures(ctx, seenSourceIDs, true, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == client.ID
	})
}

// checkQbittorrentDownloads polls qBittorrent for status changes.
func (s *Scanner) checkQbittorrentDownloads(ctx context.Context, client *models.DownloadClient) {
	qb := downloader.QbittorrentFor(client)

	allDownloads, err := s.downloads.List(ctx)
	if err != nil {
		slog.Debug("failed to list downloads", "error", err)
		return
	}

	// Poll every category this client may have grabbed under. Audiobook grabs
	// use CategoryAudiobook (e.g. "audiobooks") while ebook grabs use Category
	// (e.g. "ebook"); querying only Category leaves audiobook torrents out of
	// the result, so their downloads never match here and hang in "downloading"
	// forever. CategoriesToPoll returns both. The stall/health adapters were
	// already fixed for #700; this is the main import poll that was missed.
	torrentsMap := make(map[string]qbittorrent.Torrent)
	for _, cat := range downloader.CategoriesToPoll(client) {
		torrents, err := qb.GetTorrents(ctx, cat)
		if err != nil {
			slog.Debug("failed to fetch qBittorrent torrents", "category", cat, "error", err)
			return
		}
		for _, t := range torrents {
			torrentsMap[strings.ToLower(t.Hash)] = t
		}
	}

	// Build an UNFILTERED index of every torrent qBittorrent holds, keyed by
	// hash. This is the reconciliation fallback for two stuck-in-"downloading"
	// bugs whose common root is that the category-filtered poll above can miss a
	// torrent that genuinely belongs to a tracked download:
	//
	//   - #969 (cross-seed / complete-at-grab): when Bindery grabs a release the
	//     client already holds complete+seeding, AddTorrent recovers the existing
	//     hash via the 409 path but its setCategory call is best-effort (the
	//     error is ignored, and some qBittorrent versions refuse to recategorise
	//     an actively-seeding torrent). The torrent then stays under the
	//     cross-seed's original category, so CategoriesToPoll never returns it and
	//     the import never fires — the queue item is wedged at downloading/100%.
	//
	//   - #939 (category-filter miss): same mechanism for any torrent qBittorrent
	//     placed outside Bindery's configured categories.
	//
	// GetTorrents("") returns every torrent regardless of category, so a
	// hash/name match here recovers torrents the category filter dropped.
	allTorrents, allErr := qb.GetTorrents(ctx, "")
	if allErr != nil {
		// Non-fatal: fall back to the category-filtered map only. We still want
		// to process the torrents we did find rather than abort the whole poll.
		slog.Debug("qbittorrent: unfiltered torrent listing failed; reconciliation fallback disabled", "error", allErr)
	}
	unfilteredByHash := make(map[string]qbittorrent.Torrent, len(allTorrents))
	for _, t := range allTorrents {
		unfilteredByHash[strings.ToLower(t.Hash)] = t
	}

	slog.Debug("qbittorrent poll", "torrents", len(torrentsMap), "all_torrents", len(unfilteredByHash), "downloads", len(allDownloads), "categories", downloader.CategoriesToPoll(client))

	// Track which downloads' sources we observed this cycle (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, dl := range allDownloads {
		if dl.DownloadClientID == nil || *dl.DownloadClientID != client.ID {
			continue
		}

		var torrent qbittorrent.Torrent
		var ok bool
		if dl.TorrentID != nil {
			torrent, ok = torrentsMap[strings.ToLower(*dl.TorrentID)]
			if !ok {
				// Not in the category-filtered map. Fall back to the unfiltered
				// listing before giving up — this is the #969/#939 category-miss
				// recovery (a cross-seeded torrent qBittorrent kept under a
				// different category is still found here by hash).
				torrent, ok = unfilteredByHash[strings.ToLower(*dl.TorrentID)]
				if ok {
					slog.Info("qbittorrent: download found only via unfiltered listing (category mismatch)",
						"title", dl.Title, "hash", *dl.TorrentID, "category", torrent.Category, "dl_status", dl.Status)
				}
			}
		} else {
			// #939: a download with the client set but no torrent hash. The hash
			// is only persisted when SendDownload returned a RemoteID; if that
			// step failed (or the record predates the fix) the row was skipped
			// FOREVER, leaving the queue item stuck. Recover by matching the
			// torrent in the unfiltered listing by name (and save-path/category
			// when available), then BACKFILL the hash so all subsequent polls,
			// retries and removals work normally.
			if dl.Status == models.StateImported || dl.Status == models.StateFailed {
				continue
			}
			match, found := matchTorrentForDownload(client, &dl, allTorrents)
			if !found {
				slog.Debug("qbittorrent: download has no torrent hash and no listing match yet",
					"title", dl.Title, "dl_status", dl.Status)
				continue
			}
			torrent = match
			ok = true
			slog.Info("qbittorrent: backfilling missing torrent hash from listing match",
				"title", dl.Title, "hash", torrent.Hash, "category", torrent.Category)
			if err := s.downloads.SetTorrentID(ctx, dl.ID, torrent.Hash); err != nil {
				slog.Warn("qbittorrent: failed to backfill torrent hash", "download_id", dl.ID, "error", err)
			} else {
				h := strings.ToLower(torrent.Hash)
				dl.TorrentID = &h
			}
		}

		if !ok {
			// The torrent is not in qBittorrent's list at all (not under any
			// category). Common causes: the torrent was manually removed, or the
			// hash stored in Bindery doesn't match what qBittorrent returned. This
			// is blockStaleImportFailures territory; only log at Debug to avoid noise.
			hash := ""
			if dl.TorrentID != nil {
				hash = *dl.TorrentID
			}
			slog.Debug("qbittorrent: download not found in torrent list",
				"title", dl.Title, "hash", hash, "dl_status", dl.Status)
			continue
		}
		seenSourceIDs[dl.ID] = true

		if dl.Status == models.StateImported || dl.Status == models.StateFailed {
			continue
		}

		state := strings.ToLower(torrent.State)
		// #969: qBittorrent reports a fully-downloaded torrent as
		// {progress:1, amount_left:0, state:"stalledUP"|"uploading"|"pausedUP"|...}.
		// Treat any of those signals as complete. amount_left==0 (with a known
		// non-zero size) is the most reliable "all bytes present" indicator and
		// catches states the substring checks miss (e.g. "queuedUP", "forcedUP").
		isComplete := torrent.Progress >= 1.0 ||
			(torrent.Size > 0 && torrent.AmountLeft == 0) ||
			strings.Contains(state, "upload") ||
			strings.Contains(state, "stalledup") ||
			strings.Contains(state, "checkingup")
		isFailed := strings.Contains(state, "error")

		slog.Debug("qbittorrent: torrent status",
			"title", dl.Title,
			"qbit_state", torrent.State,
			"progress", fmt.Sprintf("%.1f%%", torrent.Progress*100),
			"dl_status", dl.Status,
			"is_complete", isComplete)

		// #969: import whenever the client reports the torrent complete and the
		// download has not yet reached a terminal/in-flight import state —
		// regardless of whether Bindery ever observed a "downloading" phase. The
		// cross-seed / complete-at-grab case lands the record at StateDownloading
		// (set right after the grab) or StateGrabbed and the torrent is already
		// seeding, so the old "must be downloading/grabbed" gate fired but the
		// torrent was invisible due to the category filter (now fixed above by the
		// unfiltered fallback). StateCompleted/StateImportPending are included so a
		// record wedged mid-transition (e.g. a crash between the status write and
		// the import call, or a stuck StateCompleted row encountered between boot
		// reconciliation sweeps) is also reconciled rather than stuck forever. The
		// importer's own idempotency guards (isBookAlreadyImported / alreadyImported*
		// — issue #706) prevent a double-import.
		importable := dl.Status == models.StateDownloading ||
			dl.Status == models.StateGrabbed ||
			dl.Status == models.StateCompleted
		if isComplete && importable {
			rawPath, ok := resolveQbitContentPath(torrent)
			if !ok {
				// content_path is absent or points to a path that no longer exists.
				// This can happen when files were moved to the library by a prior
				// Bindery import (move mode) and the torrent is re-grabbed via a
				// 409 duplicate-add (#769). If the book is already in the library,
				// close out this download immediately rather than looping forever.
				if s.isBookAlreadyImported(ctx, &dl) {
					slog.Info("qbittorrent: content path gone but book already in library — marking as imported",
						"title", dl.Title)
					// Walk the state machine from wherever we are to imported,
					// skipping any state we have already passed (a self-transition
					// is rejected by validTransitions).
					if dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed {
						s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
					}
					if dl.Status != models.StateImportPending {
						s.updateDownloadStatus(ctx, dl.ID, models.StateImportPending)
					}
					s.updateDownloadStatus(ctx, dl.ID, models.StateImporting)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
					continue
				}
				// Path doesn't exist on disk yet (qBittorrent may sanitise characters
				// in the torrent name that differ from what the API reports, e.g. ':'→'_').
				// Do NOT fall back to torrent.SavePath — for multi-file torrents that is
				// the shared download root and walking it would import every unrelated file.
				// Leave the status unchanged so the next check cycle retries.
				slog.Warn("qbittorrent: content path not found, will retry next cycle",
					"title", dl.Title,
					"save_path", torrent.SavePath,
					"name", torrent.Name)
				continue
			}
			downloadPath := s.remapDownloadClientPath(client, rawPath)

			// Issue #903: ask qBittorrent for the authoritative file list so
			// the importer only touches files belonging to THIS torrent.
			// qbittorrentFilesFor returns nil and logs a WARN on RPC error
			// or empty-file response; tryImportInternal then falls back to
			// the legacy filepath.Walk(downloadPath).
			bookFiles := s.qbittorrentFilesFor(ctx, qb, client, torrent)
			slog.Info("download completed", "title", dl.Title, "path", downloadPath, "raw_path", rawPath, "files", len(bookFiles), "dl_status", dl.Status)
			// Advance to StateCompleted only from a pre-completion state;
			// tryImportQbittorrent moves on to importPending → importing →
			// imported. A record already at StateCompleted/StateImportPending
			// skips straight to the import (a self-transition would be rejected).
			if dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed {
				s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
			}
			s.tryImportQbittorrent(ctx, &dl, downloadPath, bookFiles)
		} else if isComplete && dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
			// Bug #7: a previous import attempt failed (e.g. transient filesystem
			// error, path mismatch). The torrent is still seeding so we have the
			// files — retry the import rather than leaving it stuck permanently.
			rawPath, ok := resolveQbitContentPath(torrent)
			if !ok {
				// Same guard as the StateGrabbed branch: if the book is already in
				// the library (files moved by a prior import), close out cleanly.
				if s.isBookAlreadyImported(ctx, &dl) {
					slog.Info("qbittorrent: content path gone but book already in library — marking as imported",
						"title", dl.Title)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImportPending)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImporting)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
					continue
				}
				slog.Warn("qbittorrent: content path not found during import retry, will retry next cycle",
					"title", dl.Title,
					"save_path", torrent.SavePath,
					"name", torrent.Name,
					"attempt", dl.ImportRetryCount+1)
				continue
			}
			downloadPath := s.remapDownloadClientPath(client, rawPath)
			bookFiles := s.qbittorrentFilesFor(ctx, qb, client, torrent)
			slog.Info("retrying failed import", "title", dl.Title, "path", downloadPath,
				"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit, "files", len(bookFiles))
			if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
				slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
			}
			s.tryImportQbittorrent(ctx, &dl, downloadPath, bookFiles)
		} else if isFailed && dl.Status != models.StateFailed {
			slog.Warn("download failed", "title", dl.Title, "state", torrent.State)
			s.markDownloadFailed(ctx, &dl, "Torrent failed in qBittorrent")
		}
	}

	// Terminally block StateImportFailed downloads whose torrent has been
	// removed from qBittorrent, or whose retry budget is spent (issue #706
	// finding 4). sourceListIsComplete is true: GetTorrents returns every
	// torrent, so a missing entry definitively means the source is gone.
	s.blockStaleImportFailures(ctx, seenSourceIDs, true, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == client.ID
	})
}

// tryImportSABnzbd attempts to import a completed SABnzbd download into the library.
// sab is used to clear the SABnzbd history entry once bindery has taken
// ownership of the files; nzoID is the history slot's NZO identifier.
//
// SAB always lands a job inside a per-job completed-folder (`storage` in the
// history slot), so walking that path is safe; the issue #903 file-list
// API addition does not apply here.
func (s *Scanner) tryImportSABnzbd(ctx context.Context, sab *sabnzbd.Client, dl *models.Download, nzoID, downloadPath string) {
	s.tryImportInternal(ctx, dl, downloadPath, "sabnzbd", nzoID, "", func() error {
		// Clean up SABnzbd history
		return sab.DeleteHistory(ctx, nzoID, false)
	}, nil)
}

// tryImportTransmission attempts to import a completed Transmission download into the library.
//
// explicitFiles, when non-nil and non-empty, is the absolute Bindery-side
// path of every file that belongs to this specific torrent (built from the
// Transmission torrent-get "files" RPC and run through PathRemap). Passing
// it avoids the legacy filepath.Walk(downloadPath) and the issue #903 class
// of bug where a single-file torrent at a shared download root would cause
// every unrelated sibling to be imported. Pass nil to fall back to the
// directory walk.
func (s *Scanner) tryImportTransmission(ctx context.Context, dl *models.Download, downloadPath string, explicitFiles []string) {
	s.tryImportInternal(ctx, dl, downloadPath, "transmission", safeRemoteID(dl.TorrentID), "", nil, explicitFiles)
}

// tryImportQbittorrent attempts to import a completed qBittorrent download. See
// tryImportTransmission for the semantics of explicitFiles.
func (s *Scanner) tryImportQbittorrent(ctx context.Context, dl *models.Download, downloadPath string, explicitFiles []string) {
	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", safeRemoteID(dl.TorrentID), "", nil, explicitFiles)
}

// torrentFile is the minimal shape resolveTorrentFiles consumes; it matches
// transmission.File / qbittorrent.File / deluge.File without taking a
// dependency on any of them. Each downloader's File type is converted to
// []torrentFile at the call site.
type torrentFile struct {
	Name string
	Size int64
}

// resolveTorrentFiles maps a downloader's per-torrent file list onto absolute
// Bindery-side book-file paths. For each file:
//
//  1. Join the client's save path with the file's relative name, producing
//     the path the download client sees on its filesystem.
//  2. Apply the download-client's PathRemap (and the global scanner remapper
//     when no client-level rule matches) so Bindery sees the file at its
//     local mount point. This is the same helper checkXxxDownloads already
//     uses for the per-torrent downloadPath, so a single shared rule covers
//     both the parent dir and the files inside it.
//  3. Filter to book files via IsBookFile to match what the legacy
//     filepath.Walk path produced.
//
// Files with empty or path-traversing names ("..", absolute paths) are
// rejected and logged at WARN; they shouldn't reach here from a sane client
// response and treating them as legitimate could resolve outside the
// download root.
//
// The Bindery-side absolute path is returned with filepath.Clean applied so
// downstream code (cleanupMovedSources, alreadyImportedPath) compares clean
// forms consistently.
func (s *Scanner) resolveTorrentFiles(client *models.DownloadClient, clientSavePath string, files []torrentFile) []string {
	if len(files) == 0 || strings.TrimSpace(clientSavePath) == "" {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			continue
		}
		// Reject absolute paths and any ".." path segment: both can resolve
		// outside the torrent's save path; a sane client never produces them
		// in the files-list response, so treat them as malformed and skip
		// rather than silently quoting an attacker-controlled name through
		// Join. Splitting and matching per-segment avoids false positives on
		// legitimate names like "My..Book.epub" while still catching
		// "MyBook/../escape.epub".
		if filepath.IsAbs(name) || hasDotDotSegment(name) {
			slog.Warn("import: rejecting malformed file name from download client",
				"client", client.Name, "name", name)
			continue
		}
		clientPath := filepath.Join(clientSavePath, name)
		binderyPath := filepath.Clean(s.remapDownloadClientPath(client, clientPath))
		if !IsBookFile(binderyPath) {
			continue
		}
		out = append(out, binderyPath)
	}
	return out
}

// hasDotDotSegment reports whether p contains a ".." path segment under
// either forward-slash or platform separators. The downloader Files() APIs
// normalise to forward slash already, but checking both is defensive — a
// rogue Windows-format response then can't smuggle a "..\\" past the
// guard.
func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// resolveAudiobookSource decides what to move/copy/hardlink for an audiobook
// import when the caller supplied an explicit per-torrent file list. The
// audiobook flow normally moves the whole download folder so cover art,
// cue sheets and other non-book companions land together, which is wrong
// for a single-file torrent whose downloadPath is a shared download root
// (issue #903).
//
// Returns either (path, false) when a safe source path is found:
//
//   - For a single book file the file's path itself (the existing
//     not-a-directory branch then places it inside destDir).
//   - For multiple book files sharing a common directory strictly under
//     downloadPath, that common directory (so companion files within the
//     torrent's folder ride along).
//
// Returns ("", true) when no safe directory exists, signalling the caller
// to fall back to per-file placement. This covers the shape where bookFiles
// share no parent below the (shared) downloadPath, i.e. exactly the
// dangerous case the issue describes.
func (s *Scanner) resolveAudiobookSource(downloadPath string, bookFiles []string) (string, bool) {
	if len(bookFiles) == 0 {
		return downloadPath, false
	}
	if len(bookFiles) == 1 {
		return bookFiles[0], false
	}
	common := filepath.Clean(filepath.Dir(bookFiles[0]))
	for _, f := range bookFiles[1:] {
		fDir := filepath.Clean(filepath.Dir(f))
		// Walk common upward until it sits at or above fDir.
		for common != fDir && !pathUnderDir(fDir, common) {
			parent := filepath.Dir(common)
			if parent == common {
				break
			}
			common = parent
		}
	}
	cleanDownload := filepath.Clean(downloadPath)
	// Only accept a common directory that is strictly under downloadPath.
	// Equal-to-downloadPath means the bookFiles sit at the shared download
	// root (the issue #903 shape) and moving downloadPath would catch
	// unrelated siblings. Outside-downloadPath should never happen if the
	// remap is consistent; treat the same way.
	if common == cleanDownload || !pathUnderDir(common, cleanDownload) {
		return "", true
	}
	return common, false
}

// transmissionFilesFor calls Transmission's torrent-get "files" RPC for the
// supplied torrent and returns the absolute Bindery-side book-file paths,
// or nil when the call fails or the torrent reported no files yet. A nil
// return signals tryImportInternal to fall back to filepath.Walk; the
// caller is responsible for emitting the WARN log that records the
// fallback so an operator can spot a misconfigured / unreachable client.
func (s *Scanner) transmissionFilesFor(ctx context.Context, trans *transmission.Client, client *models.DownloadClient, torrent transmission.Torrent) []string {
	files, err := trans.Files(ctx, torrent.ID)
	if err != nil {
		slog.Warn("import: Transmission Files RPC failed, falling back to directory walk (issue #903 fallback)",
			"title", torrent.Name, "id", torrent.ID, "error", err)
		return nil
	}
	if len(files) == 0 {
		slog.Warn("import: Transmission reported no files for torrent yet, falling back to directory walk",
			"title", torrent.Name, "id", torrent.ID)
		return nil
	}
	conv := make([]torrentFile, 0, len(files))
	for _, f := range files {
		conv = append(conv, torrentFile{Name: f.Name, Size: f.Size})
	}
	return s.resolveTorrentFiles(client, torrent.DownloadDir, conv)
}

// qbittorrentFilesFor calls qBittorrent's /torrents/files API for the
// supplied torrent and returns the absolute Bindery-side book-file paths,
// or nil when the call fails or qBittorrent reported no files yet.
//
// SavePath, not ContentPath, is the join base: qBittorrent's files API
// returns names that include the torrent's display folder (e.g.
// "MyBook/file.epub") when the torrent has one, and just the basename for
// single-file torrents. Joining against SavePath reproduces what's on disk
// in both cases. ContentPath is the wrong base for multi-file torrents
// because the file names already include the folder.
func (s *Scanner) qbittorrentFilesFor(ctx context.Context, qb *qbittorrent.Client, client *models.DownloadClient, torrent qbittorrent.Torrent) []string {
	files, err := qb.Files(ctx, torrent.Hash)
	if err != nil {
		slog.Warn("import: qBittorrent Files API failed, falling back to directory walk (issue #903 fallback)",
			"title", torrent.Name, "hash", torrent.Hash, "error", err)
		return nil
	}
	if len(files) == 0 {
		slog.Warn("import: qBittorrent reported no files for torrent yet, falling back to directory walk",
			"title", torrent.Name, "hash", torrent.Hash)
		return nil
	}
	conv := make([]torrentFile, 0, len(files))
	for _, f := range files {
		conv = append(conv, torrentFile{Name: f.Name, Size: f.Size})
	}
	return s.resolveTorrentFiles(client, torrent.SavePath, conv)
}

func (s *Scanner) remapDownloadClientPath(client *models.DownloadClient, rawPath string) string {
	if client != nil && strings.TrimSpace(client.PathRemap) != "" {
		if localPath := ParseRemap(client.PathRemap).Apply(rawPath); localPath != rawPath {
			return localPath
		}
	}
	return s.remapper.Apply(rawPath)
}

// resolveQbitContentPath returns the on-disk content path for a completed torrent.
//
// qBittorrent ≥ 4.1.x populates content_path with the authoritative on-disk path,
// correctly reflecting any character sanitisation it applied to the torrent name
// (e.g. ':' → '_'). When content_path is available it is used directly.
//
// For older clients that omit content_path the function falls back to
// filepath.Join(SavePath, Name) and verifies the path exists with os.Stat.
//
// SavePath is deliberately never returned on its own. For multi-file torrents
// SavePath is the shared download root; falling back to it would cause Bindery
// to walk and import every unrelated file in that directory.
func resolveQbitContentPath(t qbittorrent.Torrent) (string, bool) {
	if t.ContentPath != "" {
		return t.ContentPath, true
	}
	candidate := filepath.Join(t.SavePath, t.Name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, true
	}
	return "", false
}

// matchTorrentForDownload finds the torrent in candidates that corresponds to a
// download whose torrent hash was never persisted (#939). The hash is only
// stored when SendDownload returned a RemoteID; if that step failed, or the row
// predates the hash-setting fix, dl.TorrentID is nil and the poll loop would
// otherwise skip the record forever, leaving the queue item stuck.
//
// Matching is by torrent name against the download title. qBittorrent reports
// the torrent's display name, which for a Bindery grab is the release title we
// stored in dl.Title — so an exact (case-insensitive) name match is the primary
// signal. When the download client declares a category, a candidate whose
// category matches is preferred to disambiguate two same-named torrents.
//
// The match is intentionally conservative: an exact name match only. A fuzzy
// match risks backfilling the WRONG hash onto a download, which would then
// import the wrong torrent's files. When no confident match exists the caller
// leaves the record untouched (logged at Debug) and tries again next cycle.
func matchTorrentForDownload(client *models.DownloadClient, dl *models.Download, candidates []qbittorrent.Torrent) (qbittorrent.Torrent, bool) {
	if dl == nil || strings.TrimSpace(dl.Title) == "" {
		return qbittorrent.Torrent{}, false
	}
	wantName := strings.ToLower(strings.TrimSpace(dl.Title))

	var nameMatches []qbittorrent.Torrent
	for _, t := range candidates {
		if strings.ToLower(strings.TrimSpace(t.Name)) == wantName {
			nameMatches = append(nameMatches, t)
		}
	}
	switch len(nameMatches) {
	case 0:
		return qbittorrent.Torrent{}, false
	case 1:
		return nameMatches[0], true
	}

	// Multiple torrents share this name. Prefer one whose category matches a
	// category this client grabs under; if exactly one qualifies, take it.
	wantCats := map[string]struct{}{}
	for _, c := range downloader.CategoriesToPoll(client) {
		if c != "" {
			wantCats[strings.ToLower(c)] = struct{}{}
		}
	}
	var catMatches []qbittorrent.Torrent
	for _, t := range nameMatches {
		if _, ok := wantCats[strings.ToLower(t.Category)]; ok {
			catMatches = append(catMatches, t)
		}
	}
	if len(catMatches) == 1 {
		return catMatches[0], true
	}
	// Ambiguous — refuse to guess.
	return qbittorrent.Torrent{}, false
}
