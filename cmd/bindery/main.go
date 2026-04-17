package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/hardcoverlistsyncer"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/logbuf"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/dnb"
	"github.com/vavallee/bindery/internal/metadata/googlebooks"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
	"github.com/vavallee/bindery/internal/opds"
	"github.com/vavallee/bindery/internal/prowlarr"
	"github.com/vavallee/bindery/internal/recommender"
	"github.com/vavallee/bindery/internal/scheduler"
	"github.com/vavallee/bindery/internal/webui"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
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

	slog.Info("starting bindery",
		"version", version,
		"commit", commit,
		"port", cfg.Port,
		"dbPath", cfg.DBPath,
		"dataDir", cfg.DataDir,
	)

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
	authorRepo := db.NewAuthorRepo(database)
	authorAliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
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
	tagRepo := db.NewTagRepo(database)
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

	// Login rate limiter: 5 failures / 15 min per IP, matches Sonarr's posture.
	loginLimiter := auth.NewLoginLimiter(5, 15*time.Minute)

	// Metadata providers
	olClient := openlibrary.New()
	var enrichers []metadata.Provider
	if setting, _ := settingsRepo.Get(context.Background(), "google_books_api_key"); setting != nil && setting.Value != "" {
		enrichers = append(enrichers, googlebooks.New(setting.Value))
		slog.Info("google books enrichment enabled")
	}
	enrichers = append(enrichers, hardcover.New())
	slog.Info("hardcover enrichment enabled")
	enrichers = append(enrichers, dnb.New())
	slog.Info("dnb enrichment enabled")
	metaAgg := metadata.NewAggregator(olClient, enrichers...)

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

	modeResolver := func() calibre.Mode { return api.LoadCalibreMode(settingsRepo) }
	calibreCfg := api.LoadCalibreConfig(settingsRepo)
	currentMode := api.LoadCalibreMode(settingsRepo)
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
	calibreImporter := calibre.NewImporter(authorRepo, authorAliasRepo, bookRepo, editionRepo, settingsRepo)
	if syncOnStartup(settingsRepo) {
		cfg := api.LoadCalibreConfig(settingsRepo)
		if cfg.Enabled && cfg.LibraryPath != "" {
			slog.Info("calibre sync_on_startup enabled — kicking off library import")
			go func() {
				if _, err := calibreImporter.Run(context.Background(), cfg.LibraryPath); err != nil {
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
		instances, _ := prowlarrRepo.List(context.Background())
		for _, inst := range instances {
			if !inst.Enabled || !inst.SyncOnStartup {
				continue
			}
			inst := inst // capture
			go func() {
				client := prowlarr.New(inst.URL, inst.APIKey)
				syncer := prowlarr.NewSyncer(client, indexerRepo, prowlarrRepo)
				if _, err := syncer.Sync(context.Background(), inst.ID); err != nil {
					slog.Warn("prowlarr startup sync failed", "instance", inst.Name, "error", err)
				}
			}()
		}
	}

	// Scheduler
	sched := scheduler.New(importScanner, idxSearcher, metaAgg,
		authorRepo, bookRepo, indexerRepo, downloadRepo, dlClientRepo, settingsRepo, blocklistRepo)
	sched.WithHistory(historyRepo)
	sched.WithDelayProfiles(delayProfileRepo)
	sched.WithPendingReleases(pendingReleaseRepo)
	// Register the Calibre importer as the 24-hour sync job. The scheduler
	// only fires the job when the syncer is non-nil, so no guard needed here.
	sched.WithCalibreSyncer(calibreImporter)

	// Recommendation engine (24-hour job, gated on recommendations.enabled).
	recRepo := db.NewRecommendationRepo(database)
	recEngine := recommender.New(bookRepo, authorRepo, seriesRepo, recRepo, settingsRepo)
	recEngine.WithOLClient(olClient)
	if s, _ := settingsRepo.Get(context.Background(), "hardcover.api_token"); s != nil && s.Value != "" {
		recEngine.WithHCClient(hardcover.New().WithToken(s.Value))
		slog.Info("hardcover wishlist integration enabled for recommendations")
	}
	sched.WithRecommender(recEngine)

	// Register the Hardcover list syncer (24-hour job).
	hcSyncer := hardcoverlistsyncer.New(importListRepo, authorRepo, bookRepo)
	sched.WithHardcoverSyncer(hcSyncer)

	sched.Start()
	defer sched.Stop()

	// Notifier
	notif := notifier.New(notificationRepo)

	// API handlers
	authHandler := api.NewAuthHandler(userRepo, settingsRepo, loginLimiter)
	searchHandler := api.NewSearchHandler(metaAgg)
	authorHandler := api.NewAuthorHandler(authorRepo, authorAliasRepo, bookRepo, seriesRepo, metaAgg, settingsRepo, metadataProfileRepo, sched).WithFinder(importScanner)
	authorAliasHandler := api.NewAuthorAliasHandler(authorRepo, authorAliasRepo)
	bookHandler := api.NewBookHandler(bookRepo, metaAgg, historyRepo, sched).WithSettings(settingsRepo)
	indexerHandler := api.NewIndexerHandler(indexerRepo, bookRepo, authorRepo, metadataProfileRepo, idxSearcher, settingsRepo, blocklistRepo)
	dlClientHandler := api.NewDownloadClientHandler(dlClientRepo)
	queueHandler := api.NewQueueHandler(downloadRepo, dlClientRepo, bookRepo, historyRepo).WithNotifier(notif)
	pendingHandler := api.NewPendingHandler(pendingReleaseRepo, queueHandler, downloadRepo, bookRepo)
	importScanner.WithSettings(settingsRepo)
	importScanner.WithRootFolders(rootFolderRepo)
	libraryHandler := api.NewLibraryHandler(importScanner).WithSettings(settingsRepo)
	fileHandler := api.NewFileHandler(bookRepo)
	historyHandler := api.NewHistoryHandler(historyRepo, blocklistRepo)
	blocklistHandler := api.NewBlocklistHandler(blocklistRepo)
	notificationHandler := api.NewNotificationHandler(notificationRepo, notif)
	qualityProfileHandler := api.NewQualityProfileHandler(qualityProfileRepo)
	settingsHandler := api.NewSettingsHandler(settingsRepo)
	seriesHandler := api.NewSeriesHandler(seriesRepo)
	tagHandler := api.NewTagHandler(tagRepo)
	importListHandler := api.NewImportListHandler(importListRepo)
	metadataProfileHandler := api.NewMetadataProfileHandler(metadataProfileRepo)
	delayProfileHandler := api.NewDelayProfileHandler(delayProfileRepo)
	customFormatHandler := api.NewCustomFormatHandler(customFormatRepo)
	bulkHandler := api.NewBulkHandler(authorRepo, bookRepo, blocklistRepo, sched)
	backupHandler := api.NewBackupHandler(cfg.DBPath, cfg.DataDir)
	rootFolderHandler := api.NewRootFolderHandler(rootFolderRepo)
	logHandler := api.NewLogHandler(ring)
	prowlarrHandler := api.NewProwlarrHandler(prowlarrRepo, indexerRepo)
	calibreHandler := api.NewCalibreHandler(settingsRepo)
	calibreImportHandler := api.NewCalibreImportHandler(calibreImporter, func() calibre.Config {
		return api.LoadCalibreConfig(settingsRepo)
	})
	recHandler := api.NewRecommendationHandler(recRepo, recEngine, authorRepo, bookRepo, sched)
	imageProxyHandler := api.NewImageProxyHandler(cfg.DataDir)
	imageProxyHandler.StartEviction(24 * time.Hour)
	migrateHandler := api.NewMigrateHandler(
		authorRepo, indexerRepo, dlClientRepo, blocklistRepo, bookRepo, metaAgg,
		// Bulk imports always populate the catalogue but never auto-grab.
		func(a *models.Author) { authorHandler.FetchAuthorBooks(a, false) },
	)

	// Router
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(api.SecurityHeaders)

	// Composite auth: session cookie (UI) OR API key (external apps) OR
	// local-IP bypass when mode=local-only. Mode, key, and secret are sourced
	// live from the DB so they can be rotated without a process restart.
	authProvider := &dbAuthProvider{settings: settingsRepo, users: userRepo}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(auth.Middleware(authProvider))

		// System
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","version":"` + version + `"}`))
		})
		r.Get("/system/status", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			cacheBytes, _ := imageProxyHandler.CacheSize()
			_, _ = fmt.Fprintf(w,
				`{"version":"%s","commit":"%s","buildDate":"%s","imageCacheBytes":%d}`,
				version, commit, date, cacheBytes,
			)
		})

		// Auth — status/login/logout/setup are always allowed through the
		// middleware (see auth.AllowUnauthPath). The config + mutation
		// endpoints below sit behind it.
		r.Get("/auth/status", authHandler.Status)
		r.Post("/auth/login", authHandler.Login)
		r.Post("/auth/logout", authHandler.Logout)
		r.Post("/auth/setup", authHandler.Setup)
		r.Get("/auth/config", authHandler.GetConfig)
		r.Post("/auth/password", authHandler.ChangePassword)
		r.Post("/auth/apikey/regenerate", authHandler.RegenerateAPIKey)
		r.Put("/auth/mode", authHandler.SetMode)

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
		r.Get("/author/{id}/aliases", authorAliasHandler.List)
		r.Post("/author/{id}/merge", authorAliasHandler.Merge)

		// Books
		r.Get("/book", bookHandler.List)
		r.Post("/book/bulk", bulkHandler.BooksBulk)
		r.Get("/book/{id}", bookHandler.Get)
		r.Put("/book/{id}", bookHandler.Update)
		r.Delete("/book/{id}", bookHandler.Delete)
		r.Delete("/book/{id}/file", bookHandler.DeleteFile)
		r.Put("/book/{id}/exclude", bookHandler.ToggleExcluded)
		r.Post("/book/{id}/enrich-audiobook", bookHandler.EnrichAudiobook)
		r.Post("/book/{id}/search", indexerHandler.SearchBook)
		r.Get("/book/{id}/file", fileHandler.Download)

		// Wanted
		r.Get("/wanted/missing", bookHandler.ListWanted)
		r.Post("/wanted/bulk", bulkHandler.WantedBulk)

		// Indexers
		r.Get("/indexer", indexerHandler.List)
		r.Post("/indexer", indexerHandler.Create)
		r.Get("/indexer/{id}", indexerHandler.Get)
		r.Put("/indexer/{id}", indexerHandler.Update)
		r.Delete("/indexer/{id}", indexerHandler.Delete)
		r.Post("/indexer/{id}/test", indexerHandler.Test)
		r.Get("/indexer/search", indexerHandler.SearchQuery)

		// Prowlarr indexer sync
		r.Get("/prowlarr", prowlarrHandler.List)
		r.Post("/prowlarr", prowlarrHandler.Create)
		r.Get("/prowlarr/{id}", prowlarrHandler.Get)
		r.Put("/prowlarr/{id}", prowlarrHandler.Update)
		r.Delete("/prowlarr/{id}", prowlarrHandler.Delete)
		r.Post("/prowlarr/{id}/test", prowlarrHandler.Test)
		r.Post("/prowlarr/{id}/sync", prowlarrHandler.Sync)

		// Root folders
		r.Get("/rootfolder", rootFolderHandler.List)
		r.Post("/rootfolder", rootFolderHandler.Create)
		r.Delete("/rootfolder/{id}", rootFolderHandler.Delete)

		// Download clients
		r.Get("/downloadclient", dlClientHandler.List)
		r.Post("/downloadclient", dlClientHandler.Create)
		r.Get("/downloadclient/{id}", dlClientHandler.Get)
		r.Put("/downloadclient/{id}", dlClientHandler.Update)
		r.Delete("/downloadclient/{id}", dlClientHandler.Delete)
		r.Post("/downloadclient/{id}/test", dlClientHandler.Test)

		// Queue
		r.Get("/queue", queueHandler.List)
		r.Post("/queue/grab", queueHandler.Grab)
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

		// Notifications
		r.Get("/notification", notificationHandler.List)
		r.Post("/notification", notificationHandler.Create)
		r.Get("/notification/{id}", notificationHandler.Get)
		r.Put("/notification/{id}", notificationHandler.Update)
		r.Delete("/notification/{id}", notificationHandler.Delete)
		r.Post("/notification/{id}/test", notificationHandler.Test)

		// Quality Profiles
		r.Get("/qualityprofile", qualityProfileHandler.List)
		r.Get("/qualityprofile/{id}", qualityProfileHandler.Get)

		// Settings
		r.Get("/setting", settingsHandler.List)
		r.Get("/setting/{key}", settingsHandler.Get)
		r.Put("/setting/{key}", settingsHandler.Set)
		r.Delete("/setting/{key}", settingsHandler.Delete)

		// Series
		r.Get("/series", seriesHandler.List)
		r.Get("/series/{id}", seriesHandler.Get)

		// Recommendations
		r.Get("/recommendations", recHandler.List)
		r.Post("/recommendations/{id}/dismiss", recHandler.Dismiss)
		r.Post("/recommendations/{id}/add", recHandler.Add)
		r.Post("/recommendations/refresh", recHandler.Refresh)
		r.Delete("/recommendations/dismissals", recHandler.ClearDismissals)
		r.Get("/recommendations/exclude-author", recHandler.ListAuthorExclusions)
		r.Post("/recommendations/exclude-author", recHandler.ExcludeAuthor)
		r.Delete("/recommendations/exclude-author/{name}", recHandler.RemoveAuthorExclusion)

		// Tags
		r.Get("/tag", tagHandler.List)
		r.Post("/tag", tagHandler.Create)
		r.Delete("/tag/{id}", tagHandler.Delete)

		// Import lists
		r.Get("/importlist", importListHandler.List)
		r.Post("/importlist", importListHandler.Create)
		r.Get("/importlist/hardcover/lists", importListHandler.HardcoverLists)
		r.Get("/importlist/{id}", importListHandler.Get)
		r.Put("/importlist/{id}", importListHandler.Update)
		r.Delete("/importlist/{id}", importListHandler.Delete)

		// Import list exclusions
		r.Get("/importlistexclusion", importListHandler.ListExclusions)
		r.Post("/importlistexclusion", importListHandler.CreateExclusion)
		r.Delete("/importlistexclusion/{id}", importListHandler.DeleteExclusion)

		// Metadata profiles
		r.Get("/metadataprofile", metadataProfileHandler.List)
		r.Post("/metadataprofile", metadataProfileHandler.Create)
		r.Get("/metadataprofile/{id}", metadataProfileHandler.Get)
		r.Put("/metadataprofile/{id}", metadataProfileHandler.Update)
		r.Delete("/metadataprofile/{id}", metadataProfileHandler.Delete)

		// Delay profiles
		r.Get("/delayprofile", delayProfileHandler.List)
		r.Post("/delayprofile", delayProfileHandler.Create)
		r.Get("/delayprofile/{id}", delayProfileHandler.Get)
		r.Put("/delayprofile/{id}", delayProfileHandler.Update)
		r.Delete("/delayprofile/{id}", delayProfileHandler.Delete)

		// Custom formats
		r.Get("/customformat", customFormatHandler.List)
		r.Post("/customformat", customFormatHandler.Create)
		r.Get("/customformat/{id}", customFormatHandler.Get)
		r.Put("/customformat/{id}", customFormatHandler.Update)
		r.Delete("/customformat/{id}", customFormatHandler.Delete)

		// Backups
		r.Get("/backup", backupHandler.List)
		r.Post("/backup", backupHandler.Create)
		r.Post("/backup/{filename}/restore", backupHandler.Restore)
		r.Delete("/backup/{filename}", backupHandler.Delete)

		// System logs
		r.Get("/system/logs", logHandler.List)
		r.Get("/system/loglevel", logHandler.GetLevel)
		r.Put("/system/loglevel", logHandler.SetLevel)

		// Library
		r.Post("/library/scan", libraryHandler.Scan)
		r.Get("/library/scan/status", libraryHandler.ScanStatus)

		// Calibre integration — settings live under /setting/calibre.*,
		// this endpoint just validates + probes the configured install.
		r.Post("/calibre/test", calibreHandler.Test)

		// Calibre library import (read side). Start is fire-and-forget;
		// the UI polls Status while it runs.
		r.Post("/calibre/import", calibreImportHandler.Start)
		r.Get("/calibre/import/status", calibreImportHandler.Status)

		// Migration imports (CSV of author names, or Readarr SQLite DB).
		r.Post("/migrate/csv", migrateHandler.ImportCSV)
		r.Post("/migrate/readarr", migrateHandler.ImportReadarr)

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
		r.Use(api.OPDSAuth(authProvider, userRepo))
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
	fileServer := http.FileServer(http.FS(distFS))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[1:]
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(distFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})

	addr := ":" + cfg.Port
	slog.Info("listening", "addr", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
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
	settings *db.SettingsRepo
	users    *db.UserRepo
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

func (p *dbAuthProvider) SetupRequired() bool {
	n, err := p.users.Count(context.Background())
	return err == nil && n == 0
}
