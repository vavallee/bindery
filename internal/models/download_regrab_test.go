package models

import (
	"testing"
	"time"
)

// TestBlocksRegrab covers the gate both the manual grab (api.regrabbable) and
// the scheduler's auto grab apply to an existing row for a release's GUID
// (#2710). Live work blocks; a finished attempt and an orphaned import do not.
func TestBlocksRegrab(t *testing.T) {
	bookID := int64(7)
	for _, tc := range []struct {
		name string
		dl   *Download
		want bool
	}{
		{"no row at all", nil, false},
		{"failed", &Download{Status: StateFailed}, false},
		{"importBlocked", &Download{Status: StateImportBlocked}, false},
		{"orphaned import", &Download{Status: StateImported}, false},
		{"imported with its book", &Download{Status: StateImported, BookID: &bookID}, true},
		{"grabbed", &Download{Status: StateGrabbed}, true},
		{"downloading with no book yet", &Download{Status: StateDownloading}, true},
		{"completed", &Download{Status: StateCompleted}, true},
		{"importing", &Download{Status: StateImporting}, true},
		{"importFailed, still being retried", &Download{Status: StateImportFailed}, true},
		{"handed off to an external tool", &Download{Status: StateImportExternal}, true},
		{"held for its sibling format", &Download{Status: StateImportHeld}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.dl.BlocksRegrab(); got != tc.want {
				t.Errorf("BlocksRegrab() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLastActivityAt pins the order the scheduler's re-grab cooldown measures
// from. downloads has no updated_at, so the most recent of the three stamped
// columns stands in for "when did this attempt end", and completed_at wins
// over the row's age: an import that failed a minute ago on a row created
// months ago is a recent attempt, not an old one.
func TestLastActivityAt(t *testing.T) {
	added := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	grabbed := added.Add(time.Hour)
	completed := added.Add(2 * time.Hour)

	var missing *Download
	if got := missing.LastActivityAt(); !got.IsZero() {
		t.Errorf("no row has no activity, got %v", got)
	}
	for _, tc := range []struct {
		name string
		dl   *Download
		want time.Time
	}{
		{"never grabbed", &Download{AddedAt: added}, added},
		{"grabbed, never completed", &Download{AddedAt: added, GrabbedAt: &grabbed}, grabbed},
		{"completed", &Download{AddedAt: added, GrabbedAt: &grabbed, CompletedAt: &completed}, completed},
		{"completed without a grab stamp", &Download{AddedAt: added, CompletedAt: &completed}, completed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.dl.LastActivityAt(); !got.Equal(tc.want) {
				t.Errorf("LastActivityAt() = %v, want %v", got, tc.want)
			}
		})
	}
}
