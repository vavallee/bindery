// Package abs provides Audiobookshelf client, normalization, and import logic.
package abs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

var ErrAlreadyRunning = errors.New("abs import already running")

func NewImporter(
	authors *db.AuthorRepo,
	aliases *db.AuthorAliasRepo,
	books *db.BookRepo,
	editions *db.EditionRepo,
	series *db.SeriesRepo,
	settings *db.SettingsRepo,
	runs *db.ABSImportRunRepo,
	runEntities *db.ABSImportRunEntityRepo,
	provenance *db.ABSProvenanceRepo,
	reviews *db.ABSReviewItemRepo,
	conflicts *db.ABSMetadataConflictRepo,
) *Importer {
	importer := &Importer{
		authors:     authors,
		aliases:     aliases,
		books:       books,
		editions:    editions,
		series:      series,
		settings:    settings,
		runs:        runs,
		runEntities: runEntities,
		provenance:  provenance,
		reviews:     reviews,
		conflicts:   conflicts,
		userAgent:   UserAgent(""),
	}
	importer.newClient = importer.defaultClient
	return importer
}

func (i *Importer) defaultClient(baseURL, apiKey string) (enumerationClient, error) {
	client, err := NewClient(baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	return client.WithUserAgent(i.userAgent), nil
}

func (i *Importer) WithMetadata(meta *metadata.Aggregator) *Importer {
	i.meta = meta
	return i
}

func (i *Importer) WithEnhancedHardcoverSeriesEnabled(enabled func(context.Context) bool) *Importer {
	i.hardcoverSeriesEnabled = enabled
	return i
}

func (i *Importer) WithVersion(version string) *Importer {
	i.userAgent = UserAgent(version)
	return i
}

func (i *Importer) WithUserAgent(userAgent string) *Importer {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		userAgent = UserAgent("")
	}
	i.userAgent = userAgent
	return i
}

func (i *Importer) WithStoragePaths(libraryDir, audiobookDir string, rootFolders *db.RootFolderRepo) *Importer {
	i.libraryDir = filepath.Clean(strings.TrimSpace(libraryDir))
	i.audiobookDir = filepath.Clean(strings.TrimSpace(audiobookDir))
	if i.audiobookDir == "." || i.audiobookDir == "" {
		i.audiobookDir = i.libraryDir
	}
	i.rootFolders = rootFolders
	return i
}

func (i *Importer) enhancedHardcoverSeriesEnabled(ctx context.Context) bool {
	return i.hardcoverSeriesEnabled != nil && i.hardcoverSeriesEnabled(ctx)
}

func (i *Importer) Progress() ImportProgress {
	i.mu.Lock()
	defer i.mu.Unlock()
	progress := i.progress
	if len(progress.Results) > 0 {
		progress.Results = append([]ImportItemResult(nil), progress.Results...)
	}
	return progress
}

func (i *Importer) Running() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.running
}

func (i *Importer) ResumeInterrupted(ctx context.Context, fallback ImportConfig) (bool, error) {
	if i.runs == nil {
		return false, nil
	}
	run, err := i.runs.LatestRunningWithCheckpoint(ctx)
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, nil
	}
	checkpoint, err := decodeImportCheckpoint(run.CheckpointJSON)
	if err != nil {
		return true, fmt.Errorf("decode interrupted abs import checkpoint: %w", err)
	}
	if checkpoint == nil {
		return false, nil
	}
	if i.settings != nil {
		if err := i.settings.Set(ctx, SettingABSImportCheckpoint, strings.TrimSpace(run.CheckpointJSON)); err != nil {
			return true, fmt.Errorf("restore abs import checkpoint: %w", err)
		}
	}

	cfg := resumeConfigFromRun(*run, fallback)
	if err := i.runs.Finish(ctx, run.ID, runStatusFailed, ImportSummary{
		DryRun:                run.DryRun,
		ResumedFromCheckpoint: true,
		Checkpoint:            checkpoint,
		Error:                 "interrupted by process restart; resumed from checkpoint",
	}); err != nil {
		return true, fmt.Errorf("mark interrupted abs import run %d failed: %w", run.ID, err)
	}
	if err := cfg.Validate(); err != nil {
		return true, fmt.Errorf("resume abs import run %d: %w", run.ID, err)
	}
	if err := i.Start(context.WithoutCancel(ctx), cfg); err != nil {
		return true, err
	}
	return true, nil
}

type reviewRequiredError struct {
	Reason string
}

func (e reviewRequiredError) Error() string {
	switch e.Reason {
	case reviewReasonAmbiguousAuthor:
		return "author match is ambiguous"
	case reviewReasonUnmatchedAuthor:
		return "author match not found"
	case reviewReasonAmbiguousBook:
		return "book match is ambiguous"
	case reviewReasonUnmatchedBook:
		return "book match not found"
	default:
		return "review required"
	}
}

func (i *Importer) ImportReview(ctx context.Context, cfg ImportConfig, item NormalizedLibraryItem) (ImportItemResult, error) {
	stats := &ImportStats{}
	matcher, err := i.newAuthorMatcher(ctx)
	if err != nil {
		return ImportItemResult{
			ItemID:  item.ItemID,
			Title:   item.Title,
			Outcome: itemOutcomeFailed,
			Message: err.Error(),
		}, err
	}
	result := i.importOne(ctx, cfg, 0, item, stats, true, matcher)
	if result.Outcome == itemOutcomeFailed {
		if result.Message == "" {
			return result, errors.New("review import failed")
		}
		return result, errors.New(result.Message)
	}
	return result, nil
}

func (i *Importer) ReviewFileMapping(ctx context.Context, cfg ImportConfig, item NormalizedLibraryItem) ReviewFileMapping {
	var messages []string
	if path := strings.TrimSpace(item.EbookPath); path != "" {
		ok, message := i.inspectFormatPath(ctx, cfg, models.MediaTypeEbook, path)
		if ok {
			return ReviewFileMapping{Found: true, Message: "ebook file is visible to Bindery"}
		}
		if message != "" {
			messages = append(messages, message)
		}
	}
	if path := strings.TrimSpace(item.Path); path != "" && len(item.AudioFiles) > 0 {
		ok, message := i.inspectFormatPath(ctx, cfg, models.MediaTypeAudiobook, path)
		if ok {
			return ReviewFileMapping{Found: true, Message: "audiobook path is visible to Bindery"}
		}
		if message != "" {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		return ReviewFileMapping{Message: "no ABS file paths available"}
	}
	return ReviewFileMapping{Message: strings.Join(messages, "; ")}
}

func (i *Importer) Start(ctx context.Context, cfg ImportConfig) error {
	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return ErrAlreadyRunning
	}
	i.running = true
	i.progress = ImportProgress{
		Running:   true,
		DryRun:    cfg.DryRun,
		StartedAt: time.Now().UTC(),
		Message:   importStartMessage(cfg.DryRun),
	}
	i.mu.Unlock()

	go i.run(ctx, cfg)
	return nil
}

func (i *Importer) Run(ctx context.Context, cfg ImportConfig) (*ImportStats, error) {
	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return nil, ErrAlreadyRunning
	}
	i.running = true
	i.progress = ImportProgress{
		Running:   true,
		DryRun:    cfg.DryRun,
		StartedAt: time.Now().UTC(),
		Message:   importStartMessage(cfg.DryRun),
	}
	i.mu.Unlock()

	stats := i.run(ctx, cfg)
	progress := i.Progress()
	if progress.Error != "" {
		return stats, errors.New(progress.Error)
	}
	return stats, nil
}

func (i *Importer) run(ctx context.Context, cfg ImportConfig) *ImportStats {
	stats := &ImportStats{}
	cfg = cfg.normalized()
	summary := ImportSummary{DryRun: cfg.DryRun, Stats: *stats}
	defer func() {
		now := time.Now().UTC()
		i.mu.Lock()
		i.running = false
		i.progress.Running = false
		i.progress.FinishedAt = &now
		i.progress.Stats = stats
		i.mu.Unlock()
	}()

	if err := cfg.Validate(); err != nil {
		i.fail(err)
		return stats
	}

	if !cfg.DryRun {
		removedAliases, err := i.cleanupABSSourcedAliases(ctx)
		if err != nil {
			i.fail(fmt.Errorf("cleanup abs-sourced author aliases: %w", err))
			return stats
		}
		if removedAliases > 0 {
			slog.Info("abs import: cleaned stale abs-sourced author aliases", "removed", removedAliases)
		}
	}

	authorMatcher, err := i.newAuthorMatcher(ctx)
	if err != nil {
		i.fail(err)
		return stats
	}

	if checkpoint, err := loadImportCheckpoint(ctx, i.settings); err == nil && checkpoint != nil && checkpoint.LibraryID == cfg.LibraryID {
		i.setProgress(func(p *ImportProgress) {
			p.ResumedFromCheckpoint = true
			p.Checkpoint = checkpoint
			if checkpoint.LastItemID != "" {
				p.Message = fmt.Sprintf("resuming from page %d after %s", checkpoint.Page, checkpoint.LastItemID)
			} else {
				p.Message = fmt.Sprintf("resuming from page %d", checkpoint.Page)
			}
		})
		summary.ResumedFromCheckpoint = true
		summary.Checkpoint = checkpoint
	}

	sourceConfigJSON, err := encodeJSON(sourceSnapshot(cfg))
	if err != nil {
		i.fail(fmt.Errorf("encode abs import source config: %w", err))
		return stats
	}
	checkpointJSON, err := encodeJSON(summary.Checkpoint)
	if err != nil {
		i.fail(fmt.Errorf("encode abs import checkpoint: %w", err))
		return stats
	}
	run := &models.ABSImportRun{
		SourceID:         cfg.SourceID,
		SourceLabel:      cfg.Label,
		BaseURL:          cfg.BaseURL,
		LibraryID:        cfg.LibraryID,
		Status:           runStatusRunning,
		DryRun:           cfg.DryRun,
		SourceConfigJSON: sourceConfigJSON,
		CheckpointJSON:   checkpointJSON,
		SummaryJSON:      "{}",
	}
	if i.runs != nil {
		if err := i.runs.Create(ctx, run); err != nil {
			i.fail(err)
			return stats
		}
		i.setProgress(func(p *ImportProgress) { p.RunID = run.ID })
	}

	enumFn, err := i.resolveEnumerator(cfg, run.ID)
	if err != nil {
		if run.ID != 0 && i.runs != nil {
			summary.Error = err.Error()
			_ = i.runs.Finish(ctx, run.ID, runStatusFailed, summary)
		}
		i.fail(err)
		return stats
	}

	enumStats, err := enumFn(ctx, cfg.LibraryID, func(ctx context.Context, item NormalizedLibraryItem) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		i.setProgress(func(p *ImportProgress) {
			p.Message = importItemMessage(cfg.DryRun, firstNonEmpty(item.Title, item.ItemID))
		})
		result := i.importOne(ctx, cfg, run.ID, item, stats, allowImmediateImport(item), authorMatcher)
		i.setProgress(func(p *ImportProgress) {
			p.Processed++
			p.Results = appendImportProgressResult(p.Results, result)
		})
		return nil
	})
	if err != nil {
		if run.ID != 0 && i.runs != nil {
			if checkpoint, checkpointErr := loadImportCheckpoint(ctx, i.settings); checkpointErr == nil {
				summary.Checkpoint = checkpoint
			}
			summary.Stats = *stats
			summary.Error = err.Error()
			_ = i.runs.Finish(ctx, run.ID, runStatusFailed, summary)
		}
		i.fail(err)
		return stats
	}

	stats.LibrariesScanned = 1
	stats.PagesScanned = enumStats.PagesScanned
	stats.ItemsSeen = enumStats.ItemsSeen
	stats.ItemsNormalized = enumStats.ItemsNormalized
	stats.ItemsDetailFetched = enumStats.ItemsDetailFetched

	if i.settings != nil && !cfg.DryRun {
		if err := i.settings.Set(ctx, SettingABSLastImportAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
			slog.Warn("abs import: persist last_import_at failed", "error", err)
		}
	}
	summary.Checkpoint = nil
	summary.Stats = *stats
	if run.ID != 0 && i.runs != nil {
		if err := i.runs.Finish(ctx, run.ID, runStatusCompleted, summary); err != nil {
			slog.Warn("abs import: finish run failed", "runID", run.ID, "error", err)
		}
	}
	slog.Info("abs import complete",
		"libraryID", cfg.LibraryID,
		"dryRun", cfg.DryRun,
		"pagesScanned", stats.PagesScanned,
		"itemsSeen", stats.ItemsSeen,
		"authorsCreated", stats.AuthorsCreated,
		"booksCreated", stats.BooksCreated,
		"booksLinked", stats.BooksLinked,
		"booksUpdated", stats.BooksUpdated,
		"seriesCreated", stats.SeriesCreated,
		"seriesLinked", stats.SeriesLinked,
		"editionsAdded", stats.EditionsAdded,
		"skipped", stats.Skipped,
		"failed", stats.Failed)
	i.setProgress(func(p *ImportProgress) {
		p.Checkpoint = nil
		p.ResumedFromCheckpoint = false
		p.Message = importDoneMessage(cfg.DryRun)
	})
	return stats
}

func (i *Importer) resolveEnumerator(cfg ImportConfig, runID int64) (enumerateFunc, error) {
	if i.enumerateFn != nil {
		return i.enumerateFn, nil
	}
	client, err := i.newClient(cfg.BaseURL, cfg.APIKey)
	if err != nil {
		return nil, err
	}
	enumerator := NewEnumerator(client, i.settings, 50).WithCheckpointObserver(func(checkpoint ImportCheckpoint) {
		cp := checkpoint
		i.setProgress(func(p *ImportProgress) {
			p.Checkpoint = &cp
		})
		if i.runs != nil && runID != 0 {
			if err := i.runs.UpdateCheckpoint(context.Background(), runID, checkpoint); err != nil {
				slog.Warn("abs import: persist checkpoint failed", "runID", runID, "error", err)
			}
		}
	})
	return enumerator.Enumerate, nil
}

func (i *Importer) importOne(ctx context.Context, cfg ImportConfig, runID int64, item NormalizedLibraryItem, stats *ImportStats, allowCreate bool, matcher *authorMatcher) ImportItemResult {
	result := ImportItemResult{
		ItemID:  item.ItemID,
		Title:   item.Title,
		Outcome: itemOutcomeUpdated,
	}
	author, authorCreated, authorMatchedBy, authorMeta, err := i.resolveAuthor(ctx, cfg, runID, item, allowCreate, matcher)
	if err != nil {
		var reviewErr reviewRequiredError
		if errors.As(err, &reviewErr) {
			if queueErr := i.queueReviewItem(ctx, runID, cfg, item, reviewErr.Reason); queueErr != nil {
				stats.Failed++
				result.Outcome = itemOutcomeFailed
				result.Message = queueErr.Error()
				return result
			}
			stats.ReviewQueued++
			result.Outcome = itemOutcomeSkipped
			result.Message = reviewQueueMessage(reviewErr.Reason, item)
			return result
		}
		stats.Failed++
		result.Outcome = itemOutcomeFailed
		result.Message = err.Error()
		return result
	}
	stats.MetadataMatched += authorMeta.Matched
	stats.MetadataRelinked += authorMeta.Relinked
	stats.MetadataConflicts += authorMeta.Conflicts
	stats.MetadataAutoResolved += authorMeta.AutoResolved
	result.AuthorID = author.ID
	if authorCreated {
		stats.AuthorsCreated++
	} else {
		stats.AuthorsLinked++
	}
	result.MatchedBy = authorMatchedBy

	if !cfg.DryRun {
		i.recordSecondaryAuthors(ctx, author.ID, item.Authors[1:], matcher)
	}

	bookResult, created, linked, bookMeta, err := i.upsertBook(ctx, cfg, runID, author, item, allowCreate)
	if err != nil {
		var reviewErr reviewRequiredError
		if errors.As(err, &reviewErr) {
			if queueErr := i.queueReviewItem(ctx, runID, cfg, item, reviewErr.Reason); queueErr != nil {
				stats.Failed++
				result.Outcome = itemOutcomeFailed
				result.Message = queueErr.Error()
				return result
			}
			stats.ReviewQueued++
			result.Outcome = itemOutcomeSkipped
			result.Message = reviewQueueMessage(reviewErr.Reason, item)
			return result
		}
		stats.Failed++
		result.Outcome = itemOutcomeFailed
		result.Message = err.Error()
		return result
	}
	stats.MetadataMatched += bookMeta.Matched
	stats.MetadataRelinked += bookMeta.Relinked
	stats.MetadataConflicts += bookMeta.Conflicts
	stats.MetadataAutoResolved += bookMeta.AutoResolved
	result.BookID = bookResult.row.ID
	if created {
		stats.BooksCreated++
		result.Outcome = itemOutcomeCreated
	} else if linked {
		stats.BooksLinked++
		result.Outcome = itemOutcomeLinked
	} else {
		stats.BooksUpdated++
		result.Outcome = itemOutcomeUpdated
	}
	if result.MatchedBy == "" {
		result.MatchedBy = bookResult.matchedBy
	}
	if !cfg.DryRun {
		i.enrichAudiobookFromASIN(ctx, bookResult.row)
	}

	seriesMemberships := map[string]struct{}{}
	for _, series := range item.Series {
		seriesResult, err := i.upsertSeries(ctx, cfg, runID, bookResult.row.ID, item.ItemID, series, stats)
		if err != nil {
			slog.Warn("abs import: series upsert failed", "itemID", item.ItemID, "series", series.Name, "error", err)
			continue
		}
		if seriesResult.Linked && seriesResult.CountKey != "" {
			seriesMemberships[seriesResult.CountKey] = struct{}{}
		}
		if seriesResult.CreatedSeries {
			stats.SeriesCreated++
		} else if seriesResult.MembershipCreated {
			stats.SeriesLinked++
		}
	}
	seriesMeta := metadataMergeResult{}
	if i.enhancedHardcoverSeriesEnabled(ctx) {
		var hardcoverSeriesResult seriesUpsertResult
		seriesMeta, hardcoverSeriesResult = i.matchHardcoverSeries(ctx, cfg, runID, author, bookResult.row, item, stats)
		stats.MetadataMatched += seriesMeta.Matched
		stats.MetadataRelinked += seriesMeta.Relinked
		stats.MetadataConflicts += seriesMeta.Conflicts
		stats.MetadataAutoResolved += seriesMeta.AutoResolved
		if hardcoverSeriesResult.Linked && hardcoverSeriesResult.CountKey != "" {
			seriesMemberships[hardcoverSeriesResult.CountKey] = struct{}{}
		}
		if hardcoverSeriesResult.CreatedSeries {
			stats.SeriesCreated++
		} else if hardcoverSeriesResult.MembershipCreated {
			stats.SeriesLinked++
		}
	}
	result.SeriesCount = len(seriesMemberships)

	addedEditions, err := i.upsertEditions(ctx, cfg, runID, bookResult.row.ID, item)
	if err != nil {
		slog.Warn("abs import: edition upsert failed", "itemID", item.ItemID, "error", err)
	} else {
		stats.EditionsAdded += addedEditions
	}

	reconcile := i.reconcileOwnedState(ctx, cfg, author, bookResult.row, item)
	stats.OwnedMarked += reconcile.OwnedMarked
	stats.PendingManual += reconcile.PendingManual
	messages := append([]string{}, authorMeta.Messages...)
	messages = append(messages, bookMeta.Messages...)
	messages = append(messages, seriesMeta.Messages...)
	if reconcile.Message != "" {
		messages = append(messages, reconcile.Message)
	}
	if len(messages) > 0 {
		result.Message = strings.Join(messages, "; ")
	}
	if !cfg.DryRun && !created {
		data := map[string]any{}
		if result.MatchedBy != "" {
			data["matchedBy"] = result.MatchedBy
		}
		if err := i.recordBookAfterSnapshot(ctx, runID, cfg, item, bookResult.row.ID, result.Outcome, data); err != nil {
			slog.Warn("abs import: persist book rollback snapshot failed", "bookID", bookResult.row.ID, "runID", runID, "error", err)
		}
	}

	return result
}

func reviewQueueMessage(reason string, item NormalizedLibraryItem) string {
	switch reason {
	case reviewReasonUnmatchedAuthor:
		return fmt.Sprintf("queued for review: no confident author match for %q", primaryAuthorName(item))
	case reviewReasonAmbiguousAuthor:
		return fmt.Sprintf("queued for review: multiple author matches for %q", primaryAuthorName(item))
	case reviewReasonUnmatchedBook:
		return fmt.Sprintf("queued for review: no confident book match for %q", strings.TrimSpace(item.Title))
	case reviewReasonAmbiguousBook:
		return fmt.Sprintf("queued for review: multiple book matches for %q", strings.TrimSpace(item.Title))
	default:
		return "queued for review"
	}
}

func (i *Importer) enrichAudiobookFromASIN(ctx context.Context, book *models.Book) {
	if i.meta == nil || i.books == nil || book == nil {
		return
	}
	if strings.TrimSpace(book.ASIN) == "" {
		return
	}
	if book.MediaType != models.MediaTypeAudiobook && book.MediaType != models.MediaTypeBoth {
		return
	}
	if err := i.meta.EnrichAudiobook(ctx, book); err != nil {
		slog.Debug("abs import: audnex enrichment skipped", "bookID", book.ID, "asin", book.ASIN, "error", err)
		return
	}
	if err := i.books.Update(ctx, book); err != nil {
		slog.Warn("abs import: persisting audnex enrichment failed", "bookID", book.ID, "asin", book.ASIN, "error", err)
	}
}

func (i *Importer) queueReviewItem(ctx context.Context, runID int64, cfg ImportConfig, item NormalizedLibraryItem, reason string) error {
	if i.reviews == nil {
		return reviewRequiredError{Reason: reason}
	}
	payloadJSON, err := encodeJSON(item)
	if err != nil {
		return fmt.Errorf("encode abs review payload: %w", err)
	}
	review := &models.ABSReviewItem{
		SourceID:      cfg.SourceID,
		LibraryID:     item.LibraryID,
		ItemID:        item.ItemID,
		Title:         strings.TrimSpace(item.Title),
		PrimaryAuthor: primaryAuthorName(item),
		ASIN:          strings.TrimSpace(item.ASIN),
		MediaType:     deriveMediaType(item),
		ReviewReason:  reason,
		PayloadJSON:   payloadJSON,
		Status:        "pending",
	}
	if runID != 0 {
		review.LatestRunID = ptrInt64(runID)
	}
	return i.reviews.UpsertPending(ctx, review)
}

func (i *Importer) recordRunEntity(ctx context.Context, runID int64, cfg ImportConfig, libraryID, itemID, entityType, externalID string, localID int64, outcome string, metadata any) error {
	if runID == 0 || i.runEntities == nil {
		return nil
	}
	metadataJSON, err := encodeJSON(metadata)
	if err != nil {
		err = fmt.Errorf("encode abs import run entity metadata: %w", err)
		slog.Warn("abs import: encode run entity metadata failed",
			"runID", runID,
			"libraryID", libraryID,
			"itemID", itemID,
			"entityType", entityType,
			"externalID", externalID,
			"localID", localID,
			"error", err)
		return err
	}
	return i.runEntities.Record(ctx, &models.ABSImportRunEntity{
		RunID:        runID,
		SourceID:     cfg.SourceID,
		LibraryID:    libraryID,
		ItemID:       itemID,
		EntityType:   entityType,
		ExternalID:   externalID,
		LocalID:      localID,
		Outcome:      outcome,
		MetadataJSON: metadataJSON,
	})
}

func (i *Importer) fail(err error) {
	slog.Error("abs import failed", "error", err)
	i.setProgress(func(p *ImportProgress) {
		p.Error = err.Error()
		p.Message = "failed"
	})
}

func (i *Importer) setProgress(mutate func(*ImportProgress)) {
	i.mu.Lock()
	defer i.mu.Unlock()
	mutate(&i.progress)
}

func appendImportProgressResult(results []ImportItemResult, result ImportItemResult) []ImportItemResult {
	if len(results) < importProgressResultsLimit {
		return append(results, result)
	}
	copy(results, results[len(results)-importProgressResultsLimit+1:])
	results[importProgressResultsLimit-1] = result
	return results[:importProgressResultsLimit]
}

func sourceSnapshot(cfg ImportConfig) ImportSourceSnapshot {
	return ImportSourceSnapshot{
		SourceID:  cfg.SourceID,
		Label:     cfg.Label,
		BaseURL:   cfg.BaseURL,
		LibraryID: cfg.LibraryID,
		PathRemap: cfg.PathRemap,
		Enabled:   cfg.Enabled,
		DryRun:    cfg.DryRun,
	}
}

func encodeJSON(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("json marshal produced empty payload")
	}
	return string(data), nil
}

func loadImportCheckpoint(ctx context.Context, settings *db.SettingsRepo) (*ImportCheckpoint, error) {
	if settings == nil {
		return nil, nil
	}
	setting, err := settings.Get(ctx, SettingABSImportCheckpoint)
	if err != nil || setting == nil || strings.TrimSpace(setting.Value) == "" {
		return nil, err
	}
	return decodeImportCheckpoint(setting.Value)
}

func decodeImportCheckpoint(raw string) (*ImportCheckpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return nil, nil
	}
	var checkpoint ImportCheckpoint
	if err := json.Unmarshal([]byte(raw), &checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func resumeConfigFromRun(run models.ABSImportRun, fallback ImportConfig) ImportConfig {
	cfg := fallback.normalized()
	var source ImportSourceSnapshot
	rawSource := strings.TrimSpace(run.SourceConfigJSON)
	hasSource := rawSource != "" && rawSource != "{}" && rawSource != "null"
	if hasSource {
		if err := json.Unmarshal([]byte(rawSource), &source); err != nil {
			hasSource = false
		}
	}
	if hasSource {
		cfg.SourceID = firstNonEmpty(source.SourceID, run.SourceID, cfg.SourceID)
		cfg.BaseURL = firstNonEmpty(source.BaseURL, run.BaseURL, cfg.BaseURL)
		cfg.LibraryID = firstNonEmpty(source.LibraryID, run.LibraryID, cfg.LibraryID)
		cfg.Label = firstNonEmpty(source.Label, run.SourceLabel, cfg.Label)
		cfg.PathRemap = source.PathRemap
	} else {
		cfg.SourceID = firstNonEmpty(run.SourceID, cfg.SourceID)
		cfg.BaseURL = firstNonEmpty(run.BaseURL, cfg.BaseURL)
		cfg.LibraryID = firstNonEmpty(run.LibraryID, cfg.LibraryID)
		cfg.Label = firstNonEmpty(run.SourceLabel, cfg.Label)
	}
	cfg.DryRun = run.DryRun
	return cfg.normalized()
}

func importStartMessage(dryRun bool) string {
	if dryRun {
		return "starting ABS dry-run…"
	}
	return "starting ABS import…"
}

func importItemMessage(dryRun bool, label string) string {
	if dryRun {
		return fmt.Sprintf("previewing %s", label)
	}
	return fmt.Sprintf("importing %s", label)
}

func importDoneMessage(dryRun bool) string {
	if dryRun {
		return "dry-run complete"
	}
	return "done"
}
