package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	oidcauth "github.com/vavallee/bindery/internal/auth/oidc"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/hardcoverlistsyncer"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/logbuf"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/dnb"
	"github.com/vavallee/bindery/internal/metadata/googlebooks"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/metrics"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
	"github.com/vavallee/bindery/internal/opds"
	"github.com/vavallee/bindery/internal/prowlarr"
	"github.com/vavallee/bindery/internal/recommender"
	"github.com/vavallee/bindery/internal/scheduler"
	"github.com/vavallee/bindery/internal/telemetry"
	"github.com/vavallee/bindery/internal/useragent"
	"github.com/vavallee/bindery/internal/webui"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

const (
	settingGoogleBooksAPIKey       = "googlebooks.apiKey"
	legacySettingGoogleBooksAPIKey = "google_books_api_key"
)

func main() {
	// Healthcheck subcommand — used by the Docker HEALTHCHECK directive.
	// Hits the local /api/v1/health endpoint and exits 0 on 200, else 1.
	// Runs before config load so it works with a minimal environment.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		runHealthcheck()
		return
	}

	cfg := config.Load()

	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	// Ring buffer captures the last 1000 entries for the UI log viewer.
	// Tee sends every record to both stdout (JSON) and the ring.
	ring := logbuf.New(logbuf.DefaultCapacity)
	ring.SetLevel(level)
	stdoutHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(logbuf.NewTee(stdoutHandler, ring)))

	// Install the canonical User-Agent before any HTTP client constructs
	// requests. Every external client reads from this singleton.
	useragent.Set(version)

	slog.Info("starting bindery",
		"version", version,
		"commit", commit,
		"port", cfg.Port,
		"dbPath", cfg.DBPath,
		"dataDir", cfg.DataDir,
		"userAgent", useragent.Get(),
	)

	// Validate config before anything else touches the filesystem or network.
	// Warnings are logged inline; a non-nil return means a clearly broken
	// config (e.g. an unparseable OIDC redirect URL) and is treated as fatal.
	if err := cfg.Validate(slog.Default()); err != nil {
		slog.Error("configuration error — refusing to start", "error", err)
		os.Exit(1)
	}

	// Fail fast if BINDERY_PUID/PGID is set but the container isn't running
	// as that UID/GID. See cmd/bindery/uidcheck.go for the full rationale.
	checkPUIDPGID()

	// Database
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// Repos
	// Persistent log store — extend the slog pipeline to also write to SQLite.
	logRepo := db.NewLogRepo(database)
	logDBHandler := db.NewLogSlogHandler(logRepo, level)
	slog.SetDefault(slog.New(logbuf.NewTee(logbuf.NewTee(stdoutHandler, ring), logDBHandler)))
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := logDBHandler.Stop(shutdownCtx); err != nil {
			slog.Warn("log handler shutdown timed out", "error", err)
		}
	}()

	authorRepo := db.NewAuthorRepo(database)
	authorAliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	absImportRunRepo := db.NewABSImportRunRepo(database)
	absImportRunEntityRepo := db.NewABSImportRunEntityRepo(database)
	absProvenanceRepo := db.NewABSProvenanceRepo(database)
	absConflictRepo := db.NewABSMetadataConflictRepo(database)
	absReviewRepo := db.NewABSReviewItemRepo(database)
	calibreImportRunRepo := db.NewCalibreImportRunRepo(database)
	calibreSnapshotRepo := db.NewCalibreEntitySnapshotRepo(database)
	calibreProvenanceRepo := db.NewCalibreProvenanceRepo(database)
	indexerRepo := db.NewIndexerRepo(database)
	dlClientRepo := db.NewDownloadClientRepo(database)
	downloadRepo := db.NewDownloadRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	historyRepo := db.NewHistoryRepo(database)
	blocklistRepo := db.NewBlocklistRepo(database)
	pendingReleaseRepo := db.NewPendingReleaseRepo(database)
	notificationRepo := db.NewNotificationRepo(database)
	qualityProfileRepo := db.NewQualityProfileRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	rootFolderRepo := db.NewRootFolderRepo(database)
	importListRepo := db.NewImportListRepo(database)
	prowlarrRepo := db.NewProwlarrRepo(database)
	metadataProfileRepo := db.NewMetadataProfileRepo(database)
	delayProfileRepo := db.NewDelayProfileRepo(database)
	customFormatRepo := db.NewCustomFormatRepo(database)
	userRepo := db.NewUserRepo(database)

	// Auth bootstrap: seed the API key and session secret on first boot if
	// they're missing, so Bindery is never "open by default". If the user has
	// explicitly set BINDERY_API_KEY in the env (legacy path), we honour it as
	// the seed value so existing integrations keep working after upgrade.
	ctxBoot := context.Background()
	if err := bootstrapAuth(ctxBoot, settingsRepo, cfg.APIKey); err != nil {
		slog.Error("auth bootstrap failed", "error", err)
		os.Exit(1)
	}

	// Process-lifecycle context. Unlike a per-request context this is not tied
	// to any single HTTP request; it is cancelled when the process receives
	// SIGINT/SIGTERM (graceful shutdown). Long-lived background goroutines —
	// the scheduler's cron jobs, the recommendations refresh, and the bulk
	// search fan-out — derive from this so they observe shutdown instead of
	// running against a context that never cancels. See #550 and #846.
	//
	// Declared before any scheduler closure that needs to capture it (the
	// Calibre mode resolver below) and threaded into NewBulkHandler/
	// NewAuthorHandler/NewCalibreHandler/NewRecommendationHandler via the
	// WithLifetimeCtx / WithAppContext builders.
	appCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Parse trusted-proxy CIDRs once at startup (shared by trustedProxyMiddleware
	// and the proxy-auth identity check).
	trustedCIDRs := parseTrustedProxyCIDRs(os.Getenv("BINDERY_TRUSTED_PROXY"))

	// Warn loudly when the trusted-proxy allowlist effectively trusts every
	// possible peer (0.0.0.0/0 or ::/0). In that shape every client's
	// X-Forwarded-For header is honoured, defeating the login rate-limiter
	// (which keys off the post-RealIP `RemoteAddr`) and any other per-IP
	// decision in the stack. Operators sometimes do this in Helm charts to
	// silence the proxy-mode safety gate without thinking through the
	// implication; surfacing it at boot makes the misconfiguration obvious.
	for _, c := range trustedCIDRs {
		ones, bits := c.Mask.Size()
		if ones == 0 && bits > 0 {
			slog.Warn("BINDERY_TRUSTED_PROXY entry trusts every peer — login rate-limiter and per-IP decisions are effectively disabled",
				"cidr", c.String())
		}
	}

	// Safety gate: proxy auth mode requires at least one trusted proxy CIDR so
	// that the identity header cannot be forged by arbitrary LAN hosts.
	if s, _ := settingsRepo.Get(ctxBoot, api.SettingAuthMode); s != nil && s.Value == string(auth.ModeProxy) {
		if len(trustedCIDRs) == 0 {
			slog.Error("proxy auth mode is active but BINDERY_TRUSTED_PROXY is empty — refusing to start (any host could forge the identity header)")
			os.Exit(1)
		}
		slog.Info("proxy auth mode: trusted proxies", "cidrs", trustedCIDRs)
	}

	// Login rate limiter: thresholds are configurable via BINDERY_RATE_LIMIT_MAX_FAILURES
	// and BINDERY_RATE_LIMIT_WINDOW_MINUTES; defaults match the original Sonarr-style posture.
	loginLimiter := auth.NewLoginLimiter(cfg.RateLimitMaxFailures, time.Duration(cfg.RateLimitWindowMinutes)*time.Minute)

	// Metadata providers
	olClient := openlibrary.New()
	dnbClient := dnb.New()

	// Determine primary provider from settings (default: openlibrary).
	// When metadata.primary_provider = "dnb", DNB is promoted to primary and
	// OpenLibrary is added as an enricher instead. This is the recommended
	// choice for German/Austrian/Swiss catalogues where OpenLibrary coverage
	// is too thin for German-language books.
	var primaryProvider metadata.Provider = olClient
	if s, _ := settingsRepo.Get(context.Background(), api.SettingMetadataPrimaryProvider); s != nil && s.Value == "dnb" {
		primaryProvider = dnbClient
		slog.Info("metadata primary provider: dnb")
	} else {
		slog.Info("metadata primary provider: openlibrary")
	}

	var enrichers []metadata.Provider
	if apiKey := googleBooksAPIKey(context.Background(), settingsRepo); apiKey != "" {
		enrichers = append(enrichers, googlebooks.New(apiKey))
		slog.Info("google books enrichment enabled")
	}
	hcClient := hardcover.New().WithTokenSource(func(ctx context.Context) string {
		return api.GetHardcoverAPIToken(ctx, settingsRepo)
	})
	enrichers = append(enrichers, hcClient)
	slog.Info("hardcover enrichment enabled")

	// Add the non-primary provider as enricher so metadata is always
	// cross-checked regardless of which provider is primary.
	if primaryProvider == olClient {
		enrichers = append(enrichers, dnbClient)
		slog.Info("dnb enrichment enabled")
	} else {
		enrichers = append(enrichers, olClient)
		slog.Info("openlibrary enrichment enabled")
	}

	metaAgg := metadata.NewAggregator(primaryProvider, enrichers...)

	// Optional CLI subcommand: `bindery migrate {csv,readarr} <path>`.
	// Runs the import and exits; does not start the HTTP server. Useful
	// for bulk first-time imports from a shell.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrate(cfg, authorRepo, indexerRepo, dlClientRepo, blocklistRepo, metaAgg)
		return
	}

	// Optional CLI subcommand: `bindery reconcile-series`.
	// Re-fetches OpenLibrary series data for every already-ingested book and
	// populates the series / series_books tables. Run once when upgrading from
	// a version that did not populate series during ingestion.
	if len(os.Args) > 1 && os.Args[1] == "reconcile-series" {
		runReconcileSeries(authorRepo, bookRepo, seriesRepo, metaAgg)
		return
	}

	// Notifier — constructed early so it can be passed into the import scanner,
	// scheduler, and download-client health store before they start firing
	// events. Before issue #849 was fixed, this was constructed only after
	// scheduler.Start() and only injected into QueueHandler, which is why
	// auto-grab / import / download-failure events never reached webhooks.
	notif := notifier.New(notificationRepo)

	// Indexer searcher
	idxSearcher := indexer.NewSearcher()

	// Import scanner
	namingTemplate := defaultNamingTemplate(settingsRepo)
	audiobookTemplate := audiobookNamingTemplate(settingsRepo)
	importScanner := importer.NewScanner(
		downloadRepo, dlClientRepo, bookRepo, authorRepo, historyRepo,
		cfg.LibraryDir, cfg.AudiobookDir, namingTemplate, audiobookTemplate,
		cfg.DownloadPathRemap,
	)
	importScanner.WithNotifier(notif)
	if cfg.AudiobookDownloadDir != "" {
		importScanner.WithAudiobookDownloadDir(cfg.AudiobookDownloadDir)
		slog.Info("audiobook download dir configured", "path", cfg.AudiobookDownloadDir)
	}

	// Wire ABS post-import scan notification (Bug #10). The notifier reads ABS
	// config from settings at call time so that config changes (URL, API key,
	// library IDs) take effect without restarting Bindery.
	importScanner.WithABSNotifier(
		abs.NewScanNotifier(settingsRepo),
		func() []string {
			cfg := api.LoadABSConfig(context.Background(), settingsRepo)
			if !cfg.Enabled {
				return nil
			}
			return cfg.LibraryIDs
		},
	)

	// The mode resolver is called from the importer on every scan; it must
	// observe shutdown when the scheduler is mid-tick, so it captures appCtx
	// (declared below) by closure rather than running on context.Background().
	// The boot-time reads on the next two lines use ctxBoot because appCtx
	// isn't constructed yet.
	modeResolver := func() calibre.Mode { return api.LoadCalibreMode(appCtx, settingsRepo) }
	calibreCfg := api.LoadCalibreConfig(ctxBoot, settingsRepo)
	currentMode := api.LoadCalibreMode(ctxBoot, settingsRepo)
	if currentMode == calibre.ModePlugin {
		pluginClient := calibre.NewPluginClient(calibreCfg.PluginURL, calibreCfg.PluginAPIKey)
		importScanner.WithCalibre(modeResolver, pluginClient)
		slog.Info("calibre integration enabled", "mode", "plugin", "url", calibreCfg.PluginURL)
	} else {
		calibreClient := calibre.New(calibreCfg)
		importScanner.WithCalibre(modeResolver, calibreClient)
		if currentMode == calibre.ModeCalibredb {
			slog.Info("calibre integration enabled", "mode", "calibredb")
		}
	}

	// Library import (read side). Importer holds live progress state in
	// memory so the UI can poll /calibre/import/status while a long scan
	// runs. A single instance is shared between the API handler and the
	// startup-sync branch below — both paths share the "only one import
	// at a time" guard.
	calibreImporter := calibre.NewImporter(authorRepo, authorAliasRepo, bookRepo, editionRepo, settingsRepo).
		WithRunTracking(calibreImportRunRepo, calibreSnapshotRepo, calibreProvenanceRepo).
		WithSeries(seriesRepo)
	absImporter := abs.NewImporter(authorRepo, authorAliasRepo, bookRepo, editionRepo, seriesRepo, settingsRepo, absImportRunRepo, absImportRunEntityRepo, absProvenanceRepo, absReviewRepo, absConflictRepo).
		WithVersion(version).
		WithStoragePaths(cfg.LibraryDir, cfg.AudiobookDir, rootFolderRepo).
		WithMetadata(metaAgg).
		WithEnhancedHardcoverSeriesEnabled(func(ctx context.Context) bool {
			return api.HardcoverFeatureStateFor(ctx, settingsRepo, cfg.EnhancedHardcoverAPI).EnhancedHardcoverAPI
		})
	storedABS := api.LoadABSConfig(ctxBoot, settingsRepo)
	resumeCfg := abs.ImportConfig{
		SourceID:   abs.DefaultSourceID,
		BaseURL:    storedABS.BaseURL,
		APIKey:     storedABS.APIKey,
		LibraryID:  storedABS.LibraryID,
		LibraryIDs: storedABS.LibraryIDs,
		PathRemap:  storedABS.PathRemap,
		Label:      storedABS.Label,
		Enabled:    storedABS.Enabled,
	}
	if resumed, err := absImporter.ResumeInterrupted(ctxBoot, resumeCfg); err != nil {
		slog.Warn("abs interrupted import resume skipped", "error", err)
	} else if resumed {
		slog.Info("abs interrupted import resumed from checkpoint")
	}
	if syncOnStartup(settingsRepo) {
		cfg := api.LoadCalibreConfig(ctxBoot, settingsRepo)
		if cfg.Enabled && cfg.LibraryPath != "" {
			slog.Info("calibre sync_on_startup enabled — kicking off library import")
			go func() {
				if _, err := calibreImporter.Run(appCtx, cfg.LibraryPath); err != nil {
					slog.Warn("calibre startup import failed", "error", err)
				}
			}()
		} else {
			slog.Info("calibre sync_on_startup is on but integration is not configured — skipping")
		}
	}

	// Prowlarr startup sync: kick off sync for all enabled instances that have
	// sync_on_startup set. Runs concurrently so it doesn't block server start.
	{
		prowlarrTimeout := api.LoadProwlarrTimeout(context.Background(), settingsRepo)
		instances, _ := prowlarrRepo.List(context.Background())
		for _, inst := range instances {
			if !inst.Enabled || !inst.SyncOnStartup {
				continue
			}
			inst := inst // capture
			go func() {
				client := prowlarr.NewWithTimeout(inst.URL, inst.APIKey, prowlarrTimeout)
				syncer := prowlarr.NewSyncer(client, indexerRepo, prowlarrRepo)
				if _, err := syncer.Sync(context.Background(), inst.ID); err != nil {
					slog.Warn("prowlarr startup sync failed", "instance", inst.Name, "error", err)
				}
			}()
		}
	}

	// Scheduler
	sched := scheduler.New(appCtx, importScanner, idxSearcher, metaAgg,
		authorRepo, bookRepo, indexerRepo, downloadRepo, dlClientRepo, settingsRepo, blocklistRepo)
	sched.WithHistory(historyRepo)
	sched.WithAliases(authorAliasRepo)
	sched.WithDelayProfiles(delayProfileRepo)
	sched.WithPendingReleases(pendingReleaseRepo)
	sched.WithStoragePaths(cfg.DownloadDir, cfg.AudiobookDownloadDir)
	sched.WithNotifier(notif)
	// Register the Calibre importer as the 24-hour sync job. The scheduler
	// only fires the job when the syncer is non-nil, so no guard needed here.
	sched.WithCalibreSyncer(calibreImporter)

	// Recommendation engine (24-hour job, gated on recommendations.enabled).
	recRepo := db.NewRecommendationRepo(database)
	recEngine := recommender.New(bookRepo, authorRepo, seriesRepo, recRepo, settingsRepo)
	recEngine.WithOLClient(olClient)
	recEngine.WithHCClient(hcClient)
	slog.Info("hardcover wishlist integration enabled for recommendations when a token is configured")
	sched.WithRecommender(recEngine)

	// Register the Hardcover list syncer (24-hour job).
	hcSyncer := hardcoverlistsyncer.New(importListRepo, authorRepo, bookRepo).
		WithSeriesRepo(seriesRepo).
		WithTokenSource(func(ctx context.Context) string {
			return api.GetHardcoverAPIToken(ctx, settingsRepo)
		}).
		WithEditionHydration(editionRepo, metaAgg)
	sched.WithHardcoverSyncer(hcSyncer)
	sched.WithLogRepo(logRepo, cfg.LogRetentionDays)

	// Anonymous install ping (opt-out via telemetry.enabled=false in settings).
	// The gatherer captures repo handles by closure and runs at ping time. It
	// must not block on network IO; all queries are local SQLite reads.
	telemetryClient := telemetry.New(settingsRepo, version).
		WithGatherer(buildTelemetryGatherer(indexerRepo, dlClientRepo, notificationRepo, userRepo, settingsRepo))
	sched.WithTelemetry(telemetryClient)

	// Recover downloads wedged mid-import by a prior crash or timeout before the
	// scheduler starts polling. StateImporting / StateImportPending are
	// non-terminal with no automatic re-entry; this sweeps them to
	// StateImportFailed so the scanner's retry path picks them up
	// (issue #706 finding 1).
	importScanner.RecoverInterruptedImports(ctxBoot)

	sched.Start()
	defer sched.Stop()

	// OIDC manager — loaded from settings, reload on config change. The
	// redirect base URL is resolved per-request from the Login/Callback
	// handlers, falling back through (1) BINDERY_OIDC_REDIRECT_BASE_URL,
	// (2) X-Forwarded-* from a trusted proxy, (3) the request Host.
	oidcMgr := oidcauth.NewManager()
	if s, _ := settingsRepo.Get(ctxBoot, api.SettingOIDCProviders); s != nil && s.Value != "" {
		if ps, err := oidcauth.ParseProviders(s.Value); err == nil && len(ps) > 0 {
			oidcMgr.Reload(ctxBoot, ps)
			slog.Info("oidc: loaded providers from settings", "count", len(ps))
			if cfg.OIDCRedirectBaseURL == "" && len(trustedCIDRs) == 0 {
				slog.Warn("oidc: BINDERY_OIDC_REDIRECT_BASE_URL is unset and no trusted-proxy CIDRs are configured — callback URLs will be derived from the request Host header, which is the internal hostname behind a proxy. Set BINDERY_OIDC_REDIRECT_BASE_URL or BINDERY_TRUSTED_PROXY to fix.")
			}
		}
	}

	// API handlers
	authHandler := api.NewAuthHandler(userRepo, settingsRepo, loginLimiter).
		WithLocalAuthEnabled(cfg.LocalAuthEnabled)
	oidcResolveBase := func(r *http.Request) string {
		return api.ResolveOIDCRedirectBase(r, cfg.OIDCRedirectBaseURL, trustedCIDRs)
	}
	oidcHandler := api.NewOIDCHandler(oidcMgr, userRepo, settingsRepo, authHandler, oidcResolveBase).
		WithBaseConfigured(cfg.OIDCRedirectBaseURL != "").
		WithOIDCAutoProvision(cfg.OIDCAutoProvision).
		WithOIDCEmailLink(cfg.OIDCEmailLink).
		WithLocalAuthEnabled(cfg.LocalAuthEnabled).
		WithOIDCDefaultRole(cfg.OIDCDefaultRole).
		WithOIDCAdminGroup(cfg.OIDCAdminGroup).
		WithOIDCGroupClaim(cfg.OIDCGroupClaim).
		WithLifetimeCtx(appCtx)
	userMgmtHandler := api.NewUserManagementHandler(userRepo).
		WithLocalAuthEnabled(cfg.LocalAuthEnabled)
	searchHandler := api.NewSearchHandler(metaAgg)
	// Library-root containment checker (Wave 1 / Bundle B): used by the book
	// and author delete handlers to refuse on-disk removal of any path that
	// isn't inside a configured root. Defaults to the legacy single-root env
	// vars so installs that never created a root_folders row still get the
	// check.
	libraryRoots := api.NewLibraryRoots(rootFolderRepo, cfg.LibraryDir, cfg.AudiobookDir)
	authorHandler := api.NewAuthorHandler(authorRepo, authorAliasRepo, bookRepo, seriesRepo, metaAgg, settingsRepo, metadataProfileRepo, sched).
		WithFinder(importScanner).
		WithHardcoverFeatureSettings(settingsRepo, cfg.EnhancedHardcoverAPI).
		WithEditionHydration(editionRepo).
		WithRoots(libraryRoots).
		WithLifetimeCtx(appCtx)
	authorAliasHandler := api.NewAuthorAliasHandler(authorRepo, authorAliasRepo)
	bookHandler := api.NewBookHandler(bookRepo, metaAgg, historyRepo, sched).
		WithSettings(settingsRepo).
		WithDownloads(downloadRepo).
		WithAuthors(authorRepo).
		WithSeries(seriesRepo).
		WithEditionHydration(editionRepo).
		WithRoots(libraryRoots).
		WithLifetimeCtx(appCtx)
	indexerHandler := api.NewIndexerHandler(indexerRepo, bookRepo, authorRepo, metadataProfileRepo, idxSearcher, settingsRepo, blocklistRepo).WithAliases(authorAliasRepo)
	downloadHealth := downloader.NewHealthStore().WithNotifier(notif)
	if clients, err := dlClientRepo.List(ctxBoot); err == nil {
		downloader.RefreshDownloadClientHealthAsync(context.Background(), downloadHealth, clients, cfg.DownloadDir, cfg.AudiobookDownloadDir)
	} else {
		slog.Warn("download client startup health check skipped", "error", err)
	}
	dlClientHandler := api.NewDownloadClientHandler(dlClientRepo).
		WithHealth(downloadHealth).
		WithStoragePaths(cfg.DownloadDir, cfg.AudiobookDownloadDir).
		WithLifetimeCtx(appCtx)
	queueHandler := api.NewQueueHandler(downloadRepo, dlClientRepo, bookRepo, historyRepo).
		WithNotifier(notif).
		WithStoragePaths(cfg.DownloadDir, cfg.AudiobookDownloadDir)
	pendingHandler := api.NewPendingHandler(pendingReleaseRepo, queueHandler, downloadRepo, bookRepo)
	importScanner.WithSettings(settingsRepo)
	importScanner.WithRootFolders(rootFolderRepo)
	importScanner.WithSeriesRepo(seriesRepo)
	importScanner.WithEditions(editionRepo)
	importScanner.WithCalibreCoverCache(filepath.Join(cfg.DataDir, "calibre-covers"))

	// Startup check: warn if the configured default root folder no longer exists on disk.
	if s, _ := settingsRepo.Get(ctxBoot, api.SettingDefaultLibraryRootFolderID); s != nil && s.Value != "" {
		if id, err := strconv.ParseInt(s.Value, 10, 64); err == nil && id > 0 {
			if rf, err := rootFolderRepo.GetByID(ctxBoot, id); err == nil && rf != nil {
				if _, statErr := os.Stat(rf.Path); statErr != nil {
					slog.Warn("default library root folder does not exist on disk — falling back to BINDERY_LIBRARY_DIR",
						"path", rf.Path, "rootFolderId", id, "error", statErr)
				}
			} else {
				slog.Warn("default library root folder ID not found in database — falling back to BINDERY_LIBRARY_DIR",
					"rootFolderId", id)
			}
		}
	}

	libraryHandler := api.NewLibraryHandler(importScanner).WithSettings(settingsRepo)
	fileHandler := api.NewFileHandler(bookRepo, cfg.LibraryDir, cfg.AudiobookDir)
	historyHandler := api.NewHistoryHandler(historyRepo, blocklistRepo)
	blocklistHandler := api.NewBlocklistHandler(blocklistRepo)
	notificationHandler := api.NewNotificationHandler(notificationRepo, notif)
	qualityProfileHandler := api.NewQualityProfileHandler(qualityProfileRepo)
	settingsHandler := api.NewSettingsHandler(settingsRepo)
	seriesHandler := api.NewSeriesHandler(seriesRepo, bookRepo, authorRepo, metaAgg, sched).
		WithHardcoverFeatureSettings(settingsRepo, cfg.EnhancedHardcoverAPI).
		WithFinder(importScanner).
		WithEditionHydration(editionRepo).
		WithLifetimeCtx(appCtx)
	importListHandler := api.NewImportListHandler(importListRepo, settingsRepo, hcSyncer)
	metadataProfileHandler := api.NewMetadataProfileHandler(metadataProfileRepo)
	delayProfileHandler := api.NewDelayProfileHandler(delayProfileRepo)
	customFormatHandler := api.NewCustomFormatHandler(customFormatRepo)
	bulkHandler := api.NewBulkHandler(authorRepo, bookRepo, blocklistRepo, sched).
		WithLifetimeCtx(appCtx).
		// Bulk "refresh" reuses the per-author catalogue fetch (metadata only,
		// never auto-grabs). Resolve the default media type per call so newly
		// discovered books inherit the global default, matching AuthorHandler.Refresh.
		WithRefreshFunc(func(a *models.Author) {
			authorHandler.FetchAuthorBooks(a, false, authorHandler.ResolveDefaultMediaType(appCtx))
		})
	backupHandler := api.NewBackupHandler(database, cfg.DBPath, cfg.DataDir)
	rootFolderHandler := api.NewRootFolderHandler(rootFolderRepo)
	logHandler := api.NewLogHandler(ring).WithLogRepo(logRepo).WithDBLogHandler(logDBHandler)
	prowlarrHandler := api.NewProwlarrHandler(prowlarrRepo, indexerRepo).WithSettings(settingsRepo)
	calibreHandler := api.NewCalibreHandler(settingsRepo).
		WithLifetimeCtx(appCtx)
	grimmoryHandler := api.NewGrimmoryHandler(settingsRepo).WithVersion(version)
	absHandler := api.NewABSHandler(settingsRepo).WithVersion(version)
	absConflictHandler := api.NewABSConflictHandler(absConflictRepo, authorRepo, bookRepo)
	absImportHandler := api.NewABSImportHandler(absImporter, func(ctx context.Context) api.ABSStoredConfig {
		return api.LoadABSConfig(ctx, settingsRepo)
	})
	absReviewHandler := api.NewABSReviewHandler(absReviewRepo, absImportRunRepo, absImporter, func(ctx context.Context) api.ABSStoredConfig {
		return api.LoadABSConfig(ctx, settingsRepo)
	})
	calibreImportHandler := api.NewCalibreImportHandler(calibreImporter, func() calibre.Config {
		return api.LoadCalibreConfig(appCtx, settingsRepo)
	})
	calibreRunsHandler := api.NewCalibreRunsHandler(calibreImporter)
	calibreSyncer := calibre.NewSyncer(bookRepo).WithMetadata(authorRepo, editionRepo)
	calibreSyncHandler := api.NewCalibreSyncHandler(
		calibreSyncer,
		func() calibre.Config { return api.LoadCalibreConfig(appCtx, settingsRepo) },
		func() calibre.Mode { return api.LoadCalibreMode(appCtx, settingsRepo) },
	)
	recHandler := api.NewRecommendationHandler(recRepo, recEngine, authorRepo, bookRepo, sched).
		WithFinder(seriesRepo, importScanner).
		WithEditionHydration(editionRepo, metaAgg).
		WithAppContext(appCtx)
	imageProxyHandler := api.NewImageProxyHandler(cfg.DataDir)
	imageProxyHandler.StartEviction(24 * time.Hour)
	// Proxied cover URLs must carry the path prefix so they resolve under a
	// subpath deploy (BINDERY_URL_BASE). No-op when URLBase is empty.
	api.SetImageProxyBase(cfg.URLBase)
	migrateHandler := api.NewMigrateHandler(
		authorRepo, indexerRepo, dlClientRepo, blocklistRepo, bookRepo, metaAgg,
		// Bulk imports always populate the catalogue but never auto-grab.
		// Pass an empty media type so the handler falls back to the global
		// default.media_type setting for each newly-created book.
		func(a *models.Author) { authorHandler.FetchAuthorBooks(a, false, "") },
	)

	// Router
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// BINDERY_TRUSTED_PROXY (optional): comma-separated IP/CIDR list of
	// reverse proxies permitted to set X-Forwarded-For. When unset, XFF
	// headers are ignored — required for local-only auth mode to be safe
	// against on-network spoofing.
	r.Use(trustedProxyMiddleware())
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(api.SecurityHeaders)
	// Cap JSON / form request bodies at 1 MiB by default so an authenticated
	// client cannot pin the process by streaming a multi-gigabyte body into
	// json.Decode. Routes that legitimately accept larger payloads opt in via
	// api.WithMaxBody on the chi sub-route (see /migrate/* below).
	// PreserveRawBody must run first so the per-route override can re-wrap
	// the raw body with a higher cap; without it the override would chain a
	// larger MaxBytesReader on top of the smaller default and the inner cap
	// would silently win.
	r.Use(api.PreserveRawBody)
	r.Use(api.MaxRequestBody)
	r.Use(metrics.HTTPMiddleware(routeTemplate))

	// Prometheus exposition. Mounted at the router root (no auth, no
	// CSRF, no XRequestedWith) because Prometheus scrapes don't carry
	// session cookies — operators are expected to restrict access via
	// NetworkPolicy / firewall / reverse-proxy ACL. Bindery's API key
	// is intentionally not honored here either; adding auth would
	// require every scrape config to also carry the key, which is a
	// worse default for the typical Helm-chart deployment.
	metrics.SetBuildInfo(version, commit, date)
	r.Handle("/metrics", metrics.Handler())

	// Composite auth: session cookie (UI) OR API key (external apps) OR
	// local-IP bypass when mode=local-only. Mode, key, and secret are sourced
	// live from the DB so they can be rotated without a process restart.
	authProvider := &dbAuthProvider{
		settings:       settingsRepo,
		users:          userRepo,
		proxyHeader:    cfg.ProxyAuthHeader,
		proxyProvision: cfg.ProxyAutoProvision,
		proxyCIDRs:     trustedCIDRs,
	}

	r.Route("/api", func(r chi.Router) {
		r.Use(auth.Middleware(authProvider))
		r.Use(auth.RequireXRequestedWith)
		r.Use(auth.RequireCSRFToken(authProvider.SessionSecrets))

		r.Get("/queue", queueHandler.ListArrCompatible)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(auth.Middleware(authProvider))
		r.Use(auth.RequireXRequestedWith)
		r.Use(auth.RequireCSRFToken(authProvider.SessionSecrets))

		// System
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","version":"` + version + `"}`))
		})
		r.Get("/system/status", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			cacheBytes, _ := imageProxyHandler.CacheSize()
			hardcoverState := api.HardcoverFeatureStateFor(r.Context(), settingsRepo, cfg.EnhancedHardcoverAPI)
			_ = json.NewEncoder(w).Encode(struct {
				Version                         string `json:"version"`
				Commit                          string `json:"commit"`
				BuildDate                       string `json:"buildDate"`
				ImageCacheBytes                 int64  `json:"imageCacheBytes"`
				EnhancedHardcoverAPI            bool   `json:"enhancedHardcoverApi"`
				HardcoverTokenConfigured        bool   `json:"hardcoverTokenConfigured"`
				EnhancedHardcoverDisabledReason string `json:"enhancedHardcoverDisabledReason,omitempty"`
			}{
				Version:                         version,
				Commit:                          commit,
				BuildDate:                       date,
				ImageCacheBytes:                 cacheBytes,
				EnhancedHardcoverAPI:            hardcoverState.EnhancedHardcoverAPI,
				HardcoverTokenConfigured:        hardcoverState.HardcoverTokenConfigured,
				EnhancedHardcoverDisabledReason: hardcoverState.EnhancedHardcoverDisabledReason,
			})
		})

		// Auth — status/login/logout/setup are always allowed through the
		// middleware (see auth.AllowUnauthPath). The config + mutation
		// endpoints below sit behind it.
		r.Get("/auth/status", authHandler.Status)
		r.Get("/auth/csrf", authHandler.CSRF)
		r.Post("/auth/login", authHandler.Login)
		r.Post("/auth/logout", authHandler.Logout)
		r.Post("/auth/setup", authHandler.Setup)
		r.Get("/auth/config", authHandler.GetConfig)
		r.Post("/auth/password", authHandler.ChangePassword)
		// OIDC — login/callback are unauthenticated; provider management requires auth.
		r.Get("/auth/oidc/providers", oidcHandler.GetProviders)
		r.Get("/auth/oidc/redirect-base", oidcHandler.GetRedirectBase)
		r.Post("/auth/oidc/test-discovery", oidcHandler.TestDiscovery)
		r.Get("/auth/oidc/{provider}/login", oidcHandler.Login)
		r.Get("/auth/oidc/{provider}/callback", oidcHandler.Callback)
		// Admin-only auth mutations.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Post("/auth/apikey/regenerate", authHandler.RegenerateAPIKey)
			r.Post("/auth/session-secret/rotate", authHandler.RotateSessionSecret)
			r.Put("/auth/oidc/providers", oidcHandler.SetProviders)
			r.Put("/auth/mode", authHandler.SetMode)
			r.Get("/auth/users", userMgmtHandler.List)
			r.Post("/auth/users", userMgmtHandler.Create)
			r.Delete("/auth/users/{id}", userMgmtHandler.Delete)
			r.Put("/auth/users/{id}/role", userMgmtHandler.SetRole)
			r.Put("/auth/users/{id}/reset-password", userMgmtHandler.ResetPassword)
		})

		// Metadata search
		r.Get("/search/author", searchHandler.SearchAuthors)
		r.Get("/search/book", searchHandler.SearchBooks)
		r.Get("/book/lookup", searchHandler.LookupByISBN)

		// Authors
		r.Get("/author", authorHandler.List)
		r.Post("/author", authorHandler.Create)
		r.Post("/author/book", authorHandler.AddBook)
		r.Post("/author/bulk", bulkHandler.AuthorsBulk)
		r.Get("/author/{id}", authorHandler.Get)
		r.Put("/author/{id}", authorHandler.Update)
		r.Delete("/author/{id}", authorHandler.Delete)
		r.Post("/author/{id}/refresh", authorHandler.Refresh)
		r.Post("/author/{id}/relink-upstream", authorHandler.RelinkUpstream)
		r.Get("/author/{id}/series", authorHandler.ListSeries)
		r.Get("/author/{id}/aliases", authorAliasHandler.List)
		r.Delete("/author/{id}/aliases/{aliasID}", authorAliasHandler.Delete)
		r.Post("/author/{id}/merge", authorAliasHandler.Merge)

		// Books
		r.Get("/book", bookHandler.List)
		r.Post("/book/bulk", bulkHandler.BooksBulk)
		r.Get("/book/{id}", bookHandler.Get)
		r.Put("/book/{id}", bookHandler.Update)
		r.Delete("/book/{id}", bookHandler.Delete)
		r.Delete("/book/{id}/file", bookHandler.DeleteFile)
		r.Put("/book/{id}/exclude", bookHandler.ToggleExcluded)
		r.Post("/book/{id}/rebind", bookHandler.Rebind)
		r.Post("/book/{id}/map", bookHandler.MapMetadata)
		r.Post("/book/{id}/enrich-audiobook", bookHandler.EnrichAudiobook)
		r.Post("/book/{id}/search", indexerHandler.SearchBook)
		r.Get("/book/{id}/file", fileHandler.Download)

		// Wanted
		r.Get("/wanted/missing", bookHandler.ListWanted)
		r.Post("/wanted/bulk", bulkHandler.WantedBulk)

		// Indexers, Prowlarr, and Download clients all return responses that
		// embed third-party credentials (APIKey, Password). Their routes are
		// registered via dedicated helpers so the admin-gate boundary is
		// enforced by a single, testable shape — see cmd/bindery/sensitive_routes.go.
		registerIndexerRoutes(r, indexerHandler)
		registerProwlarrRoutes(r, prowlarrHandler)

		// Root folders
		r.Get("/rootfolder", rootFolderHandler.List)
		r.Post("/rootfolder", rootFolderHandler.Create)
		r.Delete("/rootfolder/{id}", rootFolderHandler.Delete)

		registerDownloadClientRoutes(r, dlClientHandler)

		// Queue
		r.Get("/queue", queueHandler.List)
		r.Post("/queue/grab", queueHandler.Grab)
		r.Post("/queue/{id}/retry-import", queueHandler.RetryImport)
		r.Delete("/queue/{id}", queueHandler.Delete)
		r.Get("/pending", pendingHandler.List)
		r.Delete("/pending/{id}", pendingHandler.Delete)
		r.Post("/pending/{id}/grab", pendingHandler.Grab)

		// History
		r.Get("/history", historyHandler.List)
		r.Delete("/history/{id}", historyHandler.Delete)
		r.Post("/history/{id}/blocklist", historyHandler.Blocklist)

		// Blocklist
		r.Get("/blocklist", blocklistHandler.List)
		r.Delete("/blocklist/bulk", blocklistHandler.BulkDelete)
		r.Delete("/blocklist/{id}", blocklistHandler.Delete)

		// Notifications — Notification.Headers carries arbitrary HTTP
		// headers (often auth tokens for ntfy / Gotify / webhook routing).
		// Admin-only across the whole surface so non-admin users can't read
		// those credentials via List / Get.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Get("/notification", notificationHandler.List)
			r.Post("/notification", notificationHandler.Create)
			r.Get("/notification/{id}", notificationHandler.Get)
			r.Put("/notification/{id}", notificationHandler.Update)
			r.Delete("/notification/{id}", notificationHandler.Delete)
			r.Post("/notification/{id}/test", notificationHandler.Test)
		})

		// Quality Profiles — reads available to all; mutations admin-only.
		r.Get("/qualityprofile", qualityProfileHandler.List)
		r.Get("/qualityprofile/{id}", qualityProfileHandler.Get)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Post("/qualityprofile", qualityProfileHandler.Create)
			r.Put("/qualityprofile/{id}", qualityProfileHandler.Update)
			r.Delete("/qualityprofile/{id}", qualityProfileHandler.Delete)
		})

		// Settings — reads available to all; mutations admin-only.
		r.Get("/setting", settingsHandler.List)
		r.Get("/setting/{key}", settingsHandler.Get)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Put("/setting/{key}", settingsHandler.Set)
			r.Delete("/setting/{key}", settingsHandler.Delete)
			r.Post("/hardcover/test", settingsHandler.TestHardcover)
			r.Get("/abs/config", absHandler.GetConfig)
			r.Put("/abs/config", absHandler.SetConfig)
			r.Post("/abs/test", absHandler.Test)
			r.Post("/abs/libraries", absHandler.Libraries)
			r.Post("/abs/import", absImportHandler.Start)
			r.Get("/abs/import/status", absImportHandler.Status)
			r.Get("/abs/import/runs", absImportHandler.Runs)
			r.Post("/abs/import/runs/{runID}/rollback/preview", absImportHandler.RollbackPreview)
			r.Post("/abs/import/runs/{runID}/rollback", absImportHandler.Rollback)
			r.Get("/abs/review", absReviewHandler.List)
			r.Post("/abs/review/{id}/approve", absReviewHandler.Approve)
			r.Post("/abs/review/{id}/resolve-author", absReviewHandler.ResolveAuthor)
			r.Post("/abs/review/{id}/resolve-book", absReviewHandler.ResolveBook)
			r.Post("/abs/review/{id}/dismiss", absReviewHandler.Dismiss)
			r.Post("/abs/review/dismiss-run/{runID}", absReviewHandler.DismissRun)
			r.Get("/abs/conflicts", absConflictHandler.List)
			r.Post("/abs/conflicts/{id}/resolve", absConflictHandler.Resolve)
		})

		// Series
		registerSeriesRoutes(r, seriesHandler)

		// Recommendations
		r.Get("/recommendations", recHandler.List)
		r.Post("/recommendations/{id}/dismiss", recHandler.Dismiss)
		r.Post("/recommendations/{id}/add", recHandler.Add)
		r.Post("/recommendations/refresh", recHandler.Refresh)
		r.Delete("/recommendations/dismissals", recHandler.ClearDismissals)
		r.Get("/recommendations/exclude-author", recHandler.ListAuthorExclusions)
		r.Post("/recommendations/exclude-author", recHandler.ExcludeAuthor)
		r.Delete("/recommendations/exclude-author/{name}", recHandler.RemoveAuthorExclusion)

		// Import lists, delay profiles, custom formats: shared deployment
		// config, not per-user content. Reads and writes are admin-only so
		// non-admin sessions can't enumerate which import lists feed the
		// library or which delay profiles gate downloads. Previously every
		// authenticated user could list/read these, which leaked operational
		// detail (which TRaSH custom-format rules are active, which
		// HC-list/RSS feeds are wired up, what the delay-profile policy is).
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Get("/importlist", importListHandler.List)
			r.Post("/importlist", importListHandler.Create)
			r.Get("/importlist/hardcover/lists", importListHandler.HardcoverLists)
			r.Get("/importlist/{id}", importListHandler.Get)
			r.Put("/importlist/{id}", importListHandler.Update)
			r.Delete("/importlist/{id}", importListHandler.Delete)
			r.Post("/importlist/{id}/sync", importListHandler.Sync)

			r.Get("/importlistexclusion", importListHandler.ListExclusions)
			r.Post("/importlistexclusion", importListHandler.CreateExclusion)
			r.Delete("/importlistexclusion/{id}", importListHandler.DeleteExclusion)

			r.Get("/delayprofile", delayProfileHandler.List)
			r.Post("/delayprofile", delayProfileHandler.Create)
			r.Get("/delayprofile/{id}", delayProfileHandler.Get)
			r.Put("/delayprofile/{id}", delayProfileHandler.Update)
			r.Delete("/delayprofile/{id}", delayProfileHandler.Delete)

			r.Get("/customformat", customFormatHandler.List)
			r.Post("/customformat", customFormatHandler.Create)
			r.Get("/customformat/{id}", customFormatHandler.Get)
			r.Put("/customformat/{id}", customFormatHandler.Update)
			r.Delete("/customformat/{id}", customFormatHandler.Delete)
		})

		// Metadata profiles — per-user (owner_user_id from migration 025).
		// Reads stay available to all authenticated users; the cross-user
		// Get/Update/Delete IDOR is closed by D1's env-gated handler check.
		r.Get("/metadataprofile", metadataProfileHandler.List)
		r.Post("/metadataprofile", metadataProfileHandler.Create)
		r.Get("/metadataprofile/{id}", metadataProfileHandler.Get)
		r.Put("/metadataprofile/{id}", metadataProfileHandler.Update)
		r.Delete("/metadataprofile/{id}", metadataProfileHandler.Delete)

		// Backups — Restore overwrites the live database, Delete removes
		// stored backups, and List leaks filenames containing timestamps that
		// help an attacker target Restore. Admin-only across the whole
		// surface.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Get("/backup", backupHandler.List)
			r.Post("/backup", backupHandler.Create)
			r.Post("/backup/{filename}/restore", backupHandler.Restore)
			r.Delete("/backup/{filename}", backupHandler.Delete)
		})

		// System logs
		r.Get("/system/logs", logHandler.List)
		r.Get("/system/loglevel", logHandler.GetLevel)
		r.Put("/system/loglevel", logHandler.SetLevel)

		// Storage paths (read-only view of the env/config-driven dirs)
		storageHandler := api.NewStorageHandler(cfg)
		r.Get("/system/storage", storageHandler.Get)

		// Library
		r.Post("/library/scan", libraryHandler.Scan)
		r.Get("/library/scan/status", libraryHandler.ScanStatus)

		// Grimmory integration.
		r.Get("/grimmory/config", grimmoryHandler.GetConfig)
		r.Put("/grimmory/config", grimmoryHandler.SetConfig)
		r.Post("/grimmory/test", grimmoryHandler.Test)

		// Calibre integration — settings live under /setting/calibre.*,
		// this endpoint just validates + probes the configured install.
		r.Post("/calibre/test", calibreHandler.Test)

		// Calibre library import (read side). Start is fire-and-forget;
		// the UI polls Status while it runs.
		r.Post("/calibre/import", calibreImportHandler.Start)
		r.Get("/calibre/import/status", calibreImportHandler.Status)

		// Calibre bulk push (write side). Iterates every imported book and
		// POSTs its file to the plugin; 409 Conflict is treated as
		// idempotent. Single-job policy — second call returns 409.
		r.Post("/calibre/sync", calibreSyncHandler.Start)
		r.Get("/calibre/sync/status", calibreSyncHandler.Status)

		// Calibre import run history + rollback (#643). Admin-only — a bad
		// rollback can delete authors/books wholesale, so the destructive
		// path is gated by RequireAdmin and the read-only list is grouped
		// here for consistency.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Get("/calibre/runs", calibreRunsHandler.List)
			r.Get("/calibre/runs/{runID}/rollback/preview", calibreRunsHandler.RollbackPreview)
			r.Post("/calibre/runs/{runID}/rollback", calibreRunsHandler.Rollback)
		})

		// Migration imports (CSV of author names, or Readarr SQLite DB).
		// The Readarr import is async — POST returns 202 immediately and the
		// UI polls GET /migrate/readarr/status to track completion.
		//
		// Per-route body caps override the 1 MiB default for routes that
		// accept multipart file uploads. The handler-side acceptUpload still
		// applies the authoritative per-route cap via http.MaxBytesReader;
		// these overrides just raise the outer router-level ceiling so the
		// inner wrap is the one that decides.
		r.With(api.WithMaxBody(6<<20)).Post("/migrate/csv", migrateHandler.ImportCSV)         // CSV under 5 MiB
		r.With(api.WithMaxBody(2<<30)).Post("/migrate/readarr", migrateHandler.ImportReadarr) // readarr.db can be hundreds of MiB
		r.Get("/migrate/readarr/status", migrateHandler.ImportReadarrStatus)

		// Goodreads library CSV import — a two-step migration aid: POST the
		// export to /goodreads/preview for a dry-run, then POST the returned
		// token to /goodreads/commit to add the resolved books.
		r.With(api.WithMaxBody(24<<20)).Post("/migrate/goodreads/preview", migrateHandler.ImportGoodreadsPreview) // Goodreads export under 20 MiB
		r.Post("/migrate/goodreads/commit", migrateHandler.ImportGoodreadsCommit)

		// Image proxy — caches external cover images locally so the browser
		// never leaks the user's IP to Goodreads / OpenLibrary / etc.
		r.Get("/images", imageProxyHandler.Serve)
	})

	// OPDS 1.2 catalogue — KOReader / Moon+ Reader / Aldiko speak this
	// natively. Sits on its own auth path (Basic + API key + session) so
	// headless reading devices don't need cookies.
	opdsBuilder := opds.NewBuilder(opds.Config{Title: "Bindery", PageSize: 50}, bookRepo, authorRepo, seriesRepo)
	opdsHandler := api.NewOPDSHandler(opdsBuilder, bookRepo, fileHandler)
	r.Route("/opds", func(r chi.Router) {
		r.Use(api.OPDSAuth(authProvider, userRepo, loginLimiter))
		r.Get("/", opdsHandler.Root)
		r.Get("/authors", opdsHandler.Authors)
		r.Get("/authors/{id}", opdsHandler.Author)
		r.Get("/series", opdsHandler.Series)
		r.Get("/series/{id}", opdsHandler.OneSeries)
		r.Get("/recent", opdsHandler.Recent)
		r.Get("/book/{id}", opdsHandler.Book)
		r.Get("/book/{id}/file", opdsHandler.DownloadFile)
	})

	// Serve embedded frontend
	distFS, err := fs.Sub(webui.DistFS, "dist")
	if err != nil {
		slog.Error("failed to load embedded frontend", "error", err)
		os.Exit(1)
	}

	// Build the index.html payload once at startup. injectBaseHTML prepends two
	// things to <head> (see that function for why position matters):
	//   1. <base href="<URLBase>/"> so that Vite's relative asset URLs
	//      (./assets/…) resolve correctly regardless of which SPA route the
	//      browser navigates to directly.
	//   2. window.__BINDERY_BASE__ (via __bindery_base.js) so the frontend can
	//      set BrowserRouter basename and prefix API calls without a
	//      per-deployment build.
	rawIndex, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		slog.Error("failed to read embedded index.html", "error", err)
		os.Exit(1)
	}
	baseJSON, _ := json.Marshal(cfg.URLBase)
	// Expose the base via a same-origin EXTERNAL script rather than an inline
	// <script>. The strict CSP (script-src 'self') blocks inline scripts, which
	// would silently drop window.__BINDERY_BASE__ — harmless at root (the ""
	// fallback is correct) but fatal under a path prefix.
	baseScript := []byte(fmt.Sprintf("window.__BINDERY_BASE__=%s;", baseJSON))
	indexHTML := []byte(injectBaseHTML(string(rawIndex), cfg.URLBase))

	r.Get("/__bindery_base.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		_, _ = w.Write(baseScript)
	})

	fileServer := http.FileServer(http.FS(distFS))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[1:]
		if path == "" || path == "index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			_, _ = w.Write(indexHTML)
			return
		}
		if _, err := fs.Stat(distFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback — unknown paths render the app shell.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		_, _ = w.Write(indexHTML)
	})

	// If BINDERY_URL_BASE is set, mount the entire router under that prefix.
	// chi.Mount strips the prefix before dispatching so all inner routes and
	// the SPA handler continue to work unchanged against un-prefixed paths.
	var handler http.Handler = r
	if cfg.URLBase != "" {
		outer := chi.NewRouter()
		// Redirect bare prefix (no trailing slash) to prefix/ so the SPA
		// bootstrap and asset resolution work correctly.
		outer.Get(cfg.URLBase, http.RedirectHandler(cfg.URLBase+"/", http.StatusMovedPermanently).ServeHTTP)
		// http.StripPrefix actually rewrites r.URL.Path before dispatch, so the
		// inner router sees un-prefixed paths. chi.Mount only rewrites the
		// routing-context path and leaves r.URL.Path prefixed, which breaks the
		// static file handler and http.FileServer — they read r.URL.Path directly
		// and would look up "<prefix>/assets/…" in the embedded FS, miss, and fall
		// back to serving index.html (text/html) for every JS/CSS asset.
		outer.Handle(cfg.URLBase+"/*", http.StripPrefix(cfg.URLBase, r))
		handler = outer
		slog.Info("serving under path prefix", "urlBase", cfg.URLBase)
	}

	gracePeriod := 30 * time.Second
	if v := os.Getenv("BINDERY_SHUTDOWN_GRACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			gracePeriod = d
		}
	}

	addr := ":" + cfg.Port
	slog.Info("listening", "addr", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-appCtx.Done():
		slog.Info("received shutdown signal, draining…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), gracePeriod)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("server shutdown did not complete cleanly", "error", err)
		}
		slog.Info("shutdown complete")
	}
}

// injectBaseHTML rewrites the embedded index.html so the <base href> and the
// __bindery_base.js bootstrap script are the FIRST children of <head>, ahead of
// Vite's relative-path asset tags.
//
// Vite (base: './') emits the entry <script type="module"> and the stylesheet
// <link> into <head> with relative "./assets/…" URLs. Per the HTML spec a <base>
// element only governs relative URLs that appear AFTER it in document order, so
// the base tag MUST precede those asset tags. Injecting it before </head> (i.e.
// last) leaves the assets resolving against the document path: at /<base>/ that
// coincidentally yields /<base>/assets/…, but a deep route like /<base>/author/10
// resolves to /<base>/author/assets/… → 404 → the SPA fallback returns index.html
// (text/html), the module load fails, and the page renders blank on reload.
//
// The __bindery_base.js classic script still executes before the deferred entry
// module, so window.__BINDERY_BASE__ is set in time for the BrowserRouter basename.
func injectBaseHTML(raw, urlBase string) string {
	injection := fmt.Sprintf(`<base href="%s/"><script src="%s/__bindery_base.js"></script>`, urlBase, urlBase)
	return strings.Replace(raw, "<head>", "<head>"+injection, 1)
}

func googleBooksAPIKey(ctx context.Context, settings *db.SettingsRepo) string {
	if settings == nil {
		return ""
	}
	if s, _ := settings.Get(ctx, settingGoogleBooksAPIKey); s != nil {
		return strings.TrimSpace(s.Value)
	}
	if s, _ := settings.Get(ctx, legacySettingGoogleBooksAPIKey); s != nil {
		return strings.TrimSpace(s.Value)
	}
	return ""
}

func defaultNamingTemplate(settings *db.SettingsRepo) string {
	if s, _ := settings.Get(context.Background(), "naming_template"); s != nil && s.Value != "" {
		return s.Value
	}
	return ""
}

// syncOnStartup reads the calibre.sync_on_startup setting and returns
// true iff it's explicitly "true" (case-insensitive). Any other value
// — including absent — resolves to false so first boots don't kick off
// work the operator didn't ask for.
func syncOnStartup(settings *db.SettingsRepo) bool {
	s, _ := settings.Get(context.Background(), api.SettingCalibreSyncOnStartup)
	if s == nil {
		return false
	}
	return strings.EqualFold(s.Value, "true")
}

func audiobookNamingTemplate(settings *db.SettingsRepo) string {
	if s, _ := settings.Get(context.Background(), "naming_template_audiobook"); s != nil && s.Value != "" {
		return s.Value
	}
	return ""
}

// bootstrapAuth seeds the three auth settings on first boot:
//   - auth.api_key        (32 random bytes, hex-encoded)
//   - auth.session_secret (32 random bytes, base64)
//   - auth.mode           ('enabled' — safe default, forces first-run setup)
//
// If envSeed is non-empty (legacy BINDERY_API_KEY) and no api_key is stored
// yet, that value seeds the DB so existing integrations keep working after
// upgrade. Rotating the key in-UI then overrides the env.
func bootstrapAuth(ctx context.Context, settings *db.SettingsRepo, envSeed string) error {
	// API key
	existing, err := settings.Get(ctx, api.SettingAuthAPIKey)
	if err != nil {
		return err
	}
	if existing == nil || existing.Value == "" {
		key := envSeed
		if key == "" {
			if k, err := auth.RandomHex(32); err != nil {
				return err
			} else {
				key = k
			}
			slog.Info("generated new API key (visible in Settings → Security)")
		} else {
			slog.Info("seeded API key from BINDERY_API_KEY env var")
		}
		if err := settings.Set(ctx, api.SettingAuthAPIKey, key); err != nil {
			return err
		}
	}

	// Session signing secret
	if s, _ := settings.Get(ctx, api.SettingAuthSessionSecret); s == nil || s.Value == "" {
		secret, err := auth.RandomBase64(32)
		if err != nil {
			return err
		}
		if err := settings.Set(ctx, api.SettingAuthSessionSecret, secret); err != nil {
			return err
		}
	}

	// Auth mode default: 'enabled' so a fresh install forces first-run setup
	// before anything becomes reachable.
	if s, _ := settings.Get(ctx, api.SettingAuthMode); s == nil || s.Value == "" {
		if err := settings.Set(ctx, api.SettingAuthMode, string(auth.ModeEnabled)); err != nil {
			return err
		}
	}
	return nil
}

// dbAuthProvider adapts the DB-backed settings + user repo to the minimal
// auth.Provider interface consumed by auth.Middleware.
type dbAuthProvider struct {
	settings       *db.SettingsRepo
	users          *db.UserRepo
	proxyHeader    string
	proxyProvision bool
	proxyCIDRs     []*net.IPNet
}

func (p *dbAuthProvider) Mode() auth.Mode {
	s, _ := p.settings.Get(context.Background(), api.SettingAuthMode)
	if s == nil {
		return auth.ModeEnabled
	}
	return auth.ParseMode(s.Value)
}

func (p *dbAuthProvider) APIKey() string {
	s, _ := p.settings.Get(context.Background(), api.SettingAuthAPIKey)
	if s == nil {
		return ""
	}
	return s.Value
}

func (p *dbAuthProvider) SessionSecret() []byte {
	s, _ := p.settings.Get(context.Background(), api.SettingAuthSessionSecret)
	if s == nil {
		return nil
	}
	return []byte(s.Value)
}

// SessionSecrets returns the ordered verification candidate set
// {current, previous}. The previous secret is included only when it has been
// populated by a rotation; until then this is a single-element slice and
// verification is identical to single-secret behavior. VerifySessionMulti
// applies the minimum-length fail-closed guard to every entry.
func (p *dbAuthProvider) SessionSecrets() [][]byte {
	secrets := [][]byte{p.SessionSecret()}
	if s, _ := p.settings.Get(context.Background(), api.SettingAuthSessionSecretPrevious); s != nil && s.Value != "" {
		secrets = append(secrets, []byte(s.Value))
	}
	return secrets
}

func (p *dbAuthProvider) SetupRequired() bool {
	n, err := p.users.Count(context.Background())
	return err == nil && n == 0
}

func (p *dbAuthProvider) ProxyAuthHeader() string         { return p.proxyHeader }
func (p *dbAuthProvider) ProxyAutoProvision() bool        { return p.proxyProvision }
func (p *dbAuthProvider) TrustedProxyCIDRs() []*net.IPNet { return p.proxyCIDRs }
func (p *dbAuthProvider) UserRole(ctx context.Context, userID int64) string {
	u, err := p.users.GetByID(ctx, userID)
	if err != nil || u == nil {
		return ""
	}
	return u.Role
}

// UserSessionEpoch returns the user's current users.session_epoch, the value
// the cookie payload must match for the auth middleware to accept it. Bumped
// inside UpdatePassword so a password change immediately evicts every
// outstanding cookie for that user (Wave 1 / Bundle C audit finding).
// Returns 0 on lookup failure or missing user — that fails closed against
// any cookie minted after the 047 migration (which defaults the column to 1).
func (p *dbAuthProvider) UserSessionEpoch(ctx context.Context, userID int64) int64 {
	epoch, err := p.users.GetSessionEpoch(ctx, userID)
	if err != nil {
		return 0
	}
	return epoch
}
func (p *dbAuthProvider) UserProvisioner() auth.UserProvisioner {
	return &dbUserProvisioner{users: p.users}
}

// dbUserProvisioner implements auth.UserProvisioner using the UserRepo.
type dbUserProvisioner struct {
	users *db.UserRepo
}

func (p *dbUserProvisioner) ResolveOrProvisionUser(ctx context.Context, username string, autoProvision bool) (int64, error) {
	if autoProvision {
		u, err := p.users.GetOrCreateByUsername(ctx, username)
		if err != nil {
			return 0, err
		}
		return u.ID, nil
	}
	u, err := p.users.GetByUsername(ctx, username)
	if err != nil {
		return 0, err
	}
	if u == nil {
		return 0, nil
	}
	return u.ID, nil
}

// routeTemplate returns the chi route pattern for the request (e.g.
// "/api/v1/book/{id}") rather than the raw URL. Critical for Prometheus
// metric labels — using the raw URL would create unbounded label cardinality
// because every distinct id becomes a separate time series.
//
// Falls back to the URL path before any handler has matched the route, which
// happens for 404s. Strip query strings — they're already excluded by URL.Path
// but the comment is here for the reader.
func routeTemplate(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if pat := rc.RoutePattern(); pat != "" {
			return pat
		}
	}
	return r.URL.Path
}

// buildTelemetryGatherer returns a telemetry.Gatherer closure that reads the
// current per-subsystem configuration counts directly from SQLite. Every
// query is best-effort: a failure on any one subsystem leaves that field at
// its zero value (which the Features struct omits from the wire), rather
// than skipping the entire ping. Costs at most a handful of small SELECTs
// per call; safe to invoke from the daily ping goroutine.
//
// Published schema: getbindery.dev/telemetry-fields. Adding a new field
// here means updating that page so opt-in users know what they're sending.
func buildTelemetryGatherer(
	indexers *db.IndexerRepo,
	clients *db.DownloadClientRepo,
	notifications *db.NotificationRepo,
	users *db.UserRepo,
	settings *db.SettingsRepo,
) telemetry.Gatherer {
	return func(ctx context.Context) telemetry.Features {
		f := telemetry.Features{}

		// Count enabled indexers / download clients / notifications. These
		// repos don't have a Count() variant so we List() and filter in
		// memory; the sets are small (under 100 per install in practice).
		if list, err := indexers.List(ctx); err == nil {
			for _, ix := range list {
				if ix.Enabled {
					f.Indexers++
				}
			}
		}
		if list, err := clients.List(ctx); err == nil {
			for _, c := range list {
				if c.Enabled {
					f.DownloadClients++
				}
			}
		}
		if list, err := notifications.List(ctx); err == nil {
			for _, n := range list {
				if n.Enabled {
					f.Notifications++
				}
			}
		}
		if n, err := users.Count(ctx); err == nil {
			f.Users = n
			if n > 1 {
				f.MultiUser = true
			}
		}

		// Per-subsystem enabled flags. "Enabled" is what the user actually
		// flipped on (not "configured but disabled"), matching the way the
		// dashboard groups by intent rather than state.
		f.CalibreEnabled = settingTruthy(ctx, settings, api.SettingCalibreEnabled)
		f.ABSEnabled = settingTruthy(ctx, settings, api.SettingABSEnabled)
		f.GrimmoryEnabled = settingTruthy(ctx, settings, api.SettingGrimmoryEnabled)

		// HardcoverToken is "is there a token saved" (presence, not value).
		// Bindery's enhanced Hardcover mode is gated on having one, so this
		// closely tracks whether the install uses Hardcover features.
		if v, err := settings.Get(ctx, api.SettingHardcoverAPIToken); err == nil && v != nil && v.Value != "" {
			f.HardcoverToken = true
		}

		// OIDC enabled if the providers list is non-empty (the same gate
		// the API uses to decide whether to render OIDC login buttons).
		if v, err := settings.Get(ctx, api.SettingOIDCProviders); err == nil && v != nil {
			trimmed := strings.TrimSpace(v.Value)
			if trimmed != "" && trimmed != "[]" && trimmed != "null" {
				f.OIDCEnabled = true
			}
		}

		return f
	}
}

// settingTruthy reads a setting key and returns true when the stored value
// represents "on" ("true" / "1" / "yes"). Missing keys and errors return
// false; the gatherer treats both as "not enabled."
func settingTruthy(ctx context.Context, settings *db.SettingsRepo, key string) bool {
	v, err := settings.Get(ctx, key)
	if err != nil || v == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v.Value)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
