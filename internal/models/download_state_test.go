package models

import "testing"

func TestCanTransitionTo(t *testing.T) {
	cases := []struct {
		from DownloadState
		to   DownloadState
		want bool
	}{
		// Normal forward path
		{StateGrabbed, StateDownloading, true},
		{StateDownloading, StateCompleted, true},
		{StateCompleted, StateImportPending, true},
		{StateImportPending, StateImporting, true},
		{StateImporting, StateImported, true},
		{StateImporting, StateImportFailed, true},
		{StateImporting, StateImportBlocked, true},

		// Retry path from StateImportFailed (issue #706 finding 4)
		{StateImportFailed, StateImportPending, true},
		{StateImportFailed, StateImportBlocked, true},
		{StateImportFailed, StateImporting, true},

		// Terminal states have no outgoing transitions
		{StateImported, StateImporting, false},
		{StateFailed, StateGrabbed, false},
		{StateImportBlocked, StateImporting, false},

		// External hand-off is non-terminal (issue #706 finding 3)
		{StateImportPending, StateImportExternal, true},
		{StateImportExternal, StateImportPending, true},
		// ...but it does not jump straight to a terminal state
		{StateImportExternal, StateImported, false},
		{StateImportExternal, StateImporting, false},
		{StateImportExternal, StateImportBlocked, false},

		// No backwards transitions
		{StateImported, StateImportPending, false},
		{StateImportFailed, StateImported, false},
		{StateImportFailed, StateCompleted, false},
	}
	for _, c := range cases {
		got := c.from.CanTransitionTo(c.to)
		if got != c.want {
			t.Errorf("(%s).CanTransitionTo(%s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}
