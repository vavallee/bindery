package db

import (
	"context"
	"database/sql"
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
	quality, indexer_flags, error_message, added_at, grabbed_at, completed_at, imported_at`

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
		); err != nil {
			return nil, fmt.Errorf("scan download: %w", err)
		}
		downloads = append(downloads, d)
	}
	return downloads, rows.Err()
}
