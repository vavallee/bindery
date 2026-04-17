package models

import "time"

// PendingRelease is an indexer result that was found but not yet grabbed,
// typically because a delay profile timer has not expired. The full release
// JSON is stored so it can be re-evaluated on subsequent scheduler sweeps
// without another network round-trip to the indexer.
type PendingRelease struct {
	ID          int64     `json:"id"`
	BookID      int64     `json:"bookId"`
	Title       string    `json:"title"`
	IndexerID   *int64    `json:"indexerId,omitempty"`
	GUID        string    `json:"guid"`
	Protocol    string    `json:"protocol"`
	Size        int64     `json:"size"`
	AgeMinutes  int       `json:"ageMinutes"`
	Quality     string    `json:"quality,omitempty"`
	CustomScore int       `json:"customScore"`
	Reason      string    `json:"reason"`
	FirstSeen   time.Time `json:"firstSeen"`
	ReleaseJSON string    `json:"releaseJson"`
}
