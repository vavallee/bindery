// Package scheduler runs Bindery's periodic background jobs (wanted-book
// search, download-status polling, metadata refresh, library rescan) via
// robfig/cron.
package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metrics"
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

// HCListSyncer is the interface the scheduler calls to sync Hardcover import
// lists. Implemented by *hardcoverlistsyncer.ListSyncer.
type HCListSyncer interface {
	Sync(ctx context.Context) error
}

// TelemetryPinger is the interface the scheduler calls to send the daily
// anonymous install ping. Implemented by *telemetry.Client.
type TelemetryPinger interface {
	Ping(ctx context.Context)
}

// eventNotifier publishes a webhook event for a downstream notification
// target (Discord/ntfy/etc.). Narrow shape of *notifier.Notifier so tests can
// spy on auto-grab Send calls without standing up an HTTP fixture (issue #849).
type eventNotifier interface {
	Send(ctx context.Context, eventType string, payload map[string]interface{})
}

// Event-type strings. Kept in sync with internal/notifier.Event* — declared
// here to avoid having the scheduler import notifier just for two string
// constants (and to keep the eventNotifier interface untied to the real type).
const (
	notifierEventGrabbed = "grabbed"
)

// Scheduler runs background jobs on configurable intervals.
type Scheduler struct {
	// appCtx is the process-lifecycle context: it is not tied to any single
	// HTTP request but is cancelled when the process shuts down. Background
	// jobs that should observe shutdown will derive from it (see #707). It is
	// never nil — New falls back to context.Background() when given a nil ctx.
	appCtx context.Context

	// bgWg tracks raw goroutines that are not managed by the cron scheduler
	// (e.g. the startup telemetry ping and stall re-search). Stop() waits for
	// all of them to finish after the cron scheduler drains, so they cannot
	// run against resources that have already been torn down.
	bgWg sync.WaitGroup

	cron     *cron.Cron
	scanner  *importer.Scanner
	searcher bookSearcher
	meta     *metadata.Aggregator

	authors              *db.AuthorRepo
	books                *db.BookRepo
	indexers             *db.IndexerRepo
	downloads            *db.DownloadRepo
	clients              *db.DownloadClientRepo
	history              *db.HistoryRepo
	settings             *db.SettingsRepo
	blocklist            *db.BlocklistRepo
	delayProfiles        *db.DelayProfileRepo
	pending              *db.PendingReleaseRepo
	aliases              *db.AuthorAliasRepo  // optional; used for non-latin author matching
	calibreSyncer        CalibreSyncer        // optional; nil if Calibre is not configured
	recommender          RecommendationEngine // optional; generates recommendations
	hcSyncer             HCListSyncer         // optional; syncs Hardcover import lists
	telemetry            TelemetryPinger      // optional; sends daily anonymous install ping
	logs                 *db.LogRepo          // optional; enables periodic log retention trim
	notif                eventNotifier        // optional; fires EventGrabbed on auto-grab success (#849)
	logRetainDays        int                  // 0 = use default (14)
	downloadDir          string
	audiobookDownloadDir string
}

const scheduledWantedSearchConcurrency = 2

// New creates a new scheduler.
//
// appCtx is the process-lifecycle context — a context that is not tied to any
// single HTTP request but is cancelled when the process shuts down. It is
// stored on the Scheduler so background jobs can later derive request-free,
// shutdown-aware contexts from it (see #707). A nil appCtx is tolerated and
// replaced with context.Background().
func New(
	appCtx context.Context,
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
	if appCtx == nil {
		appCtx = context.Background()
	}
	return &Scheduler{
		appCtx:    appCtx,
		cron:      cron.New(cron.WithSeconds(), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger))),
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

// WithDelayProfiles attaches the delay profile repo used when evaluating releases.
// Must be called before Start.
func (s *Scheduler) WithDelayProfiles(dp *db.DelayProfileRepo) {
	s.delayProfiles = dp
}

// WithPendingReleases attaches the pending releases repo so delay-rejected
// results are stored for re-evaluation. Must be called before Start.
func (s *Scheduler) WithPendingReleases(pr *db.PendingReleaseRepo) {
	s.pending = pr
}

// WithHistory attaches a HistoryRepo so stall events can be recorded.
// Must be called before Start.
func (s *Scheduler) WithHistory(h *db.HistoryRepo) {
	s.history = h
}

// WithAliases attaches the author alias repo used to populate AuthorAliases
// in MatchCriteria for non-latin author name matching. Must be called before Start.
func (s *Scheduler) WithAliases(aliases *db.AuthorAliasRepo) {
	s.aliases = aliases
}

// WithStoragePaths attaches the process-level download roots used when sending
// torrent clients an explicit save path.
func (s *Scheduler) WithStoragePaths(downloadDir, audiobookDownloadDir string) {
	s.downloadDir = downloadDir
	s.audiobookDownloadDir = audiobookDownloadDir
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

// WithHardcoverSyncer registers a Hardcover list syncer that runs every 24
// hours to import books from the user's Hardcover reading lists.
func (s *Scheduler) WithHardcoverSyncer(syncer HCListSyncer) {
	s.hcSyncer = syncer
}

// WithTelemetry registers the telemetry client for the daily anonymous ping.
// Must be called before Start.
func (s *Scheduler) WithTelemetry(p TelemetryPinger) {
	s.telemetry = p
}

// WithLogRepo registers a log repository for the daily retention trim job.
// retainDays controls how many days of log entries to keep; 0 uses the
// default (14). Must be called before Start.
func (s *Scheduler) WithLogRepo(logs *db.LogRepo, retainDays int) {
	s.logs = logs
	s.logRetainDays = retainDays
}

// WithNotifier attaches a webhook event notifier so auto-grab successes
// publish EventGrabbed (issue #849). Manual grabs from the queue page already
// fire via QueueHandler; without this wiring the auto-grab path (wanted-scan,
// on-add hook, bulk grab, recommendation grab, series grab) was silent.
func (s *Scheduler) WithNotifier(n eventNotifier) {
	s.notif = n
}

// notify is a nil-safe Send wrapper so the auto-grab path stays a one-liner.
func (s *Scheduler) notify(ctx context.Context, eventType string, payload map[string]interface{}) {
	if s.notif == nil {
		return
	}
	s.notif.Send(ctx, eventType, payload)
}

// ctx returns the process-lifecycle context, falling back to
// context.Background() when appCtx was not set (e.g. in unit tests that
// construct a Scheduler literal directly rather than calling New).
func (s *Scheduler) ctx() context.Context {
	if s.appCtx != nil {
		return s.appCtx
	}
	return context.Background()
}

// runJob wraps a cron callback so each invocation is recorded via the
// metrics package — duration, completion count, and panic count. Jobs that
// panic are recovered here so a single buggy job doesn't tear down the
// scheduler goroutine; the panic is logged and counted with result="panic".
func runJob(name string, fn func()) func() {
	return func() {
		start := time.Now()
		result := "ok"
		defer func() {
			if r := recover(); r != nil {
				result = "panic"
				slog.Error("scheduler job panicked", "job", name, "panic", r)
			}
			metrics.ObserveSchedulerRun(name, result, time.Since(start))
		}()
		fn()
	}
}

// Start registers and runs all background jobs.
func (s *Scheduler) Start() {
	// Check downloads every 15 seconds so completed imports land quickly
	// after SABnzbd finishes post-processing (unrar/par-check). The actual
	// lag between "100%" and "imported" = SAB post-processing time +
	// up to 15s poll + file-move time.
	s.cron.AddFunc("@every 15s", runJob("check-downloads", func() {
		slog.Debug("job: check downloads")
		s.scanner.CheckDownloads(s.ctx())
	}))

	// Stall detection runs every 5 minutes. Checking every 15s would be
	// noisy for a condition that changes slowly; 5 minutes is frequent
	// enough to act well within any reasonable stall timeout.
	s.cron.AddFunc("@every 5m", runJob("check-stalled", func() {
		slog.Debug("job: check stalled downloads")
		s.checkStalledDownloads(s.ctx())
	}))

	// Search for wanted books every 12 hours
	s.cron.AddFunc("@every 12h", runJob("search-wanted", func() {
		slog.Info("job: search wanted books")
		s.searchWanted()
	}))

	// Refresh author metadata every 24 hours
	s.cron.AddFunc("@every 24h", runJob("refresh-metadata", func() {
		slog.Info("job: refresh metadata")
		s.refreshMetadata()
	}))

	// Scan library every 6 hours
	s.cron.AddFunc("@every 6h", runJob("scan-library", func() {
		slog.Info("job: scan library")
		s.scanner.ScanLibrary(s.ctx())
	}))

	// Sync Calibre library every 24 hours when a syncer is registered.
	if s.calibreSyncer != nil {
		s.cron.AddFunc("@every 24h", runJob("calibre-sync", func() {
			slog.Info("job: calibre library sync")
			s.calibreSyncer.RunSync(s.ctx())
		}))
	}

	// Generate recommendations every 24 hours when the engine is registered.
	if s.recommender != nil {
		s.cron.AddFunc("@every 24h", runJob("recommendations", func() {
			slog.Info("job: generate recommendations")
			if s.settings != nil {
				if setting, _ := s.settings.Get(s.ctx(), "recommendations.enabled"); setting == nil || setting.Value != "true" {
					return
				}
			}
			if err := s.recommender.Run(s.ctx(), 1); err != nil {
				slog.Error("recommendation engine failed", "error", err)
			}
		}))
	}

	// Sync Hardcover import lists every 24 hours.
	if s.hcSyncer != nil {
		s.cron.AddFunc("@every 24h", runJob("hardcover-sync", func() {
			slog.Info("job: sync hardcover lists")
			if err := s.hcSyncer.Sync(s.ctx()); err != nil {
				slog.Error("hardcover list sync failed", "error", err)
			}
		}))
	}

	// Send anonymous install ping every 24 hours.
	if s.telemetry != nil {
		s.cron.AddFunc("@every 24h", runJob("telemetry-ping", func() {
			s.telemetry.Ping(s.ctx())
		}))
		// Also fire once on startup (non-blocking). Tracked so Stop() can
		// wait for it to finish before tearing down other resources.
		s.bgWg.Add(1)
		go func() {
			defer s.bgWg.Done()
			s.telemetry.Ping(s.ctx())
		}()
	}

	// Trim old log entries once per day.
	if s.logs != nil {
		defaultRetainDays := s.logRetainDays
		if defaultRetainDays <= 0 {
			defaultRetainDays = 14
		}
		s.cron.AddFunc("@every 24h", runJob("log-trim", func() {
			slog.Debug("job: trim log entries")
			retainDays := defaultRetainDays
			// Prefer the DB setting when available so UI changes take effect without restart.
			if s.settings != nil {
				if v, _ := s.settings.Get(s.ctx(), "log.retention_days"); v != nil {
					if n, err := strconv.Atoi(v.Value); err == nil && n > 0 {
						retainDays = n
					}
				}
			}
			cutoff := time.Now().UTC().Add(-time.Duration(retainDays) * 24 * time.Hour)
			if err := s.logs.Trim(s.ctx(), cutoff); err != nil {
				slog.Warn("log trim failed", "error", err)
			}
		}))
	}

	s.cron.Start()
	slog.Info("scheduler started", "jobs", len(s.cron.Entries()))
}

// Stop gracefully stops the scheduler.
// It waits for the cron scheduler's in-flight jobs to complete and then waits
// for any background goroutines tracked by bgWg (startup telemetry ping, stall
// re-search) to finish before returning.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	// Drain goroutines that were launched outside the cron scheduler.
	s.bgWg.Wait()
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

	lang := ""
	if s.settings != nil {
		if langSetting, err := s.settings.Get(ctx, "search.preferredLanguage"); err != nil {
			slog.Warn("failed to load preferred search language", "error", err)
		} else if langSetting != nil {
			lang = langSetting.Value
		}
	}

	authorName := ""
	var authorAliases []string
	if s.authors != nil {
		if a, err := s.authors.GetByID(ctx, book.AuthorID); err != nil {
			slog.Warn("failed to load author for search", "author_id", book.AuthorID, "error", err)
		} else if a != nil {
			authorName = a.Name
			if s.aliases != nil {
				if aliases, err := s.aliases.ListByAuthor(ctx, a.ID); err == nil {
					for _, al := range aliases {
						authorAliases = append(authorAliases, al.Name)
					}
				}
			}
		}
	}
	crit := indexer.MatchCriteria{
		Title:         book.Title,
		Author:        authorName,
		MediaType:     mediaType,
		ASIN:          book.ASIN,
		AuthorAliases: authorAliases,
	}
	if book.ReleaseDate != nil {
		crit.Year = book.ReleaseDate.Year()
	}

	results := s.searcher.SearchBook(ctx, idxs, crit)
	results = indexer.FilterByLanguage(results, lang)

	var specs []decision.Specification
	if s.blocklist != nil {
		if entries, err := s.blocklist.List(ctx); err == nil {
			specs = append(specs, decision.NewBlocklistedSpec(entries))
		}
	}
	var delayProfile *models.DelayProfile
	if s.delayProfiles != nil {
		if profiles, err := s.delayProfiles.List(ctx); err == nil && len(profiles) > 0 {
			delayProfile = &profiles[0]
			specs = append(specs, decision.DelayProfileSpec{Profile: delayProfile})
		}
	}
	dm := decision.New(specs...)
	releases := make([]decision.Release, len(results))
	for i, res := range results {
		releases[i] = decision.ReleaseFromSearchResult(res)
	}

	var best *newznab.SearchResult
	for i, d := range dm.Evaluate(releases, book) {
		if d.Approved {
			best = &results[i]
			break
		}
		// Store delay-rejected releases so they can be re-evaluated next sweep.
		// The sentinel "delay not met" matches both "usenet delay not met" and
		// "torrent delay not met" produced by DelayProfileSpec. There is no typed
		// flag on Decision today; left as-is per #707 (minor finding).
		if s.pending != nil && strings.Contains(d.Rejection, "delay not met") {
			s.storePending(ctx, book.ID, mediaType, results[i], d.Rejection)
		}
	}
	if best == nil {
		// Re-evaluate any existing pending releases for this book/format with the current age.
		best = s.checkPendingReleases(ctx, book, mediaType, dm)
		if best == nil {
			return
		}
	}

	candidates, err := s.clients.GetEnabledByProtocol(ctx, best.Protocol)
	if err != nil {
		slog.Error("SearchAndGrabBook: failed to list download clients", "protocol", best.Protocol, "error", err)
		return
	}
	client := db.PickClientForMediaType(candidates, mediaType)
	// No cross-protocol fallback: a usenet release must not be pushed to a
	// torrent client (qBittorrent would accept the .nzb URL, fail to parse it
	// as a torrent, and report "hash could not be determined"), and vice versa.
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
		Status:           models.StateGrabbed,
		Protocol:         best.Protocol,
		Quality:          indexer.ParseRelease(best.Title).Format,
	}

	if err := s.downloads.Create(ctx, dl); err != nil {
		slog.Error("SearchAndGrabBook: failed to create download record", "error", err)
		return
	}

	sendRes, err := downloader.SendDownload(ctx, client, best.NZBURL, best.Title, downloader.SendOptions{
		MediaType:            mediaType,
		DownloadDir:          s.downloadDir,
		AudiobookDownloadDir: s.audiobookDownloadDir,
	})
	if err != nil {
		slog.Error("SearchAndGrabBook: failed to send to downloader", "client", client.Type, "title", best.Title, "error", err)
		if setErr := s.downloads.SetError(ctx, dl.ID, err.Error()); setErr != nil {
			slog.Warn("failed to persist download error", "download_id", dl.ID, "error", setErr)
		}
		return
	}
	if sendRes.RemoteID != "" {
		if sendRes.UsesTorrentID {
			normalised := strings.ToLower(sendRes.RemoteID)
			if err := s.downloads.SetTorrentID(ctx, dl.ID, normalised); err != nil {
				slog.Warn("failed to set torrent ID", "download_id", dl.ID, "error", err)
			}
		} else {
			if err := s.downloads.SetNzoID(ctx, dl.ID, sendRes.RemoteID); err != nil {
				slog.Warn("failed to set NZO ID", "download_id", dl.ID, "error", err)
			}
		}
	}
	if err := s.downloads.UpdateStatus(ctx, dl.ID, models.StateDownloading); err != nil {
		slog.Warn("failed to update download status", "download_id", dl.ID, "status", models.StateDownloading, "error", err)
	}
	slog.Info("sent to downloader", "client", client.Type, "title", best.Title)
	// Publish EventGrabbed so user-configured notification webhooks fire for
	// auto-grabs as well as the queue-page manual grabs (issue #849). Payload
	// shape mirrors the queue.go manual-grab Send so existing webhook
	// templates keep working without modification; "author" is added because
	// we have it here and it costs nothing.
	//
	// TODO(#849): when an upgrade-grab code path exists (the quality cutoff
	// is currently used only to reject, not to trigger an upgrade re-grab),
	// emit EventUpgrade here instead of EventGrabbed for upgrade grabs.
	s.notify(ctx, notifierEventGrabbed, map[string]interface{}{
		"title":  best.Title,
		"size":   best.Size,
		"author": authorName,
	})
	// Scope the deletion to the format just grabbed. Deleting all pending
	// entries for the book would discard the other format's candidates for a
	// dual-format ('both') book, forcing an unnecessary re-search (see #707).
	if s.pending != nil {
		_ = s.pending.DeleteByBookAndMediaType(ctx, book.ID, mediaType)
	}
}

// storePending records a delay-rejected release in pending_releases.
// mediaType ("ebook" or "audiobook") is stored so that a successful grab of
// one format does not delete the other format's pending entries for a
// dual-format book (see #707).
func (s *Scheduler) storePending(ctx context.Context, bookID int64, mediaType string, res newznab.SearchResult, reason string) {
	blob, err := json.Marshal(res)
	if err != nil {
		return
	}
	pr := &models.PendingRelease{
		BookID:      bookID,
		MediaType:   mediaType,
		Title:       res.Title,
		GUID:        res.GUID,
		Protocol:    res.Protocol,
		Size:        res.Size,
		AgeMinutes:  decision.PubDateToAge(res.PubDate),
		Quality:     indexer.ParseRelease(res.Title).Format,
		Reason:      reason,
		ReleaseJSON: string(blob),
	}
	if res.IndexerID != 0 {
		id := res.IndexerID
		pr.IndexerID = &id
	}
	if err := s.pending.Upsert(ctx, pr); err != nil {
		slog.Warn("failed to store pending release", "guid", res.GUID, "error", err)
	}
}

// checkPendingReleases re-evaluates existing pending releases for a book and
// format. Only releases whose MediaType matches are considered, so a dual-format
// book's ebook and audiobook pending entries are evaluated independently.
// If any now passes the decision engine it is returned for immediate grab.
func (s *Scheduler) checkPendingReleases(ctx context.Context, book models.Book, mediaType string, dm *decision.DecisionMaker) *newznab.SearchResult {
	if s.pending == nil {
		return nil
	}
	pendingList, err := s.pending.ListByBookAndMediaType(ctx, book.ID, mediaType)
	if err != nil || len(pendingList) == 0 {
		return nil
	}

	// Re-hydrate stored releases and re-evaluate with current age.
	for i := range pendingList {
		var res newznab.SearchResult
		if err := json.Unmarshal([]byte(pendingList[i].ReleaseJSON), &res); err != nil {
			continue
		}
		// Age is recalculated from PubDate by ReleaseFromSearchResult.
		rel := decision.ReleaseFromSearchResult(res)
		decisions := dm.Evaluate([]decision.Release{rel}, book)
		if len(decisions) > 0 && decisions[0].Approved {
			_ = s.pending.DeleteByID(ctx, pendingList[i].ID)
			return &res
		}
	}
	return nil
}

func (s *Scheduler) searchWanted() {
	ctx := s.ctx()

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

	// Books with a download parked in StateImportExternal have been handed off
	// to an external import tool; the book is deliberately still Wanted so
	// ScanLibrary can reconcile the file once it lands, but the release must
	// NOT be re-grabbed in the meantime or the importer re-downloads the same
	// book every sweep (issue #706 finding 3).
	externalHandoffBooks := make(map[int64]bool)
	if s.downloads != nil {
		if pending, derr := s.downloads.ListByStatus(ctx, models.StateImportExternal); derr != nil {
			slog.Warn("failed to list external-handoff downloads", "error", derr)
		} else {
			for _, d := range pending {
				if d.BookID != nil {
					externalHandoffBooks[*d.BookID] = true
				}
			}
		}
	}

	searchQueue := make([]models.Book, 0, len(wanted))
	for _, book := range wanted {
		if book.Excluded {
			continue
		}
		if externalHandoffBooks[book.ID] {
			slog.Debug("skipping wanted search — external import hand-off outstanding", "book", book.Title)
			continue
		}
		searchQueue = append(searchQueue, book)
	}
	runBoundedBookTasks(ctx, searchQueue, scheduledWantedSearchConcurrency, func(ctx context.Context, book models.Book) {
		s.SearchAndGrabBook(ctx, book)
	})
}

func runBoundedBookTasks(ctx context.Context, books []models.Book, concurrency int, fn func(context.Context, models.Book)) {
	if fn == nil || len(books) == 0 {
		return
	}
	if concurrency <= 0 {
		concurrency = 1
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, book := range books {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		book := book
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(ctx, book)
		}()
	}
	wg.Wait()
}

func (s *Scheduler) refreshMetadata() {
	ctx := s.ctx()

	authors, err := s.authors.List(ctx)
	if err != nil {
		slog.Error("failed to list authors", "error", err)
		return
	}

	for _, author := range authors {
		if !author.Monitored {
			continue
		}

		// Calibre-imported authors have synthetic "calibre:author:N" IDs with
		// no counterpart in OL/Hardcover; skip to avoid noisy 404 errors.
		if strings.HasPrefix(author.ForeignID, "calibre:") {
			continue
		}

		updated, err := s.meta.GetAuthor(ctx, author.ForeignID)
		if err != nil {
			slog.Warn("failed to refresh author", "author", author.Name, "error", err)
			continue
		}
		// Aggregator returns (nil, nil) when no configured provider owns this
		// foreignID prefix (e.g. a Hardcover-prefixed author after Hardcover
		// was disabled, or any author whose ForeignID prefix no longer maps to
		// a provider). Without this guard the deref below panics, which exits
		// the range loop and kills every subsequent author's refresh that
		// cycle.
		if updated == nil {
			slog.Debug("no metadata provider for author, skipping refresh", "author", author.Name, "foreignID", author.ForeignID)
			continue
		}

		// Update changed fields
		author.Description = updated.Description
		if updated.ImageURL != "" {
			author.ImageURL = updated.ImageURL
		}
		author.AverageRating = updated.AverageRating
		author.RatingsCount = updated.RatingsCount
		if err := s.authors.Update(ctx, &author); err != nil {
			slog.Warn("failed to persist refreshed author", "author", author.Name, "error", err)
			continue
		}

		slog.Debug("refreshed author", "author", author.Name)
	}
}

// stallTimeoutDefault is the duration a download must be in the downloading
// state before it can be considered stalled. Configurable via the
// stall.timeout_minutes setting (default 120 minutes / 2 hours).
const stallTimeoutDefault = 120 * time.Minute

// checkStalledDownloads detects downloads that have been stuck in
// "downloading" state for longer than the stall timeout and handles them:
// the release is marked failed, added to the blocklist so it won't be
// re-grabbed, and a fresh search is triggered for the same book.
//
// Detection uses the download client's native stall signal where available
// (qBittorrent: stalledDL state; Transmission: stopped with error). For
// SABnzbd the existing Failed-state detection in CheckDownloads already
// covers failures, so this job adds nothing for usenet downloads.
func (s *Scheduler) checkStalledDownloads(ctx context.Context) {
	timeout := stallTimeoutDefault
	if s.settings != nil {
		if v, _ := s.settings.Get(ctx, "stall.timeout_minutes"); v != nil {
			if mins, err := strconv.Atoi(v.Value); err == nil && mins > 0 {
				timeout = time.Duration(mins) * time.Minute
			}
		}
	}

	active, err := s.downloads.ListByStatus(ctx, models.StateDownloading)
	if err != nil || len(active) == 0 {
		return
	}

	cutoff := time.Now().UTC().Add(-timeout)

	// Group downloads by client to avoid fetching stall status per-download.
	byClient := make(map[int64][]models.Download)
	for _, dl := range active {
		if dl.DownloadClientID == nil || dl.GrabbedAt == nil {
			continue
		}
		if dl.GrabbedAt.After(cutoff) {
			continue // not old enough yet
		}
		byClient[*dl.DownloadClientID] = append(byClient[*dl.DownloadClientID], dl)
	}

	for clientID, dls := range byClient {
		client, err := s.clients.GetByID(ctx, clientID)
		if err != nil || client == nil || !client.Enabled {
			continue
		}

		stalledIDs, _, err := downloader.GetStalledIDs(ctx, client)
		if err != nil {
			slog.Debug("stall check: failed to fetch stalled IDs", "client", client.Name, "error", err)
			continue
		}
		if len(stalledIDs) == 0 {
			continue
		}

		for _, dl := range dls {
			if dl.TorrentID == nil {
				continue
			}
			if !stalledIDs[strings.ToLower(*dl.TorrentID)] {
				continue
			}
			slog.Warn("stall detected",
				"title", dl.Title,
				"grabbed_at", dl.GrabbedAt,
				"client", client.Name,
			)
			s.handleStalledDownload(ctx, &dl)
		}
	}
}

// handleStalledDownload marks a download as failed, records history, adds the
// release to the blocklist, and triggers a fresh search for the same book.
func (s *Scheduler) handleStalledDownload(ctx context.Context, dl *models.Download) {
	reason := "stalled: no peers / no download progress"

	// Mark failed in DB.
	if err := s.downloads.SetError(ctx, dl.ID, reason); err != nil {
		slog.Warn("stall: failed to set error", "download_id", dl.ID, "error", err)
	}

	// Record history event.
	if s.history != nil {
		data, _ := json.Marshal(map[string]string{"guid": dl.GUID, "message": reason})
		_ = s.history.Create(ctx, &models.HistoryEvent{
			BookID:      dl.BookID,
			EventType:   models.HistoryEventDownloadStalled,
			SourceTitle: dl.Title,
			Data:        string(data),
		})
	}

	// Blocklist the release so the next search skips it.
	if s.blocklist != nil && dl.IndexerID != nil {
		entry := &models.BlocklistEntry{
			BookID:    dl.BookID,
			GUID:      dl.GUID,
			Title:     dl.Title,
			IndexerID: dl.IndexerID,
			Reason:    reason,
		}
		if err := s.blocklist.Create(ctx, entry); err != nil {
			slog.Warn("stall: failed to add to blocklist", "guid", dl.GUID, "error", err)
		}
	}

	// Trigger a fresh search if auto-grab is enabled.
	if dl.BookID == nil {
		return
	}
	if s.settings != nil {
		if v, _ := s.settings.Get(ctx, "autoGrab.enabled"); v != nil && v.Value == "false" {
			return
		}
	}
	book, err := s.books.GetByID(ctx, *dl.BookID)
	if err != nil || book == nil {
		return
	}
	slog.Info("stall: triggering re-search", "title", dl.Title, "book_id", *dl.BookID)

	if s.history != nil {
		data, _ := json.Marshal(map[string]string{"guid": dl.GUID, "message": "stalled release removed, re-searching"})
		_ = s.history.Create(ctx, &models.HistoryEvent{
			BookID:      dl.BookID,
			EventType:   models.HistoryEventDownloadRequeued,
			SourceTitle: dl.Title,
			Data:        string(data),
		})
	}

	// Track the goroutine so Stop() can drain it before tearing down
	// resources (see #707).
	s.bgWg.Add(1)
	go func() {
		defer s.bgWg.Done()
		s.SearchAndGrabBook(ctx, *book)
	}()
}
