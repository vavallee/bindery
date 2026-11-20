package calibre

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/importrollback"
	"github.com/vavallee/bindery/internal/models"
)

type runEntityMetadataEnvelope = importrollback.Envelope

type runEntitySnapshotEnvelope = importrollback.SnapshotEnvelope

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
	// ImageURL was added with #2564, when the importer started giving a
	// book its library cover. Snapshots written before then decode it as
	// "", which restoreString treats as "no change recorded".
	ImageURL string `json:"imageUrl,omitempty"`
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
		ReleaseDate:      importrollback.CloneTimePtr(b.ReleaseDate),
		Language:         b.Language,
		Status:           b.Status,
		FilePath:         b.FilePath,
		MetadataProvider: b.MetadataProvider,
		CalibreID:        importrollback.CloneInt64Ptr(b.CalibreID),
		MediaType:        b.MediaType,
		AnyEditionOK:     b.AnyEditionOK,
		Monitored:        b.Monitored,
		ImageURL:         b.ImageURL,
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
		ISBN13:      importrollback.CloneStringPtr(e.ISBN13),
		PublishDate: importrollback.CloneTimePtr(e.PublishDate),
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
	foreignIDs, err := importrollback.CaptureIdentifierForeignIDs(ctx, i.authors, author.ID)
	if err != nil {
		return nil, err
	}
	snapshot.IdentifierForeignIDs = foreignIDs
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
	return importrollback.NewEnvelope(runEntityMetadataKind, runEntityMetadataVersion, entityTypeBook, data, beforePayload, afterPayload), nil
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
	return importrollback.NewEnvelope(runEntityMetadataKind, runEntityMetadataVersion, entityTypeAuthor, data, beforePayload, afterPayload), nil
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
	return importrollback.NewEnvelope(runEntityMetadataKind, runEntityMetadataVersion, entityTypeEdition, data, beforePayload, afterPayload), nil
}

// marshalSnapshotPayload encodes a snapshot struct for the envelope; a nil or
// typed nil pointer yields a nil payload.
func marshalSnapshotPayload(v any) (json.RawMessage, error) {
	return importrollback.MarshalPayload("calibre", v)
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
	return importrollback.DecodeSnapshots[bookRollbackSnapshot](raw, runEntityMetadataKind, runEntityMetadataVersion, entityTypeBook)
}

func authorRollbackSnapshotFromMetadata(raw string) (*authorRollbackSnapshot, *authorRollbackSnapshot, bool) {
	return importrollback.DecodeSnapshots[authorRollbackSnapshot](raw, runEntityMetadataKind, runEntityMetadataVersion, entityTypeAuthor)
}

func parseRunEntityMetadata(raw string) (runEntityMetadataEnvelope, bool) {
	return importrollback.ParseEnvelope(raw, runEntityMetadataKind, runEntityMetadataVersion)
}
