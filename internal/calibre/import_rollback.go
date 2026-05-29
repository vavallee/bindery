package calibre

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// RollbackStats is the per-run roll-up returned by Preview / Execute. Mirror
// of the ABS shape, minus ABS-only counts.
type RollbackStats struct {
	ActionsPlanned     int `json:"actionsPlanned"`
	EntitiesDeleted    int `json:"entitiesDeleted"`
	ProvenanceUnlinked int `json:"provenanceUnlinked"`
	FilesAffected      int `json:"filesAffected"`
	Skipped            int `json:"skipped"`
	Failed             int `json:"failed"`
}

// RollbackAction describes a single planned (or applied) entity action.
// Action ∈ {restore_book, delete_book, restore_author, delete_author,
// delete_edition, unlink_provenance, skip}.
type RollbackAction struct {
	EntityType  string `json:"entityType"`
	ExternalID  string `json:"externalId"`
	LocalID     int64  `json:"localId"`
	DisplayName string `json:"displayName,omitempty"`
	Outcome     string `json:"outcome"`
	Action      string `json:"action"`
	Reason      string `json:"reason,omitempty"`
}

// RollbackResult is the JSON payload returned by both Preview and Execute.
// Preview=true means nothing was applied; Preview=false + Applied=true means
// every planned action ran. FilesOnDiskWarning is set when the run touched
// rows that point at on-disk files — rollback is metadata-only, the files
// themselves stay put on disk.
type RollbackResult struct {
	RunID              int64            `json:"runId"`
	Preview            bool             `json:"preview"`
	Applied            bool             `json:"applied"`
	DryRun             bool             `json:"dryRun"`
	Status             string           `json:"status"`
	Stats              RollbackStats    `json:"stats"`
	Actions            []RollbackAction `json:"actions"`
	FilesOnDiskWarning string           `json:"filesOnDiskWarning,omitempty"`
	Finished           time.Time        `json:"finishedAt"`
}

// ErrRunNotFound and ErrAlreadyRolledBack get mapped to 404 / 409 at the API
// layer so the UI can show distinct messages.
var (
	ErrRunNotFound         = errors.New("calibre import run not found")
	ErrAlreadyRolledBack   = errors.New("calibre import run has already been rolled back")
	ErrRollbackUnavailable = errors.New("calibre rollback is unavailable: run tracking not configured")
)

// RecentRuns lists the most recent N Calibre import runs (DESC by start
// time). Returns nil if the importer was constructed without a run repo
// (legacy/test wiring).
func (i *Importer) RecentRuns(ctx context.Context, limit int) ([]models.CalibreImportRun, error) {
	if i.runs == nil {
		return nil, nil
	}
	return i.runs.ListRecent(ctx, limit)
}

// GetRun returns one persisted run or nil if it doesn't exist.
func (i *Importer) GetRun(ctx context.Context, runID int64) (*models.CalibreImportRun, error) {
	if i.runs == nil {
		return nil, nil
	}
	return i.runs.GetByID(ctx, runID)
}

// PreviewRollback computes the action list without mutating anything. Safe
// to call repeatedly.
func (i *Importer) PreviewRollback(ctx context.Context, runID int64) (*RollbackResult, error) {
	return i.rollback(ctx, runID, true)
}

// Rollback executes the rollback and marks the run rolled_back. Refuses to
// run on a run already in rolled_back state (returns ErrAlreadyRolledBack).
func (i *Importer) Rollback(ctx context.Context, runID int64) (*RollbackResult, error) {
	return i.rollback(ctx, runID, false)
}

func (i *Importer) rollback(ctx context.Context, runID int64, preview bool) (*RollbackResult, error) {
	if i.runs == nil || i.snapshots == nil || i.provenance == nil {
		return nil, ErrRollbackUnavailable
	}
	// Nil-guards for the repos this code actually touches: a silent
	// nil-deref would leave the run half-rolled which is worse than refusing
	// to start.
	if i.books == nil || i.authors == nil || i.editions == nil {
		return nil, ErrRollbackUnavailable
	}
	run, err := i.runs.GetByID(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrRunNotFound
	}
	if !preview && run.Status == runStatusRolledBack {
		return nil, ErrAlreadyRolledBack
	}
	result := &RollbackResult{
		RunID:    runID,
		Preview:  preview,
		DryRun:   run.DryRun,
		Status:   run.Status,
		Finished: time.Now().UTC(),
	}
	if run.DryRun {
		// A dry-run import never mutated anything, so rollback is a no-op.
		// Returning early avoids surfacing confusing "would delete" actions
		// for entities the run never actually touched.
		return result, nil
	}

	entities, err := i.snapshots.ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	// Editions before books before authors. Without this, deleting a book
	// while its edition still references it would either fail (FK) or
	// orphan the edition row.
	sort.SliceStable(entities, func(a, b int) bool {
		return rollbackEntityRank(entities[a]) < rollbackEntityRank(entities[b])
	})

	// Execute mode: wrap every write (and every read inside the loop —
	// MaxOpenConns=1 means r.db reads would deadlock against an open tx)
	// in a single transaction so a mid-loop failure aborts cleanly with
	// the database untouched. Without this, partial rollbacks leave the
	// run in 'completed' status with some entities deleted and some not;
	// re-running the rollback then mis-applies restore_* ops against
	// shifted state. Preview keeps the unwrapped repos — it issues no
	// writes anyway.
	books := i.books
	authors := i.authors
	editions := i.editions
	provenance := i.provenance
	runs := i.runs
	var tx *sql.Tx
	if !preview {
		t, err := i.runs.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("begin rollback tx: %w", err)
		}
		tx = t
		books = i.books.WithTx(tx)
		authors = i.authors.WithTx(tx)
		editions = i.editions.WithTx(tx)
		provenance = i.provenance.WithTx(tx)
		runs = i.runs.WithTx(tx)
		// Deferred Rollback no-ops after Commit (ErrTxDone) — safe to
		// always defer it as the abort-on-failure path.
		defer func() {
			if tx != nil {
				_ = tx.Rollback()
			}
		}()
	}

	deletedBooks := map[int64]struct{}{}
	filesTouched := 0

	// abortOnFail wraps an error path inside the !preview loop. With the
	// whole rollback inside one tx a single repo failure is fatal: every
	// subsequent query would error against the poisoned tx, and the deferred
	// Rollback will revert all earlier writes. Returning a sentinel up the
	// stack lets the loop break out cleanly. In preview mode we keep the
	// existing soft-skip behaviour so a partially-broken run still produces
	// a readable plan for the UI.
	var rollbackErr error
	bailOut := func() bool { return !preview && rollbackErr != nil }

	for _, entity := range entities {
		if bailOut() {
			break
		}
		current, perr := provenance.GetByExternal(ctx, entity.SourceID, entity.EntityType, entity.ExternalID)
		if perr != nil {
			if !preview {
				rollbackErr = fmt.Errorf("provenance lookup %s/%s: %w", entity.EntityType, entity.ExternalID, perr)
				continue
			}
			result.Stats.Failed++
			result.Actions = append(result.Actions, RollbackAction{
				EntityType:  entity.EntityType,
				ExternalID:  entity.ExternalID,
				LocalID:     entity.LocalID,
				DisplayName: rollbackDisplayName(ctx, books, authors, editions, entity),
				Outcome:     entity.Outcome,
				Action:      "inspect",
				Reason:      perr.Error(),
			})
			continue
		}
		if current == nil {
			result.Stats.Skipped++
			result.Actions = append(result.Actions, RollbackAction{
				EntityType:  entity.EntityType,
				ExternalID:  entity.ExternalID,
				LocalID:     entity.LocalID,
				DisplayName: rollbackDisplayName(ctx, books, authors, editions, entity),
				Outcome:     entity.Outcome,
				Action:      "skip",
				Reason:      "already rolled back",
			})
			continue
		}
		currentMatches := current.LocalID == entity.LocalID
		ownedByRun := currentMatches && current.ImportRunID != nil && *current.ImportRunID == runID
		if !currentMatches {
			result.Stats.Skipped++
			result.Actions = append(result.Actions, RollbackAction{
				EntityType:  entity.EntityType,
				ExternalID:  entity.ExternalID,
				LocalID:     entity.LocalID,
				DisplayName: rollbackDisplayName(ctx, books, authors, editions, entity),
				Outcome:     entity.Outcome,
				Action:      "skip",
				Reason:      "provenance now points to a different local entity",
			})
			continue
		}

		action := RollbackAction{
			EntityType:  entity.EntityType,
			ExternalID:  entity.ExternalID,
			LocalID:     entity.LocalID,
			DisplayName: rollbackDisplayName(ctx, books, authors, editions, entity),
			Outcome:     entity.Outcome,
		}

		switch {
		case entity.EntityType == entityTypeEdition:
			// Editions are append-only during Calibre import: when outcome
			// is "created" we delete the row; otherwise (no other path
			// today) skip — there's no field-level edition restore.
			if entity.Outcome != outcomeCreated || !ownedByRun {
				action.Action = "skip"
				if !ownedByRun {
					action.Reason = "run is no longer the current provenance owner for this edition"
				} else {
					action.Reason = "edition was not created by this run"
				}
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			action.Action = "delete_edition"
			result.Stats.ActionsPlanned++
			if !preview {
				if err := editions.Delete(ctx, entity.LocalID); err != nil {
					rollbackErr = fmt.Errorf("delete edition %d: %w", entity.LocalID, err)
					continue
				}
				unlinked, err := provenance.DeleteByLocal(ctx, entity.EntityType, entity.LocalID)
				if err != nil {
					rollbackErr = fmt.Errorf("unlink edition %d provenance: %w", entity.LocalID, err)
					continue
				}
				result.Stats.EntitiesDeleted++
				result.Stats.ProvenanceUnlinked += int(unlinked)
			}

		case entity.EntityType == entityTypeBook && entity.Outcome == outcomeCreated:
			if !ownedByRun {
				action.Action = "skip"
				action.Reason = "run is no longer the current provenance owner for this book"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			retain, err := hasProvenanceOutsideRun(ctx, provenance, entity.EntityType, entity.LocalID, runID)
			if err != nil {
				if !preview {
					rollbackErr = fmt.Errorf("provenance list for book %d: %w", entity.LocalID, err)
					continue
				}
				action.Action = "skip"
				action.Reason = err.Error()
				result.Stats.Failed++
				result.Actions = append(result.Actions, action)
				continue
			}
			if retain {
				action.Action = "unlink_provenance"
				action.Reason = "local book retained because another Calibre import link references it"
				result.Stats.ActionsPlanned++
				if !preview {
					if err := provenance.DeleteByExternal(ctx, entity.SourceID, entity.EntityType, entity.ExternalID); err != nil {
						rollbackErr = fmt.Errorf("unlink book %s provenance: %w", entity.ExternalID, err)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
				result.Actions = append(result.Actions, action)
				continue
			}
			// Books that point at on-disk files: count the file path so the
			// API response can warn the caller that on-disk files are NOT
			// removed by rollback. Metadata-only rollback is deliberate —
			// see PR description.
			if book, lookupErr := books.GetByID(ctx, entity.LocalID); lookupErr == nil && book != nil {
				if strings.TrimSpace(book.FilePath) != "" || strings.TrimSpace(book.EbookFilePath) != "" || strings.TrimSpace(book.AudiobookFilePath) != "" {
					filesTouched++
				}
			}
			action.Action = "delete_book"
			result.Stats.ActionsPlanned++
			// Mark as deleted for downstream author-pruning accounting in
			// both preview and execute modes — otherwise the author check
			// sees still-attached books and downgrades delete_author to
			// unlink_provenance only in preview, producing a misleading
			// diff against execute.
			deletedBooks[entity.LocalID] = struct{}{}
			if !preview {
				if err := books.Delete(ctx, entity.LocalID); err != nil {
					rollbackErr = fmt.Errorf("delete book %d: %w", entity.LocalID, err)
					continue
				}
				unlinked, err := provenance.DeleteByLocal(ctx, entity.EntityType, entity.LocalID)
				if err != nil {
					rollbackErr = fmt.Errorf("unlink book %d provenance: %w", entity.LocalID, err)
					continue
				}
				result.Stats.EntitiesDeleted++
				result.Stats.ProvenanceUnlinked += int(unlinked)
			}

		case entity.EntityType == entityTypeBook:
			// Updated-in-place book: field-level restore (safe even if the
			// run no longer owns the row because restoreBookFromSnapshot
			// only reverts a field when current == after).
			before, after, ok := bookRollbackSnapshotFromMetadata(entity.MetadataJSON)
			if !ok {
				action.Action = "skip"
				action.Reason = "no usable snapshot for this entity"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			action.Action = "restore_book"
			result.Stats.ActionsPlanned++
			if !preview {
				book, err := books.GetByID(ctx, entity.LocalID)
				if err != nil {
					rollbackErr = fmt.Errorf("get book %d for restore: %w", entity.LocalID, err)
					continue
				}
				if book == nil {
					action.Action = "skip"
					action.Reason = "book no longer exists"
					result.Stats.Skipped++
					result.Actions = append(result.Actions, action)
					continue
				}
				if restoreBookFromSnapshot(book, before, after) {
					if err := books.Update(ctx, book); err != nil {
						rollbackErr = fmt.Errorf("restore book %d: %w", entity.LocalID, err)
						continue
					}
				}
				if ownedByRun {
					if err := provenance.DeleteByExternal(ctx, entity.SourceID, entity.EntityType, entity.ExternalID); err != nil {
						rollbackErr = fmt.Errorf("unlink book %s provenance: %w", entity.ExternalID, err)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
			}

		case entity.EntityType == entityTypeAuthor && entity.Outcome == outcomeCreated:
			if !ownedByRun {
				action.Action = "skip"
				action.Reason = "run is no longer the current provenance owner for this author"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			// Check books still attached to this author that weren't
			// themselves deleted above; if any remain, retain the author.
			attachedBooks, err := books.ListByAuthorIncludingExcluded(ctx, entity.LocalID)
			if err != nil {
				if !preview {
					rollbackErr = fmt.Errorf("list books for author %d: %w", entity.LocalID, err)
					continue
				}
				action.Action = "skip"
				action.Reason = err.Error()
				result.Stats.Failed++
				result.Actions = append(result.Actions, action)
				continue
			}
			remaining := 0
			for _, book := range attachedBooks {
				if _, ok := deletedBooks[book.ID]; ok {
					continue
				}
				remaining++
			}
			if remaining > 0 {
				action.Action = "unlink_provenance"
				action.Reason = "local author retained because it still has linked books"
				result.Stats.ActionsPlanned++
				if !preview {
					if err := provenance.DeleteByExternal(ctx, entity.SourceID, entity.EntityType, entity.ExternalID); err != nil {
						rollbackErr = fmt.Errorf("unlink author %s provenance: %w", entity.ExternalID, err)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
				result.Actions = append(result.Actions, action)
				continue
			}
			retain, err := hasProvenanceOutsideRun(ctx, provenance, entity.EntityType, entity.LocalID, runID)
			if err != nil {
				if !preview {
					rollbackErr = fmt.Errorf("provenance list for author %d: %w", entity.LocalID, err)
					continue
				}
				action.Action = "skip"
				action.Reason = err.Error()
				result.Stats.Failed++
				result.Actions = append(result.Actions, action)
				continue
			}
			if retain {
				action.Action = "unlink_provenance"
				action.Reason = "local author retained because another Calibre import link references it"
				result.Stats.ActionsPlanned++
				if !preview {
					if err := provenance.DeleteByExternal(ctx, entity.SourceID, entity.EntityType, entity.ExternalID); err != nil {
						rollbackErr = fmt.Errorf("unlink author %s provenance: %w", entity.ExternalID, err)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
				result.Actions = append(result.Actions, action)
				continue
			}
			action.Action = "delete_author"
			result.Stats.ActionsPlanned++
			if !preview {
				if err := authors.Delete(ctx, entity.LocalID); err != nil {
					rollbackErr = fmt.Errorf("delete author %d: %w", entity.LocalID, err)
					continue
				}
				unlinked, err := provenance.DeleteByLocal(ctx, entity.EntityType, entity.LocalID)
				if err != nil {
					rollbackErr = fmt.Errorf("unlink author %d provenance: %w", entity.LocalID, err)
					continue
				}
				result.Stats.EntitiesDeleted++
				result.Stats.ProvenanceUnlinked += int(unlinked)
			}

		case entity.EntityType == entityTypeAuthor:
			before, after, ok := authorRollbackSnapshotFromMetadata(entity.MetadataJSON)
			if !ok {
				action.Action = "skip"
				action.Reason = "no usable snapshot for this entity"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			action.Action = "restore_author"
			result.Stats.ActionsPlanned++
			if !preview {
				author, err := authors.GetByID(ctx, entity.LocalID)
				if err != nil {
					rollbackErr = fmt.Errorf("get author %d for restore: %w", entity.LocalID, err)
					continue
				}
				if author == nil {
					action.Action = "skip"
					action.Reason = "author no longer exists"
					result.Stats.Skipped++
					result.Actions = append(result.Actions, action)
					continue
				}
				if restoreAuthorFromSnapshot(author, before, after) {
					if err := authors.Update(ctx, author); err != nil {
						rollbackErr = fmt.Errorf("restore author %d: %w", entity.LocalID, err)
						continue
					}
				}
				if ownedByRun {
					if err := provenance.DeleteByExternal(ctx, entity.SourceID, entity.EntityType, entity.ExternalID); err != nil {
						rollbackErr = fmt.Errorf("unlink author %s provenance: %w", entity.ExternalID, err)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
			}

		default:
			action.Action = "skip"
			action.Reason = "unsupported entity type"
			result.Stats.Skipped++
		}

		result.Actions = append(result.Actions, action)
	}

	result.Stats.FilesAffected = filesTouched
	if filesTouched > 0 {
		result.FilesOnDiskWarning = fmt.Sprintf("%d book row(s) referenced on-disk files; rollback removes the metadata row only, the files on disk are untouched.", filesTouched)
	}

	if !preview {
		if rollbackErr != nil {
			// Deferred tx.Rollback above reverts every write recorded in
			// this scope; the run row stays in 'completed' so a retry is
			// safe. Surface the error so the API layer can render it.
			return nil, rollbackErr
		}
		// UpdateStatus runs inside the same tx so the run-status flip and
		// the entity deletions commit (or roll back) atomically.
		if err := runs.UpdateStatus(ctx, runID, runStatusRolledBack); err != nil {
			return nil, fmt.Errorf("mark run rolled back: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit rollback tx: %w", err)
		}
		// Clear tx so the deferred Rollback above no-ops (it would
		// otherwise log ErrTxDone which is harmless but noisy).
		tx = nil
		result.Status = runStatusRolledBack
		result.Applied = true
	}
	return result, nil
}

// hasProvenanceOutsideRun reports whether any provenance row points at the
// same local entity but belongs to a different import run. provenance must
// be the tx-wrapped repo when called inside rollback's transaction so the
// read doesn't deadlock against the open writer.
func hasProvenanceOutsideRun(ctx context.Context, provenance *db.CalibreProvenanceRepo, entityType string, localID, runID int64) (bool, error) {
	if provenance == nil || localID == 0 {
		return false, nil
	}
	links, err := provenance.ListByLocal(ctx, entityType, localID)
	if err != nil {
		return false, err
	}
	for _, link := range links {
		if link.ImportRunID == nil || *link.ImportRunID != runID {
			return true, nil
		}
	}
	return false, nil
}

// rollbackDisplayName returns a human-readable label for an action. Takes
// the tx-wrapped repos when called inside rollback's transaction (same
// deadlock-avoidance reason as hasProvenanceOutsideRun).
func rollbackDisplayName(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo, editions *db.EditionRepo, entity models.CalibreEntitySnapshot) string {
	if entity.LocalID == 0 {
		return ""
	}
	switch entity.EntityType {
	case entityTypeBook:
		if books == nil {
			return ""
		}
		b, err := books.GetByID(ctx, entity.LocalID)
		if err != nil || b == nil {
			return ""
		}
		return strings.TrimSpace(b.Title)
	case entityTypeAuthor:
		if authors == nil {
			return ""
		}
		a, err := authors.GetByID(ctx, entity.LocalID)
		if err != nil || a == nil {
			return ""
		}
		return strings.TrimSpace(a.Name)
	case entityTypeEdition:
		if editions == nil {
			return ""
		}
		eds, err := editions.ListByBook(ctx, 0)
		if err != nil {
			return ""
		}
		for _, e := range eds {
			if e.ID == entity.LocalID {
				return strings.TrimSpace(e.Title)
			}
		}
		return ""
	default:
		return ""
	}
}

// rollbackEntityRank orders entities for rollback so children (editions) go
// before parents (books, authors). This avoids leaving a dangling
// edition→book FK while a book is being deleted/restored.
func rollbackEntityRank(entity models.CalibreEntitySnapshot) int {
	switch entity.EntityType {
	case entityTypeEdition:
		return 0
	case entityTypeBook:
		return 1
	case entityTypeAuthor:
		return 2
	default:
		return 3
	}
}

// restoreBookFromSnapshot reverts each book field where the current value
// still equals the post-import snapshot. Returns true iff any field
// changed. Designed so a post-import user edit ("After" no longer matches
// current) is preserved untouched.
func restoreBookFromSnapshot(book *models.Book, before, after *bookRollbackSnapshot) bool {
	if book == nil || before == nil || after == nil {
		return false
	}
	changed := false
	restoreString(&book.ForeignID, before.ForeignID, after.ForeignID, &changed)
	restoreInt64(&book.AuthorID, before.AuthorID, after.AuthorID, &changed)
	restoreString(&book.Title, before.Title, after.Title, &changed)
	restoreString(&book.SortTitle, before.SortTitle, after.SortTitle, &changed)
	restoreTimePtr(&book.ReleaseDate, before.ReleaseDate, after.ReleaseDate, &changed)
	restoreString(&book.Language, before.Language, after.Language, &changed)
	restoreString(&book.Status, before.Status, after.Status, &changed)
	restoreString(&book.FilePath, before.FilePath, after.FilePath, &changed)
	restoreString(&book.MetadataProvider, before.MetadataProvider, after.MetadataProvider, &changed)
	restoreInt64Ptr(&book.CalibreID, before.CalibreID, after.CalibreID, &changed)
	restoreString(&book.MediaType, before.MediaType, after.MediaType, &changed)
	restoreBool(&book.AnyEditionOK, before.AnyEditionOK, after.AnyEditionOK, &changed)
	restoreBool(&book.Monitored, before.Monitored, after.Monitored, &changed)
	return changed
}

func restoreAuthorFromSnapshot(author *models.Author, before, after *authorRollbackSnapshot) bool {
	if author == nil || before == nil || after == nil {
		return false
	}
	changed := false
	restoreString(&author.ForeignID, before.ForeignID, after.ForeignID, &changed)
	restoreString(&author.Name, before.Name, after.Name, &changed)
	restoreString(&author.SortName, before.SortName, after.SortName, &changed)
	restoreString(&author.MetadataProvider, before.MetadataProvider, after.MetadataProvider, &changed)
	restoreBool(&author.Monitored, before.Monitored, after.Monitored, &changed)
	return changed
}

func restoreString(target *string, before, after string, changed *bool) {
	if *target != after {
		return
	}
	if *target != before {
		*changed = true
	}
	*target = before
}

func restoreInt64(target *int64, before, after int64, changed *bool) {
	if *target != after {
		return
	}
	if *target != before {
		*changed = true
	}
	*target = before
}

func restoreBool(target *bool, before, after bool, changed *bool) {
	if *target != after {
		return
	}
	if *target != before {
		*changed = true
	}
	*target = before
}

func restoreInt64Ptr(target **int64, before, after *int64, changed *bool) {
	if !equalInt64Ptr(*target, after) {
		return
	}
	if !equalInt64Ptr(*target, before) {
		*changed = true
	}
	*target = cloneInt64Ptr(before)
}

func restoreTimePtr(target **time.Time, before, after *time.Time, changed *bool) {
	if !equalTimePtr(*target, after) {
		return
	}
	if !equalTimePtr(*target, before) {
		*changed = true
	}
	*target = cloneTimePtr(before)
}

func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
