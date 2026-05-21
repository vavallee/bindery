package abs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

type bookUpsertResult struct {
	row       *models.Book
	matchedBy string
}

type seriesUpsertResult struct {
	SeriesID             int64
	IdentityExternalID   string
	MembershipExternalID string
	CountKey             string
	CreatedSeries        bool
	MembershipCreated    bool
	Linked               bool
	MatchedBy            string
}

func (i *Importer) resolveAuthor(ctx context.Context, cfg ImportConfig, runID int64, item NormalizedLibraryItem, allowCreate bool, matcher *authorMatcher) (*models.Author, bool, string, metadataMergeResult, error) {
	if len(item.Authors) == 0 {
		return nil, false, "", metadataMergeResult{}, errors.New("item has no authors")
	}
	primary := item.Authors[0]
	name := strings.TrimSpace(primary.Name)
	if name == "" {
		return nil, false, "", metadataMergeResult{}, errors.New("primary author name is empty")
	}
	if strings.TrimSpace(item.ResolvedAuthorForeignID) != "" || strings.TrimSpace(item.ResolvedAuthorName) != "" {
		return i.resolveManualAuthor(ctx, cfg, runID, item, matcher)
	}
	if matcher == nil {
		loaded, err := i.newAuthorMatcher(ctx)
		if err != nil {
			return nil, false, "", metadataMergeResult{}, err
		}
		matcher = loaded
	}
	externalID := authorExternalID(primary)
	if i.provenance != nil {
		if link, err := i.provenance.GetByExternal(ctx, cfg.SourceID, item.LibraryID, entityTypeAuthor, externalID); err != nil {
			return nil, false, "", metadataMergeResult{}, err
		} else if link != nil {
			existing, err := i.authors.GetByID(ctx, link.LocalID)
			if err != nil {
				return nil, false, "", metadataMergeResult{}, err
			}
			if existing != nil {
				matches, err := matcher.authorMatchesABSName(ctx, existing, name)
				if err != nil {
					return nil, false, "", metadataMergeResult{}, err
				}
				if matches {
					if !cfg.DryRun {
						if err := i.upsertProvenance(ctx, &models.ABSProvenance{
							SourceID:    cfg.SourceID,
							LibraryID:   item.LibraryID,
							EntityType:  entityTypeAuthor,
							ExternalID:  externalID,
							LocalID:     existing.ID,
							ItemID:      item.ItemID,
							ImportRunID: ptrInt64(runID),
						}); err != nil {
							return nil, false, "", metadataMergeResult{}, err
						}
					}
					if cfg.DryRun {
						_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, externalID, existing.ID, itemOutcomeLinked, nil)
						return existing, false, "provenance", metadataMergeResult{}, nil
					}
					if err := i.recordAuthorBeforeSnapshot(ctx, runID, cfg, item, externalID, existing, itemOutcomeLinked, nil); err != nil {
						slog.Warn("abs import: persist author rollback snapshot failed", "authorID", existing.ID, "runID", runID, "error", err)
					}
					metaResult, err := i.enrichAuthor(ctx, cfg, item, existing, matcher)
					if perr := i.recordAuthorAfterSnapshot(ctx, runID, cfg, item, externalID, existing.ID, itemOutcomeLinked, nil); perr != nil {
						slog.Warn("abs import: persist author rollback snapshot failed", "authorID", existing.ID, "runID", runID, "error", perr)
					}
					return existing, false, "provenance", metaResult, err
				}
				slog.Info("abs import: ignored stale author provenance",
					"author", name,
					"authorID", existing.ID,
					"externalID", externalID)
			}
		}
	}

	if existing, matchedBy, ambiguous, err := matcher.findAuthorByName(ctx, name); err != nil {
		return nil, false, "", metadataMergeResult{}, err
	} else if existing != nil {
		if !cfg.DryRun {
			if err := i.upsertProvenance(ctx, &models.ABSProvenance{
				SourceID:    cfg.SourceID,
				LibraryID:   item.LibraryID,
				EntityType:  entityTypeAuthor,
				ExternalID:  externalID,
				LocalID:     existing.ID,
				ItemID:      item.ItemID,
				ImportRunID: ptrInt64(runID),
			}); err != nil {
				return nil, false, "", metadataMergeResult{}, err
			}
			if shouldRecordAuthorVariantAlias(matchedBy) {
				i.recordAuthorVariantAlias(ctx, existing.ID, name, matcher)
			}
		}
		if cfg.DryRun {
			_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, externalID, existing.ID, itemOutcomeLinked, nil)
			return existing, false, matchedBy, metadataMergeResult{}, nil
		}
		if err := i.recordAuthorBeforeSnapshot(ctx, runID, cfg, item, externalID, existing, itemOutcomeLinked, nil); err != nil {
			slog.Warn("abs import: persist author rollback snapshot failed", "authorID", existing.ID, "runID", runID, "error", err)
		}
		metaResult, err := i.enrichAuthor(ctx, cfg, item, existing, matcher)
		if perr := i.recordAuthorAfterSnapshot(ctx, runID, cfg, item, externalID, existing.ID, itemOutcomeLinked, nil); perr != nil {
			slog.Warn("abs import: persist author rollback snapshot failed", "authorID", existing.ID, "runID", runID, "error", perr)
		}
		return existing, false, matchedBy, metaResult, err
	} else if ambiguous && !allowCreate {
		return nil, false, "", metadataMergeResult{}, reviewRequiredError{Reason: reviewReasonAmbiguousAuthor}
	}

	// No local author matched. Previously a non-ASIN item (allowCreate=false)
	// was parked in the review queue here, which sent the great majority of a
	// folder-backed ABS library to review even for unambiguous, well-known
	// authors (#762). An unmatched author is not an uncertain match: the local
	// matcher found nothing close, so the correct outcome is to create the
	// author. enrichAuthor (below) still performs a confidence-gated upstream
	// lookup and relinks the new row to the metadata provider when it finds a
	// confident match, so import quality is preserved. Only an *ambiguous*
	// author (handled above) still requires human review.

	if cfg.DryRun {
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, externalID, 0, itemOutcomeCreated, nil)
		return &models.Author{
			ForeignID:        absForeignID("author", item.LibraryID, externalID),
			Name:             name,
			SortName:         sortNameFromFull(name),
			Monitored:        true,
			MetadataProvider: providerAudiobookshelf,
		}, true, "created", metadataMergeResult{}, nil
	}

	author := &models.Author{
		ForeignID:        absForeignID("author", item.LibraryID, externalID),
		Name:             name,
		SortName:         sortNameFromFull(name),
		Monitored:        true,
		MetadataProvider: providerAudiobookshelf,
	}
	if err := i.authors.Create(ctx, author); err != nil {
		return nil, false, "", metadataMergeResult{}, err
	}
	matcher.addAuthor(author)
	if err := i.upsertProvenance(ctx, &models.ABSProvenance{
		SourceID:    cfg.SourceID,
		LibraryID:   item.LibraryID,
		EntityType:  entityTypeAuthor,
		ExternalID:  externalID,
		LocalID:     author.ID,
		ItemID:      item.ItemID,
		ImportRunID: ptrInt64(runID),
	}); err != nil {
		return nil, false, "", metadataMergeResult{}, err
	}
	_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, externalID, author.ID, itemOutcomeCreated, nil)
	metaResult, err := i.enrichAuthor(ctx, cfg, item, author, matcher)
	return author, true, "created", metaResult, err
}

func (i *Importer) resolveManualAuthor(ctx context.Context, cfg ImportConfig, runID int64, item NormalizedLibraryItem, matcher *authorMatcher) (*models.Author, bool, string, metadataMergeResult, error) {
	primary := item.Authors[0]
	absName := strings.TrimSpace(primary.Name)
	absExternalID := authorExternalID(primary)
	foreignID := strings.TrimSpace(item.ResolvedAuthorForeignID)
	name := strings.TrimSpace(item.ResolvedAuthorName)
	if foreignID == "" || name == "" {
		return nil, false, "", metadataMergeResult{}, errors.New("resolved author requires foreignAuthorId and authorName")
	}
	if matcher == nil {
		loaded, err := i.newAuthorMatcher(ctx)
		if err != nil {
			return nil, false, "", metadataMergeResult{}, err
		}
		matcher = loaded
	}

	if existing, err := i.authors.GetByForeignID(ctx, foreignID); err != nil {
		return nil, false, "", metadataMergeResult{}, err
	} else if existing != nil {
		if !cfg.DryRun {
			if err := i.upsertProvenance(ctx, &models.ABSProvenance{
				SourceID:    cfg.SourceID,
				LibraryID:   item.LibraryID,
				EntityType:  entityTypeAuthor,
				ExternalID:  absExternalID,
				LocalID:     existing.ID,
				ItemID:      item.ItemID,
				ImportRunID: ptrInt64(runID),
			}); err != nil {
				return nil, false, "", metadataMergeResult{}, err
			}
			if normalizeAuthorName(absName) != normalizeAuthorName(existing.Name) {
				i.recordAuthorVariantAlias(ctx, existing.ID, absName, matcher)
			}
		}
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, absExternalID, existing.ID, itemOutcomeLinked, map[string]string{"matchedBy": "manual_author"})
		return existing, false, "manual_author", metadataMergeResult{}, nil
	}

	if existing, _, ambiguous, err := matcher.findAuthorByName(ctx, name); err != nil {
		return nil, false, "", metadataMergeResult{}, err
	} else if ambiguous {
		return nil, false, "", metadataMergeResult{}, reviewRequiredError{Reason: reviewReasonAmbiguousAuthor}
	} else if existing != nil {
		manualMeta := map[string]string{"matchedBy": "manual_author_name"}
		if cfg.DryRun {
			_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, absExternalID, existing.ID, itemOutcomeLinked, manualMeta)
			return existing, false, "manual_author", metadataMergeResult{}, nil
		}
		if err := i.recordAuthorBeforeSnapshot(ctx, runID, cfg, item, absExternalID, existing, itemOutcomeLinked, map[string]any{"matchedBy": "manual_author_name"}); err != nil {
			slog.Warn("abs import: persist author rollback snapshot failed", "authorID", existing.ID, "runID", runID, "error", err)
		}
		if existing.ForeignID == "" || strings.HasPrefix(existing.ForeignID, "abs:") {
			existing.ForeignID = foreignID
			if existing.MetadataProvider == "" || existing.MetadataProvider == providerAudiobookshelf {
				existing.MetadataProvider = "openlibrary"
			}
			if err := i.authors.Update(ctx, existing); err != nil {
				return nil, false, "", metadataMergeResult{}, err
			}
			matcher.addAuthor(existing)
		}
		if err := i.upsertProvenance(ctx, &models.ABSProvenance{
			SourceID:    cfg.SourceID,
			LibraryID:   item.LibraryID,
			EntityType:  entityTypeAuthor,
			ExternalID:  absExternalID,
			LocalID:     existing.ID,
			ItemID:      item.ItemID,
			ImportRunID: ptrInt64(runID),
		}); err != nil {
			return nil, false, "", metadataMergeResult{}, err
		}
		if normalizeAuthorName(absName) != normalizeAuthorName(existing.Name) {
			i.recordAuthorVariantAlias(ctx, existing.ID, absName, matcher)
		}
		if perr := i.recordAuthorAfterSnapshot(ctx, runID, cfg, item, absExternalID, existing.ID, itemOutcomeLinked, map[string]any{"matchedBy": "manual_author_name"}); perr != nil {
			slog.Warn("abs import: persist author rollback snapshot failed", "authorID", existing.ID, "runID", runID, "error", perr)
		}
		return existing, false, "manual_author", metadataMergeResult{}, nil
	}

	author := &models.Author{
		ForeignID:        foreignID,
		Name:             name,
		SortName:         sortNameFromFull(name),
		Monitored:        true,
		MetadataProvider: "openlibrary",
	}
	if i.meta != nil && !cfg.DryRun {
		if full, err := i.meta.GetAuthor(ctx, foreignID); err == nil && full != nil {
			author = full
			if author.Name == "" {
				author.Name = name
			}
			if author.SortName == "" {
				author.SortName = sortNameFromFull(author.Name)
			}
			author.ForeignID = foreignID
			author.Monitored = true
			if author.MetadataProvider == "" {
				author.MetadataProvider = "openlibrary"
			}
		} else if err != nil {
			slog.Warn("abs import: manual author metadata fetch failed", "foreignID", foreignID, "error", err)
		}
	}
	if cfg.DryRun {
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, absExternalID, 0, itemOutcomeCreated, map[string]string{"matchedBy": "manual_author"})
		return author, true, "manual_author", metadataMergeResult{}, nil
	}
	if err := i.authors.Create(ctx, author); err != nil {
		return nil, false, "", metadataMergeResult{}, err
	}
	matcher.addAuthor(author)
	if err := i.upsertProvenance(ctx, &models.ABSProvenance{
		SourceID:    cfg.SourceID,
		LibraryID:   item.LibraryID,
		EntityType:  entityTypeAuthor,
		ExternalID:  absExternalID,
		LocalID:     author.ID,
		ItemID:      item.ItemID,
		ImportRunID: ptrInt64(runID),
	}); err != nil {
		return nil, false, "", metadataMergeResult{}, err
	}
	if normalizeAuthorName(absName) != normalizeAuthorName(author.Name) {
		i.recordAuthorVariantAlias(ctx, author.ID, absName, matcher)
	}
	_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeAuthor, absExternalID, author.ID, itemOutcomeCreated, map[string]string{"matchedBy": "manual_author"})
	return author, true, "manual_author", metadataMergeResult{}, nil
}

func (i *Importer) enrichAuthor(ctx context.Context, cfg ImportConfig, item NormalizedLibraryItem, author *models.Author, matcher *authorMatcher) (metadataMergeResult, error) {
	if i.meta == nil || author == nil || len(item.Authors) == 0 {
		return metadataMergeResult{}, nil
	}

	full, ambiguous, err := i.lookupUpstreamAuthor(ctx, item.Authors[0].Name)
	if err != nil {
		slog.Warn("abs import: author metadata lookup failed", "author", item.Authors[0].Name, "error", err)
		return metadataMergeResult{}, nil
	}
	if ambiguous {
		return metadataMergeResult{Messages: []string{"author relink skipped: upstream author match was ambiguous"}}, nil
	}
	if full == nil {
		return metadataMergeResult{}, nil
	}
	matches, err := matcher.authorMatchesABSName(ctx, author, item.Authors[0].Name)
	if err != nil {
		return metadataMergeResult{}, err
	}
	if !matches {
		return metadataMergeResult{Messages: []string{"author relink skipped: ABS author no longer matches local author"}}, nil
	}

	result := metadataMergeResult{Matched: 1}
	changed := false
	if name := strings.TrimSpace(full.Name); name != "" && strings.TrimSpace(author.Name) != name {
		oldName := author.Name
		author.Name = name
		if full.SortName != "" {
			author.SortName = full.SortName
		}
		if !cfg.DryRun {
			i.recordAuthorVariantAlias(ctx, author.ID, oldName, matcher)
		}
		changed = true
	}
	if full.ForeignID != "" && author.ForeignID != full.ForeignID {
		existing, err := i.authors.GetByForeignID(ctx, full.ForeignID)
		if err != nil {
			return metadataMergeResult{}, err
		}
		if existing != nil && existing.ID != author.ID {
			result.Messages = append(result.Messages, "author relink skipped: upstream author already exists locally")
		} else {
			author.ForeignID = full.ForeignID
			if full.MetadataProvider != "" {
				author.MetadataProvider = full.MetadataProvider
			}
			result.Relinked++
			changed = true
		}
	}
	for _, field := range authorConflictFields {
		fieldResult, fieldChanged, err := i.applyConflictField(ctx, cfg, item, entityTypeAuthor, author.ID, field,
			SerializeAuthorConflictValue(author, field),
			SerializeAuthorConflictValue(full, field),
			func(value string) error { return ApplyAuthorConflictValue(author, field, value) },
			func() string { return SerializeAuthorConflictValue(author, field) },
		)
		if err != nil {
			return metadataMergeResult{}, err
		}
		result.Matched += fieldResult.Matched
		result.Relinked += fieldResult.Relinked
		result.Conflicts += fieldResult.Conflicts
		result.AutoResolved += fieldResult.AutoResolved
		result.Messages = append(result.Messages, fieldResult.Messages...)
		changed = changed || fieldChanged
	}
	if !changed && result.Conflicts == 0 && result.AutoResolved == 0 && result.Relinked == 0 {
		return result, nil
	}
	now := time.Now().UTC()
	author.LastMetadataRefreshAt = &now
	if err := i.authors.Update(ctx, author); err != nil {
		return metadataMergeResult{}, err
	}
	matcher.addAuthor(author)
	return result, nil
}

func (i *Importer) enrichBook(ctx context.Context, cfg ImportConfig, item NormalizedLibraryItem, author *models.Author, book *models.Book) (metadataMergeResult, error) {
	if i.meta == nil || book == nil {
		return metadataMergeResult{}, nil
	}

	full, matchedBy, ambiguous, err := i.lookupUpstreamBook(ctx, author, item)
	if err != nil {
		slog.Warn("abs import: book metadata lookup failed", "title", item.Title, "error", err)
		return metadataMergeResult{}, nil
	}
	if ambiguous {
		return metadataMergeResult{Messages: []string{"book relink skipped: upstream book match was ambiguous"}}, nil
	}
	if full == nil {
		return metadataMergeResult{}, nil
	}

	return i.mergeUpstreamBook(ctx, cfg, item, book, full, matchedBy)
}

func (i *Importer) mergeUpstreamBook(ctx context.Context, cfg ImportConfig, item NormalizedLibraryItem, book *models.Book, full *models.Book, matchedBy string) (metadataMergeResult, error) {
	if book == nil || full == nil {
		return metadataMergeResult{}, nil
	}
	result := metadataMergeResult{Matched: 1}
	changed := false
	if full.ForeignID != "" && book.ForeignID != full.ForeignID {
		existing, err := i.books.GetByForeignID(ctx, full.ForeignID)
		if err != nil {
			return metadataMergeResult{}, err
		}
		if existing != nil && existing.ID != book.ID {
			result.Messages = append(result.Messages, "book relink skipped: upstream book already exists locally")
			full = nil
		} else {
			book.ForeignID = full.ForeignID
			if full.MetadataProvider != "" {
				book.MetadataProvider = full.MetadataProvider
			}
			result.Relinked++
			changed = true
		}
	}
	if full == nil {
		return result, nil
	}
	for _, field := range bookConflictFields {
		fieldResult, fieldChanged, err := i.applyConflictField(ctx, cfg, item, entityTypeBook, book.ID, field,
			bookABSCandidateValue(book, item, field),
			SerializeBookConflictValue(full, field),
			func(value string) error { return ApplyBookConflictValue(book, field, value) },
			func() string { return SerializeBookConflictValue(book, field) },
		)
		if err != nil {
			return metadataMergeResult{}, err
		}
		result.Matched += fieldResult.Matched
		result.Relinked += fieldResult.Relinked
		result.Conflicts += fieldResult.Conflicts
		result.AutoResolved += fieldResult.AutoResolved
		result.Messages = append(result.Messages, fieldResult.Messages...)
		changed = changed || fieldChanged
	}
	if matchedBy != "" && result.Relinked > 0 {
		result.Messages = append(result.Messages, fmt.Sprintf("book relinked by %s metadata match", matchedBy))
	}
	if !changed && result.Conflicts == 0 && result.AutoResolved == 0 && result.Relinked == 0 {
		return result, nil
	}
	now := time.Now().UTC()
	book.LastMetadataRefreshAt = &now
	if err := i.books.Update(ctx, book); err != nil {
		return metadataMergeResult{}, err
	}
	return result, nil
}

func (i *Importer) lookupUpstreamBook(ctx context.Context, author *models.Author, item NormalizedLibraryItem) (*models.Book, string, bool, error) {
	if isbn := isbnDigits(item.ISBN); isbn != "" {
		match, err := i.meta.GetBookByISBN(ctx, isbn)
		if err != nil {
			return nil, "", false, err
		}
		if match != nil {
			return match, "isbn", false, nil
		}
	}
	if asin := strings.TrimSpace(item.ASIN); asin != "" {
		match, err := i.meta.GetCanonicalBookByASIN(ctx, asin)
		if err != nil {
			return nil, "", false, err
		}
		if match != nil {
			return match, "asin", false, nil
		}
	}
	if author == nil || author.ForeignID == "" || author.MetadataProvider == providerAudiobookshelf {
		return nil, "", false, nil
	}
	works, err := i.meta.GetAuthorWorks(ctx, author.ForeignID)
	if err != nil {
		return nil, "", false, err
	}
	var match *models.Book
	key := normalizeTitle(item.Title)
	for idx := range works {
		if normalizeTitle(works[idx].Title) != key {
			continue
		}
		if match != nil {
			return nil, "", true, nil
		}
		copy := works[idx]
		match = &copy
	}
	if match == nil {
		return nil, "", false, nil
	}
	full, err := i.meta.GetBook(ctx, match.ForeignID)
	if err != nil {
		return nil, "", false, err
	}
	if full != nil {
		return full, "title", false, nil
	}
	return match, "title", false, nil
}

func (i *Importer) upsertBook(ctx context.Context, cfg ImportConfig, runID int64, author *models.Author, item NormalizedLibraryItem, allowCreate bool) (*bookUpsertResult, bool, bool, metadataMergeResult, error) {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return nil, false, false, metadataMergeResult{}, errors.New("item title is empty")
	}
	externalID := item.ItemID
	if strings.TrimSpace(item.ResolvedBookForeignID) != "" || strings.TrimSpace(item.ResolvedBookTitle) != "" {
		return i.upsertManualBook(ctx, cfg, runID, author, item)
	}
	if i.provenance != nil {
		if link, err := i.provenance.GetByExternal(ctx, cfg.SourceID, item.LibraryID, entityTypeBook, externalID); err != nil {
			return nil, false, false, metadataMergeResult{}, err
		} else if link != nil {
			existing, err := i.books.GetByID(ctx, link.LocalID)
			if err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
			if existing != nil {
				if !cfg.DryRun {
					if err := i.recordBookBeforeSnapshot(ctx, runID, cfg, item, externalID, existing, itemOutcomeUpdated, nil); err != nil {
						return nil, false, false, metadataMergeResult{}, err
					}
					if err := i.applyBookFields(ctx, existing, author.ID, item); err != nil {
						return nil, false, false, metadataMergeResult{}, err
					}
					if err := i.upsertBookProvenance(ctx, cfg, runID, existing.ID, item); err != nil {
						return nil, false, false, metadataMergeResult{}, err
					}
				}
				_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, externalID, existing.ID, itemOutcomeUpdated, nil)
				if cfg.DryRun {
					return &bookUpsertResult{row: existing, matchedBy: "provenance"}, false, false, metadataMergeResult{}, nil
				}
				metaResult, err := i.enrichBook(ctx, cfg, item, author, existing)
				return &bookUpsertResult{row: existing, matchedBy: "provenance"}, false, false, metaResult, err
			}
		}
	}

	fid := absForeignID("book", item.LibraryID, externalID)
	if existing, err := i.books.GetByForeignID(ctx, fid); err != nil {
		return nil, false, false, metadataMergeResult{}, err
	} else if existing != nil {
		if !cfg.DryRun {
			if err := i.recordBookBeforeSnapshot(ctx, runID, cfg, item, externalID, existing, itemOutcomeUpdated, nil); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
			if err := i.applyBookFields(ctx, existing, author.ID, item); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
			if err := i.upsertBookProvenance(ctx, cfg, runID, existing.ID, item); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
		}
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, externalID, existing.ID, itemOutcomeUpdated, nil)
		if cfg.DryRun {
			return &bookUpsertResult{row: existing, matchedBy: "foreign_id"}, false, false, metadataMergeResult{}, nil
		}
		metaResult, err := i.enrichBook(ctx, cfg, item, author, existing)
		return &bookUpsertResult{row: existing, matchedBy: "foreign_id"}, false, false, metaResult, err
	}

	match, ambiguous, err := i.findBookByNormalizedTitle(ctx, author.ID, item.Title)
	if err != nil {
		return nil, false, false, metadataMergeResult{}, err
	}
	if ambiguous {
		if !allowCreate {
			return nil, false, false, metadataMergeResult{}, reviewRequiredError{Reason: reviewReasonAmbiguousBook}
		}
	} else if match != nil {
		if !cfg.DryRun {
			if err := i.recordBookBeforeSnapshot(ctx, runID, cfg, item, externalID, match, itemOutcomeLinked, nil); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
			if err := i.applyBookFields(ctx, match, author.ID, item); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
			if err := i.upsertBookProvenance(ctx, cfg, runID, match.ID, item); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
		}
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, externalID, match.ID, itemOutcomeLinked, nil)
		if cfg.DryRun {
			return &bookUpsertResult{row: match, matchedBy: "author+normalized_title"}, false, true, metadataMergeResult{}, nil
		}
		metaResult, err := i.enrichBook(ctx, cfg, item, author, match)
		return &bookUpsertResult{row: match, matchedBy: "author+normalized_title"}, false, true, metaResult, err
	}
	// No local book matched for this author. As with the author path above,
	// an unmatched book is not an uncertain match and no longer parks the item
	// in the review queue for non-ASIN items (#762): the book is created and
	// enrichBook performs a confidence-gated upstream lookup. Only an
	// *ambiguous* book (handled above) still requires human review.

	if cfg.DryRun {
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, externalID, 0, itemOutcomeCreated, nil)
		return &bookUpsertResult{row: &models.Book{
			ForeignID:        fid,
			AuthorID:         author.ID,
			Title:            title,
			SortTitle:        title,
			Description:      textutil.CleanDescription(item.Description),
			ReleaseDate:      parseABSDate(item.PublishedDate, item.PublishedYear),
			Genres:           cleanStrings(item.Genres),
			Monitored:        true,
			Status:           models.BookStatusWanted,
			AnyEditionOK:     true,
			Language:         normalizeLanguage(item.Language),
			MediaType:        deriveMediaType(item),
			Narrator:         joinNarrators(item.Narrators),
			DurationSeconds:  int(math.Round(item.DurationSeconds)),
			ASIN:             strings.TrimSpace(item.ASIN),
			MetadataProvider: providerAudiobookshelf,
		}, matchedBy: "created"}, true, false, metadataMergeResult{}, nil
	}

	book := &models.Book{
		ForeignID:        fid,
		AuthorID:         author.ID,
		Title:            title,
		SortTitle:        title,
		Description:      textutil.CleanDescription(item.Description),
		ReleaseDate:      parseABSDate(item.PublishedDate, item.PublishedYear),
		Genres:           cleanStrings(item.Genres),
		Monitored:        true,
		Status:           models.BookStatusWanted,
		AnyEditionOK:     true,
		Language:         normalizeLanguage(item.Language),
		MediaType:        deriveMediaType(item),
		Narrator:         joinNarrators(item.Narrators),
		DurationSeconds:  int(math.Round(item.DurationSeconds)),
		ASIN:             strings.TrimSpace(item.ASIN),
		MetadataProvider: providerAudiobookshelf,
	}
	if err := i.books.Create(ctx, book); err != nil {
		return nil, false, false, metadataMergeResult{}, err
	}
	if err := i.upsertBookProvenance(ctx, cfg, runID, book.ID, item); err != nil {
		return nil, false, false, metadataMergeResult{}, err
	}
	_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, externalID, book.ID, itemOutcomeCreated, nil)
	metaResult, err := i.enrichBook(ctx, cfg, item, author, book)
	return &bookUpsertResult{row: book, matchedBy: "created"}, true, false, metaResult, err
}

func (i *Importer) upsertManualBook(ctx context.Context, cfg ImportConfig, runID int64, author *models.Author, item NormalizedLibraryItem) (*bookUpsertResult, bool, bool, metadataMergeResult, error) {
	foreignID := strings.TrimSpace(item.ResolvedBookForeignID)
	title := strings.TrimSpace(firstNonEmpty(item.ResolvedBookTitle, item.EditedTitle, item.Title))
	if foreignID == "" || title == "" {
		return nil, false, false, metadataMergeResult{}, errors.New("resolved book requires foreignBookId and title")
	}
	if existing, err := i.books.GetByForeignID(ctx, foreignID); err != nil {
		return nil, false, false, metadataMergeResult{}, err
	} else if existing != nil {
		if !cfg.DryRun {
			if err := i.recordBookBeforeSnapshot(ctx, runID, cfg, item, item.ItemID, existing, itemOutcomeLinked, map[string]any{"matchedBy": "manual_book"}); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
			existing.AuthorID = author.ID
			i.applyABSFormatFields(existing, item)
			if err := i.books.Update(ctx, existing); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
			if err := i.upsertBookProvenance(ctx, cfg, runID, existing.ID, item); err != nil {
				return nil, false, false, metadataMergeResult{}, err
			}
		}
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, item.ItemID, existing.ID, itemOutcomeLinked, map[string]string{"matchedBy": "manual_book"})
		return &bookUpsertResult{row: existing, matchedBy: "manual_book"}, false, true, metadataMergeResult{}, nil
	}

	book := &models.Book{
		ForeignID:        foreignID,
		AuthorID:         author.ID,
		Title:            title,
		SortTitle:        title,
		Description:      textutil.CleanDescription(item.Description),
		ReleaseDate:      parseABSDate(item.PublishedDate, item.PublishedYear),
		Genres:           cleanStrings(item.Genres),
		Monitored:        true,
		Status:           models.BookStatusWanted,
		AnyEditionOK:     true,
		Language:         normalizeLanguage(item.Language),
		MediaType:        deriveMediaType(item),
		Narrator:         joinNarrators(item.Narrators),
		DurationSeconds:  int(math.Round(item.DurationSeconds)),
		ASIN:             strings.TrimSpace(item.ASIN),
		MetadataProvider: "openlibrary",
	}
	if i.meta != nil && !cfg.DryRun {
		if full, err := i.meta.GetBook(ctx, foreignID); err == nil && full != nil {
			book = full
			book.ForeignID = foreignID
			book.AuthorID = author.ID
			if book.Title == "" {
				book.Title = title
			}
			if book.SortTitle == "" {
				book.SortTitle = book.Title
			}
			book.Monitored = true
			book.Status = models.BookStatusWanted
			book.AnyEditionOK = true
			if book.MetadataProvider == "" {
				book.MetadataProvider = "openlibrary"
			}
			i.applyABSFormatFields(book, item)
		} else if err != nil {
			slog.Warn("abs import: manual book metadata fetch failed", "foreignID", foreignID, "error", err)
		}
	}
	if cfg.DryRun {
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, item.ItemID, 0, itemOutcomeCreated, map[string]string{"matchedBy": "manual_book"})
		return &bookUpsertResult{row: book, matchedBy: "manual_book"}, true, false, metadataMergeResult{}, nil
	}
	if err := i.books.Create(ctx, book); err != nil {
		return nil, false, false, metadataMergeResult{}, err
	}
	if err := i.upsertBookProvenance(ctx, cfg, runID, book.ID, item); err != nil {
		return nil, false, false, metadataMergeResult{}, err
	}
	_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeBook, item.ItemID, book.ID, itemOutcomeCreated, map[string]string{"matchedBy": "manual_book"})
	return &bookUpsertResult{row: book, matchedBy: "manual_book"}, true, false, metadataMergeResult{Matched: 1}, nil
}

func (i *Importer) applyABSFormatFields(book *models.Book, item NormalizedLibraryItem) {
	if mediaType := deriveMediaType(item); mediaType != "" {
		book.MediaType = mergeMediaType(book.MediaType, mediaType)
	}
	if narrator := joinNarrators(item.Narrators); narrator != "" {
		book.Narrator = narrator
	}
	if item.DurationSeconds > 0 {
		book.DurationSeconds = int(math.Round(item.DurationSeconds))
	}
	if asin := strings.TrimSpace(item.ASIN); asin != "" {
		book.ASIN = asin
	}
	if lang := normalizeLanguage(item.Language); lang != "" && book.Language == "" {
		book.Language = lang
	}
}

func (i *Importer) upsertBookProvenance(ctx context.Context, cfg ImportConfig, runID, bookID int64, item NormalizedLibraryItem) error {
	return i.upsertProvenance(ctx, &models.ABSProvenance{
		SourceID:    cfg.SourceID,
		LibraryID:   item.LibraryID,
		EntityType:  entityTypeBook,
		ExternalID:  item.ItemID,
		LocalID:     bookID,
		ItemID:      item.ItemID,
		FileIDs:     itemFileIDs(item),
		ImportRunID: ptrInt64(runID),
	})
}

func (i *Importer) findBookByNormalizedTitle(ctx context.Context, authorID int64, title string) (*models.Book, bool, error) {
	books, err := i.books.ListByAuthorIncludingExcluded(ctx, authorID)
	if err != nil {
		return nil, false, err
	}
	key := normalizeTitle(title)
	var match *models.Book
	for idx := range books {
		if normalizeTitle(books[idx].Title) != key {
			continue
		}
		if match != nil {
			return nil, true, nil
		}
		copy := books[idx]
		match = &copy
	}
	return match, false, nil
}

func (i *Importer) applyBookFields(ctx context.Context, book *models.Book, authorID int64, item NormalizedLibraryItem) error {
	book.AuthorID = authorID
	book.Title = strings.TrimSpace(item.Title)
	book.SortTitle = book.Title
	if desc := textutil.CleanDescription(item.Description); desc != "" {
		book.Description = desc
	}
	if rd := parseABSDate(item.PublishedDate, item.PublishedYear); rd != nil {
		book.ReleaseDate = rd
	}
	if genres := cleanStrings(item.Genres); len(genres) > 0 {
		book.Genres = genres
	}
	if lang := normalizeLanguage(item.Language); lang != "" {
		book.Language = lang
	}
	if narrator := joinNarrators(item.Narrators); narrator != "" {
		book.Narrator = narrator
	}
	if item.DurationSeconds > 0 {
		book.DurationSeconds = int(math.Round(item.DurationSeconds))
	}
	if asin := strings.TrimSpace(item.ASIN); asin != "" {
		book.ASIN = asin
	}
	book.MediaType = mergeMediaType(book.MediaType, deriveMediaType(item))
	book.Monitored = true
	if book.Status == "" {
		book.Status = models.BookStatusWanted
	}
	if book.MetadataProvider == "" {
		book.MetadataProvider = providerAudiobookshelf
	}
	return i.books.Update(ctx, book)
}

func (i *Importer) upsertSeries(ctx context.Context, cfg ImportConfig, runID, bookID int64, itemID string, ref NormalizedSeries, stats *ImportStats) (seriesUpsertResult, error) {
	title := strings.TrimSpace(ref.Name)
	if title == "" {
		return seriesUpsertResult{}, nil
	}
	externalID := seriesExternalID(ref)
	var existing *models.Series
	var identityLink *models.ABSProvenance
	if i.provenance != nil {
		if link, err := i.provenance.GetByExternal(ctx, cfg.SourceID, cfg.LibraryID, entityTypeSeries, externalID); err != nil {
			return seriesUpsertResult{}, err
		} else if link != nil {
			identityLink = link
			existing, err = i.series.GetByID(ctx, link.LocalID)
			if err != nil {
				return seriesUpsertResult{}, err
			}
		}
	}
	matchedBy := ""
	if existing == nil {
		match, ambiguous, err := i.findSeriesByTitle(ctx, title)
		if err != nil {
			return seriesUpsertResult{}, err
		}
		if ambiguous {
			return seriesUpsertResult{}, fmt.Errorf("ambiguous existing series match for %q", title)
		}
		if match != nil {
			existing = match
			matchedBy = "normalized_title"
		}
	}
	created := false
	if existing == nil {
		if cfg.DryRun && stats != nil && stats.dryRunSeriesAlreadyPlanned(externalID, title) {
			countKey := seriesMembershipCountKey(0, title, externalID, bookID, itemID)
			membershipCreated := !stats.dryRunSeriesMembershipAlreadyPlanned(countKey)
			membershipExternalID := seriesMembershipExternalID(externalID, bookID, itemID)
			if membershipCreated {
				metadata := map[string]any{
					"bookId":   bookID,
					"sequence": strings.TrimSpace(ref.Sequence),
				}
				_ = i.recordRunEntity(ctx, runID, cfg, cfg.LibraryID, itemID, entityTypeSeries, membershipExternalID, 0, itemOutcomeLinked, metadata)
				stats.rememberDryRunSeriesMembership(countKey)
			}
			return seriesUpsertResult{
				IdentityExternalID:   externalID,
				MembershipExternalID: membershipExternalID,
				CountKey:             countKey,
				MembershipCreated:    membershipCreated,
				Linked:               true,
				MatchedBy:            "planned",
			}, nil
		}
		existing = &models.Series{
			ForeignID:   absForeignID("series", cfg.LibraryID, externalID),
			Title:       title,
			Description: "",
		}
		if !cfg.DryRun {
			if err := i.series.CreateOrGet(ctx, existing); err != nil {
				return seriesUpsertResult{}, err
			}
		}
		created = true
		matchedBy = "created"
	}
	localID := existing.ID
	if cfg.DryRun && created {
		localID = 0
	}
	countKey := seriesMembershipCountKey(localID, title, externalID, bookID, itemID)
	membershipExternalID := seriesMembershipExternalID(externalID, bookID, itemID)
	metadata := map[string]any{
		"bookId":   bookID,
		"sequence": strings.TrimSpace(ref.Sequence),
	}
	membershipCreated := false
	if cfg.DryRun {
		if stats == nil || !stats.dryRunSeriesMembershipAlreadyPlanned(countKey) {
			membershipCreated = true
			outcome := itemOutcomeLinked
			if created {
				outcome = itemOutcomeCreated
			}
			_ = i.recordRunEntity(ctx, runID, cfg, cfg.LibraryID, itemID, entityTypeSeries, membershipExternalID, localID, outcome, metadata)
			if stats != nil {
				stats.rememberDryRunSeriesMembership(countKey)
			}
		}
		if created && stats != nil {
			stats.rememberDryRunSeries(externalID, title)
		}
		return seriesUpsertResult{
			SeriesID:             localID,
			IdentityExternalID:   externalID,
			MembershipExternalID: membershipExternalID,
			CountKey:             countKey,
			CreatedSeries:        created,
			MembershipCreated:    membershipCreated,
			Linked:               true,
			MatchedBy:            matchedBy,
		}, nil
	}
	identityChanged := identityLink == nil || identityLink.LocalID != existing.ID
	if identityChanged {
		if err := i.upsertProvenance(ctx, &models.ABSProvenance{
			SourceID:    cfg.SourceID,
			LibraryID:   cfg.LibraryID,
			EntityType:  entityTypeSeries,
			ExternalID:  externalID,
			LocalID:     existing.ID,
			ItemID:      "",
			ImportRunID: ptrInt64(runID),
		}); err != nil {
			return seriesUpsertResult{}, err
		}
		outcome := itemOutcomeLinked
		if created {
			outcome = itemOutcomeCreated
		}
		_ = i.recordRunEntity(ctx, runID, cfg, cfg.LibraryID, "", entityTypeSeries, externalID, existing.ID, outcome, map[string]any{
			"matchedBy": matchedBy,
			"identity":  true,
		})
	}
	membershipCreated, err := i.series.LinkBookIfMissing(ctx, existing.ID, bookID, strings.TrimSpace(ref.Sequence), true)
	if err != nil {
		return seriesUpsertResult{}, err
	}
	if membershipCreated {
		if err := i.upsertProvenance(ctx, &models.ABSProvenance{
			SourceID:    cfg.SourceID,
			LibraryID:   cfg.LibraryID,
			EntityType:  entityTypeSeries,
			ExternalID:  membershipExternalID,
			LocalID:     existing.ID,
			ItemID:      itemID,
			ImportRunID: ptrInt64(runID),
		}); err != nil {
			return seriesUpsertResult{}, err
		}
		outcome := itemOutcomeLinked
		if created {
			outcome = itemOutcomeCreated
		}
		_ = i.recordRunEntity(ctx, runID, cfg, cfg.LibraryID, itemID, entityTypeSeries, membershipExternalID, existing.ID, outcome, metadata)
	}
	return seriesUpsertResult{
		SeriesID:             existing.ID,
		IdentityExternalID:   externalID,
		MembershipExternalID: membershipExternalID,
		CountKey:             countKey,
		CreatedSeries:        created,
		MembershipCreated:    membershipCreated,
		Linked:               true,
		MatchedBy:            matchedBy,
	}, nil
}

func seriesMembershipExternalID(seriesExternalID string, bookID int64, itemID string) string {
	seriesExternalID = strings.TrimSpace(seriesExternalID)
	if bookID <= 0 {
		itemID = strings.TrimSpace(itemID)
		if itemID == "" {
			return seriesExternalID + ":book:planned"
		}
		return seriesExternalID + ":item:" + itemID
	}
	return fmt.Sprintf("%s:book:%d", seriesExternalID, bookID)
}

func seriesMembershipCountKey(seriesID int64, title, externalID string, bookID int64, itemID string) string {
	bookKey := strings.TrimSpace(itemID)
	if bookID > 0 {
		bookKey = fmt.Sprintf("book:%d", bookID)
	} else if bookKey != "" {
		bookKey = "item:" + bookKey
	} else {
		bookKey = "book:planned"
	}
	if seriesID > 0 {
		return fmt.Sprintf("series:%d:%s", seriesID, bookKey)
	}
	if titleKey := normalizeSeriesName(title); titleKey != "" {
		return "title:" + titleKey + ":" + bookKey
	}
	return "external:" + strings.TrimSpace(externalID) + ":" + bookKey
}

func (s *ImportStats) dryRunSeriesAlreadyPlanned(externalID, title string) bool {
	if s == nil {
		return false
	}
	if _, ok := s.dryRunSeriesExternalIDs[strings.TrimSpace(externalID)]; ok {
		return true
	}
	if _, ok := s.dryRunSeriesTitles[normalizeTitle(title)]; ok {
		return true
	}
	return false
}

func (s *ImportStats) rememberDryRunSeries(externalID, title string) {
	if s.dryRunSeriesExternalIDs == nil {
		s.dryRunSeriesExternalIDs = make(map[string]struct{})
	}
	if s.dryRunSeriesTitles == nil {
		s.dryRunSeriesTitles = make(map[string]struct{})
	}
	s.dryRunSeriesExternalIDs[strings.TrimSpace(externalID)] = struct{}{}
	s.dryRunSeriesTitles[normalizeTitle(title)] = struct{}{}
}

func (s *ImportStats) dryRunSeriesMembershipAlreadyPlanned(key string) bool {
	if s == nil {
		return false
	}
	_, ok := s.dryRunSeriesMemberships[strings.TrimSpace(key)]
	return ok
}

func (s *ImportStats) rememberDryRunSeriesMembership(key string) {
	if s.dryRunSeriesMemberships == nil {
		s.dryRunSeriesMemberships = make(map[string]struct{})
	}
	s.dryRunSeriesMemberships[strings.TrimSpace(key)] = struct{}{}
}

func (i *Importer) findSeriesByTitle(ctx context.Context, title string) (*models.Series, bool, error) {
	all, err := i.series.List(ctx)
	if err != nil {
		return nil, false, err
	}
	key := normalizeTitle(title)
	var match *models.Series
	for idx := range all {
		if normalizeTitle(all[idx].Title) != key {
			continue
		}
		if match != nil {
			return nil, true, nil
		}
		copy := all[idx]
		match = &copy
	}
	return match, false, nil
}

func (i *Importer) upsertEditions(ctx context.Context, cfg ImportConfig, runID, bookID int64, item NormalizedLibraryItem) (int, error) {
	added := 0
	for _, format := range deriveEditionFormats(item) {
		externalID := fmt.Sprintf("%s:%s", item.ItemID, format)
		prior, err := i.editions.GetByForeignID(ctx, absForeignID("edition", item.LibraryID, externalID))
		if err != nil {
			return added, err
		}
		edition := &models.Edition{
			ForeignID:   absForeignID("edition", item.LibraryID, externalID),
			BookID:      bookID,
			Title:       item.Title,
			ISBN13:      isbn13Ptr(item.ISBN),
			ISBN10:      isbn10Ptr(item.ISBN),
			ASIN:        ptrString(strings.TrimSpace(item.ASIN)),
			Publisher:   strings.TrimSpace(item.Publisher),
			PublishDate: parseABSDate(item.PublishedDate, item.PublishedYear),
			Format:      strings.ToUpper(format),
			Language:    normalizeLanguage(item.Language),
			IsEbook:     format == models.MediaTypeEbook,
			EditionInfo: "Imported from Audiobookshelf",
			Monitored:   true,
		}
		if !cfg.DryRun {
			if err := i.editions.Upsert(ctx, edition); err != nil {
				return added, err
			}
		}
		if prior == nil {
			added++
		}
		if !cfg.DryRun {
			if err := i.upsertProvenance(ctx, &models.ABSProvenance{
				SourceID:    cfg.SourceID,
				LibraryID:   item.LibraryID,
				EntityType:  entityTypeEdition,
				ExternalID:  externalID,
				LocalID:     edition.ID,
				ItemID:      item.ItemID,
				Format:      format,
				FileIDs:     itemFileIDs(item),
				ImportRunID: ptrInt64(runID),
			}); err != nil {
				return added, err
			}
		}
		outcome := itemOutcomeUpdated
		if prior == nil {
			outcome = itemOutcomeCreated
		}
		localID := edition.ID
		if cfg.DryRun && prior == nil {
			localID = 0
		}
		_ = i.recordRunEntity(ctx, runID, cfg, item.LibraryID, item.ItemID, entityTypeEdition, externalID, localID, outcome, map[string]any{"format": format, "bookId": bookID})
	}
	return added, nil
}

func (i *Importer) upsertProvenance(ctx context.Context, p *models.ABSProvenance) error {
	if i.provenance == nil {
		return nil
	}
	return i.provenance.Upsert(ctx, p)
}

func (i *Importer) deleteProvenanceByLocal(ctx context.Context, entityType string, localID int64) (int, error) {
	if i.provenance == nil || localID == 0 {
		return 0, nil
	}
	count, err := i.provenance.DeleteByLocal(ctx, entityType, localID)
	return int(count), err
}
