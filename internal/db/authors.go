package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type AuthorRepo struct {
	db   *sql.DB
	exec dbExecutor
}

func NewAuthorRepo(db *sql.DB) *AuthorRepo {
	return &AuthorRepo{db: db, exec: db}
}

// WithTx returns a clone of this repo whose tx-aware methods (GetByID,
// Update, Delete) route through tx instead of the bare *sql.DB. Used by
// calibre.Rollback to wrap a multi-repo operation in one atomic
// transaction. Methods that begin their own transaction (e.g.
// SetMonitoredSeriesIDs) stay on *sql.DB.
func (r *AuthorRepo) WithTx(tx *sql.Tx) *AuthorRepo {
	clone := *r
	clone.exec = tx
	return &clone
}

const authorSelectCols = `id, foreign_id, name, sort_name, description, image_url, disambiguation,
	       ratings_count, average_rating, monitored, quality_profile_id, metadata_profile_id, root_folder_id,
	       audiobook_root_folder_id, monitor_mode, monitor_latest_count, metadata_provider, last_metadata_refresh_at,
	       created_at, updated_at`

func (r *AuthorRepo) List(ctx context.Context) ([]models.Author, error) {
	return r.ListByUser(ctx, 0)
}

const (
	listAuthorsAll = "SELECT " + authorSelectCols + " FROM authors ORDER BY sort_name"
	// Include rows with NULL owner_user_id — these are authors created before the
	// multi-user migration ran its backfill (migration 025) or imported without a
	// user context. Excluding them causes the list to silently drop visible authors.
	listAuthorsByUser = "SELECT " + authorSelectCols + " FROM authors WHERE owner_user_id = ? OR owner_user_id IS NULL ORDER BY sort_name"
)

func (r *AuthorRepo) ListByUser(ctx context.Context, userID int64) ([]models.Author, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if userID == 0 {
		rows, err = r.db.QueryContext(ctx, listAuthorsAll)
	} else {
		rows, err = r.db.QueryContext(ctx, listAuthorsByUser, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("list authors: %w", err)
	}
	defer rows.Close()

	var authors []models.Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, err
		}
		authors = append(authors, a)
	}
	return authors, rows.Err()
}

// ListPage returns one page of the authors visible to userID, ordered by
// sort_name, alongside the total row count that matches the same filter.
// limit must be positive; offset is clamped at 0. When userID is 0 the query
// is unscoped (matches List); otherwise it matches ListByUser's predicate
// (owner_user_id = userID OR owner_user_id IS NULL).
//
// The sort_name order is backed by idx_authors_sort_name (migration 047).
func (r *AuthorRepo) ListPage(ctx context.Context, userID int64, limit, offset int) ([]models.Author, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	countQuery := "SELECT COUNT(*) FROM authors"
	listQuery := "SELECT " + authorSelectCols + " FROM authors"
	var (
		args     []any
		pageArgs []any
	)
	if userID != 0 {
		// Match ListByUser: include NULL-owner rows so pre-multiuser-migration
		// authors stay visible to every user instead of silently disappearing.
		countQuery += " WHERE owner_user_id = ? OR owner_user_id IS NULL"
		listQuery += " WHERE owner_user_id = ? OR owner_user_id IS NULL"
		args = []any{userID}
		pageArgs = []any{userID, limit, offset}
	} else {
		pageArgs = []any{limit, offset}
	}
	listQuery += " ORDER BY sort_name LIMIT ? OFFSET ?"

	var total int
	var countRow *sql.Row
	if len(args) > 0 {
		countRow = r.db.QueryRowContext(ctx, countQuery, args...)
	} else {
		countRow = r.db.QueryRowContext(ctx, countQuery)
	}
	if err := countRow.Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count authors: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, listQuery, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list authors page: %w", err)
	}
	defer rows.Close()

	var authors []models.Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, 0, err
		}
		authors = append(authors, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return authors, total, nil
}

func (r *AuthorRepo) GetByID(ctx context.Context, id int64) (*models.Author, error) {
	row := r.exec.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE id = ?`, id)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author %d: %w", id, err)
	}
	return &a, nil
}

func (r *AuthorRepo) GetByForeignID(ctx context.Context, foreignID string) (*models.Author, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE foreign_id = ?`, foreignID)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by foreign_id %s: %w", foreignID, err)
	}
	return &a, nil
}

// GetByForeignIDForUser returns the author with the given foreign_id that is
// visible to userID — i.e. owned by that user or with a NULL owner. When
// userID is 0 the search is global (same as GetByForeignID).
func (r *AuthorRepo) GetByForeignIDForUser(ctx context.Context, foreignID string, userID int64) (*models.Author, error) {
	if userID == 0 {
		return r.GetByForeignID(ctx, foreignID)
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE foreign_id = ? AND (owner_user_id = ? OR owner_user_id IS NULL)`, foreignID, userID)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by foreign_id %s: %w", foreignID, err)
	}
	return &a, nil
}

func (r *AuthorRepo) Create(ctx context.Context, a *models.Author) error {
	return r.CreateForUser(ctx, a, 0)
}

func (r *AuthorRepo) CreateForUser(ctx context.Context, a *models.Author, ownerUserID int64) error {
	now := time.Now().UTC()
	normalizeAuthorMonitorDefaults(a)
	var ownerArg any
	if ownerUserID != 0 {
		ownerArg = ownerUserID
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO authors (foreign_id, name, sort_name, description, image_url, disambiguation,
		                     ratings_count, average_rating, monitored, quality_profile_id, metadata_profile_id, root_folder_id,
		                     audiobook_root_folder_id, monitor_mode, monitor_latest_count, metadata_provider, owner_user_id,
		                     created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ForeignID, a.Name, a.SortName, a.Description, a.ImageURL, a.Disambiguation,
		a.RatingsCount, a.AverageRating, a.Monitored, a.QualityProfileID, a.MetadataProfileID, a.RootFolderID,
		a.AudiobookRootFolderID, a.MonitorMode, a.MonitorLatestCount, a.MetadataProvider, ownerArg, now, now)
	if err != nil {
		return fmt.Errorf("create author: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get author id: %w", err)
	}
	a.ID = id
	a.CreatedAt = now
	a.UpdatedAt = now
	return nil
}

// GetByDNBSyntheticName returns the synthetic DNB-only author row (one whose
// foreign_id starts with "dnb:author:") that matches the given sort_name
// case-insensitively and is visible to userID. Returns (nil, nil) when none
// exists.
//
// This is used by AddBook to detect when a canonical author (OpenLibrary /
// Hardcover) is being added for a SortName that was previously persisted as
// a synthetic DNB row — see UpgradeSyntheticDNB.
//
// When userID is 0 the lookup is unscoped; in practice multi-user installs
// always pass the requesting user's ID so they don't migrate another user's
// row.
func (r *AuthorRepo) GetByDNBSyntheticName(ctx context.Context, sortName string, userID int64) (*models.Author, error) {
	if sortName == "" {
		return nil, nil
	}
	var (
		row *sql.Row
		q   = `SELECT ` + authorSelectCols + `
			FROM authors
			WHERE foreign_id LIKE 'dnb:author:%' AND LOWER(sort_name) = LOWER(?)`
	)
	if userID == 0 {
		row = r.db.QueryRowContext(ctx, q, sortName)
	} else {
		q += ` AND (owner_user_id = ? OR owner_user_id IS NULL)`
		row = r.db.QueryRowContext(ctx, q, sortName, userID)
	}
	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by dnb-synthetic sort_name %q: %w", sortName, err)
	}
	return &a, nil
}

// UpgradeSyntheticDNB migrates a synthetic DNB-only author row to a canonical
// provider identity. The row identified by currentForeignID has its
// foreign_id, metadata_provider and (when non-empty in target) descriptive
// fields replaced. Existing relations (books, aliases) keep pointing at the
// same primary-key row so the user keeps one author record.
//
// currentForeignID is matched by exact equality; pass the value previously
// returned by GetByDNBSyntheticName. target carries the canonical fields.
// Returns an error only on a SQL failure — a no-op update (e.g. row gone)
// is silent.
func (r *AuthorRepo) UpgradeSyntheticDNB(ctx context.Context, currentForeignID string, target *models.Author) error {
	if currentForeignID == "" || target == nil || target.ForeignID == "" {
		return fmt.Errorf("upgrade synthetic dnb: missing currentForeignID or target")
	}
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE authors
		SET foreign_id        = ?,
		    name              = COALESCE(NULLIF(?, ''), name),
		    sort_name         = COALESCE(NULLIF(?, ''), sort_name),
		    description       = CASE WHEN ? != '' THEN ? ELSE description END,
		    image_url         = CASE WHEN ? != '' THEN ? ELSE image_url END,
		    disambiguation    = CASE WHEN ? != '' THEN ? ELSE disambiguation END,
		    metadata_provider = COALESCE(NULLIF(?, ''), metadata_provider),
		    updated_at        = ?
		WHERE foreign_id = ?`,
		target.ForeignID,                       // foreign_id =
		target.Name,                            // name = COALESCE(NULLIF(?,''), name)
		target.SortName,                        // sort_name = COALESCE(NULLIF(?,''), sort_name)
		target.Description, target.Description, // description CASE WHEN ? != '' THEN ?
		target.ImageURL, target.ImageURL, // image_url
		target.Disambiguation, target.Disambiguation, // disambiguation
		target.MetadataProvider, // metadata_provider = COALESCE(NULLIF(?,''), metadata_provider)
		now,                     // updated_at
		currentForeignID)        // WHERE foreign_id = ?
	if err != nil {
		return fmt.Errorf("upgrade synthetic dnb author %q -> %q: %w", currentForeignID, target.ForeignID, err)
	}
	return nil
}

func (r *AuthorRepo) Update(ctx context.Context, a *models.Author) error {
	now := time.Now().UTC()
	normalizeAuthorMonitorDefaults(a)
	_, err := r.exec.ExecContext(ctx, `
		UPDATE authors SET foreign_id=?, name=?, sort_name=?, description=?, image_url=?, disambiguation=?,
		                   ratings_count=?, average_rating=?, monitored=?, quality_profile_id=?,
		                   metadata_profile_id=?, root_folder_id=?, audiobook_root_folder_id=?, monitor_mode=?,
		                   monitor_latest_count=?, metadata_provider=?, last_metadata_refresh_at=?, updated_at=?
		WHERE id=?`,
		a.ForeignID, a.Name, a.SortName, a.Description, a.ImageURL, a.Disambiguation,
		a.RatingsCount, a.AverageRating, a.Monitored, a.QualityProfileID,
		a.MetadataProfileID, a.RootFolderID, a.AudiobookRootFolderID, a.MonitorMode,
		a.MonitorLatestCount, a.MetadataProvider, a.LastMetadataRefreshAt, now, a.ID)
	if err != nil {
		return fmt.Errorf("update author %d: %w", a.ID, err)
	}
	a.UpdatedAt = now
	return nil
}

func (r *AuthorRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.exec.ExecContext(ctx, "DELETE FROM authors WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete author %d: %w", id, err)
	}
	return nil
}

// ListMonitoredSeriesIDs returns the series IDs the author is pinned to when
// MonitorMode == AuthorMonitorModeSeries. Returns an empty slice (not nil)
// when nothing is pinned so the JSON encoder produces `[]` rather than null,
// which keeps the UI's chip list happy.
func (r *AuthorRepo) ListMonitoredSeriesIDs(ctx context.Context, authorID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT series_id FROM author_monitored_series WHERE author_id = ? ORDER BY series_id`, authorID)
	if err != nil {
		return nil, fmt.Errorf("list monitored series ids for author %d: %w", authorID, err)
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan monitored series id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetMonitoredSeriesIDs replaces the author's monitored-series selection
// atomically. Passing an empty slice clears the selection. Callers must
// validate that every ID belongs to a series the author actually has books in
// before calling — this repo trusts its inputs.
func (r *AuthorRepo) SetMonitoredSeriesIDs(ctx context.Context, authorID int64, seriesIDs []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set monitored series tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM author_monitored_series WHERE author_id = ?`, authorID); err != nil {
		return fmt.Errorf("clear monitored series for author %d: %w", authorID, err)
	}

	if len(seriesIDs) > 0 {
		now := time.Now().UTC()
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO author_monitored_series (author_id, series_id, created_at) VALUES (?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare insert monitored series: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		// Dedupe input — callers may pass duplicates and we want a clean PK insert.
		seen := make(map[int64]struct{}, len(seriesIDs))
		for _, id := range seriesIDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			if _, err := stmt.ExecContext(ctx, authorID, id, now); err != nil {
				return fmt.Errorf("insert monitored series (%d, %d): %w", authorID, id, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set monitored series: %w", err)
	}
	return nil
}

func scanAuthor(rows *sql.Rows) (models.Author, error) {
	var a models.Author
	var monitored int
	err := rows.Scan(&a.ID, &a.ForeignID, &a.Name, &a.SortName, &a.Description, &a.ImageURL,
		&a.Disambiguation, &a.RatingsCount, &a.AverageRating, &monitored,
		&a.QualityProfileID, &a.MetadataProfileID, &a.RootFolderID, &a.AudiobookRootFolderID,
		&a.MonitorMode, &a.MonitorLatestCount, &a.MetadataProvider,
		&a.LastMetadataRefreshAt, &a.CreatedAt, &a.UpdatedAt)
	a.Monitored = monitored == 1
	normalizeAuthorMonitorDefaults(&a)
	return a, err
}

func scanAuthorRow(row *sql.Row) (models.Author, error) {
	var a models.Author
	var monitored int
	err := row.Scan(&a.ID, &a.ForeignID, &a.Name, &a.SortName, &a.Description, &a.ImageURL,
		&a.Disambiguation, &a.RatingsCount, &a.AverageRating, &monitored,
		&a.QualityProfileID, &a.MetadataProfileID, &a.RootFolderID, &a.AudiobookRootFolderID,
		&a.MonitorMode, &a.MonitorLatestCount, &a.MetadataProvider,
		&a.LastMetadataRefreshAt, &a.CreatedAt, &a.UpdatedAt)
	a.Monitored = monitored == 1
	normalizeAuthorMonitorDefaults(&a)
	return a, err
}

func normalizeAuthorMonitorDefaults(a *models.Author) {
	if a == nil {
		return
	}
	if !models.IsAuthorMonitorModeValid(a.MonitorMode) {
		a.MonitorMode = models.DefaultAuthorMonitorMode
	}
	if a.MonitorLatestCount <= 0 {
		a.MonitorLatestCount = models.DefaultAuthorMonitorLatestCount
	}
}
