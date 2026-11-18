package api

import "time"

// Caps on the free-text and numeric metadata fields a client can edit. They
// are generous on purpose: the point is to refuse a megabyte body stored in
// one column, not to second-guess a real value.
const (
	// maxBookNarratorBytes fits a long comma-separated narrator list.
	maxBookNarratorBytes = 1000
	// maxBookDurationSeconds is 1000 hours, far past the longest audiobook.
	maxBookDurationSeconds = 1000 * 60 * 60
)

// parseDateField reads a date sent on a metadata PUT. It accepts YYYY-MM-DD,
// which the UI sends, and the RFC 3339 timestamp GET serializes, so a client
// can write back what it read. The result is the calendar date at midnight
// UTC, the form every date column is stored in. An empty string is a clear
// and returns nil, true.
func parseDateField(s string) (*time.Time, bool) {
	if s == "" {
		return nil, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return &t, true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, false
	}
	t = t.UTC()
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return &d, true
}

// sameDay reports whether two optional dates name the same calendar day in
// UTC, treating two nils as equal.
func sameDay(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}
