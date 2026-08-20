package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// newReadarrDB builds a fake Readarr SQLite database with the minimal
// schema the migrate code reads from.
func newReadarrDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "readarr.db")
	src, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { src.Close() })

	stmts := []string{
		// Real Readarr schema: Authors holds Monitored + a FK to AuthorMetadata,
		// where the human-readable Name actually lives.
		`CREATE TABLE AuthorMetadata (
			Id INTEGER PRIMARY KEY,
			Name TEXT NOT NULL
		)`,
		`CREATE TABLE Authors (
			Id INTEGER PRIMARY KEY,
			Monitored INTEGER NOT NULL DEFAULT 1,
			AuthorMetadataId INTEGER NOT NULL
		)`,
		`CREATE TABLE Indexers (
			Id INTEGER PRIMARY KEY,
			Name TEXT NOT NULL,
			Implementation TEXT NOT NULL,
			Settings TEXT NOT NULL,
			EnableRss INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE DownloadClients (
			Id INTEGER PRIMARY KEY,
			Name TEXT NOT NULL,
			Implementation TEXT NOT NULL,
			Settings TEXT NOT NULL,
			Enable INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE Blocklist (
			Id INTEGER PRIMARY KEY,
			SourceTitle TEXT,
			Message TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := src.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return path
}

func TestImportReadarr_EmptyDBPath(t *testing.T) {
	_, err := ImportReadarr(context.Background(), "", nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestImportReadarr_BadPath(t *testing.T) {
	// modernc sqlite opens lazily; ping() is where it fails.
	_, err := ImportReadarr(context.Background(), "/nonexistent/does/not/exist.db",
		nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error opening missing db")
	}
}

func TestImportReadarr_HappyPath(t *testing.T) {
	path := newReadarrDB(t)

	// Seed Readarr-shaped rows.
	src, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Exec(`INSERT INTO AuthorMetadata (Id, Name) VALUES
		(1, 'Andy Weir'),
		(2, ''),
		(3, 'Duplicate Author'),
		(4, 'Isaac Asimov')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Exec(`INSERT INTO Authors (Monitored, AuthorMetadataId) VALUES
		(1, 1),
		(1, 2),
		(1, 3),
		(0, 4)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Exec(`INSERT INTO Indexers (Name, Implementation, Settings, EnableRss) VALUES
		('MyNewznab', 'Newznab', ?, 1),
		('MyTorznab',  'Torznab', ?, 1),
		('BadIdx',     'Newznab', ?, 1)`,
		`{"baseUrl":"https://news.example.com/","apiKey":"abc","categories":[7000,7020]}`,
		`{"url":"https://torr.example.com","apiKey":"xyz"}`,
		`{"baseUrl":"","apiKey":""}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Exec(`INSERT INTO DownloadClients (Name, Implementation, Settings, Enable) VALUES
		('SAB',    'Sabnzbd',     ?, 1),
		('QBT',    'QBittorrent', ?, 1),
		('NoHost', 'Sabnzbd',     ?, 1)`,
		`{"host":"sab.local","port":8080,"apiKey":"sabkey","tvCategory":"ebooks"}`,
		`{"host":"qbt.local","port":8081,"username":"u","password":"p"}`,
		`{"host":""}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Exec(`INSERT INTO Blocklist (SourceTitle, Message) VALUES
		('Bad.Release.epub', 'wrong edition'),
		('', 'skip this'),
		('Another.Release.epub', NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	src.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	indexerRepo := db.NewIndexerRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	blocklistRepo := db.NewBlocklistRepo(database)

	// Pre-seed a duplicate author so the skip path triggers.
	ctx := context.Background()
	if err := authorRepo.Create(ctx, &models.Author{
		ForeignID: "OL-Duplicate Author", Name: "Duplicate Author", SortName: "Duplicate Author",
		MetadataProvider: "openlibrary", Monitored: true,
	}); err != nil {
		t.Fatal(err)
	}

	provider := &stubProvider{
		searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
			// Isaac Asimov → no match so the fail path runs.
			if q == "Isaac Asimov" {
				return nil, nil
			}
			return []models.Author{{Name: q, SortName: q, ForeignID: "OL-" + q}}, nil
		},
		getAuthorFn: func(_ context.Context, id string) (*models.Author, error) {
			return &models.Author{Name: id[len("OL-"):], SortName: id[len("OL-"):], ForeignID: id}, nil
		},
	}
	agg := metadata.NewAggregator(provider)

	res, err := ImportReadarr(ctx, path, authorRepo, indexerRepo, clientRepo, blocklistRepo, nil, agg, nil)
	if err != nil {
		t.Fatalf("ImportReadarr: %v", err)
	}

	// Authors: Andy Weir added (1), blank skipped silently, Duplicate skipped
	// (Skipped++), Isaac failed (Errors++). Requested counts non-blank rows.
	if res.Authors.Added != 1 {
		t.Errorf("Authors.Added=%d want 1", res.Authors.Added)
	}
	if res.Authors.Skipped != 1 {
		t.Errorf("Authors.Skipped=%d want 1", res.Authors.Skipped)
	}
	if res.Authors.Errors != 1 {
		t.Errorf("Authors.Errors=%d want 1 (%v)", res.Authors.Errors, res.Authors.Failures)
	}
	if res.Authors.Requested != 3 {
		t.Errorf("Authors.Requested=%d want 3", res.Authors.Requested)
	}

	// Indexers: 2 valid added, 1 missing URL/apiKey failed.
	if res.Indexers.Added != 2 {
		t.Errorf("Indexers.Added=%d want 2", res.Indexers.Added)
	}
	if res.Indexers.Errors != 1 {
		t.Errorf("Indexers.Errors=%d want 1", res.Indexers.Errors)
	}
	// Verify Torznab mapping + categories defaulting + trailing-slash trim.
	idxList, err := indexerRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var nz, tz *models.Indexer
	for i := range idxList {
		switch idxList[i].Name {
		case "MyNewznab":
			nz = &idxList[i]
		case "MyTorznab":
			tz = &idxList[i]
		}
	}
	if nz == nil || nz.Type != "newznab" || nz.URL != "https://news.example.com" {
		t.Errorf("MyNewznab unexpected: %+v", nz)
	}
	if len(nz.Categories) != 2 || nz.Categories[0] != 7000 {
		t.Errorf("MyNewznab categories=%v", nz.Categories)
	}
	if tz == nil || tz.Type != "torznab" {
		t.Errorf("MyTorznab unexpected: %+v", tz)
	}
	// Defaulted categories for MyTorznab (none set in settings).
	if len(tz.Categories) != 3 {
		t.Errorf("MyTorznab default categories=%v want len 3", tz.Categories)
	}

	// DownloadClients: 2 added (SAB, QBT), 1 failed (empty host).
	if res.DownloadClients.Added != 2 {
		t.Errorf("DownloadClients.Added=%d want 2", res.DownloadClients.Added)
	}
	if res.DownloadClients.Errors != 1 {
		t.Errorf("DownloadClients.Errors=%d want 1", res.DownloadClients.Errors)
	}
	clients, err := clientRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sab, qbt *models.DownloadClient
	for i := range clients {
		switch clients[i].Name {
		case "SAB":
			sab = &clients[i]
		case "QBT":
			qbt = &clients[i]
		}
	}
	if sab == nil || sab.Type != "sabnzbd" || sab.Category != "ebooks" || sab.APIKey != "sabkey" {
		t.Errorf("SAB unexpected: %+v", sab)
	}
	if qbt == nil || qbt.Type != "qbittorrent" || qbt.Username != "u" || qbt.Password != "p" {
		t.Errorf("QBT unexpected: %+v", qbt)
	}
	// QBT has no tvCategory — should default to "books".
	if qbt.Category != "books" {
		t.Errorf("QBT default category=%q want 'books'", qbt.Category)
	}

	// Blocklist: 2 added (blank SourceTitle skipped silently, not counted).
	if res.Blocklist.Added != 2 {
		t.Errorf("Blocklist.Added=%d want 2", res.Blocklist.Added)
	}
}

func TestImportReadarr_SkipsDuplicateAlternateIdentifier(t *testing.T) {
	path := newReadarrDB(t)
	src, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec(`INSERT INTO AuthorMetadata (Id, Name) VALUES (1, 'Duplicate Author')`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec(`INSERT INTO Authors (Monitored, AuthorMetadataId) VALUES (1, 1)`); err != nil {
		t.Fatal(err)
	}
	src.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authorRepo := db.NewAuthorRepo(database)
	ctx := context.Background()
	existing := &models.Author{
		ForeignID:        "hc:duplicate-author",
		Name:             "Duplicate Author",
		SortName:         "Duplicate Author",
		MetadataProvider: "hardcover",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, existing.ID, "OL-Duplicate Author"); err != nil {
		t.Fatalf("seed author identifier: %v", err)
	}
	agg := metadata.NewAggregator(&stubProvider{
		searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
			return []models.Author{{Name: q, SortName: q, ForeignID: "OL-" + q}}, nil
		},
	})

	res, err := ImportReadarr(ctx, path, authorRepo, db.NewIndexerRepo(database), db.NewDownloadClientRepo(database), db.NewBlocklistRepo(database), nil, agg, nil)
	if err != nil {
		t.Fatalf("ImportReadarr: %v", err)
	}
	if res.Authors.Skipped != 1 || res.Authors.Added != 0 || res.Authors.Errors != 0 {
		t.Fatalf("authors result = %+v, want one skipped alternate duplicate", res.Authors)
	}
	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want existing author reused", len(authors))
	}
}

func TestImportReadarr_BlacklistFallback(t *testing.T) {
	// Older Readarr installs use table name "Blacklist".
	dir := t.TempDir()
	path := filepath.Join(dir, "readarr.db")
	src, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE AuthorMetadata (Id INTEGER PRIMARY KEY, Name TEXT)`,
		`CREATE TABLE Authors (Id INTEGER PRIMARY KEY, Monitored INTEGER, AuthorMetadataId INTEGER)`,
		`CREATE TABLE Indexers (Id INTEGER PRIMARY KEY, Name TEXT, Implementation TEXT, Settings TEXT, EnableRss INTEGER)`,
		`CREATE TABLE DownloadClients (Id INTEGER PRIMARY KEY, Name TEXT, Implementation TEXT, Settings TEXT, Enable INTEGER)`,
		`CREATE TABLE Blacklist (Id INTEGER PRIMARY KEY, SourceTitle TEXT, Message TEXT)`,
		`INSERT INTO Blacklist (SourceTitle, Message) VALUES ('Legacy.Release.epub', 'old school')`,
	} {
		if _, err := src.Exec(stmt); err != nil {
			t.Fatalf("schema/seed: %v", err)
		}
	}
	src.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	res, err := ImportReadarr(
		context.Background(), path,
		db.NewAuthorRepo(database),
		db.NewIndexerRepo(database),
		db.NewDownloadClientRepo(database),
		db.NewBlocklistRepo(database),
		nil,
		metadata.NewAggregator(&stubProvider{}),
		nil,
	)
	if err != nil {
		t.Fatalf("ImportReadarr: %v", err)
	}
	if res.Blocklist.Added != 1 {
		t.Errorf("Blocklist.Added=%d want 1", res.Blocklist.Added)
	}
}

func TestParseSettings_BadJSON(t *testing.T) {
	// Invalid JSON should not panic — just return a zero-value settings struct.
	got := parseSettings("this is not json")
	if got.BaseURL != "" || got.APIKey != "" || got.Host != "" {
		t.Errorf("expected zero-value settings on bad JSON, got %+v", got)
	}
}

func TestParseSettings_Roundtrip(t *testing.T) {
	raw := `{"baseUrl":"https://a","url":"https://b","apiKey":"k","host":"h","port":9,"username":"u","password":"p","tvCategory":"books","useSsl":true,"categories":[1,2,3]}`
	got := parseSettings(raw)
	if got.BaseURL != "https://a" || got.URL != "https://b" || got.APIKey != "k" {
		t.Errorf("URL fields off: %+v", got)
	}
	if got.Host != "h" || got.Port != 9 || got.Username != "u" || got.Password != "p" {
		t.Errorf("connection fields off: %+v", got)
	}
	if got.Category != "books" || !got.UseSsl {
		t.Errorf("category/ssl off: %+v", got)
	}
	if len(got.Categories) != 3 || got.Categories[2] != 3 {
		t.Errorf("categories off: %+v", got)
	}
}

// TestImportReadarr_CatalogueFetchIsBoundedAndSurvivesCancellation covers the
// same #2075 fan-out fix as the CSV importer's tests: importReadarrAuthors
// used to fire `go onSearchOnAdd(full)` per author with no cap at all, and
// used ctx directly rather than a detached context, so a large Readarr
// library could storm OpenLibrary and also have its catalogue backfill cut
// short by ImportReadarr's own caller cancelling ctx.
func TestImportReadarr_CatalogueFetchIsBoundedAndSurvivesCancellation(t *testing.T) {
	catalogueFetchPaceInterval = 0
	t.Cleanup(func() { catalogueFetchPaceInterval = 3 * time.Second })

	path := newReadarrDB(t)
	src, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	const authorCount = catalogueFetchConcurrency * 2
	var metaRows, authorRows strings.Builder
	for i := 0; i < authorCount; i++ {
		if i > 0 {
			metaRows.WriteString(",")
			authorRows.WriteString(",")
		}
		fmt.Fprintf(&metaRows, "(%d, 'Author %d')", i+1, i)
		fmt.Fprintf(&authorRows, "(1, %d)", i+1)
	}
	if _, err := src.Exec(`INSERT INTO AuthorMetadata (Id, Name) VALUES ` + metaRows.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec(`INSERT INTO Authors (Monitored, AuthorMetadataId) VALUES ` + authorRows.String()); err != nil {
		t.Fatal(err)
	}
	src.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authorRepo := db.NewAuthorRepo(database)

	agg := metadata.NewAggregator(&stubProvider{
		searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
			return []models.Author{{Name: q, SortName: q, ForeignID: "OL-" + q}}, nil
		},
	})

	var inFlight, maxInFlight int32
	var wg sync.WaitGroup
	wg.Add(authorCount)
	release := make(chan struct{})
	onSearchOnAdd := func(*models.Author) {
		defer wg.Done()
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if cur <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, cur) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	res, err := ImportReadarr(ctx, path, authorRepo, nil, nil, nil, nil, agg, onSearchOnAdd)
	if err != nil {
		t.Fatalf("ImportReadarr: %v", err)
	}
	if res.Authors.Added != authorCount {
		t.Fatalf("Authors.Added=%d want %d (failures: %v)", res.Authors.Added, authorCount, res.Authors.Failures)
	}

	// Same simulated-handler-return-and-cancel as the CSV test: proves the
	// fan-out survives the caller's context being cancelled right after
	// ImportReadarr returns.
	cancel()

	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&inFlight); got > catalogueFetchConcurrency {
		close(release)
		t.Fatalf("in-flight callbacks = %d, want <= %d (catalogueFetchConcurrency)", got, catalogueFetchConcurrency)
	}
	close(release)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("not all catalogue-fetch callbacks completed")
	}
	if max := atomic.LoadInt32(&maxInFlight); max > catalogueFetchConcurrency || max == 0 {
		t.Fatalf("max in-flight = %d, want in (0, %d]", max, catalogueFetchConcurrency)
	}
}
