package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/googlebooks"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
	"github.com/vavallee/bindery/internal/scheduler"
	"github.com/vavallee/bindery/internal/webui"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

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
	indexerRepo := db.NewIndexerRepo(database)
	dlClientRepo := db.NewDownloadClientRepo(database)
	downloadRepo := db.NewDownloadRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	historyRepo := db.NewHistoryRepo(database)
	blocklistRepo := db.NewBlocklistRepo(database)
	notificationRepo := db.NewNotificationRepo(database)
	qualityProfileRepo := db.NewQualityProfileRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	tagRepo := db.NewTagRepo(database)
	importListRepo := db.NewImportListRepo(database)
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

	// Calibre client: constructed once at boot from the settings table.
	// The scanner calls Enabled() on every import, so toggling the flag
	// takes effect without a restart — but a library_path / binary_path
	// change does require one (same pattern the rest of Bindery uses).
	calibreClient := calibre.New(api.LoadCalibreConfig(settingsRepo))
	importScanner.WithCalibre(calibreClient)
	if calibreClient.Enabled() {
		slog.Info("calibre integration enabled")
	}

	// Scheduler
	sched := scheduler.New(importScanner, idxSearcher, metaAgg,
		authorRepo, bookRepo, indexerRepo, downloadRepo, dlClientRepo, settingsRepo, blocklistRepo)
	sched.Start()
	defer sched.Stop()

	// Notifier
	notif := notifier.New(notificationRepo)

	// API handlers
	authHandler := api.NewAuthHandler(userRepo, settingsRepo, loginLimiter)
	searchHandler := api.NewSearchHandler(metaAgg)
	authorHandler := api.NewAuthorHandler(authorRepo, authorAliasRepo, bookRepo, seriesRepo, metaAgg, settingsRepo, metadataProfileRepo, sched)
	authorAliasHandler := api.NewAuthorAliasHandler(authorRepo, authorAliasRepo)
	bookHandler := api.NewBookHandler(bookRepo, metaAgg, historyRepo, sched).WithSettings(settingsRepo)
	indexerHandler := api.NewIndexerHandler(indexerRepo, bookRepo, authorRepo, idxSearcher, settingsRepo, blocklistRepo)
	dlClientHandler := api.NewDownloadClientHandler(dlClientRepo)
	queueHandler := api.NewQueueHandler(downloadRepo, dlClientRepo, bookRepo, historyRepo)
	libraryHandler := api.NewLibraryHandler(importScanner)
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
	calibreHandler := api.NewCalibreHandler(settingsRepo)
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
			_, _ = w.Write([]byte(`{"version":"` + version + `","commit":"` + commit + `","buildDate":"` + date + `"}`))
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

		// Tags
		r.Get("/tag", tagHandler.List)
		r.Post("/tag", tagHandler.Create)
		r.Delete("/tag/{id}", tagHandler.Delete)

		// Import lists
		r.Get("/importlist", importListHandler.List)
		r.Post("/importlist", importListHandler.Create)
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

		// Library
		r.Post("/library/scan", libraryHandler.Scan)

		// Calibre integration — settings live under /setting/calibre.*,
		// this endpoint just validates + probes the configured install.
		r.Post("/calibre/test", calibreHandler.Test)

		// Migration imports (CSV of author names, or Readarr SQLite DB).
		r.Post("/migrate/csv", migrateHandler.ImportCSV)
		r.Post("/migrate/readarr", migrateHandler.ImportReadarr)
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
