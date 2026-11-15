package main

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// retentionWindow is the cutoff after which an install row that has stopped
// pinging is considered dormant and deleted. Picked to be generous enough
// that an install on a NAS the user only boots every couple of months
// survives, but tight enough that the table doesn't accumulate ghost rows
// forever. The active-install counts on the dashboard use shorter windows
// (7d, 30d); 60d is purely about garbage collection.
const retentionWindow = 60 * 24 * time.Hour

// retentionInterval is the cadence for sweepStaleAndDev when running as a
// daemon. Picked to be much smaller than the retention window so a dormant
// install drops off within a day of crossing the threshold.
var retentionInterval = 24 * time.Hour

// sweepStaleAndDev drops two kinds of row in one pass:
//
//  1. Anything with last_seen older than retentionWindow. Once an install
//     has been silent for 60 days, the next ping (if any) will treat it as
//     a brand new install. We don't lose anything by removing the row.
//  2. Dev/test rows: version literal "dev" plus install_ids that don't
//     match the UUID v4 shape. The server rejects new posts of these (see
//     handlePing) but older clients still get through and the startup-only
//     sweep doesn't catch them after boot. Periodic cleanup closes the gap.
//
// Returns the number of rows deleted across both kinds.
func sweepStaleAndDev(ctx context.Context, db *sql.DB) (int64, error) {
	cutoff := time.Now().UTC().Add(-retentionWindow)
	res, err := db.ExecContext(ctx, `
		DELETE FROM installs
		WHERE last_seen < ?
		   OR version = 'dev'
		   OR install_id NOT GLOB '????????-????-????-????-????????????'
	`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	// Ledger rows age out on their own, longer, clock — they are the
	// per-day history the aggregates are rebuilt from, not live state.
	ledgerCutoff := time.Now().UTC().Add(-ledgerRetentionWindow).Format("2006-01-02")
	resL, err := db.ExecContext(ctx,
		`DELETE FROM daily_activity WHERE day < ?`, ledgerCutoff)
	if err != nil {
		return 0, err
	}
	nL, _ := resL.RowsAffected()
	return n + nL, nil
}

// runRetentionLoop fires sweepStaleAndDev immediately, then every
// retentionInterval until ctx is cancelled. Logged at info on each pass so
// operator can see the count in pod logs.
func (s *server) runRetentionLoop(ctx context.Context) {
	tick := time.NewTicker(retentionInterval)
	defer tick.Stop()
	for {
		n, err := sweepStaleAndDev(ctx, s.db)
		if err != nil {
			slog.Warn("retention sweep failed", "error", err)
		} else if n > 0 {
			slog.Info("retention sweep", "rows_deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
