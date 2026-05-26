package models

import "time"

// CalibreImportRun is the persistent record of one Calibre library import.
// Mirrors ABSImportRun but drops checkpoint_json — Calibre imports are
// in-process scans of a local metadata.db with no mid-run pagination state
// to resume from.
type CalibreImportRun struct {
	ID               int64      `json:"id"`
	SourceID         string     `json:"sourceId"`
	LibraryPath      string     `json:"libraryPath"`
	Status           string     `json:"status"`
	DryRun           bool       `json:"dryRun"`
	SourceConfigJSON string     `json:"sourceConfigJson"`
	SummaryJSON      string     `json:"summaryJson"`
	StartedAt        time.Time  `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
}

// CalibreEntitySnapshot records the pre-mutation state of a single Bindery
// entity touched by a Calibre import run. The before/after JSON lives in
// MetadataJSON (decoded by the rollback layer); Outcome is the importer-side
// outcome ("created" / "updated") used to decide whether rollback deletes
// the row or restores its fields.
type CalibreEntitySnapshot struct {
	ID           int64     `json:"id"`
	RunID        int64     `json:"runId"`
	SourceID     string    `json:"sourceId"`
	EntityType   string    `json:"entityType"`
	ExternalID   string    `json:"externalId"`
	LocalID      int64     `json:"localId"`
	Outcome      string    `json:"outcome"`
	MetadataJSON string    `json:"metadataJson"`
	CreatedAt    time.Time `json:"createdAt"`
}

// CalibreProvenance links a Bindery local entity to the Calibre book/author
// id that produced it, so rollback can prove ownership before deleting.
type CalibreProvenance struct {
	ID          int64     `json:"id"`
	SourceID    string    `json:"sourceId"`
	EntityType  string    `json:"entityType"`
	ExternalID  string    `json:"externalId"`
	LocalID     int64     `json:"localId"`
	ImportRunID *int64    `json:"importRunId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
