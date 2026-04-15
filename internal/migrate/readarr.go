package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// ReadarrResult bundles per-section import stats so the UI/CLI can show
// users exactly what crossed over and what didn't.
type ReadarrResult struct {
	Authors         Result `json:"authors"`
	Indexers        Result `json:"indexers"`
	DownloadClients Result `json:"downloadClients"`
	Blocklist       Result `json:"blocklist"`
}

// ImportReadarr reads a Readarr SQLite database and ports its records
// into Bindery. Authors are re-resolved via OpenLibrary (Goodreads IDs
// are not portable since bookinfo.club is dead); indexers, download
// clients, and blocklist entries port structurally.
//
// The onSearchOnAdd hook fires asynchronously for each newly-added
// author so the library scan/book-fetch can pick them up without
// blocking the import.
func ImportReadarr(
	ctx context.Context,
	dbPath string,
	authorRepo *db.AuthorRepo,
	indexerRepo *db.IndexerRepo,
	clientRepo *db.DownloadClientRepo,
	blocklistRepo *db.BlocklistRepo,
	agg *metadata.Aggregator,
	onSearchOnAdd func(author *models.Author),
) (*ReadarrResult, error) {
	if dbPath == "" {
		return nil, errors.New("readarr db path is required")
	}
	src, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&immutable=1")
	if err != nil {
		return nil, fmt.Errorf("open readarr db: %w", err)
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping readarr db: %w", err)
	}

	res := &ReadarrResult{
		Authors:         *newResult(),
		Indexers:        *newResult(),
		DownloadClients: *newResult(),
		Blocklist:       *newResult(),
	}

	if err := importReadarrAuthors(ctx, src, authorRepo, agg, onSearchOnAdd, &res.Authors); err != nil {
		slog.Warn("readarr import: authors failed", "error", err)
	}
	if err := importReadarrIndexers(ctx, src, indexerRepo, &res.Indexers); err != nil {
		slog.Warn("readarr import: indexers failed", "error", err)
	}
	if err := importReadarrDownloadClients(ctx, src, clientRepo, &res.DownloadClients); err != nil {
		slog.Warn("readarr import: download clients failed", "error", err)
	}
	if err := importReadarrBlocklist(ctx, src, blocklistRepo, &res.Blocklist); err != nil {
		slog.Warn("readarr import: blocklist failed", "error", err)
	}
	return res, nil
}

// importReadarrAuthors pulls Authors.Name+Monitored from Readarr and
// re-resolves each one against OpenLibrary. No Goodreads IDs are trusted.
func importReadarrAuthors(ctx context.Context, src *sql.DB, repo *db.AuthorRepo, agg *metadata.Aggregator, onSearchOnAdd func(*models.Author), res *Result) error {
	rows, err := src.QueryContext(ctx, `SELECT Name, Monitored FROM Authors`)
	if err != nil {
		return fmt.Errorf("query Authors: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var monitored bool
		if err := rows.Scan(&name, &monitored); err != nil {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		res.Requested++

		matches, serr := agg.SearchAuthors(ctx, name)
		if serr != nil {
			res.fail(name, "metadata lookup failed: "+serr.Error())
			continue
		}
		if len(matches) == 0 {
			res.fail(name, "no OpenLibrary match")
			continue
		}
		top := matches[0]

		if existing, _ := repo.GetByForeignID(ctx, top.ForeignID); existing != nil {
			res.Skipped++
			continue
		}

		full, ferr := agg.GetAuthor(ctx, top.ForeignID)
		if ferr != nil || full == nil {
			full = &top
		}
		full.Monitored = monitored
		full.MetadataProvider = "openlibrary"

		if cerr := repo.Create(ctx, full); cerr != nil {
			res.fail(name, cerr.Error())
			continue
		}
		res.Added++
		res.AddedNames = append(res.AddedNames, full.Name)

		// Bulk imports are safe by default: always populate the catalogue
		// but never auto-grab. The user can trigger grabs manually from the
		// Wanted page after the import completes.
		if onSearchOnAdd != nil {
			go onSearchOnAdd(full)
		}
	}
	return rows.Err()
}

// readarrSettings is the minimal shape we pull out of Readarr's
// Indexers.Settings / DownloadClients.Settings JSON columns. Both tables
// store a pile of provider-specific fields — we only care about the
// connection bits.
type readarrSettings struct {
	BaseURL    string `json:"baseUrl"`
	URL        string `json:"url"`
	APIKey     string `json:"apiKey"`
	APIPath    string `json:"apiPath"`
	Categories []int  `json:"categories"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Category   string `json:"tvCategory"`
	UseSsl     bool   `json:"useSsl"`
}

func parseSettings(raw string) readarrSettings {
	var s readarrSettings
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

func importReadarrIndexers(ctx context.Context, src *sql.DB, repo *db.IndexerRepo, res *Result) error {
	rows, err := src.QueryContext(ctx, `SELECT Name, Implementation, Settings, EnableRss FROM Indexers`)
	if err != nil {
		return fmt.Errorf("query Indexers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, impl, settings string
		var enableRss bool
		if err := rows.Scan(&name, &impl, &settings, &enableRss); err != nil {
			continue
		}
		res.Requested++

		s := parseSettings(settings)
		url := s.BaseURL
		if url == "" {
			url = s.URL
		}
		if url == "" || s.APIKey == "" {
			res.fail(name, "missing URL or API key")
			continue
		}

		// Map Readarr Implementation to Bindery's type.
		t := "newznab"
		if strings.EqualFold(impl, "Torznab") {
			t = "torznab"
		}

		cats := s.Categories
		if len(cats) == 0 {
			cats = []int{7000, 7020, 3030}
		}

		idx := &models.Indexer{
			Name:       name,
			Type:       t,
			URL:        strings.TrimRight(url, "/"),
			APIKey:     s.APIKey,
			Categories: cats,
			Enabled:    enableRss,
		}
		if err := repo.Create(ctx, idx); err != nil {
			res.fail(name, err.Error())
			continue
		}
		res.Added++
		res.AddedNames = append(res.AddedNames, name)
	}
	return rows.Err()
}

func importReadarrDownloadClients(ctx context.Context, src *sql.DB, repo *db.DownloadClientRepo, res *Result) error {
	rows, err := src.QueryContext(ctx, `SELECT Name, Implementation, Settings, Enable FROM DownloadClients`)
	if err != nil {
		return fmt.Errorf("query DownloadClients: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, impl, settings string
		var enable bool
		if err := rows.Scan(&name, &impl, &settings, &enable); err != nil {
			continue
		}
		res.Requested++

		s := parseSettings(settings)
		if s.Host == "" {
			res.fail(name, "missing host")
			continue
		}

		t := "sabnzbd"
		if strings.Contains(strings.ToLower(impl), "qbittorrent") {
			t = "qbittorrent"
		}

		cat := s.Category
		if cat == "" {
			cat = "books"
		}

		// For qBittorrent, Readarr's Username/Password fields carry creds;
		// Bindery squashes credentials into the APIKey field for either
		// client so we don't need a separate Password column.
		apiKey := s.APIKey
		if apiKey == "" {
			apiKey = s.Password
		}

		c := &models.DownloadClient{
			Name:     name,
			Type:     t,
			Host:     s.Host,
			Port:     s.Port,
			APIKey:   apiKey,
			Category: cat,
			UseSSL:   s.UseSsl,
			Enabled:  enable,
		}
		if err := repo.Create(ctx, c); err != nil {
			res.fail(name, err.Error())
			continue
		}
		res.Added++
		res.AddedNames = append(res.AddedNames, name)
	}
	return rows.Err()
}

func importReadarrBlocklist(ctx context.Context, src *sql.DB, repo *db.BlocklistRepo, res *Result) error {
	// Readarr's column: SourceTitle is the release title. Message is rare.
	rows, err := src.QueryContext(ctx, `SELECT SourceTitle, Message FROM Blocklist`)
	if err != nil {
		// Some Readarr versions name it "Blacklist" — fall back.
		rows, err = src.QueryContext(ctx, `SELECT SourceTitle, Message FROM Blacklist`)
		if err != nil {
			return fmt.Errorf("query Blocklist/Blacklist: %w", err)
		}
	}
	defer rows.Close()

	for rows.Next() {
		var title, message sql.NullString
		if err := rows.Scan(&title, &message); err != nil {
			continue
		}
		t := strings.TrimSpace(title.String)
		if t == "" {
			continue
		}
		res.Requested++

		// We don't have a GUID, so use the release title as the key.
		entry := &models.BlocklistEntry{
			GUID:   t,
			Title:  t,
			Reason: message.String,
		}
		if err := repo.Create(ctx, entry); err != nil {
			res.fail(t, err.Error())
			continue
		}
		res.Added++
	}
	return rows.Err()
}
