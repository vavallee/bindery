package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
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

	authorRepo := db.NewAuthorRepo(database)
	authorAliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	absImportRunRepo := db.NewABSImportRunRepo(database)
	absImportRunEntityRepo := db.NewABSImportRunEntityRepo(database)
	absProvenanceRepo := db.NewABSProvenanceRepo(database)
	absConflictRepo := db.NewABSMetadataConflictRepo(database)
	absReviewRepo := db.NewABSReviewItemRepo(database)
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

	// Parse trusted-proxy CIDRs once at startup (shared by trustedProxyMiddleware
	// and the proxy-auth identity check).
	trustedCIDRs := parseTrustedProxyCIDRs(os.Getenv("BINDERY_TRUSTED_PROXY"))

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
	var enrichers []metadata.Provider
	if setting, _ := settingsRepo.Get(context.Background(), "google_books_api_key"); setting != nil && setting.Value != "" {
		enrichers = append(enrichers, googlebooks.New(setting.Value))
		slog.Info("google books enrichment enabled")
	}
	hcClient := hardcover.New().WithTokenSource(func(ctx context.Context) string {
		return api.GetHardcoverAPIToken(ctx, settingsRepo)
	})
	enrichers = append(enrichers, hcClient)
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
	absImporter := abs.NewImporter(authorRepo, authorAliasRepo, bookRepo, editionRepo, seriesRepo, settingsRepo, absImportRunRepo, absImportRunEntityRepo, absProvenanceRepo, absReviewRepo, absConflictRepo).
		WithVersion(version).
		WithStoragePaths(cfg.LibraryDir, cfg.AudiobookDir, rootFolderRepo).
		WithMetadata(metaAgg).
		WithEnhancedHardcoverSeriesEnabled(func(ctx context.Context) bool {
			return api.HardcoverFeatureStateFor(ctx, settingsRepo, cfg.EnhancedHardcoverAPI).EnhancedHardcoverAPI
		})
	if cfg.ABSFeatureEnabled {
		storedABS := api.LoadABSConfig(ctxBoot, settingsRepo)
		resumeCfg := abs.ImportConfig{
			SourceID:  abs.DefaultSourceID,
			BaseURL:   storedABS.BaseURL,
			APIKey:    storedABS.APIKey,
			LibraryID: storedABS.LibraryID,
			PathRemap: storedABS.PathRemap,
			Label:     storedABS.Label,
			Enabled:   storedABS.Enabled,
		}
		if resumed, err := absImporter.ResumeInterrupted(ctxBoot, resumeCfg); err != nil {
			slog.Warn("abs interrupted import resume skipped", "error", err)
		} else if resumed {
			slog.Info("abs interrupted import resumed from checkpoint")
		}
	}
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
	sched.WithAliases(authorAliasRepo)
	sched.WithDelayProfiles(delayProfileRepo)
	sched.WithPendingReleases(pendingReleaseRepo)
	// Register the Calibre importer as the 24-hour sync job. The scheduler
	// only fires the job when the syncer is non-nil, so no guard needed here.
	sched.WithCalibreSyncer(calibreImporter)

	// Recommendation engine (24-hour job, gated on recommendations.enabled).
	recRepo := db.NewRecommendationRepo(database)
	recEngine := recommender.New(bookRepo, authorRepo, seriesRepo, recRepo, settingsRepo)
	recEngine.WithOLClient(olClient)
	if s, _ := settingsRepo.Get(context.Background(), api.SettingHardcoverAPIToken); s != nil && s.Value != "" {
		recEngine.WithHCClient(hardcover.New().WithToken(s.Value))
		slog.Info("hardcover wishlist integration enabled for recommendations")
	}
	sched.WithRecommender(recEngine)

	// Register the Hardcover list syncer (24-hour job).
	hcSyncer := hardcoverlistsyncer.New(importListRepo, authorRepo, bookRepo)
	sched.WithHardcoverSyncer(hcSyncer)
	sched.WithLogRepo(logRepo, cfg.LogRetentionDays)

	sched.Start()
	defer sched.Stop()

	// Notifier
	notif := notifier.New(notificationRepo)

	// OIDC manager — loaded from settings, reload on config change.
	oidcMgr := oidcauth.NewManager(cfg.OIDCRedirectBaseURL)
	if s, _ := settingsRepo.Get(ctxBoot, api.SettingOIDCProviders); s != nil && s.Value != "" {
		if ps, err := oidcauth.ParseProviders(s.Value); err == nil && len(ps) > 0 {
			oidcMgr.Reload(ctxBoot, ps)
			slog.Info("oidc: loaded providers from settings", "count", len(ps))
		}
	}

	// API handlers
	authHandler := api.NewAuthHandler(userRepo, settingsRepo, loginLimiter)
	oidcHandler := api.NewOIDCHandler(oidcMgr, userRepo, settingsRepo, authHandler)
	userMgmtHandler := api.NewUserManagementHandler(userRepo)
	searchHandler := api.NewSearchHandler(metaAgg)
	authorHandler := api.NewAuthorHandler(authorRepo, authorAliasRepo, bookRepo, seriesRepo, metaAgg, settingsRepo, metadataProfileRepo, sched).WithFinder(importScanner)
	authorAliasHandler := api.NewAuthorAliasHandler(authorRepo, authorAliasRepo)
	bookHandler := api.NewBookHandler(bookRepo, metaAgg, historyRepo, sched).WithSettings(settingsRepo).WithDownloads(downloadRepo)
	indexerHandler := api.NewIndexerHandler(indexerRepo, bookRepo, authorRepo, metadataProfileRepo, idxSearcher, settingsRepo, blocklistRepo).WithAliases(authorAliasRepo)
	dlClientHandler := api.NewDownloadClientHandler(dlClientRepo)
	queueHandler := api.NewQueueHandler(downloadRepo, dlClientRepo, bookRepo, historyRepo).WithNotifier(notif)
	pendingHandler := api.NewPendingHandler(pendingReleaseRepo, queueHandler, downloadRepo, bookRepo)
	importScanner.WithSettings(settingsRepo)
	importScanner.WithRootFolders(rootFolderRepo)
	importScanner.WithSeriesRepo(seriesRepo)

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
		WithHardcoverFeatureSettings(settingsRepo, cfg.EnhancedHardcoverAPI)
	tagHandler := api.NewTagHandler(tagRepo)
	importListHandler := api.NewImportListHandler(importListRepo)
	metadataProfileHandler := api.NewMetadataProfileHandler(metadataProfileRepo)
	delayProfileHandler := api.NewDelayProfileHandler(delayProfileRepo)
	customFormatHandler := api.NewCustomFormatHandler(customFormatRepo)
	bulkHandler := api.NewBulkHandler(authorRepo, bookRepo, blocklistRepo, sched)
	backupHandler := api.NewBackupHandler(cfg.DBPath, cfg.DataDir)
	rootFolderHandler := api.NewRootFolderHandler(rootFolderRepo)
	logHandler := api.NewLogHandler(ring).WithLogRepo(logRepo)
	prowlarrHandler := api.NewProwlarrHandler(prowlarrRepo, indexerRepo)
	calibreHandler := api.NewCalibreHandler(settingsRepo)
	absHandler := api.NewABSHandler(settingsRepo).WithVersion(version).WithFeatureEnabled(cfg.ABSFeatureEnabled)
	absConflictHandler := api.NewABSConflictHandler(absConflictRepo, authorRepo, bookRepo)
	absImportHandler := api.NewABSImportHandler(absImporter, func(ctx context.Context) api.ABSStoredConfig {
		return api.LoadABSConfig(ctx, settingsRepo)
	})
	absReviewHandler := api.NewABSReviewHandler(absReviewRepo, absImporter, func(ctx context.Context) api.ABSStoredConfig {
		return api.LoadABSConfig(ctx, settingsRepo)
	})
	calibreImportHandler := api.NewCalibreImportHandler(calibreImporter, func() calibre.Config {
		return api.LoadCalibreConfig(settingsRepo)
	})
	calibreSyncer := calibre.NewSyncer(bookRepo)
	calibreSyncHandler := api.NewCalibreSyncHandler(
		calibreSyncer,
		func() calibre.Config { return api.LoadCalibreConfig(settingsRepo) },
		func() calibre.Mode { return api.LoadCalibreMode(settingsRepo) },
	)
	recHandler := api.NewRecommendationHandler(recRepo, recEngine, authorRepo, bookRepo, sched)
	imageProxyHandler := api.NewImageProxyHandler(cfg.DataDir)
	imageProxyHandler.StartEviction(24 * time.Hour)
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
		r.Use(auth.RequireCSRFToken(authProvider.SessionSecret))

		r.Get("/queue", queueHandler.ListArrCompatible)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(auth.Middleware(authProvider))
		r.Use(auth.RequireXRequestedWith)
		r.Use(auth.RequireCSRFToken(authProvider.SessionSecret))

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
		r.Post("/auth/apikey/regenerate", authHandler.RegenerateAPIKey)
		// OIDC — login/callback are unauthenticated; provider management requires auth.
		r.Get("/auth/oidc/providers", oidcHandler.GetProviders)
		r.Put("/auth/oidc/providers", oidcHandler.SetProviders)
		r.Get("/auth/oidc/{provider}/login", oidcHandler.Login)
		r.Get("/auth/oidc/{provider}/callback", oidcHandler.Callback)
		// Admin-only auth mutations.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
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

		// Indexers — reads available to all; mutations admin-only.
		r.Get("/indexer", indexerHandler.List)
		r.Get("/indexer/{id}", indexerHandler.Get)
		r.Get("/indexer/search", indexerHandler.SearchQuery)
		r.Get("/search/last-debug", indexerHandler.LastSearchDebug)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Post("/indexer", indexerHandler.Create)
			r.Put("/indexer/{id}", indexerHandler.Update)
			r.Delete("/indexer/{id}", indexerHandler.Delete)
			r.Post("/indexer/{id}/test", indexerHandler.Test)
		})

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

		// Download clients — reads available to all; mutations admin-only.
		r.Get("/downloadclient", dlClientHandler.List)
		r.Get("/downloadclient/{id}", dlClientHandler.Get)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Post("/downloadclient", dlClientHandler.Create)
			r.Put("/downloadclient/{id}", dlClientHandler.Update)
			r.Delete("/downloadclient/{id}", dlClientHandler.Delete)
			r.Post("/downloadclient/{id}/test", dlClientHandler.Test)
		})

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

		// Settings — reads available to all; mutations admin-only.
		r.Get("/setting", settingsHandler.List)
		r.Get("/setting/{key}", settingsHandler.Get)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Put("/setting/{key}", settingsHandler.Set)
			r.Delete("/setting/{key}", settingsHandler.Delete)
			r.Post("/hardcover/test", settingsHandler.TestHardcover)
			r.Get("/abs/config", absHandler.GetConfig)
			if cfg.ABSFeatureEnabled {
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
				r.Get("/abs/conflicts", absConflictHandler.List)
				r.Post("/abs/conflicts/{id}/resolve", absConflictHandler.Resolve)
			}
		})

		// Series
		r.Get("/series", seriesHandler.List)
		r.Post("/series", seriesHandler.Create)
		r.Get("/series/hardcover/search", seriesHandler.SearchHardcover)
		r.Get("/series/{id}", seriesHandler.Get)
		r.Put("/series/{id}", seriesHandler.Update)
		r.Patch("/series/{id}", seriesHandler.Monitor)
		r.Delete("/series/{id}", seriesHandler.Delete)
		r.Post("/series/{id}/books", seriesHandler.AddBook)
		r.Post("/series/{id}/fill", seriesHandler.Fill)
		r.Get("/series/{id}/hardcover-link", seriesHandler.GetHardcoverLink)
		r.Post("/series/{id}/hardcover-link/auto", seriesHandler.AutoLinkHardcover)
		r.Put("/series/{id}/hardcover-link", seriesHandler.PutHardcoverLink)
		r.Delete("/series/{id}/hardcover-link", seriesHandler.DeleteHardcoverLink)
		r.Get("/series/{id}/hardcover-diff", seriesHandler.HardcoverDiff)

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

		// Storage paths (read-only view of the env/config-driven dirs)
		storageHandler := api.NewStorageHandler(cfg)
		r.Get("/system/storage", storageHandler.Get)

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

		// Calibre bulk push (write side). Iterates every imported book and
		// POSTs its file to the plugin; 409 Conflict is treated as
		// idempotent. Single-job policy — second call returns 409.
		r.Post("/calibre/sync", calibreSyncHandler.Start)
		r.Get("/calibre/sync/status", calibreSyncHandler.Status)

		// Migration imports (CSV of author names, or Readarr SQLite DB).
		// The Readarr import is async — POST returns 202 immediately and the
		// UI polls GET /migrate/readarr/status to track completion.
		r.Post("/migrate/csv", migrateHandler.ImportCSV)
		r.Post("/migrate/readarr", migrateHandler.ImportReadarr)
		r.Get("/migrate/readarr/status", migrateHandler.ImportReadarrStatus)

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
