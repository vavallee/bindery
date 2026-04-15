// Package scheduler runs Bindery's periodic background jobs (wanted-book
// search, download-status polling, metadata refresh, library rescan) via
// robfig/cron.
package scheduler

import (
	"context"
	"log/slog"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Scheduler runs background jobs on configurable intervals.
type Scheduler struct {
	cron     *cron.Cron
	scanner  *importer.Scanner
	searcher *indexer.Searcher
	meta     *metadata.Aggregator

	authors   *db.AuthorRepo
	books     *db.BookRepo
	indexers  *db.IndexerRepo
	downloads *db.DownloadRepo
	clients   *db.DownloadClientRepo
	settings  *db.SettingsRepo
	blocklist *db.BlocklistRepo
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

	s.cron.Start()
	slog.Info("scheduler started", "jobs", len(s.cron.Entries()))
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	slog.Info("scheduler stopped")
}

// SearchAndGrabBook performs an immediate indexer search for a single wanted
// book and auto-grabs the top result. It is the same logic the 12-hour
// wanted-scan uses, promoted so on-add and status-transition hooks can trigger
// a search without waiting for the next scheduled run.
func (s *Scheduler) SearchAndGrabBook(ctx context.Context, book models.Book) {
	idxs, err := s.indexers.List(ctx)
	if err != nil {
		slog.Error("SearchAndGrabBook: failed to list indexers", "error", err)
		return
	}

	lang := "en"
	if langSetting, _ := s.settings.Get(ctx, "search.preferredLanguage"); langSetting != nil {
		lang = langSetting.Value
	}

	authorName := ""
	if a, _ := s.authors.GetByID(ctx, book.AuthorID); a != nil {
		authorName = a.Name
	}
	crit := indexer.MatchCriteria{
		Title:     book.Title,
		Author:    authorName,
		MediaType: book.MediaType,
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

	candidates, _ := s.clients.GetEnabledByProtocol(ctx, best.Protocol)
	client := db.PickClientForMediaType(candidates, book.MediaType)
	if client == nil {
		client, _ = s.clients.GetFirstEnabled(ctx)
	}
	if client == nil {
		slog.Debug("SearchAndGrabBook: no download client available", "book", book.Title)
		return
	}

	slog.Info("auto-grabbing book",
		"book", book.Title,
		"author", authorName,
		"result", best.Title,
		"indexer", best.IndexerName,
		"protocol", best.Protocol,
		"client", client.Name,
		"size", best.Size,
	)

	existing, _ := s.downloads.GetByGUID(ctx, best.GUID)
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

	if best.Protocol == "torrent" {
		qbt := qbittorrent.New(client.Host, client.Port, client.URLBase, client.APIKey, client.UseSSL)
		if err := qbt.AddTorrent(ctx, best.NZBURL, client.Category, ""); err != nil {
			slog.Error("SearchAndGrabBook: failed to send to qBittorrent", "title", best.Title, "error", err)
			s.downloads.SetError(ctx, dl.ID, err.Error())
			return
		}
		s.downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusDownloading)
		slog.Info("sent to qBittorrent", "title", best.Title)
	} else {
		sab := sabnzbd.New(client.Host, client.Port, client.APIKey, client.UseSSL)
		resp, err := sab.AddURL(ctx, best.NZBURL, best.Title, client.Category, 0)
		if err != nil {
			slog.Error("SearchAndGrabBook: failed to send to SABnzbd", "title", best.Title, "error", err)
			s.downloads.SetError(ctx, dl.ID, err.Error())
			return
		}
		if len(resp.NzoIDs) > 0 {
			s.downloads.SetNzoID(ctx, dl.ID, resp.NzoIDs[0])
		}
		s.downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusDownloading)
		slog.Info("sent to SABnzbd", "title", best.Title)
	}
}

func (s *Scheduler) searchWanted() {
	ctx := context.Background()

	// Respect the global auto-grab kill-switch. When disabled, the
	// scheduled wanted-scan is skipped entirely — users manage grabs
	// manually from the Wanted page.
	if s.settings != nil {
		if setting, _ := s.settings.Get(ctx, "autoGrab.enabled"); setting != nil && setting.Value == "false" {
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
		if blocked, _ := bl.IsBlocked(ctx, r.GUID); !blocked {
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
