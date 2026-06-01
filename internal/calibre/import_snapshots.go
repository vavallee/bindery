package calibre

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// runEntityMetadataEnvelope is the JSON shape stored in
// calibre_entity_snapshots.metadata_json. Kind+Version are stamped on every
// record so future migrations can detect old envelopes; Data holds free-form
// per-outcome breadcrumbs; Snapshot carries the typed before/after payload
// rollback restores from.
type runEntityMetadataEnvelope struct {
	Kind     string                     `json:"kind"`
	Version  int                        `json:"version"`
	Data     map[string]any             `json:"data,omitempty"`
	Snapshot *runEntitySnapshotEnvelope `json:"snapshot,omitempty"`
}

type runEntitySnapshotEnvelope struct {
	EntityType string          `json:"entityType"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
}

// bookRollbackSnapshot captures the subset of book fields the Calibre
// importer mutates (see applyBookFields + upsertBook). Fields outside this
// list are not touched by the import path so they don't need to be
// snapshotted or restored.
type bookRollbackSnapshot struct {
	ForeignID        string     `json:"foreignId"`
	AuthorID         int64      `json:"authorId"`
	Title            string     `json:"title"`
	SortTitle        string     `json:"sortTitle"`
	ReleaseDate      *time.Time `json:"releaseDate,omitempty"`
	Language         string     `json:"language"`
	Status           string     `json:"status"`
	FilePath         string     `json:"filePath"`
	MetadataProvider string     `json:"metadataProvider"`
	CalibreID        *int64     `json:"calibreId,omitempty"`
	MediaType        string     `json:"mediaType"`
	AnyEditionOK     bool       `json:"anyEditionOk"`
	Monitored        bool       `json:"monitored"`
}

// authorRollbackSnapshot mirrors bookRollbackSnapshot for the Calibre
// importer's author path (resolveAuthor — only sets Name, SortName,
// ForeignID, MetadataProvider, Monitored when creating fresh).
type authorRollbackSnapshot struct {
	ForeignID            string    `json:"foreignId"`
	Name                 string    `json:"name"`
	SortName             string    `json:"sortName"`
	MetadataProvider     string    `json:"metadataProvider"`
	Monitored            bool      `json:"monitored"`
	IdentifierForeignIDs *[]string `json:"identifierForeignIds,omitempty"`
}

// editionRollbackSnapshot captures the subset of edition fields touched by
// upsertEdition. Editions are append-only during Calibre import (Upsert
// either creates fresh or updates everything), so the snapshot mostly
// matters to identify rows to delete on rollback.
type editionRollbackSnapshot struct {
	ForeignID   string     `json:"foreignId"`
	BookID      int64      `json:"bookId"`
	Title       string     `json:"title"`
	ISBN13      *string    `json:"isbn13,omitempty"`
	PublishDate *time.Time `json:"publishDate,omitempty"`
	Format      string     `json:"format"`
	Language    string     `json:"language"`
	ImageURL    string     `json:"imageUrl"`
	IsEbook     bool       `json:"isEbook"`
	Monitored   bool       `json:"monitored"`
}

func bookSnapshot(b *models.Book) *bookRollbackSnapshot {
	if b == nil {
		return nil
	}
	return &bookRollbackSnapshot{
		ForeignID:        b.ForeignID,
		AuthorID:         b.AuthorID,
		Title:            b.Title,
		SortTitle:        b.SortTitle,
		ReleaseDate:      cloneTimePtr(b.ReleaseDate),
		Language:         b.Language,
		Status:           b.Status,
		FilePath:         b.FilePath,
		MetadataProvider: b.MetadataProvider,
		CalibreID:        cloneInt64Ptr(b.CalibreID),
		MediaType:        b.MediaType,
		AnyEditionOK:     b.AnyEditionOK,
		Monitored:        b.Monitored,
	}
}

func editionSnapshot(e *models.Edition) *editionRollbackSnapshot {
	if e == nil {
		return nil
	}
	return &editionRollbackSnapshot{
		ForeignID:   e.ForeignID,
		BookID:      e.BookID,
		Title:       e.Title,
		ISBN13:      cloneStringPtr(e.ISBN13),
		PublishDate: cloneTimePtr(e.PublishDate),
		Format:      e.Format,
		Language:    e.Language,
		ImageURL:    e.ImageURL,
		IsEbook:     e.IsEbook,
		Monitored:   e.Monitored,
	}
}

func (i *Importer) authorSnapshot(ctx context.Context, author *models.Author) (*authorRollbackSnapshot, error) {
	if author == nil {
		return nil, nil
	}
	snapshot := &authorRollbackSnapshot{
		ForeignID:        author.ForeignID,
		Name:             author.Name,
		SortName:         author.SortName,
		MetadataProvider: author.MetadataProvider,
		Monitored:        author.Monitored,
	}
	if i.authors == nil || author.ID == 0 {
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

func editionSnapshotMetadata(data map[string]any, before, after *editionRollbackSnapshot) (runEntityMetadataEnvelope, error) {
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
			EntityType: entityTypeEdition,
			Before:     beforePayload,
			After:      afterPayload,
		},
	}, nil
}

func marshalSnapshotPayload(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	// Guard against typed-nil pointers: json.Marshal on a (*T)(nil) encodes
	// "null", which is indistinguishable from an absent snapshot and would
	// defeat before/after gating in the restorers.
	switch t := v.(type) {
	case *bookRollbackSnapshot:
		if t == nil {
			return nil, nil
		}
	case *authorRollbackSnapshot:
		if t == nil {
			return nil, nil
		}
	case *editionRollbackSnapshot:
		if t == nil {
			return nil, nil
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode calibre rollback snapshot: %w", err)
	}
	return data, nil
}

// recordBookBeforeSnapshot persists the pre-mutation book snapshot. Called
// once per (runID, externalID) — snapshot-before-mutation is non-obvious:
// if we waited until after applyBookFields, the "before" snapshot would be
// the post-import state and rollback would be a no-op.
func (i *Importer) recordBookBeforeSnapshot(ctx context.Context, runID int64, externalID string, b *models.Book, outcome string, data map[string]any) {
	if runID == 0 || i.snapshots == nil || b == nil {
		return
	}
	envelope, err := bookSnapshotMetadata(data, bookSnapshot(b), nil)
	if err != nil {
		slog.Warn("calibre import: encode book before snapshot", "runID", runID, "bookID", b.ID, "error", err)
		return
	}
	i.recordSnapshot(ctx, runID, externalID, entityTypeBook, b.ID, outcome, envelope)
}

func (i *Importer) recordBookAfterSnapshot(ctx context.Context, runID int64, externalID string, bookID int64, outcome string, data map[string]any) {
	if runID == 0 || i.snapshots == nil || bookID == 0 || i.books == nil {
		return
	}
	b, err := i.books.GetByID(ctx, bookID)
	if err != nil || b == nil {
		return
	}
	envelope, err := bookSnapshotMetadata(data, nil, bookSnapshot(b))
	if err != nil {
		slog.Warn("calibre import: encode book after snapshot", "runID", runID, "bookID", bookID, "error", err)
		return
	}
	i.recordSnapshot(ctx, runID, externalID, entityTypeBook, bookID, outcome, envelope)
}

func (i *Importer) recordAuthorBeforeSnapshot(ctx context.Context, runID int64, externalID string, author *models.Author, outcome string, data map[string]any) {
	if runID == 0 || i.snapshots == nil || author == nil {
		return
	}
	before, err := i.authorSnapshot(ctx, author)
	if err != nil {
		slog.Warn("calibre import: snapshot author identifiers", "runID", runID, "authorID", author.ID, "error", err)
		return
	}
	envelope, err := authorSnapshotMetadata(data, before, nil)
	if err != nil {
		slog.Warn("calibre import: encode author before snapshot", "runID", runID, "authorID", author.ID, "error", err)
		return
	}
	i.recordSnapshot(ctx, runID, externalID, entityTypeAuthor, author.ID, outcome, envelope)
}

func (i *Importer) recordAuthorAfterSnapshot(ctx context.Context, runID int64, externalID string, authorID int64, outcome string, data map[string]any) {
	if runID == 0 || i.snapshots == nil || authorID == 0 || i.authors == nil {
		return
	}
	author, err := i.authors.GetByID(ctx, authorID)
	if err != nil || author == nil {
		if err != nil {
			slog.Warn("calibre import: load author after snapshot", "runID", runID, "authorID", authorID, "error", err)
		}
		return
	}
	after, err := i.authorSnapshot(ctx, author)
	if err != nil {
		slog.Warn("calibre import: snapshot author identifiers", "runID", runID, "authorID", author.ID, "error", err)
		return
	}
	envelope, err := authorSnapshotMetadata(data, nil, after)
	if err != nil {
		slog.Warn("calibre import: encode author after snapshot", "runID", runID, "authorID", author.ID, "error", err)
		return
	}
	i.recordSnapshot(ctx, runID, externalID, entityTypeAuthor, author.ID, outcome, envelope)
}

func (i *Importer) recordEditionBeforeSnapshot(ctx context.Context, runID int64, externalID string, e *models.Edition, outcome string, data map[string]any) {
	if runID == 0 || i.snapshots == nil || e == nil {
		return
	}
	envelope, err := editionSnapshotMetadata(data, editionSnapshot(e), nil)
	if err != nil {
		slog.Warn("calibre import: encode edition before snapshot", "runID", runID, "editionID", e.ID, "error", err)
		return
	}
	i.recordSnapshot(ctx, runID, externalID, entityTypeEdition, e.ID, outcome, envelope)
}

func (i *Importer) recordSnapshot(ctx context.Context, runID int64, externalID, entityType string, localID int64, outcome string, envelope runEntityMetadataEnvelope) {
	if i.snapshots == nil {
		return
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		slog.Warn("calibre import: encode snapshot envelope", "runID", runID, "entityType", entityType, "externalID", externalID, "error", err)
		return
	}
	if err := i.snapshots.Record(ctx, &models.CalibreEntitySnapshot{
		RunID:        runID,
		SourceID:     defaultSourceID,
		EntityType:   entityType,
		ExternalID:   externalID,
		LocalID:      localID,
		Outcome:      outcome,
		MetadataJSON: string(payload),
	}); err != nil {
		slog.Warn("calibre import: persist snapshot",
			"runID", runID, "entityType", entityType, "externalID", externalID, "localID", localID, "error", err)
	}
}

// upsertProvenance records (or refreshes) the calibre_provenance row for a
// touched entity so rollback can verify the run is still the current owner.
func (i *Importer) upsertProvenance(ctx context.Context, runID int64, entityType, externalID string, localID int64) {
	if runID == 0 || i.provenance == nil || localID == 0 || externalID == "" {
		return
	}
	rid := runID
	if err := i.provenance.Upsert(ctx, &models.CalibreProvenance{
		SourceID:    defaultSourceID,
		EntityType:  entityType,
		ExternalID:  externalID,
		LocalID:     localID,
		ImportRunID: &rid,
	}); err != nil {
		slog.Warn("calibre import: persist provenance",
			"runID", runID, "entityType", entityType, "externalID", externalID, "localID", localID, "error", err)
	}
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

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func cloneStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func cloneTimePtr(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
