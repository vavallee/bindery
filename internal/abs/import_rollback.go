package abs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type RollbackStats struct {
	ActionsPlanned     int `json:"actionsPlanned"`
	EntitiesDeleted    int `json:"entitiesDeleted"`
	ProvenanceUnlinked int `json:"provenanceUnlinked"`
	Skipped            int `json:"skipped"`
	Failed             int `json:"failed"`
}

type RollbackAction struct {
	EntityType  string `json:"entityType"`
	ExternalID  string `json:"externalId"`
	LocalID     int64  `json:"localId"`
	DisplayName string `json:"displayName,omitempty"`
	Outcome     string `json:"outcome"`
	Action      string `json:"action"`
	Reason      string `json:"reason,omitempty"`
}

type RollbackResult struct {
	RunID    int64            `json:"runId"`
	Preview  bool             `json:"preview"`
	DryRun   bool             `json:"dryRun"`
	Status   string           `json:"status"`
	Stats    RollbackStats    `json:"stats"`
	Actions  []RollbackAction `json:"actions"`
	Finished time.Time        `json:"finishedAt"`
}

func (i *Importer) RecentRuns(ctx context.Context, limit int) ([]models.ABSImportRun, error) {
	if i.runs == nil {
		return nil, nil
	}
	return i.runs.ListRecent(ctx, limit)
}

func HydrateRun(run models.ABSImportRun) PersistedImportRun {
	out := PersistedImportRun{
		ID:          run.ID,
		SourceID:    run.SourceID,
		SourceLabel: run.SourceLabel,
		BaseURL:     run.BaseURL,
		LibraryID:   run.LibraryID,
		Status:      run.Status,
		DryRun:      run.DryRun,
		StartedAt:   run.StartedAt,
		FinishedAt:  run.FinishedAt,
		Source: ImportSourceSnapshot{
			SourceID:  run.SourceID,
			Label:     run.SourceLabel,
			BaseURL:   run.BaseURL,
			LibraryID: run.LibraryID,
			DryRun:    run.DryRun,
		},
		Summary: ImportSummary{DryRun: run.DryRun},
	}
	if strings.TrimSpace(run.SourceConfigJSON) != "" {
		_ = json.Unmarshal([]byte(run.SourceConfigJSON), &out.Source)
	}
	if strings.TrimSpace(run.CheckpointJSON) != "" && strings.TrimSpace(run.CheckpointJSON) != "{}" {
		var checkpoint ImportCheckpoint
		if err := json.Unmarshal([]byte(run.CheckpointJSON), &checkpoint); err == nil {
			out.Checkpoint = &checkpoint
		}
	}
	if strings.TrimSpace(run.SummaryJSON) != "" {
		_ = json.Unmarshal([]byte(run.SummaryJSON), &out.Summary)
	}
	return out
}

func (i *Importer) GetRun(ctx context.Context, runID int64) (*models.ABSImportRun, error) {
	if i.runs == nil {
		return nil, nil
	}
	return i.runs.GetByID(ctx, runID)
}

func (i *Importer) RollbackPreview(ctx context.Context, runID int64) (*RollbackResult, error) {
	return i.rollback(ctx, runID, true)
}

func (i *Importer) Rollback(ctx context.Context, runID int64) (*RollbackResult, error) {
	return i.rollback(ctx, runID, false)
}

func (i *Importer) rollback(ctx context.Context, runID int64, preview bool) (*RollbackResult, error) {
	if i.runs == nil || i.runEntities == nil {
		return nil, errors.New("abs rollback is unavailable")
	}
	// Nil-repo guards: every case below relies on at least one of these, and a
	// silent nil-deref would turn "safe rollback" into a crash that leaves the
	// run in an inconsistent half-rolled state.
	if i.provenance == nil || i.books == nil || i.authors == nil || i.series == nil || i.editions == nil {
		return nil, errors.New("abs rollback is unavailable: one or more repositories are not configured")
	}
	run, err := i.runs.GetByID(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("abs import run %d not found", runID)
	}
	result := &RollbackResult{
		RunID:    runID,
		Preview:  preview,
		DryRun:   run.DryRun,
		Status:   run.Status,
		Finished: time.Now().UTC(),
	}
	if run.DryRun {
		return result, nil
	}
	entities, err := i.runEntities.ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entities, func(a, b int) bool {
		return rollbackEntityRank(entities[a]) < rollbackEntityRank(entities[b])
	})
	type set map[int64]struct{}
	deletedBooks := make(set)
	createdBookEntities := make(map[int64]models.ABSImportRunEntity)
	runCreatedSeries := make(set)
	seriesIdentityEntities := make(set)
	runSeriesMemberships := make(map[int64]set)
	seriesDeleteMembershipEntityID := make(map[int64]int64)
	for _, entity := range entities {
		if entity.EntityType == entityTypeBook && entity.Outcome == itemOutcomeCreated && entity.LocalID != 0 {
			createdBookEntities[entity.LocalID] = entity
		}
		if entity.EntityType != entityTypeSeries || entity.LocalID == 0 {
			continue
		}
		metadata := runEntityMetadataData(entity.MetadataJSON)
		bookID := metadataBookID(metadata)
		if entity.Outcome == itemOutcomeCreated {
			runCreatedSeries[entity.LocalID] = struct{}{}
		}
		if bookID > 0 {
			if runSeriesMemberships[entity.LocalID] == nil {
				runSeriesMemberships[entity.LocalID] = make(set)
			}
			runSeriesMemberships[entity.LocalID][bookID] = struct{}{}
			if entity.Outcome == itemOutcomeCreated && entity.ID > seriesDeleteMembershipEntityID[entity.LocalID] {
				seriesDeleteMembershipEntityID[entity.LocalID] = entity.ID
			}
		} else {
			seriesIdentityEntities[entity.LocalID] = struct{}{}
		}
	}
	for _, entity := range entities {
		current, err := i.provenance.GetByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID)
		if err != nil {
			result.Stats.Failed++
			result.Actions = append(result.Actions, RollbackAction{
				EntityType:  entity.EntityType,
				ExternalID:  entity.ExternalID,
				LocalID:     entity.LocalID,
				DisplayName: i.rollbackActionDisplayName(ctx, entity),
				Outcome:     entity.Outcome,
				Action:      "inspect",
				Reason:      err.Error(),
			})
			continue
		}
		// NOTE: Intentionally no blanket "current owner must equal runID" gate
		// here. A shared-entity restore (book/author snapshot) is safe to run
		// field-by-field even if the provenance now points to another run —
		// restoreFromSnapshot only reverts fields where current == after, so
		// post-import edits or later re-imports stay intact. Destructive cases
		// (delete_book, delete_edition, delete_author, unlink_provenance) still
		// check ownership per-case below.
		if current == nil {
			result.Stats.Skipped++
			result.Actions = append(result.Actions, RollbackAction{
				EntityType:  entity.EntityType,
				ExternalID:  entity.ExternalID,
				LocalID:     entity.LocalID,
				DisplayName: i.rollbackActionDisplayName(ctx, entity),
				Outcome:     entity.Outcome,
				Action:      "skip",
				Reason:      "already rolled back",
			})
			continue
		}
		currentMatchesEntity := current.LocalID == entity.LocalID
		ownedByRun := currentMatchesEntity && current.ImportRunID != nil && *current.ImportRunID == runID
		if !currentMatchesEntity {
			result.Stats.Skipped++
			result.Actions = append(result.Actions, RollbackAction{
				EntityType:  entity.EntityType,
				ExternalID:  entity.ExternalID,
				LocalID:     entity.LocalID,
				DisplayName: i.rollbackActionDisplayName(ctx, entity),
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
			DisplayName: i.rollbackActionDisplayName(ctx, entity),
			Outcome:     entity.Outcome,
		}
		metadata := runEntityMetadataData(entity.MetadataJSON)
		bookBefore, bookAfter, hasBookSnapshot := bookRollbackSnapshotFromMetadata(entity.MetadataJSON)
		authorBefore, authorAfter, hasAuthorSnapshot := authorRollbackSnapshotFromMetadata(entity.MetadataJSON)
		switch {
		case entity.EntityType == entityTypeBook && entity.Outcome == itemOutcomeCreated:
			if !ownedByRun {
				action.Action = "skip"
				action.Reason = "run is no longer the current provenance owner for this book"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			retainLocal, err := i.hasProvenanceOutsideRun(ctx, entity.EntityType, entity.LocalID, runID)
			if err != nil {
				action.Action = "skip"
				action.Reason = err.Error()
				result.Stats.Failed++
				result.Actions = append(result.Actions, action)
				continue
			}
			if retainLocal {
				action.Action = "unlink_provenance"
				action.Reason = "local book retained because another ABS import link references it"
				result.Stats.ActionsPlanned++
				if !preview {
					if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
				result.Actions = append(result.Actions, action)
				continue
			}
			action.Action = "delete_book"
			result.Stats.ActionsPlanned++
			if !preview {
				if err := i.books.Delete(ctx, entity.LocalID); err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				unlinked, err := i.deleteProvenanceByLocal(ctx, entity.EntityType, entity.LocalID)
				if err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				deletedBooks[entity.LocalID] = struct{}{}
				result.Stats.EntitiesDeleted++
				result.Stats.ProvenanceUnlinked += unlinked
			}
		case entity.EntityType == entityTypeBook && hasBookSnapshot:
			// Snapshot restore is safe to attempt regardless of provenance
			// ownership: restoreBookFromSnapshot only reverts fields where the
			// current value still equals the post-import ("after") snapshot,
			// so post-import user edits stay intact and a shared canonical book
			// owned by another run isn't harmed.
			action.Action = "restore_book"
			result.Stats.ActionsPlanned++
			if !preview {
				book, err := i.books.GetByID(ctx, entity.LocalID)
				if err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				if book == nil {
					action.Action = "skip"
					action.Reason = "book no longer exists"
					result.Stats.Skipped++
					result.Actions = append(result.Actions, action)
					continue
				}
				if restoreBookFromSnapshot(book, bookBefore, bookAfter) {
					if err := i.books.Update(ctx, book); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
				}
				if ownedByRun {
					if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
			}
		case entity.EntityType == entityTypeAuthor && entity.Outcome == itemOutcomeCreated:
			if !ownedByRun {
				action.Action = "skip"
				action.Reason = "run is no longer the current provenance owner for this author"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			books, err := i.books.ListByAuthorIncludingExcluded(ctx, entity.LocalID)
			if err != nil {
				action.Action = "skip"
				action.Reason = err.Error()
				result.Stats.Failed++
				result.Actions = append(result.Actions, action)
				continue
			}
			remaining := 0
			blocked := false
			for _, book := range books {
				if _, ok := deletedBooks[book.ID]; ok {
					continue
				}
				if bookEntity, ok := createdBookEntities[book.ID]; ok {
					bookCurrent, err := i.provenance.GetByExternal(ctx, bookEntity.SourceID, bookEntity.LibraryID, bookEntity.EntityType, bookEntity.ExternalID)
					if err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						blocked = true
						break
					}
					if bookCurrent != nil && bookCurrent.ImportRunID != nil && *bookCurrent.ImportRunID == runID {
						if !preview {
							if err := i.books.Delete(ctx, book.ID); err != nil {
								action.Action = "skip"
								action.Reason = err.Error()
								result.Stats.Failed++
								blocked = true
								break
							}
							unlinked, err := i.deleteProvenanceByLocal(ctx, bookEntity.EntityType, book.ID)
							if err != nil {
								action.Action = "skip"
								action.Reason = err.Error()
								result.Stats.Failed++
								blocked = true
								break
							}
							deletedBooks[book.ID] = struct{}{}
							result.Stats.EntitiesDeleted++
							result.Stats.ProvenanceUnlinked += unlinked
						}
						continue
					}
				}
				remaining++
			}
			if blocked {
				result.Actions = append(result.Actions, action)
				continue
			}
			if remaining > 0 {
				action.Action = "unlink_provenance"
				action.Reason = "local author retained because it still has linked books"
				result.Stats.ActionsPlanned++
				if !preview {
					if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
				result.Actions = append(result.Actions, action)
				continue
			}
			retainLocal, err := i.hasProvenanceOutsideRun(ctx, entity.EntityType, entity.LocalID, runID)
			if err != nil {
				action.Action = "skip"
				action.Reason = err.Error()
				result.Stats.Failed++
				result.Actions = append(result.Actions, action)
				continue
			}
			if retainLocal {
				action.Action = "unlink_provenance"
				action.Reason = "local author retained because another ABS import link references it"
				result.Stats.ActionsPlanned++
				if !preview {
					if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
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
				if err := i.authors.Delete(ctx, entity.LocalID); err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				unlinked, err := i.deleteProvenanceByLocal(ctx, entity.EntityType, entity.LocalID)
				if err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				result.Stats.EntitiesDeleted++
				result.Stats.ProvenanceUnlinked += unlinked
			}
		case entity.EntityType == entityTypeAuthor && hasAuthorSnapshot:
			// Same safety argument as the book snapshot case: field-level
			// restore preserves post-import author edits and won't trample
			// canonical shared data owned by another run.
			action.Action = "restore_author"
			result.Stats.ActionsPlanned++
			if !preview {
				author, err := i.authors.GetByID(ctx, entity.LocalID)
				if err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				if author == nil {
					action.Action = "skip"
					action.Reason = "author no longer exists"
					result.Stats.Skipped++
					result.Actions = append(result.Actions, action)
					continue
				}
				if restoreAuthorFromSnapshot(author, authorBefore, authorAfter) {
					if err := i.authors.Update(ctx, author); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
				}
				if ownedByRun {
					if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					result.Stats.ProvenanceUnlinked++
				}
			}
		case entity.EntityType == entityTypeEdition && entity.Outcome == itemOutcomeCreated:
			if bookID, _ := metadata["bookId"].(float64); int64(bookID) != 0 {
				if _, ok := deletedBooks[int64(bookID)]; ok {
					action.Action = "skip"
					action.Reason = "parent book rollback removes this edition implicitly"
					result.Stats.Skipped++
					result.Actions = append(result.Actions, action)
					continue
				}
			}
			if !ownedByRun {
				action.Action = "skip"
				action.Reason = "run is no longer the current provenance owner for this edition"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			action.Action = "delete_edition"
			result.Stats.ActionsPlanned++
			if !preview {
				if err := i.editions.Delete(ctx, entity.LocalID); err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				unlinked, err := i.deleteProvenanceByLocal(ctx, entity.EntityType, entity.LocalID)
				if err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				result.Stats.EntitiesDeleted++
				result.Stats.ProvenanceUnlinked += unlinked
			}
		case entity.EntityType == entityTypeSeries:
			if !ownedByRun {
				action.Action = "skip"
				action.Reason = "run is no longer the current provenance owner for this series"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			bookID := metadataBookID(metadata)
			if bookID > 0 {
				action.Action = "unlink_series"
				result.Stats.ActionsPlanned++
				retainMembership, err := i.hasMatchingProvenanceOutsideRun(ctx, entity, runID)
				if err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				if !retainMembership {
					retainBook, err := i.hasProvenanceOutsideRun(ctx, entityTypeBook, int64(bookID), runID)
					if err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					retainSeries, err := i.hasProvenanceOutsideRun(ctx, entity.EntityType, entity.LocalID, runID)
					if err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					retainMembership = retainBook && retainSeries
				}
				if retainMembership {
					action.Action = "unlink_provenance"
					action.Reason = "series membership retained because another ABS import link references it"
					if !preview {
						if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
							action.Action = "skip"
							action.Reason = err.Error()
							result.Stats.Failed++
							result.Actions = append(result.Actions, action)
							continue
						}
						result.Stats.ProvenanceUnlinked++
					}
					result.Actions = append(result.Actions, action)
					continue
				}
				_, createdByRun := runCreatedSeries[entity.LocalID]
				_, hasIdentityEntity := seriesIdentityEntities[entity.LocalID]
				deleteSeries := false
				if createdByRun && !hasIdentityEntity && seriesDeleteMembershipEntityID[entity.LocalID] == entity.ID {
					remaining, err := i.remainingSeriesBooksAfterRunRollback(ctx, entity.LocalID, runSeriesMemberships[entity.LocalID])
					if err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					retainSeries, err := i.hasProvenanceOutsideRun(ctx, entity.EntityType, entity.LocalID, runID)
					if err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					if remaining <= 0 && !retainSeries {
						action.Action = "delete_series"
						deleteSeries = true
					}
				}
				if !preview {
					if err := i.series.UnlinkBook(ctx, entity.LocalID, int64(bookID)); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					if deleteSeries {
						if err := i.series.Delete(ctx, entity.LocalID); err != nil {
							action.Action = "skip"
							action.Reason = err.Error()
							result.Stats.Failed++
							result.Actions = append(result.Actions, action)
							continue
						}
						unlinked, err := i.deleteProvenanceByLocal(ctx, entity.EntityType, entity.LocalID)
						if err != nil {
							action.Action = "skip"
							action.Reason = err.Error()
							result.Stats.Failed++
							result.Actions = append(result.Actions, action)
							continue
						}
						result.Stats.EntitiesDeleted++
						result.Stats.ProvenanceUnlinked += unlinked
					} else if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					} else {
						result.Stats.ProvenanceUnlinked++
					}
				}
			} else {
				action.Action = "unlink_provenance"
				if entity.Outcome == itemOutcomeCreated {
					retainSeries, err := i.hasProvenanceOutsideRun(ctx, entity.EntityType, entity.LocalID, runID)
					if err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					if retainSeries {
						action.Reason = "local series retained because another ABS import link references it"
						result.Stats.ActionsPlanned++
						if !preview {
							if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
								action.Action = "skip"
								action.Reason = err.Error()
								result.Stats.Failed++
								result.Actions = append(result.Actions, action)
								continue
							}
							result.Stats.ProvenanceUnlinked++
						}
						result.Actions = append(result.Actions, action)
						continue
					}
					remaining, err := i.remainingSeriesBooksAfterRunRollback(ctx, entity.LocalID, runSeriesMemberships[entity.LocalID])
					if err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					}
					if remaining <= 0 {
						action.Action = "delete_series"
					}
				}
				result.Stats.ActionsPlanned++
				if !preview {
					if action.Action == "delete_series" {
						if err := i.series.Delete(ctx, entity.LocalID); err != nil {
							action.Action = "skip"
							action.Reason = err.Error()
							result.Stats.Failed++
							result.Actions = append(result.Actions, action)
							continue
						}
						unlinked, err := i.deleteProvenanceByLocal(ctx, entity.EntityType, entity.LocalID)
						if err != nil {
							action.Action = "skip"
							action.Reason = err.Error()
							result.Stats.Failed++
							result.Actions = append(result.Actions, action)
							continue
						}
						result.Stats.EntitiesDeleted++
						result.Stats.ProvenanceUnlinked += unlinked
					} else if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
						action.Action = "skip"
						action.Reason = err.Error()
						result.Stats.Failed++
						result.Actions = append(result.Actions, action)
						continue
					} else {
						result.Stats.ProvenanceUnlinked++
					}
				}
			}
		default:
			if !ownedByRun {
				action.Action = "skip"
				action.Reason = "run is no longer the current provenance owner for this entity"
				result.Stats.Skipped++
				result.Actions = append(result.Actions, action)
				continue
			}
			action.Action = "unlink_provenance"
			result.Stats.ActionsPlanned++
			if !preview {
				if err := i.provenance.DeleteByExternal(ctx, entity.SourceID, entity.LibraryID, entity.EntityType, entity.ExternalID); err != nil {
					action.Action = "skip"
					action.Reason = err.Error()
					result.Stats.Failed++
					result.Actions = append(result.Actions, action)
					continue
				}
				result.Stats.ProvenanceUnlinked++
			}
		}
		result.Actions = append(result.Actions, action)
	}
	if !preview && result.Stats.Failed == 0 {
		if err := i.runs.UpdateStatus(ctx, runID, runStatusRolledBack); err != nil {
			return nil, err
		}
		result.Status = runStatusRolledBack
	}
	return result, nil
}

func restoreBookFromSnapshot(book *models.Book, before, after *bookRollbackSnapshot) bool {
	if book == nil || before == nil || after == nil {
		return false
	}
	changed := false
	restoreString(&book.ForeignID, before.ForeignID, after.ForeignID, &changed)
	restoreInt64(&book.AuthorID, before.AuthorID, after.AuthorID, &changed)
	restoreString(&book.Title, before.Title, after.Title, &changed)
	restoreString(&book.SortTitle, before.SortTitle, after.SortTitle, &changed)
	restoreString(&book.OriginalTitle, before.OriginalTitle, after.OriginalTitle, &changed)
	restoreString(&book.Description, before.Description, after.Description, &changed)
	restoreString(&book.ImageURL, before.ImageURL, after.ImageURL, &changed)
	restoreTimePtr(&book.ReleaseDate, before.ReleaseDate, after.ReleaseDate, &changed)
	restoreStrings(&book.Genres, before.Genres, after.Genres, &changed)
	restoreFloat64(&book.AverageRating, before.AverageRating, after.AverageRating, &changed)
	restoreInt(&book.RatingsCount, before.RatingsCount, after.RatingsCount, &changed)
	restoreBool(&book.Monitored, before.Monitored, after.Monitored, &changed)
	restoreString(&book.Status, before.Status, after.Status, &changed)
	restoreBool(&book.AnyEditionOK, before.AnyEditionOK, after.AnyEditionOK, &changed)
	restoreInt64Ptr(&book.SelectedEditionID, before.SelectedEditionID, after.SelectedEditionID, &changed)
	restoreString(&book.Language, before.Language, after.Language, &changed)
	restoreString(&book.MediaType, before.MediaType, after.MediaType, &changed)
	restoreString(&book.Narrator, before.Narrator, after.Narrator, &changed)
	restoreInt(&book.DurationSeconds, before.DurationSeconds, after.DurationSeconds, &changed)
	restoreString(&book.ASIN, before.ASIN, after.ASIN, &changed)
	restoreInt64Ptr(&book.CalibreID, before.CalibreID, after.CalibreID, &changed)
	restoreString(&book.MetadataProvider, before.MetadataProvider, after.MetadataProvider, &changed)
	restoreTimePtr(&book.LastMetadataRefreshAt, before.LastMetadataRefreshAt, after.LastMetadataRefreshAt, &changed)
	return changed
}

// restoreAuthorFromSnapshot mirrors restoreBookFromSnapshot: it only touches
// fields the importer writes, and only when the current value still matches
// the post-import snapshot (so a post-import user edit stays intact).
func restoreAuthorFromSnapshot(author *models.Author, before, after *authorRollbackSnapshot) bool {
	if author == nil || before == nil || after == nil {
		return false
	}
	changed := false
	restoreString(&author.ForeignID, before.ForeignID, after.ForeignID, &changed)
	restoreString(&author.Name, before.Name, after.Name, &changed)
	restoreString(&author.SortName, before.SortName, after.SortName, &changed)
	restoreString(&author.Description, before.Description, after.Description, &changed)
	restoreString(&author.ImageURL, before.ImageURL, after.ImageURL, &changed)
	restoreString(&author.Disambiguation, before.Disambiguation, after.Disambiguation, &changed)
	restoreString(&author.MetadataProvider, before.MetadataProvider, after.MetadataProvider, &changed)
	restoreTimePtr(&author.LastMetadataRefreshAt, before.LastMetadataRefreshAt, after.LastMetadataRefreshAt, &changed)
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

func restoreInt(target *int, before, after int, changed *bool) {
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

func restoreFloat64(target *float64, before, after float64, changed *bool) {
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

func restoreStrings(target *[]string, before, after []string, changed *bool) {
	if !equalStrings(*target, after) {
		return
	}
	if !equalStrings(*target, before) {
		*changed = true
	}
	*target = append([]string(nil), before...)
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

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for idx := range a {
		if a[idx] != b[idx] {
			return false
		}
	}
	return true
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

func (i *Importer) rollbackActionDisplayName(ctx context.Context, entity models.ABSImportRunEntity) string {
	if entity.LocalID == 0 {
		return ""
	}
	switch entity.EntityType {
	case entityTypeBook:
		if i.books == nil {
			return ""
		}
		book, err := i.books.GetByID(ctx, entity.LocalID)
		if err != nil || book == nil {
			return ""
		}
		return strings.TrimSpace(book.Title)
	case entityTypeAuthor:
		if i.authors == nil {
			return ""
		}
		author, err := i.authors.GetByID(ctx, entity.LocalID)
		if err != nil || author == nil {
			return ""
		}
		return strings.TrimSpace(author.Name)
	case entityTypeSeries:
		if i.series == nil {
			return ""
		}
		series, err := i.series.GetByID(ctx, entity.LocalID)
		if err != nil || series == nil {
			return ""
		}
		return strings.TrimSpace(series.Title)
	case entityTypeEdition:
		if i.editions == nil {
			return ""
		}
		edition, err := i.editions.GetByForeignID(ctx, absForeignID("edition", entity.LibraryID, entity.ExternalID))
		if err != nil || edition == nil {
			return ""
		}
		return strings.TrimSpace(edition.Title)
	default:
		return ""
	}
}

func metadataBookID(metadata map[string]any) int64 {
	switch value := metadata["bookId"].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func (i *Importer) hasProvenanceOutsideRun(ctx context.Context, entityType string, localID, runID int64) (bool, error) {
	if i.provenance == nil || localID == 0 {
		return false, nil
	}
	links, err := i.provenance.ListByLocal(ctx, entityType, localID)
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

func (i *Importer) hasMatchingProvenanceOutsideRun(ctx context.Context, entity models.ABSImportRunEntity, runID int64) (bool, error) {
	if i.provenance == nil || entity.LocalID == 0 {
		return false, nil
	}
	links, err := i.provenance.ListByLocal(ctx, entity.EntityType, entity.LocalID)
	if err != nil {
		return false, err
	}
	for _, link := range links {
		if link.ExternalID != entity.ExternalID {
			continue
		}
		if link.ImportRunID == nil || *link.ImportRunID != runID {
			return true, nil
		}
	}
	return false, nil
}

func (i *Importer) remainingSeriesBooksAfterRunRollback(ctx context.Context, seriesID int64, runOwnedBookIDs map[int64]struct{}) (int, error) {
	books, err := i.series.ListBooksInSeries(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	remaining := 0
	for _, book := range books {
		if _, ok := runOwnedBookIDs[book.ID]; ok {
			continue
		}
		remaining++
	}
	return remaining, nil
}

// rollbackEntityRank orders entities for rollback so children (editions) are
// unwound before parents (books, series, authors). Editions first avoids
// leaving dangling edition→book FK references while we restore the book;
// authors last so book-count preconditions have already been reduced by
// prior book-delete steps.
func rollbackEntityRank(entity models.ABSImportRunEntity) int {
	switch entity.EntityType {
	case entityTypeEdition:
		return 0
	case entityTypeBook:
		return 1
	case entityTypeSeries:
		if metadataBookID(runEntityMetadataData(entity.MetadataJSON)) > 0 {
			return 2
		}
		return 3
	case entityTypeAuthor:
		return 4
	default:
		return 5
	}
}
