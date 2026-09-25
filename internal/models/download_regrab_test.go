package models

import (
	"testing"
	"time"
)

// TestBlocksRegrab covers the gate the manual grab (api.regrabbable) applies
// to an existing row for a release's GUID (#2710). Live work blocks; a
// finished attempt and an orphaned import do not.
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

// TestBlocksAutoRegrab is the scheduler's gate. It differs from the manual one
// in exactly one state: importBlocked, whose files are still on disk and whose
// re-download is a choice for a person to make, not for an unattended sweep.
func TestBlocksAutoRegrab(t *testing.T) {
	bookID := int64(7)
	blocked := map[DownloadState]bool{
		StateGrabbed:        true,
		StateDownloading:    true,
		StateCompleted:      true,
		StateImportPending:  true,
		StateImporting:      true,
		StateImported:       true, // with its book; the orphan case is below
		StateImportFailed:   true,
		StateImportBlocked:  true,
		StateImportExternal: true,
		StateImportHeld:     true,
		StateFailed:         false,
	}
	for _, s := range AllStates() {
		want, ok := blocked[s]
		if !ok {
			t.Fatalf("state %q is not covered here; decide whether the sweep may re-grab it", s)
		}
		dl := &Download{Status: s, BookID: &bookID}
		if got := dl.BlocksAutoRegrab(); got != want {
			t.Errorf("BlocksAutoRegrab(%q) = %v, want %v", s, got, want)
		}
	}
	if (&Download{Status: StateImported}).BlocksAutoRegrab() {
		t.Error("an orphaned import must not block the sweep (#2289)")
	}
	var missing *Download
	if missing.BlocksAutoRegrab() {
		t.Error("no row at all blocks nothing")
	}
	// The one difference from the manual gate, stated as a test so a change to
	// either predicate has to come past it.
	if (&Download{Status: StateImportBlocked}).BlocksRegrab() {
		t.Error("the manual grab must still allow a blocked row (#1955)")
	}
}

// TestDeadSince pins the instant the scheduler's re-grab cooldown measures
// from: dead_at, the stamp written when the row died. The fallback chain is
// COALESCE precedence, not a maximum, and every column in it belongs to the
// attempt's BEGINNING, which is why dead_at had to exist at all: a row grabbed
// at noon and failed at ten in the evening would otherwise count its cooldown
// from noon and be eligible the moment it died.
func TestDeadSince(t *testing.T) {
	added := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	grabbed := added.Add(time.Hour)
	completed := added.Add(2 * time.Hour)
	died := added.Add(10 * time.Hour)

	var missing *Download
	if got := missing.DeadSince(); !got.IsZero() {
		t.Errorf("no row has no death, got %v", got)
	}
	for _, tc := range []struct {
		name string
		dl   *Download
		want time.Time
	}{
		{"stamped at death", &Download{AddedAt: added, GrabbedAt: &grabbed, CompletedAt: &completed, DeadAt: &died}, died},
		{"stamped, and nothing else", &Download{AddedAt: added, DeadAt: &died}, died},
		// The rest only describe a row that died before migration 091 and
		// escaped its backfill. Each answer is earlier than the real death,
		// never later, so such a row is retried sooner rather than pinned.
		{"unstamped, completed", &Download{AddedAt: added, GrabbedAt: &grabbed, CompletedAt: &completed}, completed},
		{"unstamped, grabbed but never completed", &Download{AddedAt: added, GrabbedAt: &grabbed}, grabbed},
		{"unstamped, never grabbed", &Download{AddedAt: added}, added},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.dl.DeadSince(); !got.Equal(tc.want) {
				t.Errorf("DeadSince() = %v, want %v", got, tc.want)
			}
		})
	}
}
