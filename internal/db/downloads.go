package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type DownloadRepo struct {
	db *sql.DB
}

const downloadSelectColumns = `
	id, guid, book_id, edition_id, indexer_id, download_client_id,
	title, nzb_url, size, sabnzbd_nzo_id, torrent_id, status, protocol,
	quality, indexer_flags, error_message, added_at, grabbed_at, completed_at, imported_at,
	import_retry_count`

func NewDownloadRepo(db *sql.DB) *DownloadRepo {
	return &DownloadRepo{db: db}
}

func (r *DownloadRepo) List(ctx context.Context) ([]models.Download, error) {
	return r.query(ctx, "SELECT "+downloadSelectColumns+" FROM downloads ORDER BY added_at DESC")
}

func (r *DownloadRepo) ListByUser(ctx context.Context, userID int64) ([]models.Download, error) {
	where, args := QueryScope("", userID)
	q := "SELECT " + downloadSelectColumns + " FROM downloads " + where + " ORDER BY added_at DESC"
	return r.query(ctx, q, args...)
}

func (r *DownloadRepo) ListByStatus(ctx context.Context, status models.DownloadState) ([]models.Download, error) {
	return r.query(ctx, "SELECT "+downloadSelectColumns+" FROM downloads WHERE status=? ORDER BY added_at DESC", status)
}

func (r *DownloadRepo) ListByStatusAndUser(ctx context.Context, status models.DownloadState, userID int64) ([]models.Download, error) {
	where, args := QueryScope("WHERE status=?", userID, status)
	return r.query(ctx, "SELECT "+downloadSelectColumns+" FROM downloads "+where+" ORDER BY added_at DESC", args...)
}

func (r *DownloadRepo) GetByGUID(ctx context.Context, guid string) (*models.Download, error) {
	dl, err := r.query(ctx, "SELECT "+downloadSelectColumns+" FROM downloads WHERE guid=?", guid)
	if err != nil || len(dl) == 0 {
		return nil, err
	}
	return &dl[0], nil
}

func (r *DownloadRepo) GetByNzoID(ctx context.Context, nzoID string) (*models.Download, error) {
	dl, err := r.query(ctx, "SELECT "+downloadSelectColumns+" FROM downloads WHERE sabnzbd_nzo_id=?", nzoID)
	if err != nil || len(dl) == 0 {
		return nil, err
	}
	return &dl[0], nil
}

func (r *DownloadRepo) GetByTorrentID(ctx context.Context, torrentID string) (*models.Download, error) {
	torrentID = strings.ToLower(torrentID)
	dl, err := r.query(ctx, "SELECT "+downloadSelectColumns+" FROM downloads WHERE torrent_id=?", torrentID)
	if err != nil || len(dl) == 0 {
		return nil, err
	}
	return &dl[0], nil
}

func (r *DownloadRepo) Create(ctx context.Context, d *models.Download) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO downloads (guid, book_id, edition_id, indexer_id, download_client_id,
		                       title, nzb_url, size, sabnzbd_nzo_id, torrent_id, status, protocol,
		                       quality, indexer_flags, error_message, added_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.GUID, d.BookID, d.EditionID, d.IndexerID, d.DownloadClientID,
		d.Title, d.NZBURL, d.Size, d.SABnzbdNzoID, d.TorrentID, d.Status, d.Protocol,
		d.Quality, d.IndexerFlags, d.ErrorMessage, now)
	if err != nil {
		return fmt.Errorf("create download: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get download id: %w", err)
	}
	d.ID = id
	d.AddedAt = now
	return nil
}

// RetryFailed atomically claims an existing failed download for a manual retry.
// It returns false when the row is no longer in the failed state.
func (r *DownloadRepo) RetryFailed(ctx context.Context, d *models.Download) (bool, error) {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		UPDATE downloads
		SET book_id=?,
		    edition_id=?,
		    indexer_id=?,
		    download_client_id=?,
		    title=?,
		    nzb_url=?,
		    size=?,
		    sabnzbd_nzo_id=NULL,
		    torrent_id=NULL,
		    status=?,
		    protocol=?,
		    quality=?,
		    indexer_flags=?,
		    error_message='',
		    added_at=?,
		    grabbed_at=NULL,
		    completed_at=NULL,
		    imported_at=NULL,
		    import_retry_count=0
		WHERE id=? AND status=?`,
		d.BookID, d.EditionID, d.IndexerID, d.DownloadClientID,
		d.Title, d.NZBURL, d.Size, models.StateGrabbed, d.Protocol,
		d.Quality, d.IndexerFlags, now, d.ID, models.StateFailed)
	if err != nil {
		return false, fmt.Errorf("retry failed download: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("retry failed download rows affected: %w", err)
	}
	if affected == 0 {
		return false, nil
	}
	d.Status = models.StateGrabbed
	d.SABnzbdNzoID = nil
	d.TorrentID = nil
	d.ErrorMessage = ""
	d.AddedAt = now
	d.GrabbedAt = nil
	d.CompletedAt = nil
	d.ImportedAt = nil
	d.ImportRetryCount = 0
	return true, nil
}

// ResetImportRetry atomically re-enables scanner retries for a download stuck
// in StateImportFailed. It returns accepted=false when the row exists but is in
// another state, and found=false when no row exists.
func (r *DownloadRepo) ResetImportRetry(ctx context.Context, id int64) (accepted bool, found bool, err error) {
	result, err := r.db.ExecContext(ctx,
		"UPDATE downloads SET import_retry_count=0 WHERE id=? AND status=?",
		id, models.StateImportFailed)
	if err != nil {
		return false, false, fmt.Errorf("reset import retry: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, false, fmt.Errorf("reset import retry rows affected: %w", err)
	}
	if affected > 0 {
		return true, true, nil
	}

	var existingID int64
	if err := r.db.QueryRowContext(ctx, "SELECT id FROM downloads WHERE id=?", id).Scan(&existingID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("lookup download for import retry: %w", err)
	}
	return false, true, nil
}

// UpdateStatus validates the transition from the current state to next and
// applies it, also stamping the per-state timestamp column when one is
// associated with the destination state.
//
// Invariant (Wave 4 / finding 22): grabbed_at MUST be populated whenever the
// download has ever entered StateGrabbed or any later in-flight state. The
// stall detector and queue UI both filter on grabbed_at being non-NULL, so a
// row that leaves it NULL is invisible to stall detection and shows a blank
// "grabbed" timestamp in the queue.
//
// Historically StateGrabbed itself was only set by Create (which writes
// added_at, not grabbed_at) and the StateDownloading branch below was the
// only path that stamped grabbed_at. A direct StateGrabbed -> StateCompleted
// hop (#769 duplicate-add fast-path: qBittorrent reports the torrent already
// at 100 percent and the scanner skips Downloading) therefore left grabbed_at
// NULL forever. Auto-stamping it here whenever the row transitions out of
// StateGrabbed without it ever having been set fixes the wedge without
// requiring every caller to remember.
//
// SetGrabbedAt remains available for callers that need to restore a historical
// value (backup replay, manual fixup). The auto-stamp only fires when
// grabbed_at IS NULL, so it never clobbers a value SetGrabbedAt has written.
func (r *DownloadRepo) UpdateStatus(ctx context.Context, id int64, next models.DownloadState) error {
	// Look up the current state to validate the transition.
	var current models.DownloadState
	err := r.db.QueryRowContext(ctx, "SELECT status FROM downloads WHERE id=?", id).Scan(&current)
	if err != nil {
		return fmt.Errorf("lookup current state: %w", err)
	}
	if !current.CanTransitionTo(next) {
		return models.ErrInvalidTransition{From: current, To: next}
	}

	now := time.Now().UTC()
	switch next {
	case models.StateDownloading:
		// Stamp grabbed_at on the Grabbed -> Downloading transition. The
		// COALESCE preserves an earlier SetGrabbedAt value (e.g. a replayed
		// historical timestamp) so an explicit override is never clobbered.
		_, err = r.db.ExecContext(ctx,
			"UPDATE downloads SET status=?, grabbed_at=COALESCE(grabbed_at, ?) WHERE id=?",
			next, now, id)
	case models.StateCompleted:
		// Backfill grabbed_at on the duplicate-add fast-path (#769 and finding
		// 22): a torrent that was already at 100 percent jumps straight from
		// StateGrabbed to StateCompleted without ever passing through
		// StateDownloading. Without the backfill the row stays invisible to
		// the stall detector (which filters on grabbed_at IS NOT NULL) and
		// shows an empty Grabbed column in the queue UI.
		_, err = r.db.ExecContext(ctx,
			"UPDATE downloads SET status=?, completed_at=?, grabbed_at=COALESCE(grabbed_at, ?) WHERE id=?",
			next, now, now, id)
	case models.StateImported:
		_, err = r.db.ExecContext(ctx, "UPDATE downloads SET status=?, imported_at=? WHERE id=?", next, now, id)
	default:
		// StateGrabbed -> StateFailed (couldn't send to client) deliberately
		// leaves grabbed_at NULL: nothing was ever grabbed. All other forward
		// transitions stay in the import lifecycle, which is gated by the
		// validTransitions table and only reachable after StateCompleted has
		// already backfilled grabbed_at above.
		_, err = r.db.ExecContext(ctx, "UPDATE downloads SET status=? WHERE id=?", next, id)
	}
	return err
}

// SetGrabbedAt overrides the grabbed_at timestamp for a download. It exists
// for callers that need to restore a historical value (backup replay, manual
// fixup); the normal forward state machine in UpdateStatus auto-stamps the
// column on the way through StateGrabbed and never overwrites a value already
// set by SetGrabbedAt.
func (r *DownloadRepo) SetGrabbedAt(ctx context.Context, id int64, t time.Time) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE downloads SET grabbed_at=? WHERE id=?", t.UTC(), id)
	return err
}

func (r *DownloadRepo) SetNzoID(ctx context.Context, id int64, nzoID string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE downloads SET sabnzbd_nzo_id=? WHERE id=?", nzoID, id)
	return err
}

func (r *DownloadRepo) SetTorrentID(ctx context.Context, id int64, torrentID string) error {
	torrentID = strings.ToLower(torrentID)
	_, err := r.db.ExecContext(ctx, "UPDATE downloads SET torrent_id=? WHERE id=?", torrentID, id)
	return err
}

func (r *DownloadRepo) SetError(ctx context.Context, id int64, errMsg string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE downloads SET status=?, error_message=? WHERE id=?",
		models.StateFailed, errMsg, id)
	return err
}

// SetErrorWithStatus transitions the download to the given failure state and
// persists the error message. Use this for import failures (StateImportFailed,
// StateImportBlocked) so the user can see why an import didn't complete.
func (r *DownloadRepo) SetErrorWithStatus(ctx context.Context, id int64, status models.DownloadState, errMsg string) error {
	var current models.DownloadState
	if err := r.db.QueryRowContext(ctx, "SELECT status FROM downloads WHERE id=?", id).Scan(&current); err != nil {
		return fmt.Errorf("lookup current state: %w", err)
	}
	if current != status && !current.CanTransitionTo(status) {
		return models.ErrInvalidTransition{From: current, To: status}
	}
	_, err := r.db.ExecContext(ctx,
		"UPDATE downloads SET status=?, error_message=? WHERE id=?",
		status, errMsg, id)
	return err
}

// IncrementImportRetryCount bumps the import_retry_count for the download by
// one. It is called just before each automatic import retry (Bug #7) so the
// retry cap can be enforced on subsequent check cycles.
func (r *DownloadRepo) IncrementImportRetryCount(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE downloads SET import_retry_count = import_retry_count + 1 WHERE id=?", id)
	return err
}

// RecoverInterruptedImports moves every download stuck in a non-terminal,
// non-resumable import state (StateImporting, StateImportPending) to
// StateImportFailed so the scanner's retry path (which only fires from
// StateImportFailed) can pick them up. It is called once on startup: a process
// crash or timeout mid-import leaves the download in StateImporting/Pending
// with no automatic re-entry, wedging it forever (issue #706 finding 1).
//
// StateImportExternal is intentionally NOT swept — it is a legitimate
// long-lived parked state awaiting reconciliation by ScanLibrary, not a
// crash artefact.
//
// It returns the IDs of the downloads it transitioned so the caller can log /
// emit history events for them.
func (r *DownloadRepo) RecoverInterruptedImports(ctx context.Context) ([]int64, error) {
	ids, err := r.interruptedImportIDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Both StateImporting and StateImportPending can legally transition to
	// StateImportFailed (see validTransitions), so a single guarded UPDATE is
	// safe. The error_message records why the state moved so the cause is
	// visible in the Queue/History UI.
	const msg = "import interrupted (process restart) — re-queued for retry"
	recovered := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, err := r.db.ExecContext(ctx,
			"UPDATE downloads SET status=?, error_message=? WHERE id=? AND status IN (?, ?)",
			models.StateImportFailed, msg, id, models.StateImporting, models.StateImportPending); err != nil {
			return recovered, fmt.Errorf("recover interrupted import %d: %w", id, err)
		}
		recovered = append(recovered, id)
	}
	return recovered, nil
}

// interruptedImportIDs returns the ids of downloads stuck mid-import. It is a
// separate function so the result set is closed (via defer) before the caller
// issues its recovery UPDATEs — the pool is single-connection, so an open
// query would block the writes.
func (r *DownloadRepo) interruptedImportIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id FROM downloads WHERE status IN (?, ?)",
		models.StateImporting, models.StateImportPending)
	if err != nil {
		return nil, fmt.Errorf("list interrupted imports: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan interrupted import id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate interrupted imports: %w", err)
	}
	return ids, nil
}

// RecoverWedgedCompleted finds downloads stuck in StateCompleted that never
// progressed to import and re-queues them as StateImportPending so the normal
// CheckDownloads tick will pick them up.
//
// Background (Wave 4 / finding 21): the state machine allows
// StateCompleted -> StateImportPending, but the transition is only driven by
// the scanner's tick after the download client reports the file ready. If the
// process restarts between StateCompleted being set and the scanner moving
// the row on, nothing else ever transitions it: the row sits in Completed
// forever, the file may already be on the seed/download path, and the user
// sees "not imported" in the queue with no obvious cause.
//
// Behaviour:
//   - Only StateCompleted rows whose import_retry_count is below
//     importRetryLimit are touched. A row that has already been retried the
//     full budget is left in Completed and logged at ERROR by the caller, so
//     we never loop on the same broken row across restart cycles.
//   - At most cap rows are re-queued per call. Setting cap <= 0 disables the
//     cap. The caller passes a small number so a database with thousands of
//     wedged rows does not thundering-herd the importer on first boot after
//     upgrade.
//   - The row's import_retry_count is bumped so the cap is enforced even when
//     the scanner later runs out of attempts; see CheckDownloads for the
//     consumption side.
//
// Returns the IDs of the rows it re-queued so the caller can log them and
// emit history events.
func (r *DownloadRepo) RecoverWedgedCompleted(ctx context.Context, retryLimit, cap int) ([]int64, error) {
	ids, err := r.wedgedCompletedIDs(ctx, retryLimit, cap)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	const msg = "import never started (process restart before scanner tick) — re-queued for retry"
	recovered := make([]int64, 0, len(ids))
	for _, id := range ids {
		// Bump import_retry_count atomically with the state change so a row
		// that has already burned its budget cannot be re-queued by the next
		// startup sweep. The guarded WHERE ensures we never accidentally move
		// a row that has been transitioned out of StateCompleted in the
		// meantime (e.g. by a concurrent CheckDownloads tick).
		if _, err := r.db.ExecContext(ctx, `
			UPDATE downloads
			SET status=?,
			    error_message=?,
			    import_retry_count = import_retry_count + 1
			WHERE id=? AND status=?`,
			models.StateImportPending, msg, id, models.StateCompleted); err != nil {
			return recovered, fmt.Errorf("recover wedged completed %d: %w", id, err)
		}
		recovered = append(recovered, id)
	}
	return recovered, nil
}

// wedgedCompletedIDs returns the ids of downloads that look stuck in
// StateCompleted: status is Completed, the retry budget has room, and no
// book_files row has been written for the associated book yet. The last
// condition rules out a row whose import actually landed but whose terminal
// state-update lost the race (would otherwise re-import a file that is
// already on disk). The result set is closed via defer before the caller
// runs UPDATEs against the single-writer connection pool.
func (r *DownloadRepo) wedgedCompletedIDs(ctx context.Context, retryLimit, cap int) ([]int64, error) {
	q := `
		SELECT d.id
		FROM downloads d
		WHERE d.status = ?
		  AND d.import_retry_count < ?
		  AND (
		    d.book_id IS NULL OR NOT EXISTS (
		      SELECT 1 FROM book_files bf WHERE bf.book_id = d.book_id
		    )
		  )
		ORDER BY d.id`
	args := []interface{}{models.StateCompleted, retryLimit}
	if cap > 0 {
		q += " LIMIT ?"
		args = append(args, cap)
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list wedged completed: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan wedged completed id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wedged completed: %w", err)
	}
	return ids, nil
}

// CountWedgedCompletedOverRetryLimit returns the number of StateCompleted
// rows whose import_retry_count has reached or exceeded retryLimit. The
// caller uses this to log an ERROR on startup so an operator can see the
// rows that the boot reconciliation deliberately skipped (Wave 4 / finding 21).
func (r *DownloadRepo) CountWedgedCompletedOverRetryLimit(ctx context.Context, retryLimit int) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM downloads d
		WHERE d.status = ?
		  AND d.import_retry_count >= ?
		  AND (
		    d.book_id IS NULL OR NOT EXISTS (
		      SELECT 1 FROM book_files bf WHERE bf.book_id = d.book_id
		    )
		  )`, models.StateCompleted, retryLimit).Scan(&n); err != nil {
		return 0, fmt.Errorf("count over-limit wedged completed: %w", err)
	}
	return n, nil
}

func (r *DownloadRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM downloads WHERE id=?", id)
	return err
}

// GetOwnerByID returns the owner_user_id of a download. The second return
// value reports whether the row exists; the int64 may be 0 even when the row
// exists if the column is NULL (pre-migration-025 data, or a row inserted by
// a code path that does not populate the FK yet). Callers in the auth path
// should map a missing row to 404 and pass the (possibly zero) owner id to
// auth.CheckOwnership, which has explicit semantics for the 0-owner case.
func (r *DownloadRepo) GetOwnerByID(ctx context.Context, id int64) (int64, bool, error) {
	var owner sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		"SELECT owner_user_id FROM downloads WHERE id=?", id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("get download owner: %w", err)
	}
	return owner.Int64, true, nil
}

func (r *DownloadRepo) DeleteByBook(ctx context.Context, bookID int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM downloads WHERE book_id=?", bookID)
	return err
}

func (r *DownloadRepo) query(ctx context.Context, q string, args ...interface{}) ([]models.Download, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var downloads []models.Download
	for rows.Next() {
		var d models.Download
		if err := rows.Scan(
			&d.ID, &d.GUID, &d.BookID, &d.EditionID, &d.IndexerID, &d.DownloadClientID,
			&d.Title, &d.NZBURL, &d.Size, &d.SABnzbdNzoID, &d.TorrentID, &d.Status, &d.Protocol,
			&d.Quality, &d.IndexerFlags, &d.ErrorMessage,
			&d.AddedAt, &d.GrabbedAt, &d.CompletedAt, &d.ImportedAt,
			&d.ImportRetryCount,
		); err != nil {
			return nil, fmt.Errorf("scan download: %w", err)
		}
		downloads = append(downloads, d)
	}
	return downloads, rows.Err()
}
