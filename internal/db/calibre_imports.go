package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// CalibreImportRunRepo manages the calibre_import_runs table. Mirrors
// ABSImportRunRepo line-for-line modulo the dropped checkpoint column
// (Calibre imports have no mid-run resumable state).
type CalibreImportRunRepo struct {
	db   *sql.DB
	exec dbExecutor
}

func NewCalibreImportRunRepo(db *sql.DB) *CalibreImportRunRepo {
	return &CalibreImportRunRepo{db: db, exec: db}
}

// WithTx returns a clone of this repo with its tx-aware methods
// (UpdateStatus) routed through tx. See dbExecutor for the rationale.
func (r *CalibreImportRunRepo) WithTx(tx *sql.Tx) *CalibreImportRunRepo {
	clone := *r
	clone.exec = tx
	return &clone
}

// BeginTx exposes the underlying *sql.DB's BeginTx so callers driving a
// multi-repo atomic operation (calibre.Rollback) can obtain the
// transaction without separately injecting *sql.DB. The repo already owns
// the DB handle, so this avoids fanning out the *sql.DB to every consumer
// just to start a tx.
func (r *CalibreImportRunRepo) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return r.db.BeginTx(ctx, opts)
}

func (r *CalibreImportRunRepo) Create(ctx context.Context, run *models.CalibreImportRun) error {
	now := time.Now().UTC()
	if run.SourceID == "" {
		run.SourceID = "default"
	}
	sourceConfigJSON := run.SourceConfigJSON
	if sourceConfigJSON == "" {
		sourceConfigJSON = "{}"
	}
	summaryJSON := run.SummaryJSON
	if summaryJSON == "" {
		summaryJSON = "{}"
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO calibre_import_runs (source_id, library_path, status, dry_run, source_config_json, summary_json, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.SourceID, run.LibraryPath, run.Status, boolToInt(run.DryRun), sourceConfigJSON, summaryJSON, now)
	if err != nil {
		return fmt.Errorf("create calibre import run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("calibre import run last insert id: %w", err)
	}
	run.ID = id
	run.StartedAt = now
	run.SourceConfigJSON = sourceConfigJSON
	run.SummaryJSON = summaryJSON
	return nil
}

func (r *CalibreImportRunRepo) Finish(ctx context.Context, id int64, status string, summary any) error {
	if id == 0 {
		return nil
	}
	now := time.Now().UTC()
	payload := "{}"
	if summary != nil {
		data, err := json.Marshal(summary)
		if err != nil {
			return fmt.Errorf("encode calibre import summary: %w", err)
		}
		payload = string(data)
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE calibre_import_runs
		SET status = ?, summary_json = ?, finished_at = ?
		WHERE id = ?`,
		status, payload, now, id)
	if err != nil {
		return fmt.Errorf("finish calibre import run %d: %w", id, err)
	}
	return nil
}

func (r *CalibreImportRunRepo) UpdateStatus(ctx context.Context, id int64, status string) error {
	if id == 0 {
		return nil
	}
	_, err := r.exec.ExecContext(ctx, `
		UPDATE calibre_import_runs
		SET status = ?
		WHERE id = ?`,
		status, id)
	if err != nil {
		return fmt.Errorf("update calibre import run status %d: %w", id, err)
	}
	return nil
}

func (r *CalibreImportRunRepo) GetByID(ctx context.Context, id int64) (*models.CalibreImportRun, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, source_id, library_path, status, dry_run, source_config_json, summary_json, started_at, finished_at
		FROM calibre_import_runs
		WHERE id = ?`, id)
	var run models.CalibreImportRun
	var dryRun int
	if err := row.Scan(&run.ID, &run.SourceID, &run.LibraryPath, &run.Status, &dryRun, &run.SourceConfigJSON, &run.SummaryJSON, &run.StartedAt, &run.FinishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get calibre import run %d: %w", id, err)
	}
	run.DryRun = dryRun == 1
	return &run, nil
}

func (r *CalibreImportRunRepo) ListRecent(ctx context.Context, limit int) ([]models.CalibreImportRun, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, source_id, library_path, status, dry_run, source_config_json, summary_json, started_at, finished_at
		FROM calibre_import_runs
		ORDER BY started_at DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent calibre import runs: %w", err)
	}
	defer rows.Close()

	var out []models.CalibreImportRun
	for rows.Next() {
		var run models.CalibreImportRun
		var dryRun int
		if err := rows.Scan(&run.ID, &run.SourceID, &run.LibraryPath, &run.Status, &dryRun, &run.SourceConfigJSON, &run.SummaryJSON, &run.StartedAt, &run.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan calibre import run: %w", err)
		}
		run.DryRun = dryRun == 1
		out = append(out, run)
	}
	return out, rows.Err()
}

// CalibreProvenanceRepo manages the calibre_provenance table — the
// (entity_type, external_id) → local_id mapping rollback uses to prove a
// run still owns the row it's about to delete.
type CalibreProvenanceRepo struct {
	db   *sql.DB
	exec dbExecutor
}

func NewCalibreProvenanceRepo(db *sql.DB) *CalibreProvenanceRepo {
	return &CalibreProvenanceRepo{db: db, exec: db}
}

// WithTx returns a clone of this repo with its tx-aware methods
// (GetByExternal, ListByLocal, DeleteByLocal, DeleteByExternal) routed
// through tx. See dbExecutor for the rationale.
func (r *CalibreProvenanceRepo) WithTx(tx *sql.Tx) *CalibreProvenanceRepo {
	clone := *r
	clone.exec = tx
	return &clone
}

func (r *CalibreProvenanceRepo) GetByExternal(ctx context.Context, sourceID, entityType, externalID string) (*models.CalibreProvenance, error) {
	row := r.exec.QueryRowContext(ctx, `
		SELECT id, source_id, entity_type, external_id, local_id, import_run_id, created_at, updated_at
		FROM calibre_provenance
		WHERE source_id = ? AND entity_type = ? AND external_id = ?`,
		sourceID, entityType, externalID)
	return scanCalibreProvenance(row, "get calibre provenance")
}

func (r *CalibreProvenanceRepo) ListByLocal(ctx context.Context, entityType string, localID int64) ([]models.CalibreProvenance, error) {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT id, source_id, entity_type, external_id, local_id, import_run_id, created_at, updated_at
		FROM calibre_provenance
		WHERE entity_type = ? AND local_id = ?
		ORDER BY id`, entityType, localID)
	if err != nil {
		return nil, fmt.Errorf("list calibre provenance for %s %d: %w", entityType, localID, err)
	}
	defer rows.Close()

	var out []models.CalibreProvenance
	for rows.Next() {
		item, err := scanCalibreProvenanceRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *CalibreProvenanceRepo) Upsert(ctx context.Context, p *models.CalibreProvenance) error {
	now := time.Now().UTC()
	if p.SourceID == "" {
		p.SourceID = "default"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO calibre_provenance (source_id, entity_type, external_id, local_id, import_run_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, entity_type, external_id) DO UPDATE SET
			local_id      = excluded.local_id,
			import_run_id = excluded.import_run_id,
			updated_at    = excluded.updated_at`,
		p.SourceID, p.EntityType, p.ExternalID, p.LocalID, p.ImportRunID, now, now)
	if err != nil {
		return fmt.Errorf("upsert calibre provenance %s/%s: %w", p.EntityType, p.ExternalID, err)
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, created_at, updated_at
		FROM calibre_provenance
		WHERE source_id = ? AND entity_type = ? AND external_id = ?`,
		p.SourceID, p.EntityType, p.ExternalID)
	if err := row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return fmt.Errorf("reload calibre provenance %s/%s: %w", p.EntityType, p.ExternalID, err)
	}
	return nil
}

func (r *CalibreProvenanceRepo) DeleteByExternal(ctx context.Context, sourceID, entityType, externalID string) error {
	_, err := r.exec.ExecContext(ctx, `
		DELETE FROM calibre_provenance
		WHERE source_id = ? AND entity_type = ? AND external_id = ?`,
		sourceID, entityType, externalID)
	if err != nil {
		return fmt.Errorf("delete calibre provenance %s/%s: %w", entityType, externalID, err)
	}
	return nil
}

func (r *CalibreProvenanceRepo) DeleteByLocal(ctx context.Context, entityType string, localID int64) (int64, error) {
	if localID == 0 {
		return 0, nil
	}
	result, err := r.exec.ExecContext(ctx, `
		DELETE FROM calibre_provenance
		WHERE entity_type = ? AND local_id = ?`,
		entityType, localID)
	if err != nil {
		return 0, fmt.Errorf("delete calibre provenance for %s %d: %w", entityType, localID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted calibre provenance for %s %d: %w", entityType, localID, err)
	}
	return count, nil
}

// CalibreEntitySnapshotRepo manages calibre_entity_snapshots. Each row holds
// one before/after snapshot of a single (book/author/edition) the importer
// mutated during a run. Rollback reads these in reverse to restore state.
type CalibreEntitySnapshotRepo struct {
	db *sql.DB
}

func NewCalibreEntitySnapshotRepo(db *sql.DB) *CalibreEntitySnapshotRepo {
	return &CalibreEntitySnapshotRepo{db: db}
}

func (r *CalibreEntitySnapshotRepo) Record(ctx context.Context, entity *models.CalibreEntitySnapshot) error {
	if entity == nil || entity.RunID == 0 {
		return nil
	}
	sourceID := entity.SourceID
	if sourceID == "" {
		sourceID = "default"
	}
	metadataJSON := entity.MetadataJSON
	if metadataJSON == "" {
		metadataJSON = "{}"
	}
	// Snapshot-before-mutation must win: on conflict we keep the first
	// "before" we ever saw for this entity in this run but always overwrite
	// "after" (and outcome) with the latest. Without this, a second
	// applyBookFields() call inside the same run would clobber the original
	// pre-import state and break rollback.
	var existingMetadata string
	var existingOutcome string
	if err := r.db.QueryRowContext(ctx, `
		SELECT metadata_json, outcome
		FROM calibre_entity_snapshots
		WHERE run_id = ? AND entity_type = ? AND external_id = ? AND local_id = ?`,
		entity.RunID, entity.EntityType, entity.ExternalID, entity.LocalID).Scan(&existingMetadata, &existingOutcome); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect calibre snapshot %d/%s/%s: %w", entity.RunID, entity.EntityType, entity.ExternalID, err)
	}
	outcome := mergeCalibreSnapshotOutcome(existingOutcome, entity.Outcome)
	mergedJSON := mergeCalibreSnapshotMetadata(existingMetadata, metadataJSON)

	result, err := r.db.ExecContext(ctx, `
		INSERT INTO calibre_entity_snapshots (run_id, source_id, entity_type, external_id, local_id, outcome, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, entity_type, external_id, local_id) DO UPDATE SET
			outcome       = excluded.outcome,
			metadata_json = excluded.metadata_json`,
		entity.RunID, sourceID, entity.EntityType, entity.ExternalID, entity.LocalID, outcome, mergedJSON)
	if err != nil {
		return fmt.Errorf("record calibre snapshot %d/%s/%s: %w", entity.RunID, entity.EntityType, entity.ExternalID, err)
	}
	if entity.ID == 0 {
		if id, err := result.LastInsertId(); err == nil && id > 0 {
			entity.ID = id
		}
	}
	entity.SourceID = sourceID
	entity.Outcome = outcome
	entity.MetadataJSON = mergedJSON
	return nil
}

func (r *CalibreEntitySnapshotRepo) ListByRun(ctx context.Context, runID int64) ([]models.CalibreEntitySnapshot, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, run_id, source_id, entity_type, external_id, local_id, outcome, metadata_json, created_at
		FROM calibre_entity_snapshots
		WHERE run_id = ?
		ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list calibre snapshots %d: %w", runID, err)
	}
	defer rows.Close()

	var out []models.CalibreEntitySnapshot
	for rows.Next() {
		var entity models.CalibreEntitySnapshot
		if err := rows.Scan(&entity.ID, &entity.RunID, &entity.SourceID, &entity.EntityType, &entity.ExternalID, &entity.LocalID, &entity.Outcome, &entity.MetadataJSON, &entity.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan calibre snapshot: %w", err)
		}
		out = append(out, entity)
	}
	return out, rows.Err()
}

func mergeCalibreSnapshotOutcome(existing, incoming string) string {
	if existing == "" {
		return incoming
	}
	if incoming == "" {
		return existing
	}
	// Once an entity is recorded as "created" within a run, no later
	// "updated" should overwrite that — the rollback path uses outcome to
	// decide between delete-row and restore-fields, and a created row must
	// always be deleted on rollback.
	if existing == "created" || incoming == "created" {
		return "created"
	}
	return incoming
}

// mergeCalibreSnapshotMetadata merges two run_entity_metadata envelopes,
// keeping the earliest "before" snapshot (snapshot-before-mutation) and
// overwriting the "after" snapshot + Data with the latest. Anything not
// matching the envelope shape falls back to a JSON-object merge.
func mergeCalibreSnapshotMetadata(existingJSON, incomingJSON string) string {
	existing := decodeJSONMap(existingJSON)
	incoming := decodeJSONMap(incomingJSON)
	if len(existing) == 0 {
		if len(incoming) == 0 {
			return "{}"
		}
		return encodeJSONMap(incoming)
	}
	if len(incoming) == 0 {
		return encodeJSONMap(existing)
	}
	merged := make(map[string]any, len(existing)+len(incoming))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range incoming {
		switch k {
		case "data":
			merged[k] = mergeJSONObjects(asJSONMap(merged[k]), asJSONMap(v))
		case "snapshot":
			merged[k] = mergeCalibreSnapshotPart(asJSONMap(merged[k]), asJSONMap(v))
		default:
			merged[k] = v
		}
	}
	return encodeJSONMap(merged)
}

func mergeCalibreSnapshotPart(existing, incoming map[string]any) map[string]any {
	if len(existing) == 0 {
		return incoming
	}
	if len(incoming) == 0 {
		return existing
	}
	merged := make(map[string]any, len(existing)+len(incoming))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range incoming {
		if k == "before" {
			if _, ok := merged[k]; ok {
				continue
			}
		}
		merged[k] = v
	}
	return merged
}

type calibreProvenanceScanner interface {
	Scan(dest ...any) error
}

func scanCalibreProvenance(scanner calibreProvenanceScanner, context string) (*models.CalibreProvenance, error) {
	var item models.CalibreProvenance
	if err := scanner.Scan(&item.ID, &item.SourceID, &item.EntityType, &item.ExternalID, &item.LocalID, &item.ImportRunID, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", context, err)
	}
	return &item, nil
}

func scanCalibreProvenanceRows(rows *sql.Rows) (models.CalibreProvenance, error) {
	item, err := scanCalibreProvenance(rows, "scan calibre provenance")
	if err != nil {
		return models.CalibreProvenance{}, err
	}
	if item == nil {
		return models.CalibreProvenance{}, sql.ErrNoRows
	}
	return *item, nil
}
