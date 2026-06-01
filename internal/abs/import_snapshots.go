package abs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type runEntityMetadataEnvelope struct {
	Kind     string                     `json:"kind"`
	Version  int                        `json:"version"`
	Data     map[string]any             `json:"data,omitempty"`
	Snapshot *runEntitySnapshotEnvelope `json:"snapshot,omitempty"`
}

// runEntitySnapshotEnvelope carries a before/after snapshot of a single entity
// so rollback can restore field-level state. Before/After are encoded as raw
// JSON with the shape indicated by EntityType; decoders should match on
// EntityType before unmarshaling into a concrete snapshot struct.
type runEntitySnapshotEnvelope struct {
	EntityType string          `json:"entityType"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
}

type bookRollbackSnapshot struct {
	ForeignID             string     `json:"foreignId"`
	AuthorID              int64      `json:"authorId"`
	Title                 string     `json:"title"`
	SortTitle             string     `json:"sortTitle"`
	OriginalTitle         string     `json:"originalTitle"`
	Description           string     `json:"description"`
	ImageURL              string     `json:"imageUrl"`
	ReleaseDate           *time.Time `json:"releaseDate,omitempty"`
	Genres                []string   `json:"genres"`
	AverageRating         float64    `json:"averageRating"`
	RatingsCount          int        `json:"ratingsCount"`
	Monitored             bool       `json:"monitored"`
	Status                string     `json:"status"`
	AnyEditionOK          bool       `json:"anyEditionOk"`
	SelectedEditionID     *int64     `json:"selectedEditionId,omitempty"`
	Language              string     `json:"language"`
	MediaType             string     `json:"mediaType"`
	Narrator              string     `json:"narrator"`
	DurationSeconds       int        `json:"durationSeconds"`
	ASIN                  string     `json:"asin"`
	CalibreID             *int64     `json:"calibreId,omitempty"`
	MetadataProvider      string     `json:"metadataProvider"`
	LastMetadataRefreshAt *time.Time `json:"lastMetadataRefreshAt,omitempty"`
}

// authorRollbackSnapshot captures the subset of author fields the importer
// mutates so rollback can restore prior state without trampling post-import
// user edits. Fields omitted here (stats, profile FKs, Monitored) are not
// touched by the ABS import path.
type authorRollbackSnapshot struct {
	ForeignID             string     `json:"foreignId"`
	Name                  string     `json:"name"`
	SortName              string     `json:"sortName"`
	Description           string     `json:"description"`
	ImageURL              string     `json:"imageUrl"`
	Disambiguation        string     `json:"disambiguation"`
	MetadataProvider      string     `json:"metadataProvider"`
	IdentifierForeignIDs  *[]string  `json:"identifierForeignIds,omitempty"`
	LastMetadataRefreshAt *time.Time `json:"lastMetadataRefreshAt,omitempty"`
}

func bookSnapshot(book *models.Book) *bookRollbackSnapshot {
	if book == nil {
		return nil
	}
	return &bookRollbackSnapshot{
		ForeignID:             book.ForeignID,
		AuthorID:              book.AuthorID,
		Title:                 book.Title,
		SortTitle:             book.SortTitle,
		OriginalTitle:         book.OriginalTitle,
		Description:           book.Description,
		ImageURL:              book.ImageURL,
		ReleaseDate:           cloneTimePtr(book.ReleaseDate),
		Genres:                append([]string(nil), book.Genres...),
		AverageRating:         book.AverageRating,
		RatingsCount:          book.RatingsCount,
		Monitored:             book.Monitored,
		Status:                book.Status,
		AnyEditionOK:          book.AnyEditionOK,
		SelectedEditionID:     cloneInt64Ptr(book.SelectedEditionID),
		Language:              book.Language,
		MediaType:             book.MediaType,
		Narrator:              book.Narrator,
		DurationSeconds:       book.DurationSeconds,
		ASIN:                  book.ASIN,
		CalibreID:             cloneInt64Ptr(book.CalibreID),
		MetadataProvider:      book.MetadataProvider,
		LastMetadataRefreshAt: cloneTimePtr(book.LastMetadataRefreshAt),
	}
}

func bookSnapshotMetadata(data map[string]any, before, after *bookRollbackSnapshot) (runEntityMetadataEnvelope, error) {
	beforePayload, err := marshalSnapshotPayload(before)
	if err != nil {
		return runEntityMetadataEnvelope{}, err
	}
	afterPayload, err := marshalSnapshotPayload(after)
	if err != nil {
		return runEntityMetadataEnvelope{}, err
	}
	return runEntityMetadataEnvelope{
		Kind:    runEntityMetadataKind,
		Version: runEntityMetadataVersion,
		Data:    data,
		Snapshot: &runEntitySnapshotEnvelope{
			EntityType: entityTypeBook,
			Before:     beforePayload,
			After:      afterPayload,
		},
	}, nil
}

func authorSnapshot(author *models.Author) *authorRollbackSnapshot {
	if author == nil {
		return nil
	}
	return &authorRollbackSnapshot{
		ForeignID:             author.ForeignID,
		Name:                  author.Name,
		SortName:              author.SortName,
		Description:           author.Description,
		ImageURL:              author.ImageURL,
		Disambiguation:        author.Disambiguation,
		MetadataProvider:      author.MetadataProvider,
		LastMetadataRefreshAt: cloneTimePtr(author.LastMetadataRefreshAt),
	}
}

func (i *Importer) authorSnapshot(ctx context.Context, author *models.Author) (*authorRollbackSnapshot, error) {
	snapshot := authorSnapshot(author)
	if snapshot == nil || i.authors == nil || author.ID == 0 {
		return snapshot, nil
	}
	identifiers, err := i.authors.ListAuthorIdentifiers(ctx, author.ID)
	if err != nil {
		return nil, err
	}
	foreignIDs := make([]string, 0, len(identifiers))
	for _, identifier := range identifiers {
		if foreignID := strings.TrimSpace(identifier.ForeignID); foreignID != "" {
			foreignIDs = append(foreignIDs, foreignID)
		}
	}
	sort.Strings(foreignIDs)
	snapshot.IdentifierForeignIDs = &foreignIDs
	return snapshot, nil
}

func authorSnapshotMetadata(data map[string]any, before, after *authorRollbackSnapshot) (runEntityMetadataEnvelope, error) {
	beforePayload, err := marshalSnapshotPayload(before)
	if err != nil {
		return runEntityMetadataEnvelope{}, err
	}
	afterPayload, err := marshalSnapshotPayload(after)
	if err != nil {
		return runEntityMetadataEnvelope{}, err
	}
	return runEntityMetadataEnvelope{
		Kind:    runEntityMetadataKind,
		Version: runEntityMetadataVersion,
		Data:    data,
		Snapshot: &runEntitySnapshotEnvelope{
			EntityType: entityTypeAuthor,
			Before:     beforePayload,
			After:      afterPayload,
		},
	}, nil
}

// marshalSnapshotPayload encodes a concrete snapshot struct into the
// envelope's RawMessage slot. A nil input returns a nil payload so the
// omitempty tag drops the field entirely.
func marshalSnapshotPayload(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	// Guard against typed-nil pointers: json.Marshal on a (*T)(nil) encodes
	// "null", which is indistinguishable from a genuinely absent snapshot
	// and would defeat before/after gating.
	switch t := v.(type) {
	case *bookRollbackSnapshot:
		if t == nil {
			return nil, nil
		}
	case *authorRollbackSnapshot:
		if t == nil {
			return nil, nil
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode abs rollback snapshot: %w", err)
	}
	return data, nil
}

func (i *Importer) recordBookBeforeSnapshot(ctx context.Context, runID int64, cfg ImportConfig, item NormalizedLibraryItem, externalID string, book *models.Book, outcome string, data map[string]any) error {
	if cfg.DryRun || book == nil {
		return nil
	}
	metadata, err := bookSnapshotMetadata(data, bookSnapshot(book), nil)
	if err != nil {
		return err
	}
	return i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, externalID, book.ID, outcome, metadata)
}

func (i *Importer) recordBookAfterSnapshot(ctx context.Context, runID int64, cfg ImportConfig, item NormalizedLibraryItem, bookID int64, outcome string, data map[string]any) error {
	if cfg.DryRun || bookID == 0 {
		return nil
	}
	book, err := i.books.GetByID(ctx, bookID)
	if err != nil || book == nil {
		return err
	}
	metadata, err := bookSnapshotMetadata(data, nil, bookSnapshot(book))
	if err != nil {
		return err
	}
	return i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, item.ItemID, book.ID, outcome, metadata)
}

func (i *Importer) recordAuthorBeforeSnapshot(ctx context.Context, runID int64, cfg ImportConfig, item NormalizedLibraryItem, externalID string, author *models.Author, outcome string, data map[string]any) error {
	if cfg.DryRun || author == nil {
		return nil
	}
	before, err := i.authorSnapshot(ctx, author)
	if err != nil {
		return err
	}
	metadata, err := authorSnapshotMetadata(data, before, nil)
	if err != nil {
		return err
	}
	return i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, externalID, author.ID, outcome, metadata)
}

func (i *Importer) recordAuthorAfterSnapshot(ctx context.Context, runID int64, cfg ImportConfig, item NormalizedLibraryItem, externalID string, authorID int64, outcome string, data map[string]any) error {
	if cfg.DryRun || authorID == 0 || i.authors == nil {
		return nil
	}
	author, err := i.authors.GetByID(ctx, authorID)
	if err != nil || author == nil {
		return err
	}
	after, err := i.authorSnapshot(ctx, author)
	if err != nil {
		return err
	}
	metadata, err := authorSnapshotMetadata(data, nil, after)
	if err != nil {
		return err
	}
	return i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, externalID, author.ID, outcome, metadata)
}

func runEntityMetadataData(raw string) map[string]any {
	envelope, ok := parseRunEntityMetadata(raw)
	if ok {
		return envelope.Data
	}
	return parseJSONObject(raw)
}

func bookRollbackSnapshotFromMetadata(raw string) (*bookRollbackSnapshot, *bookRollbackSnapshot, bool) {
	envelope, ok := parseRunEntityMetadata(raw)
	if !ok || envelope.Snapshot == nil || envelope.Snapshot.EntityType != entityTypeBook {
		return nil, nil, false
	}
	if len(envelope.Snapshot.Before) == 0 || len(envelope.Snapshot.After) == 0 {
		return nil, nil, false
	}
	var before, after bookRollbackSnapshot
	if err := json.Unmarshal(envelope.Snapshot.Before, &before); err != nil {
		return nil, nil, false
	}
	if err := json.Unmarshal(envelope.Snapshot.After, &after); err != nil {
		return nil, nil, false
	}
	return &before, &after, true
}

func authorRollbackSnapshotFromMetadata(raw string) (*authorRollbackSnapshot, *authorRollbackSnapshot, bool) {
	envelope, ok := parseRunEntityMetadata(raw)
	if !ok || envelope.Snapshot == nil || envelope.Snapshot.EntityType != entityTypeAuthor {
		return nil, nil, false
	}
	if len(envelope.Snapshot.Before) == 0 || len(envelope.Snapshot.After) == 0 {
		return nil, nil, false
	}
	var before, after authorRollbackSnapshot
	if err := json.Unmarshal(envelope.Snapshot.Before, &before); err != nil {
		return nil, nil, false
	}
	if err := json.Unmarshal(envelope.Snapshot.After, &after); err != nil {
		return nil, nil, false
	}
	return &before, &after, true
}

func parseRunEntityMetadata(raw string) (runEntityMetadataEnvelope, bool) {
	var envelope runEntityMetadataEnvelope
	if strings.TrimSpace(raw) == "" {
		return envelope, false
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return envelope, false
	}
	if envelope.Kind != runEntityMetadataKind || envelope.Version != runEntityMetadataVersion {
		return envelope, false
	}
	if envelope.Data == nil {
		envelope.Data = map[string]any{}
	}
	return envelope, true
}

func parseJSONObject(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
