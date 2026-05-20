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
		_, err = r.db.ExecContext(ctx, "UPDATE downloads SET status=?, grabbed_at=? WHERE id=?", next, now, id)
	case models.StateCompleted:
		_, err = r.db.ExecContext(ctx, "UPDATE downloads SET status=?, completed_at=? WHERE id=?", next, now, id)
	case models.StateImported:
		_, err = r.db.ExecContext(ctx, "UPDATE downloads SET status=?, imported_at=? WHERE id=?", next, now, id)
	default:
		_, err = r.db.ExecContext(ctx, "UPDATE downloads SET status=? WHERE id=?", next, id)
	}
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
	rows, err := r.db.QueryContext(ctx,
		"SELECT id FROM downloads WHERE status IN (?, ?)",
		models.StateImporting, models.StateImportPending)
	if err != nil {
		return nil, fmt.Errorf("list interrupted imports: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan interrupted import id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate interrupted imports: %w", err)
	}
	rows.Close()
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

func (r *DownloadRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM downloads WHERE id=?", id)
	return err
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
