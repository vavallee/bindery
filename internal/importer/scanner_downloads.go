package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// importRetryLimit is the maximum number of times CheckDownloads will
// automatically retry a download stuck in StateImportFailed before giving
// up and leaving it for manual intervention (Bug #7).
//
// The budget only ever pays for attempts that could actually have worked —
// see importSourcePresent — so it is a bound on "how many times may a real
// import fail", not a wall-clock deadline. Raised from 3 with #1884: three
// attempts at the 15 s poll cadence gave a transient filesystem hiccup a
// 45-second window to clear before the download was terminally blocked.
const importRetryLimit = 5

// importSkipLimit bounds how many CONSECUTIVE poll cycles a StateImportFailed
// download may decline its retry (importSourcePresent found nothing on this
// host) before it is terminally blocked.
//
// This is the escape hatch for the trap the retry guard would otherwise create:
// a skipped cycle costs no retry attempt, so a download whose files never
// appear satisfies neither of blockStaleImportFailures' arms — the budget is
// never spent, and the torrent is still in the client, so the source has not
// vanished either. Without a bound it sits in StateImportFailed forever, which
// no automatic path revisits and which the Grab 409 refuses to re-grab, so the
// only way out is deleting the queue row.
//
// 120 cycles is ~30 minutes at the 15 s check-downloads cadence: long enough
// for a slow move, a re-mounted share or an operator fixing PathRemap to
// resolve itself with the import running unattended, short enough that a
// genuinely gone payload reaches a state the user can act on the same session.
// Blocking is not the end of the road — StateImportBlocked is re-grabbable
// (regrabbableState) and Queue → Retry import re-arms the row.
const importSkipLimit = 120

// importSourcePresent reports whether there is anything on this host for an
// import attempt to work with: at least one file from the download client's
// authoritative per-torrent list, or — when no list was available — the
// download path itself.
//
// Callers use it to decide whether spending a retry attempt is honest. A
// download whose files are simply not here yet (or not here any more) fails
// identically no matter how many times it is retried, so counting those
// attempts spends the whole budget on a condition the retry cannot influence
// and then terminally blocks a download that was never broken. That is the
// #1884 failure: the retry budget was exhausted 60 seconds after the grab
// while qBittorrent was still downloading into its temp directory, and the
// download was blocked with a PathRemap error against a save path that had
// simply not been created yet.
//
// Skipping the attempt leaves the row in StateImportFailed carrying the
// message from the attempt that DID run, so the queue keeps showing the real
// reason and picks the import back up the moment the files appear. Skipping
// forever is not an option either — skipImportRetry bounds the streak.
//
// The per-file predicate is importableSourceFile, the SAME one
// filterImportableFiles applies to the client's list. They must agree: when
// this said "present" for an entry the filter drops (a symlink with a live
// target passed os.Stat), every poll spent a retry attempt on a file list the
// importer had already emptied.
func importSourcePresent(downloadPath string, explicitFiles []string) bool {
	for _, f := range explicitFiles {
		if importableSourceFile(f) {
			return true
		}
	}
	if len(explicitFiles) > 0 {
		return false
	}
	if strings.TrimSpace(downloadPath) == "" {
		return false
	}
	// The fallback is a directory to walk, not a file to import, so it is
	// Stat'd rather than run through importableSourceFile: a symlinked
	// download root is an ordinary deployment shape, and discoverBookFiles
	// decides what inside it is importable.
	if _, err := os.Stat(downloadPath); err != nil {
		return false
	}
	// A release packed as a RAR set has nothing to import until an unpacker
	// extracts it (#3203). Counted as present, every poll spent a retry and the
	// download was blocked about 90 seconds after completing, before unpackerr
	// (one minute start delay by default) had begun. As not present, it waits
	// on the skip streak, up to importSkipLimit, and imports once the extracted
	// files appear.
	return archiveAwaitingExtraction(downloadPath) == ""
}

// importSkipState is one download's consecutive-skip streak, plus a snapshot of
// the row fields that any outside change to the download would move. Skipping
// deliberately writes nothing, so the snapshot is stable for as long as the
// streak is genuinely untouched; a manual Queue → Retry import (which zeroes
// import_retry_count and clears error_message), a real attempt, or a
// match-to-book all change it and restart the count.
type importSkipState struct {
	count   int
	retries int
	errMsg  string
}

// recordImportSkip counts one skipped retry for dl and returns the length of
// the current streak. See importSkipState for how a streak is reset.
func (s *Scanner) recordImportSkip(dl *models.Download) int {
	s.importSkipsMu.Lock()
	defer s.importSkipsMu.Unlock()
	if s.importSkips == nil {
		s.importSkips = make(map[int64]importSkipState)
	}
	st, ok := s.importSkips[dl.ID]
	if !ok || st.retries != dl.ImportRetryCount || st.errMsg != dl.ErrorMessage {
		st = importSkipState{retries: dl.ImportRetryCount, errMsg: dl.ErrorMessage}
	}
	st.count++
	s.importSkips[dl.ID] = st
	return st.count
}

// clearImportSkips drops a download's streak.
func (s *Scanner) clearImportSkips(id int64) {
	s.importSkipsMu.Lock()
	defer s.importSkipsMu.Unlock()
	delete(s.importSkips, id)
}

// forgetImportSkipsExcept garbage-collects streaks for downloads that are no
// longer waiting to import (imported, deleted, blocked, re-grabbed), so the map
// cannot grow with the queue's history.
func (s *Scanner) forgetImportSkipsExcept(stillFailing map[int64]bool) {
	s.importSkipsMu.Lock()
	defer s.importSkipsMu.Unlock()
	for id := range s.importSkips {
		if !stillFailing[id] {
			delete(s.importSkips, id)
		}
	}
}

// importContentMissingReason is the message recorded the first time the
// download client reports a complete download whose content path is not on
// this host. Unlike importSourceGoneReason it is written while the files may
// still appear, so it names both causes the operator can act on rather than
// telling them to stop waiting.
func importContentMissingReason(savePath string) string {
	return fmt.Sprintf("the download client reports this download as complete, but no content path for it is on this host (client save path %s). The files may have been moved or deleted after the download finished, or the client's paths may not map into Bindery (set PathRemap on the download client). Use Retry import on the Queue page once that is fixed, or grab the release again from search.",
		savePath)
}

// importSourceGoneReason is the message a download is blocked with once its
// skip streak runs out. It must not tell the user to wait: by the time it is
// written, waiting is precisely what has already failed importSkipLimit times.
func importSourceGoneReason(downloadPath string) string {
	if archive := archiveAwaitingExtraction(downloadPath); archive != "" {
		return fmt.Sprintf("this download holds only an archive (%s), and nothing extracted it in the %d checks Bindery waited. Bindery does not unpack archives: an unpacker such as unpackerr has to extract it into %s (see Storage and Hardlinks, RAR-packed torrent releases). Once the files are there, use Retry import on the Queue page.",
			filepath.Base(archive), importSkipLimit, downloadPath)
	}
	return fmt.Sprintf("the download client still reports this download as complete, but none of its files are on this host — checked %d times at %s and found nothing to import. Either the files were moved or deleted after the download finished, or the client's paths do not map into Bindery (set PathRemap on the download client). Fix that and use Retry import on the Queue page, or grab the release again from search.",
		importSkipLimit, downloadPath)
}

// skipImportRetry records an import retry that was deliberately not attempted
// because importSourcePresent found nothing on disk to import. The download is
// normally left exactly as it was — still StateImportFailed, retry count
// untouched — so no state is spent on a cycle that could not have succeeded
// (#1884).
//
// Once the streak reaches importSkipLimit the download is instead blocked. The
// retry guard alone left a download whose files never appear stuck in
// StateImportFailed permanently: no attempt is ever counted, so
// blockStaleImportFailures' retry-exhaustion arm never fires, and the torrent
// is still in the client, so its vanished-source arm never fires either. That
// row is invisible to every automatic path and the Grab 409 refused to re-grab
// it, which is the same dead end #1884 set out to remove — just reached from
// the other side. StateImportBlocked is terminal, legible in the queue, and
// re-grabbable, so the user always has a move.
//
// Debug, not Warn, for the ordinary skip: it fires on every 15 s poll for as
// long as the condition lasts. The reason the operator needs is already on the
// download row (and in the queue) from the attempt that did run.
func (s *Scanner) skipImportRetry(ctx context.Context, dl *models.Download, downloadPath string) {
	skips := s.recordImportSkip(dl)
	if skips >= importSkipLimit {
		s.clearImportSkips(dl.ID)
		reason := importSourceGoneReason(downloadPath)
		slog.Warn("blocking import: the download's files never appeared",
			"title", dl.Title, "download_id", dl.ID, "path", downloadPath,
			"checks", skips, "reason", reason)
		s.failImport(ctx, dl, models.StateImportBlocked, reason)
		return
	}
	slog.Debug("import retry skipped: nothing to import at the download path yet — retry budget not spent",
		"title", dl.Title, "download_id", dl.ID, "path", downloadPath,
		"attempts_used", dl.ImportRetryCount, "limit", importRetryLimit,
		"consecutive_skips", skips, "skip_limit", importSkipLimit)
}

// CheckDownloads polls all enabled download clients for status changes and
// updates the local download records. Every enabled client is polled in
// priority order so that downloads from all configured clients (e.g. a
// SABnzbd + qBittorrent combo) are imported each cycle (issue #1090).
func (s *Scanner) CheckDownloads(ctx context.Context) {
	clients, err := s.clients.ListEnabled(ctx)
	if err != nil || len(clients) == 0 {
		return
	}

	for i := range clients {
		client := &clients[i]
		switch client.Type {
		case "transmission":
			s.checkTransmissionDownloads(ctx, client)
		case "qbittorrent":
			s.checkQbittorrentDownloads(ctx, client)
		case "deluge":
			s.checkDelugeDownloads(ctx, client)
		case "rtorrent":
			s.checkRtorrentDownloads(ctx, client)
		case "nzbget":
			s.checkNZBGetDownloads(ctx, client)
		case "sabnzbd":
			s.checkSABnzbdDownloads(ctx, client)
		default:
			// An unknown client type must NOT silently fall through to the SABnzbd
			// poller: it would hit SABnzbd's HTTP endpoints against the wrong host,
			// error, and (logged at Debug only) leave every download stuck at
			// "downloading" with no visible failure — exactly the Deluge bug in
			// #1019. Surface it loudly instead.
			slog.Warn("check downloads: unsupported download client type — downloads will not be imported",
				"type", client.Type, "client", client.Name)
		}
	}
}

// wedgedCompletedReconcileCap caps how many StateCompleted rows the boot
// reconciliation re-queues per startup. The cap exists so a site that has
// accumulated many wedged rows (e.g. first boot after the Wave 4 upgrade
// surfaces a long-standing bug) does not thundering-herd the importer in a
// single sweep — subsequent restarts pick up the rest a batch at a time.
// Each sweep is bounded by importRetryLimit per row so a permanently broken
// row terminates after at most that many cycles.
const wedgedCompletedReconcileCap = 50

// RecoverInterruptedImports sweeps downloads stuck mid-import back into a
// retryable state. It must be called once at startup, before the scheduler
// begins polling.
//
// Two distinct wedge shapes are handled:
//
//  1. StateImporting / StateImportPending — a process crash or the per-import
//     timeout can leave the row in one of these non-terminal states with no
//     automatic re-entry. CheckDownloads only retries from StateImportFailed,
//     so without this sweep the download is wedged forever (issue #706
//     finding 1). Moving it to StateImportFailed lets the existing retry path
//     (CheckDownloads, while the source is still in the client) pick it up; if
//     the source has since vanished, the same path now terminally blocks it
//     (finding 4) rather than leaving it silently stuck.
//
//  2. StateCompleted with no book_files row — the state machine allows
//     Completed -> ImportPending, but the transition is driven by the scanner
//     tick, not the state itself. If the process restarts between Completed
//     being set and the next tick, the row sits in Completed forever (Wave 4
//     finding 21). RecoverWedgedCompleted re-queues such rows as
//     ImportPending, bounded by wedgedCompletedReconcileCap so a database
//     full of wedged rows does not stampede the importer on first boot.
//     import_retry_count is bumped on every requeue so a permanently broken
//     row exhausts the budget after importRetryLimit attempts; rows over the
//     budget are logged at ERROR on startup but otherwise left alone.
//
// Release-notes implication: after upgrading past this version, downloads
// that were stuck in Completed before the upgrade (some users have reported
// books quietly sitting there for weeks) will be retried for import on
// startup. The retry surfaces a file that may already be on disk; the
// idempotency guard in the importer means a re-imported file that already
// landed is detected and skipped. This is intended behaviour and resolves
// the wedge without user action.
func (s *Scanner) RecoverInterruptedImports(ctx context.Context) {
	if s.downloads == nil {
		return
	}
	recovered, err := s.downloads.RecoverInterruptedImports(ctx)
	if err != nil {
		slog.Warn("failed to sweep interrupted imports on startup", "error", err)
		// Fall through: some downloads may still have been recovered before the
		// error; emit history for those.
	}
	for _, id := range recovered {
		slog.Info("recovered interrupted import — re-queued for retry", "download_id", id)
		dl, getErr := s.downloadByID(ctx, id)
		if getErr != nil || dl == nil {
			continue
		}
		s.createHistoryEvent(ctx, models.HistoryEventImportFailed, dl.Title, dl.BookID, dl.OwnerUserID, map[string]string{
			"guid":    dl.GUID,
			"message": "import interrupted (process restart) — re-queued for retry",
			"status":  string(models.StateImportFailed),
		})
		// Intentionally NOT firing EventDownloadFailed here: this is a benign
		// startup-recovery transition that re-queues the import for retry, not a
		// terminal failure. Webhooking every interrupted import on every restart
		// would be noise for the user (issue #849).
	}

	s.recoverWedgedCompleted(ctx)
}

// recoverWedgedCompleted handles the Wave 4 finding 21 case: downloads that
// finished in StateCompleted but never advanced to import (typically because
// the process restarted between Completed being set and the scanner tick that
// would have moved them on). The work is capped per startup so a backlog of
// thousands does not stampede the importer; over-budget rows are surfaced via
// an ERROR log so an operator can see what is being skipped on each boot.
func (s *Scanner) recoverWedgedCompleted(ctx context.Context) {
	if s.downloads == nil {
		return
	}
	recovered, err := s.downloads.RecoverWedgedCompleted(ctx, importRetryLimit, wedgedCompletedReconcileCap)
	if err != nil {
		slog.Warn("failed to sweep wedged completed downloads on startup", "error", err)
		// Fall through: some rows may have been re-queued before the error.
	}
	for _, id := range recovered {
		slog.Warn("re-queueing wedged Completed download for import (boot reconciliation)",
			"download_id", id, "cap", wedgedCompletedReconcileCap)
		dl, getErr := s.downloadByID(ctx, id)
		if getErr != nil || dl == nil {
			continue
		}
		age := ""
		if dl.CompletedAt != nil {
			age = time.Since(*dl.CompletedAt).Truncate(time.Minute).String()
		}
		s.createHistoryEvent(ctx, models.HistoryEventImportFailed, dl.Title, dl.BookID, dl.OwnerUserID, map[string]string{
			"guid":    dl.GUID,
			"message": "import never started (process restart before scanner tick) — re-queued for retry",
			"status":  string(models.StateImportPending),
			"age":     age,
		})
	}
	overLimit, countErr := s.downloads.CountWedgedCompletedOverRetryLimit(ctx, importRetryLimit)
	if countErr != nil {
		slog.Warn("failed to count over-budget wedged Completed downloads", "error", countErr)
		return
	}
	if overLimit > 0 {
		slog.Error("wedged Completed downloads have exhausted their import retry budget and will not be re-queued — investigate manually",
			"count", overLimit, "retry_limit", importRetryLimit)
	}
}

// downloadByID is a small helper used by recovery/diagnostic paths that only
// have an ID. It was a scan of the whole table; GetByID reads the one row.
func (s *Scanner) downloadByID(ctx context.Context, id int64) (*models.Download, error) {
	return s.downloads.GetByID(ctx, id)
}

func (s *Scanner) setDownloadError(ctx context.Context, id int64, message string) {
	if err := s.downloads.SetError(ctx, id, message); err != nil {
		slog.Warn("failed to persist download error", "download_id", id, "error", err)
	}
}

func (s *Scanner) updateDownloadStatus(ctx context.Context, id int64, status models.DownloadState) {
	if err := s.downloads.UpdateStatus(ctx, id, status); err != nil {
		slog.Warn("failed to update download status", "download_id", id, "status", status, "error", err)
	}
}

// failImport records an import failure with a user-facing reason. It persists
// the status + message and emits an importFailed history event so the cause
// is visible in the Queue/History UI. Also publishes EventDownloadFailed so
// user-configured webhooks see the failure (issue #849) — there is no separate
// "import failed" event in the notifier enum, so importFailed and
// downloadFailed share the channel.
func (s *Scanner) failImport(ctx context.Context, dl *models.Download, status models.DownloadState, reason string) {
	s.failImportWithCheck(ctx, dl, status, reason, nil)
}

// failImportWithCheck is failImport for a download a content check refused
// (#3015, #3246): the importFailed event and the failure webhook carry the same
// contentCheck object a warn mode import attaches to bookImported, so a
// consumer sees what was found, not only the message.
func (s *Scanner) failImportWithCheck(ctx context.Context, dl *models.Download, status models.DownloadState, reason string, contentCheck map[string]any) {
	if err := s.downloads.SetErrorWithStatus(ctx, dl.ID, status, reason); err != nil {
		slog.Warn("failed to persist import error", "download_id", dl.ID, "status", status, "error", err)
	}
	data := map[string]any{
		"guid":    dl.GUID,
		"message": reason,
		"status":  string(status),
	}
	payload := map[string]interface{}{
		"title":   dl.Title,
		"message": reason,
	}
	if contentCheck != nil {
		data["contentCheck"] = contentCheck
		payload["contentCheck"] = contentCheck
	}
	s.createHistoryEventData(ctx, models.HistoryEventImportFailed, dl.Title, dl.BookID, dl.OwnerUserID, data)
	s.notify(ctx, notifierEventDownloadFailed, payload)
	observeImportFailure(dl, status, reason)
}

// blockTinyOnlyDownload blocks a download whose only ebook files are under
// MinPlausibleEbookBytes (#3224). Retrying cannot change that, so it is blocked
// with the override named, and the path is kept for manual import.
func (s *Scanner) blockTinyOnlyDownload(ctx context.Context, dl *models.Download, downloadPath string) {
	s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
	// importPending cannot go straight to blocked.
	s.updateDownloadStatus(ctx, dl.ID, models.StateImporting)
	s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf(
		"nothing was imported: the only ebook files in %q are under %d bytes, too small to be a book. If one of them really is the book, use manual import",
		downloadPath, MinPlausibleEbookBytes))
}

// recordUnmatchedImportPath persists where a completed download's files are so a
// later manual "Match to book" (#1589) can import them directly. Best-effort: a
// failure here only loses the manual-import shortcut, not any files.
func (s *Scanner) recordUnmatchedImportPath(ctx context.Context, downloadID int64, path string) {
	if path == "" {
		return
	}
	if err := s.downloads.SetImportPath(ctx, downloadID, path); err != nil {
		slog.Warn("failed to record unmatched import path", "download_id", downloadID, "path", path, "error", err)
	}
}

// createHistoryEvent records a history row. owner is the download's owner (or
// the book's), used when the book has none; see HistoryRepo.Create.
func (s *Scanner) createHistoryEvent(ctx context.Context, eventType string, sourceTitle string, bookID *int64, owner int64, data map[string]string) {
	s.createHistoryEventData(ctx, eventType, sourceTitle, bookID, owner, data)
}

// createHistoryEventData is createHistoryEvent for a payload with a nested
// value, such as the contentCheck object. The data column is JSON either way.
func (s *Scanner) createHistoryEventData(ctx context.Context, eventType string, sourceTitle string, bookID *int64, owner int64, data any) {
	if s.history == nil {
		return
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		slog.Warn("failed to marshal history event data", "event_type", eventType, "error", err)
		return
	}
	if err := s.history.Create(ctx, &models.HistoryEvent{
		BookID:      bookID,
		EventType:   eventType,
		SourceTitle: sourceTitle,
		Data:        string(dataJSON),
		OwnerUserID: owner,
	}); err != nil {
		slog.Warn("failed to create history event", "event_type", eventType, "error", err)
	}
}

// ebookImportHistoryData builds the data payload of the one bookImported
// history row an ebook download writes (#2764), from the library paths of
// every file it placed.
//
// Shape, and why:
//
//   - "format" stays the media type ("ebook"), unchanged from the per-file
//     rows and from the audiobook branch, so anything reading it (the Bug #13
//     ebook-vs-audiobook check, the History filter) keeps working. Rows
//     written by older versions carry exactly this key and still read the same.
//   - "path" stays the thing the History page renders as the row's detail
//     line. A single-format download records the file itself, exactly as
//     before. Several formats share one folder, so the row records the folder
//     rather than picking one file arbitrarily, which is what the audiobook
//     row already does.
//   - "formats" is the new part: the extensions that landed, sorted, e.g.
//     "azw3, epub, mobi". Which formats arrived is the useful content of this
//     event and it was the thing lost when three rows collapsed into one.
//   - "fileCount" is the number of files behind that list.
//   - "paths" is the exact set of library files that this event placed. It is
//     intentionally separate from the display-oriented "path": consumers
//     that need to act on an import must never turn a shared parent directory
//     into a deletion target.
//
// Older rows have no "formats"/"fileCount"/"paths"; readers must treat them as
// optional.
func ebookImportHistoryData(destPaths []string) map[string]any {
	data := map[string]any{"format": models.MediaTypeEbook}
	if len(destPaths) == 0 {
		return data
	}
	if len(destPaths) == 1 {
		data["path"] = destPaths[0]
	} else {
		data["path"] = filepath.Dir(destPaths[0])
	}
	seen := make(map[string]bool, len(destPaths))
	formats := make([]string, 0, len(destPaths))
	for _, p := range destPaths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if ext == "" || seen[ext] {
			continue
		}
		seen[ext] = true
		formats = append(formats, ext)
	}
	sort.Strings(formats)
	if len(formats) > 0 {
		data["formats"] = strings.Join(formats, ", ")
	}
	data["fileCount"] = strconv.Itoa(len(destPaths))
	data["paths"] = append([]string(nil), destPaths...)
	return data
}

// importHistoryProvenance annotates the one bookImported History row with the
// source Bindery can identify without inference. History is durable after the
// corresponding downloads row expires, so keeping the release GUID here lets
// an operator understand or blocklist an old import without guessing from its
// title (#3248).
//
// A manual import's synthetic GUID is deliberately not recorded: it names the
// local action, not a release. A downloaded release records its stable GUID
// plus the local download row and the optional configured indexer/client IDs.
// Future origins (for example library scans) must add values only where the
// caller can know them rather than trying to reconstruct them from a path.
func importHistoryProvenance(dl *models.Download, data map[string]any) map[string]any {
	out := make(map[string]any, len(data)+5)
	for key, value := range data {
		out[key] = value
	}
	if dl == nil || strings.HasPrefix(dl.GUID, ManualDownloadGUIDPrefix) {
		out["origin"] = "manual"
		return out
	}

	out["origin"] = "download"
	if dl.GUID != "" {
		out["guid"] = dl.GUID
	}
	if dl.ID != 0 {
		out["downloadId"] = strconv.FormatInt(dl.ID, 10)
	}
	if dl.IndexerID != nil {
		out["indexerId"] = strconv.FormatInt(*dl.IndexerID, 10)
	}
	if dl.DownloadClientID != nil {
		out["downloadClientId"] = strconv.FormatInt(*dl.DownloadClientID, 10)
	}
	return out
}

// applyEmbeddedLanguage reconciles the language recorded for a book with the
// one embedded in the EPUB just imported for it.
//
// Precedence is user > file > provider. A user's own edit locks the field and
// the caller never even opens the EPUB. Otherwise the file wins: a provider
// describes an abstract *work* whose editions exist in many languages, while
// the file on disk is one specific edition and is the thing the user opens.
// Before #1933 the provider won unconditionally, so a Spanish EPUB imported
// against an English OpenLibrary record displayed "English" indefinitely, with
// the release name buried in a history row the only hint otherwise.
//
// detected is expected already normalised to ISO 639-2/B (ReadEpubMetadata does
// this); book.Language is normalised here before comparing because provider
// values arrive in both two- and three-letter forms (#1729).
//
// Best-effort throughout: a persistence failure is logged and leaves the
// existing value alone rather than failing an import that otherwise succeeded.
func (s *Scanner) applyEmbeddedLanguage(ctx context.Context, book *models.Book, detected, sourceTitle string) {
	if book == nil || detected == "" {
		return
	}
	previous := models.NormalizeLanguageCode(book.Language)
	if previous == detected {
		return
	}
	if err := s.books.SetLanguage(ctx, book.ID, detected); err != nil {
		slog.Warn("failed to persist EPUB-detected language", "bookID", book.ID, "language", detected, "error", err)
		return
	}
	book.Language = detected

	if previous == "" {
		// The catalogue had nothing; this is the #1160 backfill, and there is
		// no disagreement to report.
		slog.Info("filled book language from embedded EPUB metadata", "bookID", book.ID, "language", detected)
		return
	}
	// A genuine disagreement means the file is not the edition the catalogue
	// described. That is worth a history row: it is the answer to "why does my
	// English book read as Spanish", and the language field alone cannot say
	// that it was ever wrong.
	slog.Info("corrected book language from embedded EPUB metadata",
		"bookID", book.ID, "from", previous, "to", detected)
	bookID := book.ID
	s.createHistoryEvent(ctx, models.HistoryEventBookLanguageCorrected, sourceTitle, &bookID, book.OwnerUserID, map[string]string{
		"from": previous,
		"to":   detected,
	})
}

func (s *Scanner) markDownloadFailed(ctx context.Context, dl *models.Download, message string) {
	s.setDownloadError(ctx, dl.ID, message)
	s.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, dl.Title, dl.BookID, dl.OwnerUserID, map[string]string{"guid": dl.GUID, "message": message})
	s.notify(ctx, notifierEventDownloadFailed, map[string]interface{}{
		"title":   dl.Title,
		"message": message,
	})
}

// blockStaleImportFailures terminally blocks downloads that are stuck in
// StateImportFailed with no remaining automatic recovery path (issue #706
// finding 4).
//
// CheckDownloads only retries a StateImportFailed download while its source
// still appears in the client's torrent list / history. Two cases leave such a
// download wedged below the retry limit forever, with no terminal state:
//
//   - the retry budget is exhausted (ImportRetryCount >= importRetryLimit);
//   - the source has vanished from the client (the torrent was removed, or the
//     usenet history entry was cleared), so no future poll will ever revisit it.
//
// For both, this transitions the download to terminal StateImportBlocked with
// an actionable message so the operator sees it needs manual intervention
// rather than a silent stuck queue entry.
//
// seenSourceIDs holds the IDs of every download whose source was observed in
// this poll cycle — a StateImportFailed download NOT in that set has lost its
// source. belongsToClient reports whether a download is owned by the polling
// client (torrent vs. usenet ownership differs, so the caller supplies the
// predicate).
//
// sourceListIsComplete must be true only when seenSourceIDs was built from a
// complete enumeration of the client's sources. An UNFILTERED torrent listing
// qualifies: a missing entry definitively means the source is gone. A
// category/label-filtered or degraded listing does NOT (#1461): a torrent moved
// to another directory/category or stripped of its label is absent from the
// filtered view while still healthy in the client. Usenet history APIs are
// paginated (SABnzbd is capped at 50 slots here), so a missing entry there
// could merely be an aged-out-but-healthy download. For all such callers this
// is false and only the retry-exhaustion case blocks, never the
// vanished-source case.
//
// The download list is re-fetched here rather than reusing the caller's
// snapshot: a retry that ran earlier in the same poll cycle may have changed a
// download's status / retry count, and acting on the stale snapshot could
// misjudge the retry budget.
func (s *Scanner) blockStaleImportFailures(
	ctx context.Context,
	seenSourceIDs map[int64]bool,
	sourceListIsComplete bool,
	belongsToClient func(models.Download) bool,
) {
	// Only two states are acted on below (downloading and importFailed), so ask
	// for those rather than the whole table, which every poller used to load
	// once more at the end of every cycle.
	allDownloads, err := s.downloads.ListByStatuses(ctx, models.StateDownloading, models.StateImportFailed)
	if err != nil {
		slog.Warn("blockStaleImportFailures: failed to list downloads", "error", err)
		return
	}
	// Every poller ends its cycle here with the full download list, which makes
	// this the natural place to garbage-collect skip streaks: anything that is
	// no longer StateImportFailed (imported, blocked, re-grabbed, deleted) will
	// never be skipped again.
	stillFailing := make(map[int64]bool)
	for i := range allDownloads {
		if allDownloads[i].Status == models.StateImportFailed {
			stillFailing[allDownloads[i].ID] = true
		}
	}
	s.forgetImportSkipsExcept(stillFailing)
	for i := range allDownloads {
		dl := allDownloads[i]
		if !belongsToClient(dl) {
			continue
		}
		if dl.Status == models.StateDownloading {
			s.failDownloadThatNeverArrived(ctx, &dl, seenSourceIDs, sourceListIsComplete)
			continue
		}
		if dl.Status != models.StateImportFailed {
			continue
		}
		var reason string
		switch {
		case dl.ImportRetryCount >= importRetryLimit:
			reason = fmt.Sprintf("import retry limit reached (%d attempts) — fix the underlying problem, then retry manually", importRetryLimit)
		case sourceListIsComplete && !seenSourceIDs[dl.ID]:
			reason = "download source no longer available in the client — re-download or import the files manually"
		default:
			continue
		}
		slog.Warn("blocking unrecoverable import failure", "title", dl.Title, "download_id", dl.ID, "reason", reason)
		s.failImport(ctx, &dl, models.StateImportBlocked, reason)
	}
}

// neverArrivedGrace is how long a grabbed download may be absent from its
// client before failDownloadThatNeverArrived gives up on it.
//
// It is generous on purpose. The window has to cover a client that accepts an
// add and takes its time publishing the torrent (qBittorrent does this while it
// resolves a magnet's metadata), plus the poll interval, plus a restart. Ten
// minutes is far longer than any of those and still far shorter than "forever",
// which is what the wait used to be.
const neverArrivedGrace = 10 * time.Minute

// failDownloadThatNeverArrived fails a download that Bindery reported as
// grabbed but that never turned up in the download client (#2505).
//
// The grab path can report success without the client ever receiving anything:
// the reporter's case was an unsigned indexer URL answered with 401, but a
// client that drops the add, or a magnet whose metadata never resolves, land
// here the same way. Every poll then found the torrent missing, logged
// "download not found in torrent list" at Debug, and did nothing, because the
// only thing that acts on a vanished source is the StateImportFailed arm above.
// The queue item sat at downloading with an empty errorMessage indefinitely,
// which on screen is indistinguishable from a torrent waiting on peers.
//
// Three guards, and all three matter:
//
//   - sourceListIsComplete, so absence is definitive. Under a degraded or
//     category-filtered listing a healthy torrent can be missing from the view
//     (#1461), and failing on that would be the same mistake in a new place.
//   - not seen this cycle, the same signal the arm above uses.
//   - grabbed longer ago than neverArrivedGrace, so a client that is merely
//     slow to publish the torrent is left alone.
//
// The download is failed rather than blocked: nothing was placed on disk and
// nothing needs unpicking, so this is recoverable by grabbing again. It
// deliberately does not blocklist the release, because the usual cause is on
// Bindery's side of the wire and blocklisting a good release for that would
// cost the user the best copy.
func (s *Scanner) failDownloadThatNeverArrived(
	ctx context.Context,
	dl *models.Download,
	seenSourceIDs map[int64]bool,
	sourceListIsComplete bool,
) {
	if !sourceListIsComplete || seenSourceIDs[dl.ID] {
		return
	}
	// No recorded source id means absence from the client's list proves
	// nothing: every poller skips such a row before it reaches seenSourceIDs,
	// so it is unseen because it was never looked up, not because it is
	// missing. qBittorrent backfills the hash from a listing match; Deluge and
	// rTorrent simply continue. Failing on that would kill a healthy download.
	if dl.TorrentID == nil && dl.SABnzbdNzoID == nil {
		return
	}
	since := dl.AddedAt
	if dl.GrabbedAt != nil {
		since = *dl.GrabbedAt
	}
	if since.IsZero() || time.Since(since) < neverArrivedGrace {
		return
	}
	const reason = "never reached the download client — the client has no record of it, so nothing was downloaded. Check the indexer and the client are both reachable, then grab it again"
	slog.Warn("failing a download the client never received",
		"title", dl.Title, "download_id", dl.ID, "grabbed_at", since)
	s.markDownloadFailed(ctx, dl, reason)
}

// alreadyImportedFormat reports whether book already has a tracked, on-disk
// file for the given format. It is the idempotency guard for issue #706
// finding 2: the book-file write, the download-status write and the history
// event are three separate non-transactional ops, so a crash between them
// leaves the file on disk and recorded in book_files while the download stays
// non-terminal. The startup sweep (finding 1) then re-queues the download and
// the retry would import the SAME files a second time — and because the
// audiobook destination is run through UniqueDir, the retry lands a duplicate
// folder ("Title (2)") that INSERT OR IGNORE cannot dedupe.
//
// Checking book_files AND os.Stat (rather than book_files alone) means a
// recorded-but-since-deleted file does not wrongly suppress a legitimate
// re-import.
func (s *Scanner) alreadyImportedFormat(ctx context.Context, book *models.Book, format string) bool {
	return s.importedFormatPath(ctx, book, format) != ""
}

// importedFormatPath is alreadyImportedFormat returning the recorded path of
// the on-disk file or folder it found, or "" when there is none.
func (s *Scanner) importedFormatPath(ctx context.Context, book *models.Book, format string) string {
	if book == nil {
		return ""
	}
	files, err := s.books.ListFiles(ctx, book.ID)
	if err != nil {
		slog.Warn("idempotency check: failed to list book files", "bookID", book.ID, "error", err)
		return ""
	}
	for _, f := range files {
		if f.Format != format {
			continue
		}
		if _, statErr := os.Stat(f.Path); statErr == nil {
			return f.Path
		}
	}
	return ""
}

// existingEbookDir returns the directory holding this book's
// already-imported, still-on-disk ebook, if it has one.
//
// An ebook book_files row stores the file itself (the .epub/.mobi/…), so
// the book's folder is its parent — unlike an audiobook row, which stores
// the destination directory directly because audiobooks are placed as a
// folder unit.
//
// Used to recognise that a freshly computed audiobook destination is the
// folder this book already occupies via its ebook — the shared-folder
// layout, the default when BINDERY_AUDIOBOOK_DIR is unset — so the import
// merges into it rather than being split off by UniqueDir's collision
// disambiguation, which cannot tell "same book, other format" from a
// genuine clash with an unrelated book (#1959).
//
// A row whose file is gone from disk is ignored: there is nothing to merge
// into, so the caller should place the audiobook normally.
//
// A folder that also holds a file another live book tracks is not offered
// either (#3556). Two books that render to one ebook name share a folder
// ("Name.epub" and "Name (2).epub", #3560), and an audiobook row covers its
// whole folder, so merging would make this book's audiobook the owner of the
// other book's ebook and a later Reorganize of it would carry that ebook away.
// The audiobook gets a folder of its own instead, as it would for an
// unrelated book.
func (s *Scanner) existingEbookDir(ctx context.Context, book *models.Book) (string, bool) {
	if book == nil {
		return "", false
	}
	files, err := s.books.ListFiles(ctx, book.ID)
	if err != nil {
		slog.Warn("shared-folder check: failed to list book files", "bookID", book.ID, "error", err)
		return "", false
	}
	for _, f := range files {
		if f.Format != models.MediaTypeEbook {
			continue
		}
		if _, statErr := os.Stat(f.Path); statErr != nil {
			continue
		}
		dir := filepath.Clean(filepath.Dir(f.Path))
		if held, err := s.folderHoldsOtherBooksFiles(ctx, dir, book.ID); err != nil || held {
			if err != nil {
				slog.Warn("shared-folder check: failed to look for other books' files", "dir", dir, "error", err)
			}
			continue
		}
		return dir, true
	}
	return "", false
}

// formatForRelease resolves the media type a grabbed release is for from its
// title and the format token parsed at grab time, or "" when the release does
// not say — including when it says BOTH.
//
// It deliberately does not trust Download.Quality on its own.
// indexer.ParseRelease sets Quality to the first match in its formatTokens
// order, and that order lists every ebook container ahead of every audio one,
// so an audiobook titled "… (Unabridged) M4B + PDF booklet" — an audio file
// plus the publisher's PDF booklet, an entirely ordinary shape — arrives with
// Quality "pdf". Consulting the EBOOK slot for that download and closing it out
// as imported is #1885 with the formats swapped, which is the bug this resolver
// exists to prevent.
//
// A release naming containers of BOTH kinds is settled by whether it also
// carries an audiobook marker (unabridged/abridged, an Audible ASIN, or the
// word audiobook/audible). "M4B + PDF" with "(Unabridged)" is an audiobook
// with the publisher's booklet, not an ebook and not a coin toss. Without such
// a marker it stays unknown, and the caller's contract for "" is to decline the
// short-circuit — the safe direction (see isBookAlreadyImported).
func formatForRelease(dl *models.Download) string {
	if dl == nil {
		return ""
	}
	var audiobook, ebook bool
	mark := func(mediaType string) {
		switch mediaType {
		case models.MediaTypeAudiobook:
			audiobook = true
		case models.MediaTypeEbook:
			ebook = true
		}
	}
	for _, token := range indexer.ReleaseFormats(dl.Title) {
		mark(indexer.MediaTypeForFormat(token))
	}
	// Quality is normally one of the title's own tokens, but it is what a grab
	// recorded and a title can be edited or absent, so it still counts.
	mark(indexer.MediaTypeForFormat(dl.Quality))
	switch {
	case audiobook && !ebook:
		return models.MediaTypeAudiobook
	case ebook && !audiobook:
		return models.MediaTypeEbook
	case audiobook && ebook && audiobookMarked(dl.Title):
		return models.MediaTypeAudiobook
	default:
		return ""
	}
}

// audiobookMarked reports whether a release title advertises itself as an
// audiobook independently of its container tokens. Only used to break the tie
// when a title names both an audio and an ebook container, which in practice
// means an audiobook shipped with its PDF booklet.
func audiobookMarked(title string) bool {
	p := indexer.ParseRelease(title)
	if p.Unabridged || p.Abridged || p.ASIN != "" {
		return true
	}
	for _, marker := range []string{"audiobook", "audible"} {
		if indexer.WordBoundaryRegex(marker).MatchString(p.Normalized) {
			return true
		}
	}
	return false
}

// downloadFormat resolves which format SLOT of the book a download is working
// toward — models.MediaTypeEbook, models.MediaTypeAudiobook, or "" when it
// cannot be determined. Signals, strongest first:
//
//  0. the media type stamped on the download when it was grabbed (#1964),
//     absent on rows that predate migration 126 and on manual imports;
//  1. an explicit caller hint (manual import, drop-folder hand-off): a human or
//     an API caller declared the format outright, so it wins over inference;
//  2. the book's own media type when the book is monitored for a SINGLE format
//     — a download attached to an ebook-only book can only fill its ebook slot,
//     whatever tokens the release title happens to carry;
//  3. the release itself — its title and the format token parsed from it at
//     grab time — which is the only per-download signal available for a
//     media_type=both book. See formatForRelease: a release naming both an
//     audio and an ebook container answers "unknown", not "whichever token the
//     parser happened to keep".
//
// A dual-format book whose release names no usable format, or names both,
// resolves to "" — callers MUST treat that as "unknown", never as "either
// format" (#1885).
func downloadFormat(dl *models.Download, book *models.Book, formatHint string) string {
	// The grab's own stamp (#1964) is what the decision engine actually
	// picked, so it outranks every inference below, the caller's hint included:
	// the poll path's hint is itself recovered from a client category.
	if dl != nil && (dl.MediaType == models.MediaTypeEbook || dl.MediaType == models.MediaTypeAudiobook) {
		return dl.MediaType
	}
	if formatHint == models.MediaTypeEbook || formatHint == models.MediaTypeAudiobook {
		return formatHint
	}
	if book != nil {
		switch book.MediaType {
		case models.MediaTypeEbook, models.MediaTypeAudiobook:
			return book.MediaType
		}
	}
	return formatForRelease(dl)
}

// isBookAlreadyImported reports whether the book linked to dl already has an
// on-disk file tracked in book_files FOR THE FORMAT THIS DOWNLOAD TARGETS.
//
// It is used as a guard for duplicate-add re-grabs (#769): when a torrent is
// re-grabbed after qBittorrent already holds it (409 response), the original
// download files may have been moved to the library by a prior Bindery import
// (move mode), leaving the download path empty. Rather than failing with "no
// book files found" and burning through the retry budget, the caller can check
// this and mark the Download StateImported directly.
//
// Scoping (#1885): each title holds independent ebook/audiobook slots with
// separate pipelines, so an import of one format must never satisfy a pending
// download of the other. The download's format is resolved by downloadFormat;
// only that format's slot is consulted. When the format cannot be determined —
// a media_type=both book whose release names no format, or names one of each —
// this returns false rather than accepting either.
//
// That asymmetry is deliberate, and it is not free: declining sends the
// download down the ordinary no-book-files path into StateImportFailed. It is
// still the right side to err on. Being wrong the other way silently marks a
// still-downloading grab as imported and abandons the format with no signal to
// the operator at all, which is exactly what #1885 reported (an audiobook
// import closing out the pending ebook). Being wrong THIS way is bounded and
// visible: the download either imports when its files land, or reaches
// StateImportBlocked — via the retry budget if attempts keep failing, or via
// the skip limit if its files never appear (#1884) — where the queue shows the
// reason, Retry import is offered, and the release can be grabbed again.
//
// Note that the unknown case is not exotic. The #769 shape this guard exists
// for (a re-grabbed torrent whose files a prior import already moved) reaches
// here from the qBittorrent pollers with no format hint, and since #1884 drops
// client-listed files that are not on this host, an emptied file list is the
// ordinary way in. A dual-format book therefore leans on the release tokens
// alone — which is why formatForRelease reads the whole title rather than the
// single token the parser kept.
func (s *Scanner) isBookAlreadyImported(ctx context.Context, dl *models.Download, formatHint string) bool {
	if dl.BookID == nil {
		return false
	}
	book, err := s.books.GetByID(ctx, *dl.BookID)
	if err != nil || book == nil {
		return false
	}
	format := downloadFormat(dl, book, formatHint)
	if format == "" {
		slog.Debug("already-in-library check skipped: cannot tell which format this download targets",
			"title", dl.Title, "bookID", book.ID, "mediaType", book.MediaType, "quality", dl.Quality)
		return false
	}
	return s.alreadyImportedFormat(ctx, book, format)
}

// alreadyImportedPath reports whether the exact destination path is already
// tracked in book_files for the book and still exists on disk. This is the
// per-file idempotency guard for ebook imports, whose destination paths are
// deterministic (no UniqueDir): a retry recomputes the identical destPath, so
// a match here means that specific file already landed and must not be
// re-imported. It is intentionally per-file so a retry of a genuine PARTIAL
// failure still imports the files that never landed.
func (s *Scanner) alreadyImportedPath(ctx context.Context, book *models.Book, destPath string) bool {
	if book == nil {
		return false
	}
	files, err := s.books.ListFiles(ctx, book.ID)
	if err != nil {
		slog.Warn("idempotency check: failed to list book files", "bookID", book.ID, "error", err)
		return false
	}
	cleanDest := s.pathKey(destPath)
	for _, f := range files {
		if s.pathKey(f.Path) != cleanDest {
			continue
		}
		if _, statErr := os.Stat(f.Path); statErr == nil {
			return true
		}
	}
	return false
}
