package models

import "time"

type ABSImportRun struct {
	ID               int64      `json:"id"`
	SourceID         string     `json:"sourceId"`
	SourceLabel      string     `json:"sourceLabel"`
	BaseURL          string     `json:"baseUrl"`
	LibraryID        string     `json:"libraryId"`
	Status           string     `json:"status"`
	DryRun           bool       `json:"dryRun"`
	SourceConfigJSON string     `json:"sourceConfigJson"`
	CheckpointJSON   string     `json:"checkpointJson"`
	SummaryJSON      string     `json:"summaryJson"`
	StartedAt        time.Time  `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
}

type ABSImportRunEntity struct {
	ID           int64     `json:"id"`
	RunID        int64     `json:"runId"`
	SourceID     string    `json:"sourceId"`
	LibraryID    string    `json:"libraryId"`
	ItemID       string    `json:"itemId"`
	EntityType   string    `json:"entityType"`
	ExternalID   string    `json:"externalId"`
	LocalID      int64     `json:"localId"`
	Outcome      string    `json:"outcome"`
	MetadataJSON string    `json:"metadataJson"`
	CreatedAt    time.Time `json:"createdAt"`
}

type ABSProvenance struct {
	ID          int64    `json:"id"`
	SourceID    string   `json:"sourceId"`
	LibraryID   string   `json:"libraryId"`
	EntityType  string   `json:"entityType"`
	ExternalID  string   `json:"externalId"`
	LocalID     int64    `json:"localId"`
	ItemID      string   `json:"itemId"`
	Format      string   `json:"format"`
	FileIDs     []string `json:"fileIds"`
	ImportRunID *int64   `json:"importRunId,omitempty"`
	// KeepIdentity marks a book link made through a file the book already
	// tracked (#1691). Imports through it keep the book's own author and title.
	KeepIdentity bool      `json:"keepIdentity,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type ABSMetadataConflict struct {
	ID               int64     `json:"id"`
	SourceID         string    `json:"sourceId"`
	LibraryID        string    `json:"libraryId"`
	ItemID           string    `json:"itemId"`
	EntityType       string    `json:"entityType"`
	LocalID          int64     `json:"localId"`
	FieldName        string    `json:"fieldName"`
	ABSValue         string    `json:"absValue"`
	UpstreamValue    string    `json:"upstreamValue"`
	AppliedSource    string    `json:"appliedSource"`
	PreferredSource  string    `json:"preferredSource"`
	ResolutionStatus string    `json:"resolutionStatus"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type ABSReviewItem struct {
	ID                      int64     `json:"id"`
	SourceID                string    `json:"sourceId"`
	LibraryID               string    `json:"libraryId"`
	ItemID                  string    `json:"itemId"`
	Title                   string    `json:"title"`
	PrimaryAuthor           string    `json:"primaryAuthor"`
	ASIN                    string    `json:"asin"`
	MediaType               string    `json:"mediaType"`
	ReviewReason            string    `json:"reviewReason"`
	PayloadJSON             string    `json:"payloadJson"`
	ResolvedAuthorForeignID string    `json:"resolvedAuthorForeignId,omitempty"`
	ResolvedAuthorName      string    `json:"resolvedAuthorName,omitempty"`
	ResolvedBookForeignID   string    `json:"resolvedBookForeignId,omitempty"`
	ResolvedBookTitle       string    `json:"resolvedBookTitle,omitempty"`
	EditedTitle             string    `json:"editedTitle,omitempty"`
	FileMappingFound        bool      `json:"fileMappingFound"`
	FileMappingMessage      string    `json:"fileMappingMessage,omitempty"`
	LatestRunID             *int64    `json:"latestRunId,omitempty"`
	Status                  string    `json:"status"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}
