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

// hydrateSeriesExtras fills the joined data every series list shares: the
// Hardcover link and the monitoring state.
func (r *SeriesRepo) hydrateSeriesExtras(ctx context.Context, series []models.Series) error {
	if err := r.hydrateHardcoverLinks(ctx, series); err != nil {
		return err
	}
	return r.hydrateMonitoring(ctx, series)
}

const monitoringCols = `series_id, mode, owner_user_id, last_check_at, next_check_at, last_status, last_message, last_created`

type monitoringScanner interface{ Scan(dest ...any) error }

func scanMonitoring(sc monitoringScanner) (int64, *models.SeriesMonitoring, error) {
	var id int64
	var m models.SeriesMonitoring
	var last, next sql.NullString
	if err := sc.Scan(&id, &m.Mode, &m.OwnerUserID, &last, &next, &m.LastStatus, &m.LastMessage, &m.LastCreated); err != nil {
		return 0, nil, err
	}
	var err error
	if m.LastCheckAt, err = parseFlexibleTime(last); err != nil {
		return 0, nil, err
	}
	if m.NextCheckAt, err = parseFlexibleTime(next); err != nil {
		return 0, nil, err
	}
	return id, &m, nil
}

func (r *SeriesRepo) hydrateMonitoring(ctx context.Context, series []models.Series) error {
	if len(series) == 0 {
		return nil
	}
	placeholders := make([]string, len(series))
	args := make([]any, len(series))
	for i, s := range series {
		placeholders[i] = "?"
		args[i] = s.ID
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+monitoringCols+" FROM series_monitoring WHERE series_id IN ("+strings.Join(placeholders, ",")+")", args...) // #nosec G202 -- generated ? tokens, ids stay bound
	if err != nil {
		return fmt.Errorf("list series monitoring: %w", err)
	}
	defer rows.Close()
	byID := make(map[int64]*models.SeriesMonitoring)
	for rows.Next() {
		id, m, err := scanMonitoring(rows)
		if err != nil {
			return fmt.Errorf("scan series monitoring: %w", err)
		}
		byID[id] = m
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range series {
		series[i].Monitoring = byID[series[i].ID]
	}
	return nil
}

// GetMonitoring returns the monitoring row of a series, nil when it has none.
func (r *SeriesRepo) GetMonitoring(ctx context.Context, seriesID int64) (*models.SeriesMonitoring, error) {
	_, m, err := scanMonitoring(r.db.QueryRowContext(ctx,
		"SELECT "+monitoringCols+" FROM series_monitoring WHERE series_id = ?", seriesID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get series monitoring %d: %w", seriesID, err)
	}
	return m, nil
}

// SetMonitoring turns monitoring on or off for a series and records the mode
// and owner. Turning it on or changing the mode clears next_check_at so the
// next job tick picks the series up. The monitored flag and the monitoring
// row change in one transaction, so the flag is never on without a mode.
func (r *SeriesRepo) SetMonitoring(ctx context.Context, seriesID int64, monitored bool, mode string, ownerUserID int64) error {
	if mode != models.SeriesMonitorAll {
		mode = models.SeriesMonitorUpcoming
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	val := 0
	if monitored {
		val = 1
	}
	if _, err := tx.ExecContext(ctx, "UPDATE series SET monitored=? WHERE id=?", val, seriesID); err != nil {
		return err
	}
	if monitored {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO series_monitoring (series_id, mode, owner_user_id) VALUES (?, ?, ?)
			ON CONFLICT(series_id) DO UPDATE SET owner_user_id = excluded.owner_user_id,
			  next_check_at = CASE WHEN series_monitoring.mode = excluded.mode THEN series_monitoring.next_check_at ELSE NULL END,
			  mode = excluded.mode`,
			seriesID, mode, ownerUserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const monitoringEligibleWhere = `s.monitored = 1`

// CountMonitoringEligible counts every series the job would ever visit.
func (r *SeriesRepo) CountMonitoringEligible(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM series s WHERE "+monitoringEligibleWhere).Scan(&n); err != nil {
		return 0, fmt.Errorf("count monitored series: %w", err)
	}
	return n, nil
}

// ListMonitoringDue returns up to limit monitored series whose next check is
// at or before now, never checked first, oldest cursor next.
func (r *SeriesRepo) ListMonitoringDue(ctx context.Context, now time.Time, limit int) ([]models.Series, error) {
	if limit < 1 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.foreign_id, s.title, s.description, s.monitored, s.genre_override, s.created_at
		FROM series s LEFT JOIN series_monitoring m ON m.series_id = s.id
		WHERE `+monitoringEligibleWhere+` AND (m.next_check_at IS NULL OR m.next_check_at <= ?)
		ORDER BY m.next_check_at IS NOT NULL, m.next_check_at, s.id
		LIMIT ?`, now.UTC().Format(discoveryTimeLayout), limit)
	if err != nil {
		return nil, fmt.Errorf("list series due for monitoring: %w", err)
	}
	defer rows.Close()
	var out []models.Series
	for rows.Next() {
		var s models.Series
		var monitored int
		var genre sql.NullString
		if err := rows.Scan(&s.ID, &s.ForeignID, &s.Title, &s.Description, &monitored, &genre, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan series due for monitoring: %w", err)
		}
		s.Monitored = monitored == 1
		if err := applyGenreOverride(&s, genre); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.hydrateMonitoring(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// StampCheck records one check: when it ran, how it went, how many books it
// added, and when the series is next due. An upsert that leaves mode and owner
// alone, so a user changing the mode while a check runs loses nothing.
func (r *SeriesRepo) StampCheck(ctx context.Context, seriesID int64, when, next time.Time, status, message string, created int) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO series_monitoring (series_id, last_check_at, next_check_at, last_status, last_message, last_created)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(series_id) DO UPDATE SET last_check_at = excluded.last_check_at, next_check_at = excluded.next_check_at,
		  last_status = excluded.last_status, last_message = excluded.last_message, last_created = excluded.last_created`,
		seriesID, when.UTC().Format(discoveryTimeLayout), next.UTC().Format(discoveryTimeLayout), status, message, created)
	if err != nil {
		return fmt.Errorf("stamp series check %d: %w", seriesID, err)
	}
	return nil
}

// DistinctBookOwners returns the distinct owners of the books in a series,
// used to infer whose series a legacy monitored flag belongs to.
func (r *SeriesRepo) DistinctBookOwners(ctx context.Context, seriesID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT b.owner_user_id FROM series_books sb JOIN books b ON b.id = sb.book_id
		WHERE sb.series_id = ? AND b.owner_user_id IS NOT NULL AND b.owner_user_id <> 0`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("list series book owners: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
