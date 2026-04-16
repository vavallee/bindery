// Package scheduler runs Bindery's periodic background jobs (wanted-book
// search, download-status polling, metadata refresh, library rescan) via
// robfig/cron.
package scheduler

import (
	"context"
	"log/slog"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// CalibreSyncer is the narrow interface the scheduler calls to trigger a
// Calibre library import. Implemented by *calibre.Importer; the interface
// keeps the scheduler package free of a direct import of the calibre package.
type CalibreSyncer interface {
	RunSync(ctx context.Context)
}

// bookSearcher is the narrow interface the scheduler uses for indexer
// searches. *indexer.Searcher satisfies it; the interface keeps the scheduler
// testable without real network calls.
type bookSearcher interface {
	SearchBook(ctx context.Context, indexers []models.Indexer, c indexer.MatchCriteria) []newznab.SearchResult
}

// RecommendationEngine is the narrow interface the scheduler calls to
// regenerate recommendations. *recommender.Engine satisfies it.
type RecommendationEngine interface {
	Run(ctx context.Context, userID int64) error
}

// Scheduler runs background jobs on configurable intervals.
type Scheduler struct {
	cron     *cron.Cron
	scanner  *importer.Scanner
	searcher bookSearcher
	meta     *metadata.Aggregator

	authors       *db.AuthorRepo
	books         *db.BookRepo
	indexers      *db.IndexerRepo
	downloads     *db.DownloadRepo
	clients       *db.DownloadClientRepo
	settings      *db.SettingsRepo
	blocklist     *db.BlocklistRepo
	calibreSyncer CalibreSyncer        // optional; nil if Calibre is not configured
	recommender   RecommendationEngine // optional; generates recommendations
}

// New creates a new scheduler.
func New(
	scanner *importer.Scanner,
	searcher *indexer.Searcher,
	meta *metadata.Aggregator,
	authors *db.AuthorRepo,
	books *db.BookRepo,
	indexers *db.IndexerRepo,
	downloads *db.DownloadRepo,
	clients *db.DownloadClientRepo,
	settings *db.SettingsRepo,
	blocklist *db.BlocklistRepo,
) *Scheduler {
	return &Scheduler{
		cron:      cron.New(cron.WithSeconds()),
		scanner:   scanner,
		searcher:  searcher,
		meta:      meta,
		authors:   authors,
		books:     books,
		indexers:  indexers,
		downloads: downloads,
		clients:   clients,
		settings:  settings,
		blocklist: blocklist,
	}
}

// WithCalibreSyncer registers a CalibreSyncer that the scheduler will call
// every 24 hours when Calibre is configured. Must be called before Start.
func (s *Scheduler) WithCalibreSyncer(syncer CalibreSyncer) {
	s.calibreSyncer = syncer
}

// WithRecommender registers a recommendation engine that runs every 24 hours.
// Must be called before Start.
func (s *Scheduler) WithRecommender(engine RecommendationEngine) {
	s.recommender = engine
}

// Start registers and runs all background jobs.
func (s *Scheduler) Start() {
	// Check downloads every 15 seconds so completed imports land quickly
	// after SABnzbd finishes post-processing (unrar/par-check). The actual
	// lag between "100%" and "imported" = SAB post-processing time +
	// up to 15s poll + file-move time.
	s.cron.AddFunc("@every 15s", func() {
		slog.Debug("job: check downloads")
		s.scanner.CheckDownloads(context.Background())
	})

	// Search for wanted books every 12 hours
	s.cron.AddFunc("@every 12h", func() {
		slog.Info("job: search wanted books")
		s.searchWanted()
	})

	// Refresh author metadata every 24 hours
	s.cron.AddFunc("@every 24h", func() {
		slog.Info("job: refresh metadata")
		s.refreshMetadata()
	})

	// Scan library every 6 hours
	s.cron.AddFunc("@every 6h", func() {
		slog.Info("job: scan library")
		s.scanner.ScanLibrary(context.Background())
	})

	// Sync Calibre library every 24 hours when a syncer is registered.
	if s.calibreSyncer != nil {
		s.cron.AddFunc("@every 24h", func() {
			slog.Info("job: calibre library sync")
			s.calibreSyncer.RunSync(context.Background())
		})
	}

	// Generate recommendations every 24 hours when the engine is registered.
	if s.recommender != nil {
		s.cron.AddFunc("@every 24h", func() {
			slog.Info("job: generate recommendations")
			if s.settings != nil {
				if setting, _ := s.settings.Get(context.Background(), "recommendations.enabled"); setting == nil || setting.Value != "true" {
					return
				}
			}
			if err := s.recommender.Run(context.Background(), 1); err != nil {
				slog.Error("recommendation engine failed", "error", err)
			}
		})
	}

	s.cron.Start()
	slog.Info("scheduler started", "jobs", len(s.cron.Entries()))
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	slog.Info("scheduler stopped")
}

// SearchAndGrabBook performs an immediate indexer search for a wanted book and
// auto-grabs the top result. For dual-format books (media_type='both') it fires
// independent searches for whichever formats still lack a file on disk.
// It is the same logic the 12-hour wanted-scan uses, promoted so on-add and
// status-transition hooks can trigger a search without waiting for the next run.
func (s *Scheduler) SearchAndGrabBook(ctx context.Context, book models.Book) {
	if book.NeedsEbook() {
		s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook)
	}
	if book.NeedsAudiobook() {
		s.searchAndGrabFormat(ctx, book, models.MediaTypeAudiobook)
	}
}

// searchAndGrabFormat searches for and grabs a specific format of a book.
// mediaType must be MediaTypeEbook or MediaTypeAudiobook.
func (s *Scheduler) searchAndGrabFormat(ctx context.Context, book models.Book, mediaType string) {
	var idxs []models.Indexer
	if s.indexers != nil {
		var err error
		idxs, err = s.indexers.List(ctx)
		if err != nil {
			slog.Error("SearchAndGrabBook: failed to list indexers", "error", err)
			return
		}
	}

	lang := "en"
	if s.settings != nil {
		if langSetting, err := s.settings.Get(ctx, "search.preferredLanguage"); err != nil {
			slog.Warn("failed to load preferred search language", "error", err)
		} else if langSetting != nil {
			lang = langSetting.Value
		}
	}

	authorName := ""
	if s.authors != nil {
		if a, err := s.authors.GetByID(ctx, book.AuthorID); err != nil {
			slog.Warn("failed to load author for search", "author_id", book.AuthorID, "error", err)
		} else if a != nil {
			authorName = a.Name
		}
	}
	crit := indexer.MatchCriteria{
		Title:     book.Title,
		Author:    authorName,
		MediaType: mediaType,
		ASIN:      book.ASIN,
	}
	if book.ReleaseDate != nil {
		crit.Year = book.ReleaseDate.Year()
	}

	results := s.searcher.SearchBook(ctx, idxs, crit)
	results = indexer.FilterByLanguage(results, lang)
	results = filterBlocklisted(ctx, s.blocklist, results)
	if len(results) == 0 {
		return
	}

	best := results[0]

	candidates, err := s.clients.GetEnabledByProtocol(ctx, best.Protocol)
	if err != nil {
		slog.Warn("failed to list clients for protocol", "protocol", best.Protocol, "error", err)
	}
	client := db.PickClientForMediaType(candidates, mediaType)
	if client == nil {
		client, err = s.clients.GetFirstEnabled(ctx)
		if err != nil {
			slog.Warn("failed to load fallback download client", "error", err)
		}
	}
	if client == nil {
		slog.Warn("SearchAndGrabBook: no enabled download client for protocol", "book", book.Title, "protocol", best.Protocol)

		return
	}

	slog.Info("auto-grabbing book",
		"book", book.Title,
		"author", authorName,
		"format", mediaType,
		"result", best.Title,
		"indexer", best.IndexerName,
		"protocol", best.Protocol,
		"client", client.Name,
		"size", best.Size,
	)

	existing, err := s.downloads.GetByGUID(ctx, best.GUID)
	if err != nil {
		slog.Warn("failed to check existing download", "guid", best.GUID, "error", err)
		return
	}
	if existing != nil {
		return
	}

	dl := &models.Download{
		GUID:             best.GUID,
		BookID:           &book.ID,
		IndexerID:        &best.IndexerID,
		DownloadClientID: &client.ID,
		Title:            best.Title,
		NZBURL:           best.NZBURL,
		Size:             best.Size,
		Status:           models.DownloadStatusQueued,
		Protocol:         best.Protocol,
		Quality:          indexer.ParseRelease(best.Title).Format,
	}

	if err := s.downloads.Create(ctx, dl); err != nil {
		slog.Error("SearchAndGrabBook: failed to create download record", "error", err)
		return
	}

	sendRes, err := downloader.SendDownload(ctx, client, best.NZBURL, best.Title)
	if err != nil {
		slog.Error("SearchAndGrabBook: failed to send to downloader", "client", client.Type, "title", best.Title, "error", err)
		if setErr := s.downloads.SetError(ctx, dl.ID, err.Error()); setErr != nil {
			slog.Warn("failed to persist download error", "download_id", dl.ID, "error", setErr)
		}
		return
	}
	if sendRes.RemoteID != "" {
		if sendRes.UsesTorrentID {
			if err := s.downloads.SetTorrentID(ctx, dl.ID, sendRes.RemoteID); err != nil {
				slog.Warn("failed to set torrent ID", "download_id", dl.ID, "error", err)
			}
		} else {
			if err := s.downloads.SetNzoID(ctx, dl.ID, sendRes.RemoteID); err != nil {
				slog.Warn("failed to set NZO ID", "download_id", dl.ID, "error", err)
			}
		}
	}
	if err := s.downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusDownloading); err != nil {
		slog.Warn("failed to update download status", "download_id", dl.ID, "status", models.DownloadStatusDownloading, "error", err)
	}
	slog.Info("sent to downloader", "client", client.Type, "title", best.Title)
}

func (s *Scheduler) searchWanted() {
	ctx := context.Background()

	// Respect the global auto-grab kill-switch. When disabled, the
	// scheduled wanted-scan is skipped entirely — users manage grabs
	// manually from the Wanted page.
	if s.settings != nil {
		if setting, err := s.settings.Get(ctx, "autoGrab.enabled"); err != nil {
			slog.Warn("failed to load auto-grab setting", "error", err)
		} else if setting != nil && setting.Value == "false" {
			slog.Info("job: auto-grab disabled globally, skipping wanted search")
			return
		}
	}

	wanted, err := s.books.ListByStatus(ctx, models.BookStatusWanted)
	if err != nil {
		slog.Error("failed to list wanted books", "error", err)
		return
	}
	if len(wanted) == 0 {
		return
	}

	for _, book := range wanted {
		s.SearchAndGrabBook(ctx, book)
	}
}

// filterBlocklisted drops any result whose GUID is in the blocklist. A nil
// or erroring repo is treated as "nothing blocked".
func filterBlocklisted(ctx context.Context, bl *db.BlocklistRepo, results []newznab.SearchResult) []newznab.SearchResult {
	if bl == nil {
		return results
	}
	out := make([]newznab.SearchResult, 0, len(results))
	for _, r := range results {
		blocked, err := bl.IsBlocked(ctx, r.GUID)
		if err != nil {
			slog.Warn("failed to check blocklist", "guid", r.GUID, "error", err)
			out = append(out, r)
			continue
		}
		if !blocked {
			out = append(out, r)
		}
	}
	return out
}

func (s *Scheduler) refreshMetadata() {
	ctx := context.Background()

	authors, err := s.authors.List(ctx)
	if err != nil {
		slog.Error("failed to list authors", "error", err)
		return
	}

	for _, author := range authors {
		if !author.Monitored {
			continue
		}

		updated, err := s.meta.GetAuthor(ctx, author.ForeignID)
		if err != nil {
			slog.Warn("failed to refresh author", "author", author.Name, "error", err)
			continue
		}

		// Update changed fields
		author.Description = updated.Description
		if updated.ImageURL != "" {
			author.ImageURL = updated.ImageURL
		}
		author.AverageRating = updated.AverageRating
		author.RatingsCount = updated.RatingsCount
		s.authors.Update(ctx, &author)

		slog.Debug("refreshed author", "author", author.Name)
	}
}
