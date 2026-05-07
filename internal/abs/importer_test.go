package abs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func newABSImporterFixture(t *testing.T) (*Importer, *db.AuthorRepo, *db.BookRepo, *db.SeriesRepo, *db.EditionRepo, *db.ABSProvenanceRepo, *db.ABSImportRunRepo, *db.ABSImportRunEntityRepo, *db.ABSReviewItemRepo, *db.ABSMetadataConflictRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)
	editionRepo := db.NewEditionRepo(database)
	provenanceRepo := db.NewABSProvenanceRepo(database)
	runRepo := db.NewABSImportRunRepo(database)
	runEntityRepo := db.NewABSImportRunEntityRepo(database)
	reviewRepo := db.NewABSReviewItemRepo(database)
	conflictRepo := db.NewABSMetadataConflictRepo(database)

	importer := NewImporter(
		authorRepo,
		db.NewAuthorAliasRepo(database),
		bookRepo,
		editionRepo,
		seriesRepo,
		db.NewSettingsRepo(database),
		runRepo,
		runEntityRepo,
		provenanceRepo,
		reviewRepo,
		conflictRepo,
	)
	return importer, authorRepo, bookRepo, seriesRepo, editionRepo, provenanceRepo, runRepo, runEntityRepo, reviewRepo, conflictRepo
}

func sampleABSItem() NormalizedLibraryItem {
	return NormalizedLibraryItem{
		ItemID:        "li-project-hail-mary",
		LibraryID:     "lib-books",
		Title:         "Project Hail Mary",
		Description:   "A lone astronaut must save the earth.",
		Language:      "eng",
		ASIN:          "B08FHBV4ZX",
		PublishedDate: "2021-05-04",
		Authors: []NormalizedAuthor{
			{ID: "author-andy-weir", Name: "Andy Weir"},
		},
		Series: []NormalizedSeries{
			{ID: "series-bobiverse", Name: "Standalone", Sequence: "1"},
		},
		Narrators: []string{"Ray Porter"},
		AudioFiles: []NormalizedAudioFile{
			{INO: "audio-1", Path: "/abs/Project Hail Mary/part1.m4b"},
			{INO: "audio-2", Path: "/abs/Project Hail Mary/part2.m4b"},
		},
		EbookPath:       "/abs/Project Hail Mary/book.epub",
		EbookINO:        "ebook-1",
		DurationSeconds: 57600,
	}
}

func mustJSONForTest(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("marshal json produced empty payload")
	}
	return string(data)
}

func runSingleABSImport(t *testing.T, importer *Importer, item NormalizedLibraryItem) int64 {
	t.Helper()
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	if _, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	runs, err := importer.RecentRuns(context.Background(), 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("RecentRuns = %d err=%v, want 1 run", len(runs), err)
	}
	return runs[0].ID
}

func enableHardcoverSeriesMatching(t *testing.T, importer *Importer) {
	t.Helper()
	importer.WithEnhancedHardcoverSeriesEnabled(func(context.Context) bool { return true })
}

func TestImporter_ProgressResultsKeepsLatestHundredItems(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	const itemCount = 125
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for idx := 0; idx < itemCount; idx++ {
			item := sampleABSItem()
			item.ItemID = fmt.Sprintf("li-progress-%03d", idx)
			item.Title = fmt.Sprintf("Progress Book %03d", idx)
			item.ASIN = fmt.Sprintf("ASIN-PROGRESS-%03d", idx)
			item.Authors = []NormalizedAuthor{{
				ID:   fmt.Sprintf("author-progress-%03d", idx),
				Name: fmt.Sprintf("Progress Author %03d", idx),
			}}
			item.Series = nil
			item.Path = ""
			item.AudioFiles = nil
			item.EbookPath = ""
			item.EbookINO = ""
			if err := fn(ctx, item); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{PagesScanned: 3, ItemsSeen: itemCount, ItemsNormalized: itemCount}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: "lib-books",
		Label:     "Shelf",
		Enabled:   true,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.BooksCreated != itemCount || stats.AuthorsCreated != itemCount {
		t.Fatalf("stats = %+v, want %d created books and authors", stats, itemCount)
	}

	progress := importer.Progress()
	if progress.Processed != itemCount {
		t.Fatalf("processed = %d, want %d", progress.Processed, itemCount)
	}
	if progress.Stats == nil {
		t.Fatal("progress stats = nil, want final stats")
	}
	if progress.Stats.ItemsSeen != itemCount || progress.Stats.BooksCreated != itemCount {
		t.Fatalf("progress stats = %+v, want full-run counts", progress.Stats)
	}
	if len(progress.Results) != 100 {
		t.Fatalf("results = %d, want 100", len(progress.Results))
	}
	for idx, result := range progress.Results {
		wantItemID := fmt.Sprintf("li-progress-%03d", idx+25)
		if result.ItemID != wantItemID {
			t.Fatalf("results[%d].ItemID = %q, want %q", idx, result.ItemID, wantItemID)
		}
	}
}

func TestImporter_ResumeInterruptedStartsFromPersistedCheckpoint(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, runRepo, _, _, _ := newABSImporterFixture(t)
	checkpoint := ImportCheckpoint{
		LibraryID:  "lib-books",
		Page:       1,
		LastItemID: "li-before-restart",
		PageSize:   50,
		UpdatedAt:  time.Now().UTC(),
	}
	run := &models.ABSImportRun{
		SourceID:    DefaultSourceID,
		SourceLabel: "Shelf",
		BaseURL:     "https://abs.example.com",
		LibraryID:   "lib-books",
		Status:      runStatusRunning,
		DryRun:      true,
		SourceConfigJSON: mustJSONForTest(t, sourceSnapshot(ImportConfig{
			SourceID:  DefaultSourceID,
			BaseURL:   "https://abs.example.com",
			LibraryID: "lib-books",
			Label:     "Shelf",
			Enabled:   true,
			DryRun:    true,
		})),
		CheckpointJSON: mustJSONForTest(t, checkpoint),
		SummaryJSON:    "{}",
	}
	if err := runRepo.Create(context.Background(), run); err != nil {
		t.Fatalf("Create interrupted run: %v", err)
	}

	started := make(chan struct{})
	statusAtStart := make(chan string, 1)
	release := make(chan struct{})
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if libraryID != "lib-books" {
			t.Errorf("libraryID = %q, want lib-books", libraryID)
		}
		staleRun, err := runRepo.GetByID(ctx, run.ID)
		if err != nil {
			t.Errorf("GetByID stale run at start: %v", err)
			statusAtStart <- ""
		} else if staleRun == nil {
			t.Errorf("stale run missing at resumed import start")
			statusAtStart <- ""
		} else {
			statusAtStart <- staleRun.Status
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return EnumerationStats{}, ctx.Err()
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	resumed, err := importer.ResumeInterrupted(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		APIKey:    "secret",
		Enabled:   true,
		BaseURL:   "https://current-settings.example.com",
		LibraryID: "current-lib",
		Label:     "Current Settings",
	})
	if err != nil {
		t.Fatalf("ResumeInterrupted: %v", err)
	}
	if !resumed {
		t.Fatal("ResumeInterrupted resumed = false, want true")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resumed import to start")
	}
	if status := <-statusAtStart; status != runStatusFailed {
		t.Fatalf("stale run status at resumed import start = %q, want failed", status)
	}

	progress := importer.Progress()
	if !progress.Running || !progress.ResumedFromCheckpoint {
		t.Fatalf("progress = %+v, want running resumed import", progress)
	}
	if progress.Checkpoint == nil || progress.Checkpoint.LastItemID != checkpoint.LastItemID {
		t.Fatalf("checkpoint = %+v, want %s", progress.Checkpoint, checkpoint.LastItemID)
	}
	setting, err := importer.settings.Get(context.Background(), SettingABSImportCheckpoint)
	if err != nil {
		t.Fatalf("settings.Get checkpoint: %v", err)
	}
	if setting == nil || !strings.Contains(setting.Value, checkpoint.LastItemID) {
		t.Fatalf("checkpoint setting = %+v, want restored checkpoint", setting)
	}
	staleRun, err := runRepo.GetByID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByID stale run: %v", err)
	}
	if staleRun == nil || staleRun.Status != runStatusFailed {
		t.Fatalf("stale run = %+v, want failed status", staleRun)
	}

	close(release)
	deadline := time.After(time.Second)
	for importer.Running() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for resumed import to finish")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestImporter_ResumeInterruptedDoesNotStartIfMarkingStaleRunFailedFails(t *testing.T) {
	t.Parallel()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	importer := NewImporter(
		authorRepo,
		db.NewAuthorAliasRepo(database),
		db.NewBookRepo(database),
		db.NewEditionRepo(database),
		db.NewSeriesRepo(database),
		db.NewSettingsRepo(database),
		db.NewABSImportRunRepo(database),
		db.NewABSImportRunEntityRepo(database),
		db.NewABSProvenanceRepo(database),
		db.NewABSReviewItemRepo(database),
		db.NewABSMetadataConflictRepo(database),
	)
	runRepo := db.NewABSImportRunRepo(database)
	checkpoint := ImportCheckpoint{
		LibraryID:  "lib-books",
		Page:       1,
		LastItemID: "li-before-restart",
		PageSize:   50,
		UpdatedAt:  time.Now().UTC(),
	}
	run := &models.ABSImportRun{
		SourceID:    DefaultSourceID,
		SourceLabel: "Shelf",
		BaseURL:     "https://abs.example.com",
		LibraryID:   "lib-books",
		Status:      runStatusRunning,
		DryRun:      true,
		SourceConfigJSON: mustJSONForTest(t, sourceSnapshot(ImportConfig{
			SourceID:  DefaultSourceID,
			BaseURL:   "https://abs.example.com",
			LibraryID: "lib-books",
			Label:     "Shelf",
			Enabled:   true,
			DryRun:    true,
		})),
		CheckpointJSON: mustJSONForTest(t, checkpoint),
		SummaryJSON:    "{}",
	}
	if err := runRepo.Create(context.Background(), run); err != nil {
		t.Fatalf("Create interrupted run: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `
		CREATE TRIGGER fail_abs_resume_finish
		BEFORE UPDATE OF status ON abs_import_runs
		WHEN NEW.status = 'failed'
		BEGIN
			SELECT RAISE(ABORT, 'blocked stale failure');
		END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	started := make(chan struct{})
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		close(started)
		return EnumerationStats{}, nil
	}
	resumed, err := importer.ResumeInterrupted(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		APIKey:    "secret",
		Enabled:   true,
		BaseURL:   "https://current-settings.example.com",
		LibraryID: "current-lib",
		Label:     "Current Settings",
	})
	if err == nil || !strings.Contains(err.Error(), "blocked stale failure") {
		t.Fatalf("ResumeInterrupted error = %v, want trigger failure", err)
	}
	if !resumed {
		t.Fatal("ResumeInterrupted resumed = false, want true")
	}
	select {
	case <-started:
		t.Fatal("resumed import started after stale run failure was not persisted")
	default:
	}
	if importer.Running() {
		t.Fatal("importer is running after stale run failure was not persisted")
	}
	staleRun, err := runRepo.GetByID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByID stale run: %v", err)
	}
	if staleRun == nil || staleRun.Status != runStatusRunning {
		t.Fatalf("stale run = %+v, want still running after failed status update", staleRun)
	}
	runs, err := runRepo.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want no replacement run", runs)
	}
}

func TestImporter_RecordSnapshotReturnsRunEntityMetadataMarshalError(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, _, runRepo, runEntityRepo, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	run := &models.ABSImportRun{
		SourceID:    DefaultSourceID,
		SourceLabel: "Shelf",
		BaseURL:     "https://abs.example.com",
		LibraryID:   "lib-books",
		Status:      runStatusRunning,
	}
	if err := runRepo.Create(ctx, run); err != nil {
		t.Fatalf("Create run: %v", err)
	}
	author := &models.Author{
		ForeignID:        "OL-BAD-META",
		Name:             "Bad Metadata",
		SortName:         "Metadata, Bad",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	item := sampleABSItem()
	err := importer.recordAuthorBeforeSnapshot(ctx, run.ID, ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}, item, "author-bad-meta", author, itemOutcomeLinked, map[string]any{
		"invalid": func() {},
	})
	if err == nil || !strings.Contains(err.Error(), "encode abs import run entity metadata") {
		t.Fatalf("recordAuthorBeforeSnapshot error = %v, want metadata marshal error", err)
	}
	entities, err := runEntityRepo.ListByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(entities) != 0 {
		t.Fatalf("entities = %+v, want invalid metadata not stored", entities)
	}
}

func mustDate(t *testing.T, value string) *time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatalf("parse date %q: %v", value, err)
	}
	utc := parsed.UTC()
	return &utc
}

func rollbackActionSignatures(actions []RollbackAction) []string {
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		out = append(out, action.EntityType+":"+action.ExternalID+":"+action.Action)
	}
	return out
}

func requireStringSlicesEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for idx := range got {
		if got[idx] != want[idx] {
			t.Fatalf("actions = %v, want %v", got, want)
		}
	}
}

type stubABSMetadataProvider struct {
	name                 string
	searchAuthors        []models.Author
	searchAuthorsByQuery map[string][]models.Author
	authors              map[string]*models.Author
	books                map[string]*models.Book
	booksByISBN          map[string]*models.Book
	works                map[string][]models.Book
	series               map[string][]metadata.SeriesSearchResult
	catalogs             map[string]*metadata.SeriesCatalog
	searchSeriesCalls    int
}

func (p *stubABSMetadataProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "stub"
}
func (p *stubABSMetadataProvider) SearchAuthors(_ context.Context, query string) ([]models.Author, error) {
	if p.searchAuthorsByQuery != nil {
		return append([]models.Author(nil), p.searchAuthorsByQuery[query]...), nil
	}
	return append([]models.Author(nil), p.searchAuthors...), nil
}
func (p *stubABSMetadataProvider) SearchBooks(context.Context, string) ([]models.Book, error) {
	return nil, nil
}
func (p *stubABSMetadataProvider) GetAuthor(_ context.Context, foreignID string) (*models.Author, error) {
	if p.authors == nil {
		return nil, nil
	}
	return p.authors[foreignID], nil
}
func (p *stubABSMetadataProvider) GetBook(_ context.Context, foreignID string) (*models.Book, error) {
	if p.books == nil {
		return nil, nil
	}
	return p.books[foreignID], nil
}
func (p *stubABSMetadataProvider) GetEditions(context.Context, string) ([]models.Edition, error) {
	return nil, nil
}
func (p *stubABSMetadataProvider) GetBookByISBN(_ context.Context, isbn string) (*models.Book, error) {
	if p.booksByISBN == nil {
		return nil, nil
	}
	return p.booksByISBN[isbn], nil
}
func (p *stubABSMetadataProvider) GetAuthorWorks(_ context.Context, foreignID string) ([]models.Book, error) {
	if p.works == nil {
		return nil, nil
	}
	return append([]models.Book(nil), p.works[foreignID]...), nil
}
func (p *stubABSMetadataProvider) SearchSeries(_ context.Context, query string, limit int) ([]metadata.SeriesSearchResult, error) {
	p.searchSeriesCalls++
	if p.series == nil {
		return nil, nil
	}
	results := append([]metadata.SeriesSearchResult(nil), p.series[query]...)
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
func (p *stubABSMetadataProvider) GetSeriesCatalog(_ context.Context, foreignID string) (*metadata.SeriesCatalog, error) {
	if p.catalogs == nil {
		return nil, nil
	}
	return p.catalogs[foreignID], nil
}

func TestImporter_NormalizedAuthorMatchLinksExistingAuthor(t *testing.T) {
	t.Parallel()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	bookRepo := db.NewBookRepo(database)
	importer := NewImporter(
		authorRepo,
		aliasRepo,
		bookRepo,
		db.NewEditionRepo(database),
		db.NewSeriesRepo(database),
		db.NewSettingsRepo(database),
		db.NewABSImportRunRepo(database),
		db.NewABSImportRunEntityRepo(database),
		db.NewABSProvenanceRepo(database),
		db.NewABSReviewItemRepo(database),
		db.NewABSMetadataConflictRepo(database),
	)

	existing := &models.Author{
		ForeignID:        "OL23919A",
		Name:             "J. K. Rowling",
		SortName:         "Rowling, J. K.",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}

	item := sampleABSItem()
	item.ItemID = "li-rowling-1"
	item.Title = "Harry Potter and the Philosopher's Stone"
	item.Authors = []NormalizedAuthor{{ID: "author-rowling", Name: "J.K. Rowling"}}
	item.AudioFiles = nil
	item.ASIN = ""
	item.EbookPath = "/abs/HP1.epub"
	item.EbookINO = "ebook-rowling-1"
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.AuthorsCreated != 0 || stats.AuthorsLinked != 1 {
		t.Fatalf("stats = %+v, want linked existing author", stats)
	}
	authors, err := authorRepo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1", len(authors))
	}
	aliases, err := aliasRepo.ListByAuthor(context.Background(), authors[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Name != "J.K. Rowling" {
		t.Fatalf("aliases = %+v, want J.K. Rowling alias", aliases)
	}
}

func TestImporter_FindAuthorByName_MatchingTiers(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	for _, author := range []*models.Author{
		{ForeignID: "OL-RR", Name: "R.R. Haywood", SortName: "Haywood, R.R.", Monitored: true},
		{ForeignID: "OL-WEIR", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true},
		{ForeignID: "OL-SMITH", Name: "John Smith", SortName: "Smith, John", Monitored: true},
		{ForeignID: "OL-SANDERSON", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", Monitored: true},
		{ForeignID: "OL-JAMES", Name: "Alice James", SortName: "James, Alice", Monitored: true},
	} {
		if err := authorRepo.Create(ctx, author); err != nil {
			t.Fatalf("Create author %q: %v", author.Name, err)
		}
	}

	cases := []struct {
		name      string
		query     string
		wantID    string
		wantMatch string
	}{
		{name: "exact normalized initials", query: "RR Haywood", wantID: "OL-RR", wantMatch: "normalized_name"},
		{name: "spaced initials last first", query: "Haywood, R.R.", wantID: "OL-RR", wantMatch: "normalized_name"},
		{name: "last first", query: "Weir, Andy", wantID: "OL-WEIR", wantMatch: "normalized_name"},
		{name: "suffix stripped", query: "John Smith III", wantID: "OL-SMITH", wantMatch: "normalized_name"},
		{name: "fuzzy auto", query: "Brandon Sandersen", wantID: "OL-SANDERSON", wantMatch: "fuzzy_name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matchedBy, ambiguous, err := importer.findAuthorByName(ctx, tc.query)
			if err != nil {
				t.Fatalf("findAuthorByName: %v", err)
			}
			if ambiguous {
				t.Fatal("findAuthorByName returned ambiguous=true, want safe match")
			}
			if got == nil || got.ForeignID != tc.wantID || matchedBy != tc.wantMatch {
				t.Fatalf("findAuthorByName(%q) = author=%+v matchedBy=%q, want %s/%s", tc.query, got, matchedBy, tc.wantID, tc.wantMatch)
			}
		})
	}

	got, matchedBy, ambiguous, err := importer.findAuthorByName(ctx, "Alice Jones")
	if err != nil {
		t.Fatalf("findAuthorByName ambiguous: %v", err)
	}
	if got != nil || matchedBy != "" || !ambiguous {
		t.Fatalf("findAuthorByName ambiguous = author=%+v matchedBy=%q ambiguous=%v, want review path", got, matchedBy, ambiguous)
	}
}

func TestImporter_DoesNotUseSecondaryAuthorsAsPrimaryIdentity(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()

	first := sampleABSItem()
	first.ItemID = "li-cache-first"
	first.Title = "Cache First"
	first.ASIN = "ASIN-CACHE-FIRST"
	first.Authors = []NormalizedAuthor{
		{ID: "author-cache-primary", Name: "Cache Primary"},
		{ID: "author-cache-alias", Name: "Cache Pen Name"},
	}
	first.Series = nil
	first.AudioFiles = []NormalizedAudioFile{{INO: "audio-cache-first", Path: "/abs/cache/first.m4b"}}
	first.EbookPath = ""
	first.EbookINO = ""

	second := sampleABSItem()
	second.ItemID = "li-cache-second"
	second.Title = "Cache Second"
	second.ASIN = "ASIN-CACHE-SECOND"
	second.Authors = []NormalizedAuthor{{ID: "author-cache-second", Name: "Cache Pen Name"}}
	second.Series = nil
	second.AudioFiles = []NormalizedAudioFile{{INO: "audio-cache-second", Path: "/abs/cache/second.m4b"}}
	second.EbookPath = ""
	second.EbookINO = ""

	items := []NormalizedLibraryItem{first, second}
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for _, item := range items {
			if err := fn(ctx, item); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: len(items), ItemsNormalized: len(items)}, nil
	}

	stats, err := importer.Run(ctx, ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: first.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.AuthorsCreated != 2 || stats.AuthorsLinked != 0 {
		t.Fatalf("stats = %+v, want two created authors and no linked authors", stats)
	}

	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 2 {
		t.Fatalf("authors = %+v, want Cache Primary and Cache Pen Name", authors)
	}
	var primaryID int64
	authorNames := make(map[string]bool)
	for _, author := range authors {
		authorNames[author.Name] = true
		if author.Name == "Cache Primary" {
			primaryID = author.ID
		}
	}
	if !authorNames["Cache Primary"] || !authorNames["Cache Pen Name"] {
		t.Fatalf("authors = %+v, want Cache Primary and Cache Pen Name", authors)
	}
	aliases, err := importer.aliases.ListByAuthor(ctx, primaryID)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("aliases = %+v, want arbitrary secondary author not recorded as alias", aliases)
	}

	progress := importer.Progress()
	if len(progress.Results) != 2 || progress.Results[1].MatchedBy != "created" {
		t.Fatalf("progress results = %+v, want second item created separately", progress.Results)
	}
}

func TestImporter_ABSAuthorIdentityCorruptionRegression(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	makeItem := func(itemID, title, asin, authorID, authorName string, extras ...NormalizedAuthor) NormalizedLibraryItem {
		item := sampleABSItem()
		item.ItemID = itemID
		item.Title = title
		item.ASIN = asin
		item.Authors = append([]NormalizedAuthor{{ID: authorID, Name: authorName}}, extras...)
		item.Series = nil
		item.AudioFiles = []NormalizedAudioFile{{INO: "audio-" + itemID, Path: "/abs/" + itemID + ".m4b"}}
		item.EbookPath = ""
		item.EbookINO = ""
		return item
	}
	items := []NormalizedLibraryItem{
		makeItem("li-wheel", "The Gathering Storm", "ASIN-WHEEL", "author-robert-jordan", "Robert Jordan", NormalizedAuthor{ID: "author-brandon-sanderson", Name: "Brandon Sanderson"}),
		makeItem("li-mistborn", "Mistborn", "ASIN-MISTBORN", "author-brandon-sanderson", "Brandon Sanderson"),
		makeItem("li-graphic", "GraphicAudio Production", "ASIN-GRAPHIC", "author-graphic-audio", "GraphicAudio"),
	}
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for _, item := range items {
			if err := fn(ctx, item); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: len(items), ItemsNormalized: len(items)}, nil
	}

	stats, err := importer.Run(ctx, ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: "lib-books",
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.AuthorsCreated != 3 || stats.AuthorsLinked != 0 {
		t.Fatalf("stats = %+v, want three independent authors", stats)
	}

	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	authorsByName := make(map[string]models.Author)
	for _, author := range authors {
		authorsByName[author.Name] = author
	}
	for _, name := range []string{"Robert Jordan", "Brandon Sanderson", "GraphicAudio"} {
		if authorsByName[name].ID == 0 {
			t.Fatalf("authors = %+v, missing %s", authors, name)
		}
	}

	books, err := bookRepo.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatalf("List books: %v", err)
	}
	booksByTitle := make(map[string]models.Book)
	for _, book := range books {
		booksByTitle[book.Title] = book
	}
	if booksByTitle["Mistborn"].AuthorID != authorsByName["Brandon Sanderson"].ID {
		t.Fatalf("Mistborn author_id = %d, want Brandon Sanderson %d", booksByTitle["Mistborn"].AuthorID, authorsByName["Brandon Sanderson"].ID)
	}
	if booksByTitle["Mistborn"].AuthorID == authorsByName["GraphicAudio"].ID {
		t.Fatal("Mistborn was attached to GraphicAudio")
	}
}

func TestImporter_RepairsStaleABSAuthorProvenance(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	wrong := &models.Author{
		ForeignID:        "OL-GRAPHIC",
		Name:             "GraphicAudio",
		SortName:         "GraphicAudio",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, wrong); err != nil {
		t.Fatalf("Create wrong author: %v", err)
	}
	correct := &models.Author{
		ForeignID:        "OL-BRANDON",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, correct); err != nil {
		t.Fatalf("Create correct author: %v", err)
	}
	if err := importer.aliases.Create(ctx, &models.AuthorAlias{AuthorID: wrong.ID, Name: "Brandon Sanderson"}); err != nil {
		t.Fatalf("Create bad alias: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-arcanum"
	item.Title = "Arcanum Unbounded: The Cosmere Collection"
	item.ASIN = "B01K5Q6VWO"
	item.Authors = []NormalizedAuthor{{ID: "author-brandon-sanderson", Name: "Brandon Sanderson"}}
	item.Series = nil
	item.AudioFiles = []NormalizedAudioFile{{INO: "audio-arcanum", Path: "/abs/arcanum.m4b"}}
	item.EbookPath = ""
	item.EbookINO = ""

	book := &models.Book{
		ForeignID:        absForeignID("book", item.LibraryID, item.ItemID),
		AuthorID:         wrong.ID,
		Title:            item.Title,
		SortTitle:        item.Title,
		Monitored:        true,
		Status:           models.BookStatusWanted,
		AnyEditionOK:     true,
		MediaType:        models.MediaTypeAudiobook,
		ASIN:             item.ASIN,
		MetadataProvider: providerAudiobookshelf,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatalf("Create bad book: %v", err)
	}
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID:   DefaultSourceID,
		LibraryID:  item.LibraryID,
		EntityType: entityTypeAuthor,
		ExternalID: authorExternalID(item.Authors[0]),
		LocalID:    wrong.ID,
		ItemID:     item.ItemID,
	}); err != nil {
		t.Fatalf("Upsert bad author provenance: %v", err)
	}
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID:   DefaultSourceID,
		LibraryID:  item.LibraryID,
		EntityType: entityTypeBook,
		ExternalID: item.ItemID,
		LocalID:    book.ID,
		ItemID:     item.ItemID,
	}); err != nil {
		t.Fatalf("Upsert book provenance: %v", err)
	}

	runSingleABSImport(t, importer, item)

	updated, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.AuthorID != correct.ID {
		t.Fatalf("book author_id = %d, want corrected Brandon Sanderson author %d", updated.AuthorID, correct.ID)
	}
	link, err := provenanceRepo.GetByExternal(ctx, DefaultSourceID, item.LibraryID, entityTypeAuthor, authorExternalID(item.Authors[0]))
	if err != nil {
		t.Fatalf("GetByExternal author provenance: %v", err)
	}
	if link == nil || link.LocalID != correct.ID {
		t.Fatalf("author provenance = %+v, want local_id %d", link, correct.ID)
	}
}

func TestImporter_TrustedSourceAliasStillMatchesAuthor(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	existing := &models.Author{
		ForeignID:        "OL-TWAIN",
		Name:             "Mark Twain",
		SortName:         "Twain, Mark",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	if err := importer.aliases.Create(ctx, &models.AuthorAlias{AuthorID: existing.ID, Name: "Samuel Clemens", SourceOLID: "OL-CLEMENS"}); err != nil {
		t.Fatalf("Create trusted alias: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-huck"
	item.Title = "Adventures of Huckleberry Finn"
	item.ASIN = "ASIN-HUCK"
	item.Authors = []NormalizedAuthor{{ID: "author-samuel-clemens", Name: "Samuel Clemens"}}
	item.Series = nil
	item.AudioFiles = []NormalizedAudioFile{{INO: "audio-huck", Path: "/abs/huck.m4b"}}
	item.EbookPath = ""
	item.EbookINO = ""

	runSingleABSImport(t, importer, item)

	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 || authors[0].ID != existing.ID {
		t.Fatalf("authors = %+v, want existing Mark Twain only", authors)
	}
	books, err := bookRepo.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatalf("List books: %v", err)
	}
	if len(books) != 1 || books[0].AuthorID != existing.ID {
		t.Fatalf("books = %+v, want book linked to trusted alias author", books)
	}
	progress := importer.Progress()
	if len(progress.Results) != 1 || progress.Results[0].MatchedBy != "alias" {
		t.Fatalf("progress results = %+v, want alias match", progress.Results)
	}
}

func TestImporter_EnrichAuthorSkipsRelinkForUntrustedAliasOnlyMatch(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	existing := &models.Author{
		ForeignID:        "OL-CACHE-PRIMARY",
		Name:             "Cache Primary",
		SortName:         "Primary, Cache",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	if err := importer.aliases.Create(ctx, &models.AuthorAlias{AuthorID: existing.ID, Name: "Cache Pen Name"}); err != nil {
		t.Fatalf("Create untrusted alias: %v", err)
	}
	importer.WithMetadata(metadata.NewAggregator(&stubABSMetadataProvider{
		searchAuthorsByQuery: map[string][]models.Author{
			"Cache Pen Name": {{ForeignID: "OL-CACHE-PEN", Name: "Cache Pen Name"}},
		},
		authors: map[string]*models.Author{
			"OL-CACHE-PEN": {
				ForeignID:        "OL-CACHE-PEN",
				Name:             "Cache Pen Name",
				SortName:         "Name, Cache Pen",
				MetadataProvider: "openlibrary",
				Monitored:        true,
			},
		},
	}))
	matcher, err := importer.newAuthorMatcher(ctx)
	if err != nil {
		t.Fatalf("newAuthorMatcher: %v", err)
	}
	item := sampleABSItem()
	item.Authors = []NormalizedAuthor{{ID: "author-cache-pen", Name: "Cache Pen Name"}}

	result, err := importer.enrichAuthor(ctx, ImportConfig{SourceID: DefaultSourceID, LibraryID: item.LibraryID}, item, existing, matcher)
	if err != nil {
		t.Fatalf("enrichAuthor: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("metadata result = %+v, want skipped relink message", result)
	}
	updated, err := authorRepo.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Name != "Cache Primary" || updated.ForeignID != "OL-CACHE-PRIMARY" {
		t.Fatalf("updated author = %+v, want canonical author unchanged", updated)
	}
}

func TestImporter_ResolveAuthor_SafeVariantMatchRecordsAlias(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	existing := &models.Author{
		ForeignID:        "OL-RR",
		Name:             "R.R. Haywood",
		SortName:         "Haywood, R.R.",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("Create author: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-rr-haywood"
	item.Title = "The Undead"
	item.Authors = []NormalizedAuthor{{ID: "author-rr-haywood", Name: "RR Haywood"}}
	runSingleABSImport(t, importer, item)

	aliases, err := importer.aliases.ListByAuthor(ctx, existing.ID)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	found := false
	for _, alias := range aliases {
		if alias.Name == "RR Haywood" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("aliases = %+v, want ABS variant alias", aliases)
	}
	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want existing canonical only", len(authors))
	}
}

func TestImporter_ResolveAuthor_AmbiguousMatchQueuesReviewWithoutAlias(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, _, _, _, reviewRepo, _ := newABSImporterFixture(t)
	ctx := context.Background()
	existing := &models.Author{ForeignID: "OL-JAMES", Name: "Alice James", SortName: "James, Alice", Monitored: true}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("Create author: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-alice-jones"
	item.Title = "Ambiguous Author"
	item.Authors = []NormalizedAuthor{{ID: "author-alice-jones", Name: "Alice Jones"}}
	item.ASIN = ""
	item.AudioFiles = nil
	item.EbookPath = ""
	item.EbookINO = ""
	runSingleABSImport(t, importer, item)

	reviews, err := reviewRepo.ListByStatus(ctx, "pending")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(reviews) != 1 || reviews[0].ReviewReason != reviewReasonAmbiguousAuthor {
		t.Fatalf("reviews = %+v, want one ambiguous-author review", reviews)
	}
	aliases, err := importer.aliases.ListByAuthor(ctx, existing.ID)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("aliases = %+v, want no alias for ambiguous match", aliases)
	}
}

func TestImporter_LookupUpstreamAuthor_FuzzyCanonicalRelink(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	importer.WithMetadata(metadata.NewAggregator(&stubABSMetadataProvider{
		searchAuthors: []models.Author{{ForeignID: "OL-RR", Name: "R.R. Haywood"}},
		authors: map[string]*models.Author{
			"OL-RR": {ForeignID: "OL-RR", Name: "R.R. Haywood", SortName: "Haywood, R.R.", MetadataProvider: "openlibrary"},
		},
	}))

	got, ambiguous, err := importer.lookupUpstreamAuthor(context.Background(), "RR Haywood")
	if err != nil {
		t.Fatalf("lookupUpstreamAuthor: %v", err)
	}
	if ambiguous || got == nil || got.ForeignID != "OL-RR" {
		t.Fatalf("lookupUpstreamAuthor = author=%+v ambiguous=%v, want canonical fuzzy/variant relink", got, ambiguous)
	}
}

func TestImporter_RelinksInitialedAuthorUsingFallbackSearch(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	provider := &stubABSMetadataProvider{
		searchAuthorsByQuery: map[string][]models.Author{
			"J.R.R. Tolkien": {{ForeignID: "OL26320A", Name: "J.R.R. Tolkien"}},
		},
		authors: map[string]*models.Author{
			"OL26320A": {
				ForeignID:        "OL26320A",
				Name:             "J.R.R. Tolkien",
				SortName:         "Tolkien, J.R.R.",
				Description:      "Author of The Hobbit.",
				MetadataProvider: "openlibrary",
			},
		},
		works: map[string][]models.Book{
			"OL26320A": {{ForeignID: "OL-HOBBIT", Title: "The Hobbit", SortTitle: "The Hobbit", Language: "eng", MetadataProvider: "openlibrary", Status: models.BookStatusWanted}},
		},
		books: map[string]*models.Book{
			"OL-HOBBIT": {ForeignID: "OL-HOBBIT", Title: "The Hobbit", SortTitle: "The Hobbit", Language: "eng", MetadataProvider: "openlibrary", Status: models.BookStatusWanted},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(provider))

	item := sampleABSItem()
	item.ItemID = "li-hobbit"
	item.Title = "The Hobbit"
	item.Authors = []NormalizedAuthor{{ID: "author-tolkien", Name: "J. R. R. Tolkien"}}
	item.EbookPath = "/abs/The Hobbit/book.epub"
	item.EbookINO = "ebook-hobbit"
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.MetadataRelinked == 0 {
		t.Fatalf("metadataRelinked = %d, want author relink", stats.MetadataRelinked)
	}
	authors, err := authorRepo.List(context.Background())
	if err != nil || len(authors) != 1 {
		t.Fatalf("authors = %d err=%v, want 1", len(authors), err)
	}
	if authors[0].ForeignID != "OL26320A" || authors[0].Name != "J.R.R. Tolkien" {
		t.Fatalf("author = %+v, want upstream Tolkien", authors[0])
	}
	books, err := bookRepo.ListByAuthor(context.Background(), authors[0].ID)
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want 1", len(books), err)
	}
}

func TestImporter_CleansABSDescriptionBeforeStoring(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	item := sampleABSItem()
	item.Description = `<p><b>First paragraph.</b></p><p>Second&nbsp;paragraph.</p>`
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	if _, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want 1", len(books), err)
	}
	want := "First paragraph.\n\nSecond paragraph."
	if books[0].Description != want {
		t.Fatalf("description = %q, want %q", books[0].Description, want)
	}
}

func TestImporter_UnmatchedBookQueuesReview(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, _, _, _, reviewRepo, _ := newABSImporterFixture(t)
	existing := &models.Author{
		ForeignID:        "OL23919A",
		Name:             "Andy Weir",
		SortName:         "Weir, Andy",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}

	item := sampleABSItem()
	item.ItemID = "li-unmatched-book"
	item.Title = "Completely Unmatched Title"
	item.Authors = []NormalizedAuthor{{ID: "author-andy-weir", Name: "Andy Weir"}}
	item.AudioFiles = nil
	item.ASIN = ""
	item.EbookPath = "/abs/unmatched-book.epub"
	item.EbookINO = "ebook-unmatched"
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.ReviewQueued != 1 || stats.BooksCreated != 0 {
		t.Fatalf("stats = %+v, want queued review without creating book", stats)
	}
	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Fatalf("books = %d, want 0", len(books))
	}
	reviews, err := reviewRepo.ListByStatus(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0].ReviewReason != reviewReasonUnmatchedBook {
		t.Fatalf("reviews = %+v, want unmatched_book review", reviews)
	}
}

func TestImporter_UnmatchedAuthorReviewMessageReportsAuthor(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	item := sampleABSItem()
	item.ItemID = "li-onyx-storm"
	item.Title = "Onyx Storm"
	item.Authors = []NormalizedAuthor{{ID: "author-rebecca-yarros", Name: "Rebecca Yarros"}}
	item.ASIN = ""
	item.AudioFiles = nil
	item.EbookPath = "/abs/onyx-storm.epub"
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.ReviewQueued != 1 {
		t.Fatalf("stats = %+v, want one review item", stats)
	}
	progress := importer.Progress()
	if len(progress.Results) != 1 || !strings.Contains(progress.Results[0].Message, "Rebecca Yarros") {
		t.Fatalf("result message = %+v, want author name reported", progress.Results)
	}
}

func TestImporter_ImportReviewUsesResolvedAuthor(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	provider := &stubABSMetadataProvider{
		authors: map[string]*models.Author{
			"OL123A": {
				ForeignID:        "OL123A",
				Name:             "Brandon Sanderson",
				SortName:         "Sanderson, Brandon",
				MetadataProvider: "openlibrary",
				Monitored:        true,
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(provider))
	item := sampleABSItem()
	item.ItemID = "li-bands"
	item.Title = "The Bands of Mourning (2 of 2)"
	item.Authors = []NormalizedAuthor{{ID: "author-abs-brandon", Name: "Brandon Sanderson"}}
	item.ResolvedAuthorForeignID = "OL123A"
	item.ResolvedAuthorName = "Brandon Sanderson"

	if _, err := importer.ImportReview(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}, item); err != nil {
		t.Fatalf("ImportReview: %v", err)
	}

	authors, err := authorRepo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1", len(authors))
	}
	if authors[0].ForeignID != "OL123A" || authors[0].MetadataProvider == providerAudiobookshelf {
		t.Fatalf("author = %+v, want upstream Brandon Sanderson", authors[0])
	}
}

func TestImporter_ImportReviewUsesResolvedBook(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	provider := &stubABSMetadataProvider{
		authors: map[string]*models.Author{
			"OL123A": {ForeignID: "OL123A", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "openlibrary", Monitored: true},
		},
		books: map[string]*models.Book{
			"OL456W": {
				ForeignID:        "OL456W",
				Title:            "The Bands of Mourning",
				SortTitle:        "The Bands of Mourning",
				Description:      "A Wax and Wayne novel.",
				MetadataProvider: "openlibrary",
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(provider))
	item := sampleABSItem()
	item.ItemID = "li-bands"
	item.Title = "The Bands of Mourning"
	item.Authors = []NormalizedAuthor{{ID: "author-abs-brandon", Name: "Brandon Sanderson"}}
	item.ResolvedAuthorForeignID = "OL123A"
	item.ResolvedAuthorName = "Brandon Sanderson"
	item.ResolvedBookForeignID = "OL456W"
	item.ResolvedBookTitle = "The Bands of Mourning"
	item.EditedTitle = "The Bands of Mourning"

	if _, err := importer.ImportReview(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}, item); err != nil {
		t.Fatalf("ImportReview: %v", err)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if books[0].ForeignID != "OL456W" || books[0].Title != "The Bands of Mourning" {
		t.Fatalf("book = %+v, want selected upstream book", books[0])
	}
}

func TestImporter_IdempotentRerunAndProvenanceTraceable(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, seriesRepo, editionRepo, provenanceRepo, runRepo, _, _, _ := newABSImporterFixture(t)
	item := sampleABSItem()
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{
			PagesScanned:       1,
			ItemsSeen:          1,
			ItemsNormalized:    1,
			ItemsDetailFetched: 0,
		}, nil
	}
	cfg := ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: "lib-books",
		Label:     "Shelf",
		Enabled:   true,
	}

	first, err := importer.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.BooksCreated != 1 || first.AuthorsCreated != 1 || first.SeriesCreated != 1 {
		t.Fatalf("first stats = %+v", first)
	}
	if first.EditionsAdded != 2 {
		t.Fatalf("editionsAdded = %d, want 2", first.EditionsAdded)
	}

	second, err := importer.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.BooksCreated != 0 || second.BooksUpdated != 1 {
		t.Fatalf("second stats = %+v", second)
	}

	authors, _ := authorRepo.List(context.Background())
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1", len(authors))
	}
	books, _ := bookRepo.ListIncludingExcluded(context.Background())
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	allSeries, _ := seriesRepo.List(context.Background())
	if len(allSeries) != 1 {
		t.Fatalf("series = %d, want 1", len(allSeries))
	}
	editions, _ := editionRepo.ListByBook(context.Background(), books[0].ID)
	if len(editions) != 2 {
		t.Fatalf("editions = %d, want 2", len(editions))
	}

	links, err := provenanceRepo.ListByLocal(context.Background(), entityTypeBook, books[0].ID)
	if err != nil {
		t.Fatalf("ListByLocal: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("book provenance links = %d, want 1", len(links))
	}
	if links[0].ExternalID != item.ItemID {
		t.Fatalf("book provenance externalID = %q, want %q", links[0].ExternalID, item.ItemID)
	}
	if len(links[0].FileIDs) != 3 {
		t.Fatalf("book provenance file IDs = %v, want 3 entries", links[0].FileIDs)
	}
	if links[0].ImportRunID == nil || *links[0].ImportRunID == 0 {
		t.Fatal("expected provenance to retain latest run id")
	}
	run, err := runRepo.GetByID(context.Background(), *links[0].ImportRunID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if run == nil || run.Status != runStatusCompleted {
		t.Fatalf("run = %+v, want completed run", run)
	}
}

func TestImporter_MetadataEnrichmentRelinksAuthorAndBook(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	provider := &stubABSMetadataProvider{
		searchAuthors: []models.Author{{ForeignID: "OL-ANDY", Name: "Andy Weir"}},
		authors: map[string]*models.Author{
			"OL-ANDY": {
				ForeignID:        "OL-ANDY",
				Name:             "Andy Weir",
				SortName:         "Weir, Andy",
				ImageURL:         "https://img.example.com/andy.jpg",
				MetadataProvider: "openlibrary",
			},
		},
		booksByISBN: map[string]*models.Book{
			"9780593135204": {
				ForeignID:        "OL-PHM",
				Title:            "Project Hail Mary",
				ImageURL:         "https://img.example.com/phm.jpg",
				MetadataProvider: "openlibrary",
				Language:         "eng",
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(provider))

	item := sampleABSItem()
	item.ISBN = "9780593135204"
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.MetadataRelinked != 2 {
		t.Fatalf("metadataRelinked = %d, want 2", stats.MetadataRelinked)
	}

	authors, err := authorRepo.List(context.Background())
	if err != nil || len(authors) != 1 {
		t.Fatalf("authors = %v err=%v, want 1 author", len(authors), err)
	}
	if authors[0].ForeignID != "OL-ANDY" || authors[0].MetadataProvider != "openlibrary" {
		t.Fatalf("author = %+v, want upstream identity", authors[0])
	}
	if authors[0].ImageURL != "https://img.example.com/andy.jpg" {
		t.Fatalf("author image = %q", authors[0].ImageURL)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %v err=%v, want 1 book", len(books), err)
	}
	if books[0].ForeignID != "OL-PHM" || books[0].MetadataProvider != "openlibrary" {
		t.Fatalf("book = %+v, want upstream identity", books[0])
	}
	if books[0].ImageURL != "https://img.example.com/phm.jpg" {
		t.Fatalf("book image = %q", books[0].ImageURL)
	}

	links, err := provenanceRepo.ListByLocal(context.Background(), entityTypeBook, books[0].ID)
	if err != nil || len(links) != 1 {
		t.Fatalf("book provenance links = %d err=%v", len(links), err)
	}
	if links[0].ExternalID != item.ItemID {
		t.Fatalf("book provenance externalID = %q, want %q", links[0].ExternalID, item.ItemID)
	}
}

func TestImporter_MetadataEnrichmentUsesSecondaryISBNResultDirectly(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	primary := &stubABSMetadataProvider{name: "openlibrary"}
	google := &stubABSMetadataProvider{
		name: "googlebooks",
		booksByISBN: map[string]*models.Book{
			"9780593135204": {
				ForeignID:        "gb:project-hail-mary",
				Title:            "Project Hail Mary",
				ImageURL:         "https://img.example.com/gb-phm.jpg",
				Description:      "Google Books metadata for Project Hail Mary.",
				MetadataProvider: "googlebooks",
				Language:         "eng",
				Author:           &models.Author{Name: "Andy Weir", MetadataProvider: "googlebooks"},
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(primary, google))

	item := sampleABSItem()
	item.ISBN = "9780593135204"
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.MetadataRelinked == 0 {
		t.Fatalf("metadataRelinked = %d, want secondary ISBN relink", stats.MetadataRelinked)
	}
	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want 1", len(books), err)
	}
	if books[0].ForeignID != "gb:project-hail-mary" || books[0].MetadataProvider != "googlebooks" {
		t.Fatalf("book = %+v, want provider-native googlebooks identity", books[0])
	}
	if books[0].ImageURL != "https://img.example.com/gb-phm.jpg" {
		t.Fatalf("book image = %q", books[0].ImageURL)
	}
}

func TestImporter_MetadataConflictPersistsAndUsesUpstreamTemporarily(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, conflictRepo := newABSImporterFixture(t)
	provider := &stubABSMetadataProvider{
		searchAuthors: []models.Author{{ForeignID: "OL-ANDY", Name: "Andy Weir"}},
		authors: map[string]*models.Author{
			"OL-ANDY": {ForeignID: "OL-ANDY", Name: "Andy Weir", MetadataProvider: "openlibrary"},
		},
		booksByISBN: map[string]*models.Book{
			"9780593135204": {
				ForeignID:        "OL-PHM",
				Title:            "Project Hail Mary",
				Description:      "Upstream version of the story.",
				MetadataProvider: "openlibrary",
				Language:         "eng",
			},
		},
		books: map[string]*models.Book{
			"OL-PHM": {
				ForeignID:        "OL-PHM",
				Title:            "Project Hail Mary",
				Description:      "Upstream version of the story.",
				MetadataProvider: "openlibrary",
				Language:         "eng",
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(provider))

	item := sampleABSItem()
	item.ISBN = "9780593135204"
	item.Description = "ABS version of the story."
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.MetadataConflicts != 1 {
		t.Fatalf("metadataConflicts = %d, want 1", stats.MetadataConflicts)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %v err=%v, want 1 book", len(books), err)
	}
	if books[0].Description != "Upstream version of the story." {
		t.Fatalf("book description = %q, want upstream value", books[0].Description)
	}

	conflicts, err := conflictRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List conflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1", len(conflicts))
	}
	if conflicts[0].FieldName != "description" || conflicts[0].ResolutionStatus != "unresolved" {
		t.Fatalf("conflict = %+v, want unresolved description conflict", conflicts[0])
	}
	if conflicts[0].AppliedSource != MetadataSourceUpstream {
		t.Fatalf("appliedSource = %q, want upstream", conflicts[0].AppliedSource)
	}
}

func TestImporter_RerunReusesResolvedConflictPreference(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, conflictRepo := newABSImporterFixture(t)
	provider := &stubABSMetadataProvider{
		searchAuthors: []models.Author{{ForeignID: "OL-ANDY", Name: "Andy Weir"}},
		authors: map[string]*models.Author{
			"OL-ANDY": {ForeignID: "OL-ANDY", Name: "Andy Weir", MetadataProvider: "openlibrary"},
		},
		booksByISBN: map[string]*models.Book{
			"9780593135204": {
				ForeignID:        "OL-PHM",
				Title:            "Project Hail Mary",
				Description:      "Upstream version of the story.",
				MetadataProvider: "openlibrary",
				Language:         "eng",
			},
		},
		books: map[string]*models.Book{
			"OL-PHM": {
				ForeignID:        "OL-PHM",
				Title:            "Project Hail Mary",
				Description:      "Upstream version of the story.",
				MetadataProvider: "openlibrary",
				Language:         "eng",
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(provider))

	item := sampleABSItem()
	item.ISBN = "9780593135204"
	item.Description = "ABS version of the story."
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	cfg := ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}

	if _, err := importer.Run(context.Background(), cfg); err != nil {
		t.Fatalf("first run: %v", err)
	}
	conflicts, err := conflictRepo.List(context.Background())
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %d err=%v, want 1", len(conflicts), err)
	}
	conflicts[0].PreferredSource = MetadataSourceABS
	conflicts[0].AppliedSource = MetadataSourceABS
	conflicts[0].ResolutionStatus = conflictStatusResolved
	if err := conflictRepo.Upsert(context.Background(), &conflicts[0]); err != nil {
		t.Fatalf("Upsert conflict: %v", err)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want 1", len(books), err)
	}
	if err := ApplyBookConflictValue(&books[0], "description", item.Description); err != nil {
		t.Fatalf("ApplyBookConflictValue: %v", err)
	}
	if err := bookRepo.Update(context.Background(), &books[0]); err != nil {
		t.Fatalf("Update book: %v", err)
	}

	stats, err := importer.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats.MetadataAutoResolved == 0 {
		t.Fatalf("metadataAutoResolved = %d, want > 0", stats.MetadataAutoResolved)
	}

	books, err = bookRepo.ListIncludingExcluded(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want 1", len(books), err)
	}
	if books[0].Description != item.Description {
		t.Fatalf("book description = %q, want ABS value", books[0].Description)
	}

	conflicts, err = conflictRepo.List(context.Background())
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %d err=%v, want 1", len(conflicts), err)
	}
	if conflicts[0].ResolutionStatus != conflictStatusResolved || conflicts[0].PreferredSource != MetadataSourceABS {
		t.Fatalf("conflict = %+v, want resolved ABS preference", conflicts[0])
	}
}

func TestImporter_LinksExistingBookUsingNormalizedTitleAndAuthorName(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	existingAuthor := &models.Author{
		ForeignID:        "ol:author:le-guin",
		Name:             "Ursula K Le Guin",
		SortName:         "Le Guin, Ursula K",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(context.Background(), existingAuthor); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	existingBook := &models.Book{
		ForeignID:        "ol:book:left-hand",
		AuthorID:         existingAuthor.ID,
		Title:            "The Left Hand of Darkness",
		SortTitle:        "The Left Hand of Darkness",
		Status:           models.BookStatusWanted,
		Monitored:        true,
		AnyEditionOK:     true,
		MediaType:        models.MediaTypeAudiobook,
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(context.Background(), existingBook); err != nil {
		t.Fatalf("seed book: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-left-hand"
	item.Title = "The Left Hand of Darkness (German Edition)"
	item.Authors = []NormalizedAuthor{{ID: "author-ursula", Name: "  URSULA K LE GUIN  "}}
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: "lib-books",
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.AuthorsLinked != 1 || stats.BooksLinked != 1 {
		t.Fatalf("stats = %+v, want linked author/book", stats)
	}

	books, _ := bookRepo.ListIncludingExcluded(context.Background())
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if books[0].ForeignID != "ol:book:left-hand" {
		t.Fatalf("existing book foreign id should be preserved, got %q", books[0].ForeignID)
	}

	links, err := provenanceRepo.ListByLocal(context.Background(), entityTypeBook, existingBook.ID)
	if err != nil {
		t.Fatalf("ListByLocal: %v", err)
	}
	if len(links) != 1 || links[0].ExternalID != "li-left-hand" {
		t.Fatalf("links = %+v, want ABS item provenance on existing book", links)
	}
}

func TestImporter_ReconcilesSharedPathsIntoOwnedState(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	audiobookDir := filepath.Join(storageRoot, "audiobooks")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatalf("mkdir library dir: %v", err)
	}
	if err := os.MkdirAll(audiobookDir, 0o755); err != nil {
		t.Fatalf("mkdir audiobook dir: %v", err)
	}
	ebookPath := filepath.Join(libraryDir, "Andy Weir", "Project Hail Mary.epub")
	if err := os.MkdirAll(filepath.Dir(ebookPath), 0o755); err != nil {
		t.Fatalf("mkdir ebook dir: %v", err)
	}
	if err := os.WriteFile(ebookPath, []byte("ebook"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	audiobookPath := filepath.Join(audiobookDir, "Andy Weir", "Project Hail Mary")
	if err := os.MkdirAll(audiobookPath, 0o755); err != nil {
		t.Fatalf("mkdir audiobook path: %v", err)
	}

	item := sampleABSItem()
	item.Path = audiobookPath
	item.EbookPath = ebookPath
	importer.WithStoragePaths(libraryDir, audiobookDir, nil)
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.OwnedMarked != 2 {
		t.Fatalf("ownedMarked = %d, want 2", stats.OwnedMarked)
	}
	if stats.PendingManual != 0 {
		t.Fatalf("pendingManual = %d, want 0", stats.PendingManual)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("ListIncludingExcluded: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if books[0].Status != models.BookStatusImported {
		t.Fatalf("status = %q, want imported", books[0].Status)
	}
	if books[0].EbookFilePath != ebookPath {
		t.Fatalf("ebook path = %q, want %q", books[0].EbookFilePath, ebookPath)
	}
	if books[0].AudiobookFilePath != audiobookPath {
		t.Fatalf("audiobook path = %q, want %q", books[0].AudiobookFilePath, audiobookPath)
	}
	files, err := bookRepo.ListFiles(context.Background(), books[0].ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("book files = %d, want 2", len(files))
	}
}

func TestImporter_LeavesMissingSharedPathsPendingManual(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	audiobookDir := filepath.Join(storageRoot, "audiobooks")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatalf("mkdir library dir: %v", err)
	}
	if err := os.MkdirAll(audiobookDir, 0o755); err != nil {
		t.Fatalf("mkdir audiobook dir: %v", err)
	}

	item := sampleABSItem()
	item.Path = filepath.Join(audiobookDir, "Andy Weir", "Project Hail Mary")
	item.EbookPath = filepath.Join(libraryDir, "Andy Weir", "Project Hail Mary.epub")
	importer.WithStoragePaths(libraryDir, audiobookDir, nil)
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.OwnedMarked != 0 {
		t.Fatalf("ownedMarked = %d, want 0", stats.OwnedMarked)
	}
	if stats.PendingManual != 2 {
		t.Fatalf("pendingManual = %d, want 2", stats.PendingManual)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("ListIncludingExcluded: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if books[0].Status != models.BookStatusWanted {
		t.Fatalf("status = %q, want wanted", books[0].Status)
	}
	files, err := bookRepo.ListFiles(context.Background(), books[0].ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("book files = %d, want 0", len(files))
	}
	progress := importer.Progress()
	if len(progress.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(progress.Results))
	}
	if !strings.Contains(progress.Results[0].Message, "metadata only") {
		t.Fatalf("message = %q, want metadata-only guidance", progress.Results[0].Message)
	}
}

func TestImporter_AppliesPathRemapBeforeOwnedStateReconciliation(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	audiobookDir := filepath.Join(storageRoot, "audiobooks")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatalf("mkdir library dir: %v", err)
	}
	if err := os.MkdirAll(audiobookDir, 0o755); err != nil {
		t.Fatalf("mkdir audiobook dir: %v", err)
	}
	ebookPath := filepath.Join(libraryDir, "audiobookshelf", "Andy Weir", "Project Hail Mary.epub")
	if err := os.MkdirAll(filepath.Dir(ebookPath), 0o755); err != nil {
		t.Fatalf("mkdir ebook dir: %v", err)
	}
	if err := os.WriteFile(ebookPath, []byte("ebook"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	audiobookPath := filepath.Join(audiobookDir, "audiobookshelf", "Andy Weir", "Project Hail Mary")
	if err := os.MkdirAll(audiobookPath, 0o755); err != nil {
		t.Fatalf("mkdir audiobook path: %v", err)
	}

	item := sampleABSItem()
	item.Path = "/audiobookshelf/Andy Weir/Project Hail Mary"
	item.EbookPath = "/audiobookshelf/Andy Weir/Project Hail Mary.epub"
	importer.WithStoragePaths(libraryDir, audiobookDir, nil)
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		PathRemap: "/audiobookshelf:" + filepath.Join(storageRoot, "books", "audiobookshelf"),
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.OwnedMarked != 1 || stats.PendingManual != 1 {
		t.Fatalf("stats = %+v, want remapped ebook + pending audiobook", stats)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want 1", len(books), err)
	}
	if books[0].EbookFilePath != ebookPath {
		t.Fatalf("ebook path = %q, want %q", books[0].EbookFilePath, ebookPath)
	}
}

func TestImporter_ReviewFileMappingReportsVisibleRemappedPath(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	if err := os.MkdirAll(filepath.Join(libraryDir, "audiobookshelf"), 0o755); err != nil {
		t.Fatalf("mkdir library dir: %v", err)
	}
	ebookPath := filepath.Join(libraryDir, "audiobookshelf", "Onyx Storm.epub")
	if err := os.WriteFile(ebookPath, []byte("ebook"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	importer.WithStoragePaths(libraryDir, libraryDir, nil)
	item := sampleABSItem()
	item.EbookPath = "/abs/Onyx Storm.epub"
	item.AudioFiles = nil
	item.Path = ""

	mapping := importer.ReviewFileMapping(context.Background(), ImportConfig{
		PathRemap: "/abs:" + filepath.Join(libraryDir, "audiobookshelf"),
	}, item)
	if !mapping.Found {
		t.Fatalf("mapping = %+v, want found", mapping)
	}
}

func TestImporter_ReviewFileMappingReportsMissingPath(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	libraryDir := t.TempDir()
	importer.WithStoragePaths(libraryDir, libraryDir, nil)
	item := sampleABSItem()
	item.EbookPath = filepath.Join(libraryDir, "missing.epub")
	item.AudioFiles = nil
	item.Path = ""

	mapping := importer.ReviewFileMapping(context.Background(), ImportConfig{}, item)
	if mapping.Found || !strings.Contains(mapping.Message, "not visible") {
		t.Fatalf("mapping = %+v, want missing path message", mapping)
	}
}

func TestImporter_RerunKeepsOwnedStateStable(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	audiobookDir := filepath.Join(storageRoot, "audiobooks")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatalf("mkdir library dir: %v", err)
	}
	if err := os.MkdirAll(audiobookDir, 0o755); err != nil {
		t.Fatalf("mkdir audiobook dir: %v", err)
	}
	ebookPath := filepath.Join(libraryDir, "Andy Weir", "Project Hail Mary.epub")
	if err := os.MkdirAll(filepath.Dir(ebookPath), 0o755); err != nil {
		t.Fatalf("mkdir ebook dir: %v", err)
	}
	if err := os.WriteFile(ebookPath, []byte("ebook"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	audiobookPath := filepath.Join(audiobookDir, "Andy Weir", "Project Hail Mary")
	if err := os.MkdirAll(audiobookPath, 0o755); err != nil {
		t.Fatalf("mkdir audiobook path: %v", err)
	}

	item := sampleABSItem()
	item.Path = audiobookPath
	item.EbookPath = ebookPath
	importer.WithStoragePaths(libraryDir, audiobookDir, nil)
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	cfg := ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}

	first, err := importer.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.OwnedMarked != 2 {
		t.Fatalf("first ownedMarked = %d, want 2", first.OwnedMarked)
	}

	second, err := importer.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.OwnedMarked != 0 {
		t.Fatalf("second ownedMarked = %d, want 0", second.OwnedMarked)
	}
	if second.PendingManual != 0 {
		t.Fatalf("second pendingManual = %d, want 0", second.PendingManual)
	}

	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("ListIncludingExcluded: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if books[0].Status != models.BookStatusImported {
		t.Fatalf("status = %q, want imported", books[0].Status)
	}
	files, err := bookRepo.ListFiles(context.Background(), books[0].ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("book files = %d, want 2 after rerun", len(files))
	}
}

func TestImporter_DryRunDoesNotMutateCatalogButPersistsRunSummary(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, runRepo, _, _, _ := newABSImporterFixture(t)
	item := sampleABSItem()
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.BooksCreated != 1 || stats.AuthorsCreated != 1 {
		t.Fatalf("dry-run stats = %+v", stats)
	}

	authors, err := authorRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 0 {
		t.Fatalf("authors = %d, want 0 after dry-run", len(authors))
	}
	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("List books: %v", err)
	}
	if len(books) != 0 {
		t.Fatalf("books = %d, want 0 after dry-run", len(books))
	}
	links, err := provenanceRepo.ListByLocal(context.Background(), entityTypeBook, 1)
	if err != nil {
		t.Fatalf("ListByLocal: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("provenance links = %d, want 0 after dry-run", len(links))
	}

	runs, err := runRepo.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(runs) != 1 || !runs[0].DryRun {
		t.Fatalf("runs = %+v, want one dry-run record", runs)
	}
	hydrated := HydrateRun(runs[0])
	if !hydrated.Summary.DryRun || hydrated.Summary.Stats.BooksCreated != 1 {
		t.Fatalf("hydrated run summary = %+v", hydrated.Summary)
	}
}

func TestImporter_DryRunCountsPlannedSeriesOnce(t *testing.T) {
	t.Parallel()

	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	first := sampleABSItem()
	second := sampleABSItem()
	second.ItemID = "li-artemis"
	second.Title = "Artemis"
	second.Series = []NormalizedSeries{
		{ID: first.Series[0].ID, Name: first.Series[0].Name, Sequence: "2"},
	}
	items := []NormalizedLibraryItem{first, second}
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for _, item := range items {
			if err := fn(ctx, item); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: len(items), ItemsNormalized: len(items)}, nil
	}

	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: first.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.SeriesCreated != 1 || stats.SeriesLinked != 1 {
		t.Fatalf("dry-run series stats = %+v, want 1 created and 1 linked", stats)
	}
}

func TestImporter_RepeatedABSSeriesRollbackUnlinksAllRunMemberships(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, seriesRepo, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	author := &models.Author{Name: "Repeat Author", SortName: "Author, Repeat", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	firstBook := &models.Book{
		ForeignID:        "local:repeat:1",
		AuthorID:         author.ID,
		Title:            "Repeat One",
		SortTitle:        "Repeat One",
		Status:           models.BookStatusWanted,
		AnyEditionOK:     true,
		Language:         "eng",
		MediaType:        models.MediaTypeAudiobook,
		MetadataProvider: "local",
	}
	secondBook := &models.Book{
		ForeignID:        "local:repeat:2",
		AuthorID:         author.ID,
		Title:            "Repeat Two",
		SortTitle:        "Repeat Two",
		Status:           models.BookStatusWanted,
		AnyEditionOK:     true,
		Language:         "eng",
		MediaType:        models.MediaTypeAudiobook,
		MetadataProvider: "local",
	}
	if err := bookRepo.Create(ctx, firstBook); err != nil {
		t.Fatalf("Create first book: %v", err)
	}
	if err := bookRepo.Create(ctx, secondBook); err != nil {
		t.Fatalf("Create second book: %v", err)
	}

	first := sampleABSItem()
	first.ItemID = "li-repeat-series-1"
	first.Title = firstBook.Title
	first.Authors = []NormalizedAuthor{{ID: "author-repeat", Name: author.Name}}
	first.Series = []NormalizedSeries{{ID: "series-repeat", Name: "Repeat Saga", Sequence: "1"}}
	first.AudioFiles = []NormalizedAudioFile{{INO: "audio-repeat-series-1", Path: "/abs/repeat/one.m4b"}}
	first.EbookPath = ""
	first.EbookINO = ""
	second := first
	second.ItemID = "li-repeat-series-2"
	second.Title = secondBook.Title
	second.Series = []NormalizedSeries{{ID: "series-repeat", Name: "Repeat Saga", Sequence: "2"}}
	second.AudioFiles = []NormalizedAudioFile{{INO: "audio-repeat-series-2", Path: "/abs/repeat/two.m4b"}}
	items := []NormalizedLibraryItem{first, second}
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for _, item := range items {
			if err := fn(ctx, item); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: len(items), ItemsNormalized: len(items)}, nil
	}

	stats, err := importer.Run(ctx, ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: first.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.SeriesCreated != 1 || stats.SeriesLinked != 1 {
		t.Fatalf("series stats = %+v, want one created series and one linked membership", stats)
	}
	runs, err := importer.RecentRuns(ctx, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("RecentRuns = %d err=%v, want 1 run", len(runs), err)
	}
	series, err := seriesRepo.GetByForeignID(ctx, absForeignID("series", first.LibraryID, "series-repeat"))
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if series == nil {
		t.Fatal("expected imported series")
	}
	hydrated, err := seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatalf("GetByID before rollback: %v", err)
	}
	if len(hydrated.Books) != 2 {
		t.Fatalf("series books before rollback = %+v, want two run-owned memberships", hydrated.Books)
	}

	result, err := importer.Rollback(ctx, runs[0].ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.Failed != 0 {
		t.Fatalf("rollback result = %+v, want no failures", result)
	}
	books, err := bookRepo.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatalf("List books after rollback: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("books after rollback = %d, want existing books preserved", len(books))
	}
	allSeries, err := seriesRepo.List(ctx)
	if err != nil {
		t.Fatalf("List series after rollback: %v", err)
	}
	if len(allSeries) != 0 {
		t.Fatalf("series after rollback = %+v, want empty imported series deleted", allSeries)
	}
	itemBookIDs := map[string]int64{
		first.ItemID:  firstBook.ID,
		second.ItemID: secondBook.ID,
	}
	for _, item := range items {
		link, err := provenanceRepo.GetByExternal(ctx, DefaultSourceID, item.LibraryID, entityTypeSeries, seriesMembershipExternalID("series-repeat", itemBookIDs[item.ItemID], item.ItemID))
		if err != nil {
			t.Fatalf("GetByExternal membership: %v", err)
		}
		if link != nil {
			t.Fatalf("series membership provenance for %s = %+v, want nil", item.ItemID, link)
		}
	}
}

func TestImporter_ABSSeriesRerunRollbackPreservesOriginalMembership(t *testing.T) {
	t.Parallel()

	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	item := sampleABSItem()
	firstRunID := runSingleABSImport(t, importer, item)
	if firstRunID == 0 {
		t.Fatal("first run id is required")
	}

	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	stats, err := importer.Run(ctx, ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if stats.SeriesCreated != 0 || stats.SeriesLinked != 0 {
		t.Fatalf("second run series stats = %+v, want no new series ownership", stats)
	}
	runs, err := importer.RecentRuns(ctx, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("RecentRuns = %d err=%v, want second run", len(runs), err)
	}
	if _, err := importer.Rollback(ctx, runs[0].ID); err != nil {
		t.Fatalf("Rollback second run: %v", err)
	}
	series, err := seriesRepo.GetByForeignID(ctx, absForeignID("series", item.LibraryID, item.Series[0].ID))
	if err != nil {
		t.Fatalf("GetByForeignID after rollback: %v", err)
	}
	if series == nil {
		t.Fatal("series after second rollback = nil, want original series preserved")
	}
	hydrated, err := seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if len(hydrated.Books) != 1 {
		t.Fatalf("series books after rollback = %+v, want original membership preserved", hydrated.Books)
	}
}

func hardcoverSeriesABSItem() NormalizedLibraryItem {
	item := sampleABSItem()
	item.ItemID = "li-different-seasons"
	item.Title = "1982 - Different Seasons (4 novellas - read by Frank Muller)"
	item.Authors = []NormalizedAuthor{{ID: "author-stephen-king", Name: "Stephen King"}}
	item.Series = nil
	return item
}

func hardcoverSeriesProvider(query string, result metadata.SeriesSearchResult, catalog *metadata.SeriesCatalog) *stubABSMetadataProvider {
	return &stubABSMetadataProvider{
		series: map[string][]metadata.SeriesSearchResult{
			query: {result},
		},
		catalogs: map[string]*metadata.SeriesCatalog{
			result.ForeignID: catalog,
		},
	}
}

func TestImporter_HardcoverSeriesMatchSkippedWhenFeatureDisabled(t *testing.T) {
	t.Parallel()

	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	item := hardcoverSeriesABSItem()
	catalog := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:disabled",
		ProviderID: "disabled",
		Title:      "Different Seasons",
		AuthorName: "Stephen King",
		Books: []metadata.SeriesCatalogBook{{
			ForeignID: "hc:different-seasons",
			Title:     "Different Seasons",
			Position:  "1",
			Book: models.Book{
				ForeignID:        "hc:different-seasons",
				Title:            "Different Seasons",
				MetadataProvider: providerHardcover,
				Author:           &models.Author{Name: "Stephen King"},
			},
		}},
	}
	provider := hardcoverSeriesProvider(item.Title, metadata.SeriesSearchResult{
		ForeignID:  catalog.ForeignID,
		ProviderID: catalog.ProviderID,
		Title:      catalog.Title,
		AuthorName: catalog.AuthorName,
	}, catalog)
	importer.WithMetadata(metadata.NewAggregator(provider))
	runSingleABSImport(t, importer, item)

	if provider.searchSeriesCalls != 0 {
		t.Fatalf("SearchSeries calls = %d, want 0 when enhanced Hardcover series is disabled", provider.searchSeriesCalls)
	}
	series, err := seriesRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List series: %v", err)
	}
	if len(series) != 0 {
		t.Fatalf("series = %+v, want no Hardcover series while feature is disabled", series)
	}
}

func TestImporter_HardcoverSeriesMatchLinksItemWithoutABSSeries(t *testing.T) {
	t.Parallel()

	importer, _, bookRepo, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	catalog := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:100",
		ProviderID: "100",
		Title:      "Different Seasons",
		AuthorName: "Stephen King",
		Books: []metadata.SeriesCatalogBook{
			{
				ForeignID: "hc:different-seasons",
				Title:     "Different Seasons",
				Position:  "1",
				Book: models.Book{
					ForeignID:        "hc:different-seasons",
					Title:            "Different Seasons",
					MetadataProvider: providerHardcover,
					Author:           &models.Author{Name: "Stephen King"},
				},
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(hardcoverSeriesProvider(item.Title, metadata.SeriesSearchResult{
		ForeignID:  "hc-series:100",
		ProviderID: "100",
		Title:      "Different Seasons",
		AuthorName: "Stephen King",
	}, catalog)))
	runSingleABSImport(t, importer, item)

	series, err := seriesRepo.GetByForeignID(context.Background(), "hc-series:100")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if series == nil {
		t.Fatal("expected Hardcover series")
	}
	link, err := seriesRepo.GetHardcoverLink(context.Background(), series.ID)
	if err != nil {
		t.Fatalf("GetHardcoverLink: %v", err)
	}
	if link == nil || link.HardcoverSeriesID != "hc-series:100" || link.HardcoverBookCount != 1 {
		t.Fatalf("hardcover link = %+v, want catalog link", link)
	}
	hydrated, err := seriesRepo.GetByID(context.Background(), series.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(hydrated.Books) != 1 || hydrated.Books[0].PositionInSeries != "1" {
		t.Fatalf("series books = %+v, want one linked at position 1", hydrated.Books)
	}
	books, err := bookRepo.ListIncludingExcluded(context.Background())
	if err != nil {
		t.Fatalf("List books: %v", err)
	}
	if len(books) != 1 || books[0].ForeignID != "hc:different-seasons" {
		t.Fatalf("book relink = %+v, want hc:different-seasons", books)
	}
}

func TestImporter_HardcoverSeriesLinksExistingCatalogBookWithRollback(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, seriesRepo, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	ctx := context.Background()
	author := &models.Author{
		ForeignID:        "local:stephen-king",
		Name:             "Stephen King",
		SortName:         "King, Stephen",
		Monitored:        true,
		MetadataProvider: "manual",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	existingBook := &models.Book{
		ForeignID:        "hc:skeleton-crew",
		AuthorID:         author.ID,
		Title:            "Skeleton Crew",
		SortTitle:        "Skeleton Crew",
		Status:           models.BookStatusWanted,
		Monitored:        true,
		AnyEditionOK:     true,
		Language:         "eng",
		MediaType:        models.MediaTypeEbook,
		MetadataProvider: providerHardcover,
	}
	if err := bookRepo.Create(ctx, existingBook); err != nil {
		t.Fatalf("Create existing book: %v", err)
	}

	item := hardcoverSeriesABSItem()
	catalog := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:existing-catalog-book",
		ProviderID: "700",
		Title:      "Different Seasons",
		AuthorName: "Stephen King",
		BookCount:  2,
		Books: []metadata.SeriesCatalogBook{
			{
				ForeignID: "hc:different-seasons",
				Title:     "Different Seasons",
				Position:  "1",
				Book: models.Book{
					ForeignID:        "hc:different-seasons",
					Title:            "Different Seasons",
					MetadataProvider: providerHardcover,
					Author:           &models.Author{Name: "Stephen King"},
				},
			},
			{
				ForeignID: "hc:skeleton-crew",
				Title:     "Skeleton Crew",
				Position:  "2",
				Book: models.Book{
					ForeignID:        "hc:skeleton-crew",
					Title:            "Skeleton Crew",
					MetadataProvider: providerHardcover,
					Author:           &models.Author{Name: "Stephen King"},
				},
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(hardcoverSeriesProvider(item.Title, metadata.SeriesSearchResult{
		ForeignID:  catalog.ForeignID,
		ProviderID: catalog.ProviderID,
		Title:      catalog.Title,
		AuthorName: catalog.AuthorName,
		BookCount:  catalog.BookCount,
	}, catalog)))
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	stats, err := importer.Run(ctx, ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.SeriesCreated != 1 || stats.SeriesLinked != 1 {
		t.Fatalf("series stats = %+v, want created imported link and linked existing catalog book", stats)
	}
	runs, err := importer.RecentRuns(ctx, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("RecentRuns = %d err=%v, want 1 run", len(runs), err)
	}
	runID := runs[0].ID
	series, err := seriesRepo.GetByForeignID(ctx, catalog.ForeignID)
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if series == nil {
		t.Fatal("expected Hardcover series")
	}
	hydrated, err := seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(hydrated.Books) != 2 {
		t.Fatalf("series books = %+v, want imported book and existing catalog book", hydrated.Books)
	}
	foundExisting := false
	for _, seriesBook := range hydrated.Books {
		if seriesBook.BookID == existingBook.ID && seriesBook.PositionInSeries == "2" {
			foundExisting = true
		}
	}
	if !foundExisting {
		t.Fatalf("series books = %+v, want existing catalog book linked at position 2", hydrated.Books)
	}

	linkExternalID := seriesMembershipExternalID(catalog.ForeignID, existingBook.ID, "")
	link, err := provenanceRepo.GetByExternal(ctx, DefaultSourceID, item.LibraryID, entityTypeSeries, linkExternalID)
	if err != nil {
		t.Fatalf("GetByExternal: %v", err)
	}
	if link == nil || link.LocalID != series.ID || link.ImportRunID == nil || *link.ImportRunID != runID {
		t.Fatalf("series membership provenance = %+v, want run-owned link to series", link)
	}

	result, err := importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.Failed != 0 {
		t.Fatalf("rollback result = %+v, want no failures", result)
	}
	remainingBook, err := bookRepo.GetByID(ctx, existingBook.ID)
	if err != nil {
		t.Fatalf("GetByID existing book after rollback: %v", err)
	}
	if remainingBook == nil {
		t.Fatal("existing catalog book was deleted by rollback")
	}
	series, err = seriesRepo.GetByForeignID(ctx, catalog.ForeignID)
	if err != nil {
		t.Fatalf("GetByForeignID after rollback: %v", err)
	}
	if series != nil {
		t.Fatalf("series after rollback = %+v, want deleted after run-owned links removed", series)
	}
	link, err = provenanceRepo.GetByExternal(ctx, DefaultSourceID, item.LibraryID, entityTypeSeries, linkExternalID)
	if err != nil {
		t.Fatalf("GetByExternal after rollback: %v", err)
	}
	if link != nil {
		t.Fatalf("series membership provenance after rollback = %+v, want nil", link)
	}
}

func TestImporter_HardcoverSeriesMatchPromotesExactABSSeries(t *testing.T) {
	t.Parallel()

	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := sampleABSItem()
	item.ItemID = "li-all-systems-red"
	item.Title = "All Systems Red"
	item.Authors = []NormalizedAuthor{{ID: "author-martha-wells", Name: "Martha Wells"}}
	item.Series = []NormalizedSeries{{Name: "The Murderbot Diaries", Sequence: "1"}}
	catalog := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:200",
		ProviderID: "200",
		Title:      "The Murderbot Diaries",
		AuthorName: "Martha Wells",
		Books: []metadata.SeriesCatalogBook{{
			ForeignID: "hc:all-systems-red",
			Title:     "All Systems Red",
			Position:  "1",
			Book: models.Book{
				ForeignID:        "hc:all-systems-red",
				Title:            "All Systems Red",
				MetadataProvider: providerHardcover,
				Author:           &models.Author{Name: "Martha Wells"},
			},
		}},
	}
	importer.WithMetadata(metadata.NewAggregator(hardcoverSeriesProvider("The Murderbot Diaries", metadata.SeriesSearchResult{
		ForeignID:  "hc-series:200",
		ProviderID: "200",
		Title:      "The Murderbot Diaries",
		AuthorName: "Martha Wells",
	}, catalog)))
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.SeriesCreated != 1 || stats.SeriesLinked != 0 {
		t.Fatalf("series stats = %+v, want one created row and no duplicate Hardcover link count", stats)
	}
	progress := importer.Progress()
	if len(progress.Results) != 1 || progress.Results[0].SeriesCount != 1 {
		t.Fatalf("progress results = %+v, want one final series membership", progress.Results)
	}

	series, err := seriesRepo.GetByForeignID(context.Background(), "hc-series:200")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if series == nil {
		t.Fatal("expected promoted Hardcover series")
	}
	all, err := seriesRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List series: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("series rows = %+v, want one promoted row", all)
	}
}

func TestImporter_HardcoverSeriesAmbiguousCandidatesDoNotLink(t *testing.T) {
	t.Parallel()

	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	resultA := metadata.SeriesSearchResult{ForeignID: "hc-series:301", ProviderID: "301", Title: "Different Seasons", AuthorName: "Stephen King"}
	resultB := metadata.SeriesSearchResult{ForeignID: "hc-series:302", ProviderID: "302", Title: "Different Seasons", AuthorName: "Stephen King"}
	catalog := func(id string) *metadata.SeriesCatalog {
		return &metadata.SeriesCatalog{
			ForeignID:  id,
			Title:      "Different Seasons",
			AuthorName: "Stephen King",
			Books: []metadata.SeriesCatalogBook{{
				ForeignID: "hc:different-seasons",
				Title:     "Different Seasons",
				Position:  "1",
				Book:      models.Book{ForeignID: "hc:different-seasons", Title: "Different Seasons", Author: &models.Author{Name: "Stephen King"}},
			}},
		}
	}
	importer.WithMetadata(metadata.NewAggregator(&stubABSMetadataProvider{
		series: map[string][]metadata.SeriesSearchResult{
			item.Title: {resultA, resultB},
		},
		catalogs: map[string]*metadata.SeriesCatalog{
			resultA.ForeignID: catalog(resultA.ForeignID),
			resultB.ForeignID: catalog(resultB.ForeignID),
		},
	}))
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.SeriesCreated != 0 || stats.SeriesLinked != 0 {
		t.Fatalf("series stats = %+v, want no links", stats)
	}
	series, err := seriesRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List series: %v", err)
	}
	if len(series) != 0 {
		t.Fatalf("series = %+v, want none", series)
	}
}

func TestImporter_HardcoverSeriesRerunIsIdempotent(t *testing.T) {
	t.Parallel()

	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	catalog := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:400",
		Title:      "Different Seasons",
		AuthorName: "Stephen King",
		Books: []metadata.SeriesCatalogBook{{
			ForeignID: "hc:different-seasons",
			Title:     "Different Seasons",
			Position:  "1",
			Book:      models.Book{ForeignID: "hc:different-seasons", Title: "Different Seasons", Author: &models.Author{Name: "Stephen King"}},
		}},
	}
	importer.WithMetadata(metadata.NewAggregator(hardcoverSeriesProvider(item.Title, metadata.SeriesSearchResult{
		ForeignID: "hc-series:400", Title: "Different Seasons", AuthorName: "Stephen King",
	}, catalog)))
	runSingleABSImport(t, importer, item)
	secondRunID := runSingleABSImport(t, importer, item)

	series, err := seriesRepo.GetByForeignID(context.Background(), "hc-series:400")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	hydrated, err := seriesRepo.GetByID(context.Background(), series.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(hydrated.Books) != 1 {
		t.Fatalf("series books = %+v, want one idempotent link", hydrated.Books)
	}
	if _, err := importer.Rollback(context.Background(), secondRunID); err != nil {
		t.Fatalf("Rollback second run: %v", err)
	}
	hydrated, err = seriesRepo.GetByID(context.Background(), series.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if hydrated == nil || len(hydrated.Books) != 1 {
		t.Fatalf("series books after rollback = %+v, want original link preserved", hydrated)
	}
}

func TestImporter_HardcoverSeriesDryRunPlansOnly(t *testing.T) {
	t.Parallel()

	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	catalog := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:500",
		Title:      "Different Seasons",
		AuthorName: "Stephen King",
		Books: []metadata.SeriesCatalogBook{{
			ForeignID: "hc:different-seasons",
			Title:     "Different Seasons",
			Position:  "1",
			Book:      models.Book{ForeignID: "hc:different-seasons", Title: "Different Seasons", Author: &models.Author{Name: "Stephen King"}},
		}},
	}
	importer.WithMetadata(metadata.NewAggregator(hardcoverSeriesProvider(item.Title, metadata.SeriesSearchResult{
		ForeignID: "hc-series:500", Title: "Different Seasons", AuthorName: "Stephen King",
	}, catalog)))
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}
	stats, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.SeriesCreated != 1 || stats.SeriesLinked != 0 {
		t.Fatalf("dry-run series stats = %+v, want planned created link", stats)
	}
	series, err := seriesRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List series: %v", err)
	}
	if len(series) != 0 {
		t.Fatalf("series = %+v, want no mutation", series)
	}
}

func TestImporter_HardcoverSeriesRollbackRemovesLinks(t *testing.T) {
	t.Parallel()

	importer, _, _, seriesRepo, _, _, _, _, _, _ := newABSImporterFixture(t)
	enableHardcoverSeriesMatching(t, importer)
	item := hardcoverSeriesABSItem()
	catalog := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:600",
		Title:      "Different Seasons",
		AuthorName: "Stephen King",
		Books: []metadata.SeriesCatalogBook{{
			ForeignID: "hc:different-seasons",
			Title:     "Different Seasons",
			Position:  "1",
			Book:      models.Book{ForeignID: "hc:different-seasons", Title: "Different Seasons", Author: &models.Author{Name: "Stephen King"}},
		}},
	}
	importer.WithMetadata(metadata.NewAggregator(hardcoverSeriesProvider(item.Title, metadata.SeriesSearchResult{
		ForeignID: "hc-series:600", Title: "Different Seasons", AuthorName: "Stephen King",
	}, catalog)))
	runID := runSingleABSImport(t, importer, item)
	if series, err := seriesRepo.GetByForeignID(context.Background(), "hc-series:600"); err != nil || series == nil {
		t.Fatalf("series before rollback = %+v err=%v, want present", series, err)
	}
	if _, err := importer.Rollback(context.Background(), runID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	series, err := seriesRepo.GetByForeignID(context.Background(), "hc-series:600")
	if err != nil {
		t.Fatalf("GetByForeignID after rollback: %v", err)
	}
	if series != nil {
		t.Fatalf("series after rollback = %+v, want deleted", series)
	}
}

func TestImporter_RollbackRemovesCreatedBatch(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, seriesRepo, editionRepo, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	item := sampleABSItem()
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	if _, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs, err := importer.RecentRuns(context.Background(), 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("RecentRuns = %d err=%v, want 1 run", len(runs), err)
	}
	preview, err := importer.RollbackPreview(context.Background(), runs[0].ID)
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if preview.Stats.ActionsPlanned == 0 {
		t.Fatalf("preview = %+v, want planned actions", preview)
	}
	previewActions := rollbackActionSignatures(preview.Actions)
	result, err := importer.Rollback(context.Background(), runs[0].ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	requireStringSlicesEqual(t, rollbackActionSignatures(result.Actions), previewActions)
	if result.Stats.EntitiesDeleted == 0 {
		t.Fatalf("rollback result = %+v, want deletions", result)
	}
	if result.Status != runStatusRolledBack {
		t.Fatalf("rollback status = %q, want %q", result.Status, runStatusRolledBack)
	}
	run, err := importer.GetRun(context.Background(), runs[0].ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run == nil || run.Status != runStatusRolledBack {
		t.Fatalf("run = %+v, want rolled_back", run)
	}

	authors, _ := authorRepo.List(context.Background())
	if len(authors) != 0 {
		t.Fatalf("authors = %d, want 0 after rollback", len(authors))
	}
	books, _ := bookRepo.ListIncludingExcluded(context.Background())
	if len(books) != 0 {
		t.Fatalf("books = %d, want 0 after rollback", len(books))
	}
	allSeries, _ := seriesRepo.List(context.Background())
	if len(allSeries) != 0 {
		t.Fatalf("series = %d, want 0 after rollback", len(allSeries))
	}
	editions, err := editionRepo.ListByBook(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListByBook: %v", err)
	}
	if len(editions) != 0 {
		t.Fatalf("editions = %d, want 0 after rollback", len(editions))
	}
	link, err := provenanceRepo.GetByExternal(context.Background(), DefaultSourceID, item.LibraryID, entityTypeBook, item.ItemID)
	if err != nil {
		t.Fatalf("GetByExternal: %v", err)
	}
	if link != nil {
		t.Fatalf("book provenance = %+v, want nil after rollback", link)
	}
}

func TestImporter_RollbackKeepsRunCreatedSeriesWithUserMembership(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, seriesRepo, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	item := sampleABSItem()
	runID := runSingleABSImport(t, importer, item)
	series, err := seriesRepo.GetByForeignID(ctx, absForeignID("series", item.LibraryID, item.Series[0].ID))
	if err != nil {
		t.Fatalf("GetByForeignID before rollback: %v", err)
	}
	if series == nil {
		t.Fatal("expected imported series before rollback")
	}
	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want imported author", len(authors))
	}
	userBook := &models.Book{
		ForeignID:        "manual:user-series-book",
		AuthorID:         authors[0].ID,
		Title:            "User Added Sequel",
		SortTitle:        "User Added Sequel",
		Status:           models.BookStatusWanted,
		AnyEditionOK:     true,
		Language:         "eng",
		MediaType:        models.MediaTypeAudiobook,
		MetadataProvider: "manual",
	}
	if err := bookRepo.Create(ctx, userBook); err != nil {
		t.Fatalf("Create user book: %v", err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, userBook.ID, "99", false); err != nil {
		t.Fatalf("Link user book: %v", err)
	}

	result, err := importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.Failed != 0 {
		t.Fatalf("rollback result = %+v, want no failures", result)
	}
	surviving, err := seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if surviving == nil {
		t.Fatal("series after rollback = nil, want kept because user membership remains")
	}
	if len(surviving.Books) != 1 || surviving.Books[0].BookID != userBook.ID {
		t.Fatalf("series books after rollback = %+v, want only user-added membership", surviving.Books)
	}
	identity, err := provenanceRepo.GetByExternal(ctx, DefaultSourceID, item.LibraryID, entityTypeSeries, item.Series[0].ID)
	if err != nil {
		t.Fatalf("GetByExternal identity after rollback: %v", err)
	}
	if identity != nil {
		t.Fatalf("series identity provenance = %+v, want nil after rollback keeps row", identity)
	}
}

func TestImporter_RollbackDeletesCreatedEntitiesAfterSameRunRelink(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, _, runEntityRepo, _, _ := newABSImporterFixture(t)
	ctx := context.Background()

	first := sampleABSItem()
	first.ItemID = "li-repeat-1"
	first.Title = "Repeated Book"
	first.ASIN = "ASIN-REPEAT-1"
	first.Authors = []NormalizedAuthor{{ID: "author-repeat", Name: "Repeat Author"}}
	first.Series = nil
	first.AudioFiles = []NormalizedAudioFile{{INO: "audio-repeat-1", Path: "/abs/repeated/part1.m4b"}}
	first.EbookPath = ""
	first.EbookINO = ""

	second := first
	second.ItemID = "li-repeat-2"
	second.ASIN = "ASIN-REPEAT-2"
	second.AudioFiles = []NormalizedAudioFile{{INO: "audio-repeat-2", Path: "/abs/repeated/part2.m4b"}}
	items := []NormalizedLibraryItem{first, second}

	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for _, item := range items {
			if err := fn(ctx, item); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: len(items), ItemsNormalized: len(items), ItemsDetailFetched: len(items)}, nil
	}

	stats, err := importer.Run(ctx, ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: first.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.AuthorsCreated != 1 || stats.AuthorsLinked != 1 || stats.BooksCreated != 1 || stats.BooksLinked != 1 {
		t.Fatalf("stats = %+v, want one created and one linked author/book", stats)
	}

	runs, err := importer.RecentRuns(ctx, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("RecentRuns = %d err=%v, want 1 run", len(runs), err)
	}
	runID := runs[0].ID
	entities, err := runEntityRepo.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	var authorOutcome, firstBookOutcome, secondBookOutcome string
	for _, entity := range entities {
		switch {
		case entity.EntityType == entityTypeAuthor && entity.ExternalID == "author-repeat":
			authorOutcome = entity.Outcome
		case entity.EntityType == entityTypeBook && entity.ExternalID == first.ItemID:
			firstBookOutcome = entity.Outcome
		case entity.EntityType == entityTypeBook && entity.ExternalID == second.ItemID:
			secondBookOutcome = entity.Outcome
		}
	}
	if authorOutcome != itemOutcomeCreated {
		t.Fatalf("author run entity outcome = %q, want created", authorOutcome)
	}
	if firstBookOutcome != itemOutcomeCreated || secondBookOutcome != itemOutcomeLinked {
		t.Fatalf("book run entity outcomes = first:%q second:%q, want created/linked", firstBookOutcome, secondBookOutcome)
	}

	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1 before rollback", len(authors))
	}
	books, err := bookRepo.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatalf("List books: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1 before rollback", len(books))
	}
	authorID, bookID := authors[0].ID, books[0].ID
	bookLinks, err := provenanceRepo.ListByLocal(ctx, entityTypeBook, bookID)
	if err != nil {
		t.Fatalf("ListByLocal book before rollback: %v", err)
	}
	if len(bookLinks) != 2 {
		t.Fatalf("book provenance links = %+v, want both ABS items linked to the same local book", bookLinks)
	}

	result, err := importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.Failed != 0 {
		t.Fatalf("rollback result = %+v, want no failures", result)
	}
	authors, err = authorRepo.List(ctx)
	if err != nil {
		t.Fatalf("List authors after rollback: %v", err)
	}
	if len(authors) != 0 {
		t.Fatalf("authors = %d, want 0 after rollback", len(authors))
	}
	books, err = bookRepo.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatalf("List books after rollback: %v", err)
	}
	if len(books) != 0 {
		t.Fatalf("books = %d, want 0 after rollback", len(books))
	}
	authorLinks, err := provenanceRepo.ListByLocal(ctx, entityTypeAuthor, authorID)
	if err != nil {
		t.Fatalf("ListByLocal author after rollback: %v", err)
	}
	if len(authorLinks) != 0 {
		t.Fatalf("author provenance links = %+v, want none after deleting created author", authorLinks)
	}
	bookLinks, err = provenanceRepo.ListByLocal(ctx, entityTypeBook, bookID)
	if err != nil {
		t.Fatalf("ListByLocal book after rollback: %v", err)
	}
	if len(bookLinks) != 0 {
		t.Fatalf("book provenance links = %+v, want none after deleting created book", bookLinks)
	}
}

func TestImporter_RollbackRestoresExistingBookMetadata(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, _, runEntityRepo, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	author := &models.Author{
		ForeignID:        "OL-AUTHOR",
		Name:             "Andy Weir",
		SortName:         "Weir, Andy",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	selectedEditionID := int64(42)
	book := &models.Book{
		ForeignID:         "OL-LOCAL-BOOK",
		AuthorID:          author.ID,
		Title:             "Local Title",
		SortTitle:         "Local Title",
		OriginalTitle:     "Original Local Title",
		Description:       "Local description.",
		ImageURL:          "https://covers.example.com/local.jpg",
		ReleaseDate:       mustDate(t, "1999-01-02"),
		Genres:            []string{"local", "sci-fi"},
		AverageRating:     4.2,
		RatingsCount:      17,
		Monitored:         false,
		Status:            models.BookStatusSkipped,
		AnyEditionOK:      false,
		SelectedEditionID: &selectedEditionID,
		Language:          "fre",
		MediaType:         models.MediaTypeEbook,
		Narrator:          "Local Narrator",
		DurationSeconds:   12,
		ASIN:              "LOCALASIN",
		MetadataProvider:  "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatalf("Create book: %v", err)
	}

	item := sampleABSItem()
	item.Title = "ABS Title"
	item.Description = "ABS description."
	item.Language = "eng"
	item.PublishedDate = "2021-05-04"
	item.Genres = []string{"imported", "space"}
	item.Narrators = []string{"ABS Narrator"}
	item.DurationSeconds = 3600
	item.ASIN = "ABSASIN"
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID:   DefaultSourceID,
		LibraryID:  item.LibraryID,
		EntityType: entityTypeBook,
		ExternalID: item.ItemID,
		LocalID:    book.ID,
		ItemID:     item.ItemID,
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}

	runID := runSingleABSImport(t, importer, item)
	updated, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID after import: %v", err)
	}
	if updated.Title != "ABS Title" || updated.Description != "ABS description." || updated.ASIN != "ABSASIN" {
		t.Fatalf("book after import = %+v, want ABS metadata", updated)
	}
	entities, err := runEntityRepo.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	foundSnapshot := false
	for _, entity := range entities {
		if entity.EntityType != entityTypeBook || entity.LocalID != book.ID {
			continue
		}
		var envelope runEntityMetadataEnvelope
		if err := json.Unmarshal([]byte(entity.MetadataJSON), &envelope); err != nil {
			t.Fatalf("decode metadata envelope: %v", err)
		}
		foundSnapshot = envelope.Kind == runEntityMetadataKind && envelope.Snapshot != nil && envelope.Snapshot.Before != nil && envelope.Snapshot.After != nil
	}
	if !foundSnapshot {
		t.Fatalf("run entities = %+v, want typed book before/after snapshot", entities)
	}

	result, err := importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.ProvenanceUnlinked == 0 {
		t.Fatalf("rollback result = %+v, want provenance unlink", result)
	}
	restored, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if restored.Title != "Local Title" ||
		restored.SortTitle != "Local Title" ||
		restored.OriginalTitle != "Original Local Title" ||
		restored.Description != "Local description." ||
		restored.ImageURL != "https://covers.example.com/local.jpg" ||
		restored.Language != "fre" ||
		restored.MediaType != models.MediaTypeEbook ||
		restored.Narrator != "Local Narrator" ||
		restored.DurationSeconds != 12 ||
		restored.ASIN != "LOCALASIN" ||
		restored.MetadataProvider != "openlibrary" ||
		restored.Status != models.BookStatusSkipped ||
		restored.Monitored ||
		restored.AnyEditionOK {
		t.Fatalf("restored book = %+v, want pre-import metadata", restored)
	}
	if restored.ReleaseDate == nil || !restored.ReleaseDate.Equal(*book.ReleaseDate) {
		t.Fatalf("release date = %v, want %v", restored.ReleaseDate, book.ReleaseDate)
	}
	if strings.Join(restored.Genres, ",") != "local,sci-fi" {
		t.Fatalf("genres = %+v, want local sci-fi", restored.Genres)
	}
	if restored.SelectedEditionID == nil || *restored.SelectedEditionID != selectedEditionID {
		t.Fatalf("selected edition = %v, want %d", restored.SelectedEditionID, selectedEditionID)
	}
	link, err := provenanceRepo.GetByExternal(ctx, DefaultSourceID, item.LibraryID, entityTypeBook, item.ItemID)
	if err != nil {
		t.Fatalf("GetByExternal: %v", err)
	}
	if link != nil {
		t.Fatalf("book provenance = %+v, want nil after rollback", link)
	}
}

func TestImporter_RollbackPreservesPostImportBookEdits(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	author := &models.Author{Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	book := &models.Book{
		ForeignID:        "OL-LOCAL-BOOK",
		AuthorID:         author.ID,
		Title:            "Local Title",
		SortTitle:        "Local Title",
		Description:      "Local description.",
		Genres:           []string{"local"},
		Monitored:        true,
		Status:           models.BookStatusWanted,
		AnyEditionOK:     true,
		Language:         "eng",
		MediaType:        models.MediaTypeEbook,
		ASIN:             "LOCALASIN",
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatalf("Create book: %v", err)
	}
	item := sampleABSItem()
	item.Title = "ABS Title"
	item.Description = "ABS description."
	item.ASIN = "ABSASIN"
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID:   DefaultSourceID,
		LibraryID:  item.LibraryID,
		EntityType: entityTypeBook,
		ExternalID: item.ItemID,
		LocalID:    book.ID,
		ItemID:     item.ItemID,
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	runID := runSingleABSImport(t, importer, item)

	edited, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	edited.Description = "User edited after import."
	edited.ASIN = "USERASIN"
	if err := bookRepo.Update(ctx, edited); err != nil {
		t.Fatalf("Update post-import edit: %v", err)
	}

	if _, err := importer.Rollback(ctx, runID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	restored, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if restored.Description != "User edited after import." || restored.ASIN != "USERASIN" {
		t.Fatalf("book = %+v, want post-import edits preserved", restored)
	}
	if restored.Title != "Local Title" {
		t.Fatalf("title = %q, want untouched field restored to Local Title", restored.Title)
	}
}

func TestImporter_RollbackExistingBookIsIdempotent(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	author := &models.Author{Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	book := &models.Book{
		ForeignID:        "OL-LOCAL-BOOK",
		AuthorID:         author.ID,
		Title:            "Local Title",
		SortTitle:        "Local Title",
		Description:      "Local description.",
		Genres:           []string{"local"},
		Monitored:        true,
		Status:           models.BookStatusWanted,
		AnyEditionOK:     true,
		Language:         "eng",
		MediaType:        models.MediaTypeEbook,
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatalf("Create book: %v", err)
	}
	item := sampleABSItem()
	item.Title = "ABS Title"
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID:   DefaultSourceID,
		LibraryID:  item.LibraryID,
		EntityType: entityTypeBook,
		ExternalID: item.ItemID,
		LocalID:    book.ID,
		ItemID:     item.ItemID,
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	runID := runSingleABSImport(t, importer, item)

	first, err := importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("first Rollback: %v", err)
	}
	if first.Stats.Failed != 0 || first.Stats.ProvenanceUnlinked == 0 {
		t.Fatalf("first rollback = %+v, want successful unlink", first)
	}
	restored, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID after first rollback: %v", err)
	}
	second, err := importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("second Rollback: %v", err)
	}
	if second.Stats.Failed != 0 || second.Stats.Skipped == 0 {
		t.Fatalf("second rollback = %+v, want skip-only safe result", second)
	}
	afterSecond, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID after second rollback: %v", err)
	}
	if afterSecond.Title != restored.Title || afterSecond.Description != restored.Description || afterSecond.MetadataProvider != restored.MetadataProvider {
		t.Fatalf("book after second rollback = %+v, want unchanged from first rollback %+v", afterSecond, restored)
	}
}

func TestImporter_RollbackRestoresAuthorMetadataAndPreservesAliases(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, provenanceRepo, _, runEntityRepo, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	existing := &models.Author{
		ForeignID:        absForeignID("author", "lib-books", "author-andy-weir"),
		Name:             "A. Weir",
		SortName:         "Weir, A.",
		Description:      "Local author description.",
		ImageURL:         "https://img.example.com/local-author.jpg",
		Disambiguation:   "local disambiguation",
		MetadataProvider: providerAudiobookshelf,
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID:   DefaultSourceID,
		LibraryID:  "lib-books",
		EntityType: entityTypeAuthor,
		ExternalID: "author-andy-weir",
		LocalID:    existing.ID,
		ItemID:     "li-project-hail-mary",
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	provider := &stubABSMetadataProvider{
		searchAuthors: []models.Author{{ForeignID: "OL-ANDY", Name: "Andy Weir"}},
		authors: map[string]*models.Author{
			"OL-ANDY": {
				ForeignID:        "OL-ANDY",
				Name:             "Andy Weir",
				SortName:         "Weir, Andy",
				Description:      "Upstream author description.",
				ImageURL:         "https://img.example.com/upstream-author.jpg",
				Disambiguation:   "upstream disambiguation",
				MetadataProvider: "openlibrary",
			},
		},
	}
	importer.WithMetadata(metadata.NewAggregator(provider))

	item := sampleABSItem()
	runID := runSingleABSImport(t, importer, item)

	updated, err := authorRepo.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatalf("GetByID after import: %v", err)
	}
	if updated.ForeignID != "OL-ANDY" || updated.Name != "Andy Weir" || updated.Description != "Upstream author description." {
		t.Fatalf("author after import = %+v, want upstream metadata", updated)
	}
	entities, err := runEntityRepo.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	foundSnapshot := false
	for _, entity := range entities {
		if entity.EntityType != entityTypeAuthor || entity.LocalID != existing.ID {
			continue
		}
		before, after, ok := authorRollbackSnapshotFromMetadata(entity.MetadataJSON)
		foundSnapshot = ok && before != nil && after != nil && before.Name == "A. Weir" && after.Name == "Andy Weir"
	}
	if !foundSnapshot {
		t.Fatalf("run entities = %+v, want author before/after rollback snapshot", entities)
	}

	result, err := importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.Failed != 0 || result.Stats.ProvenanceUnlinked == 0 {
		t.Fatalf("rollback result = %+v, want clean author provenance unlink", result)
	}
	restored, err := authorRepo.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if restored.ForeignID != existing.ForeignID ||
		restored.Name != "A. Weir" ||
		restored.SortName != "Weir, A." ||
		restored.Description != "Local author description." ||
		restored.ImageURL != "https://img.example.com/local-author.jpg" ||
		restored.Disambiguation != "local disambiguation" ||
		restored.MetadataProvider != providerAudiobookshelf {
		t.Fatalf("restored author = %+v, want pre-import metadata", restored)
	}
	aliases, err := importer.aliases.ListByAuthor(ctx, existing.ID)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	foundAlias := false
	for _, alias := range aliases {
		if alias.Name == "A. Weir" {
			foundAlias = true
			break
		}
	}
	if !foundAlias {
		t.Fatalf("aliases = %+v, want rollback to preserve import-created alias", aliases)
	}
}

func TestImporter_RollbackPreservesPostImportAuthorEdits(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	existing := &models.Author{
		ForeignID:        absForeignID("author", "lib-books", "author-andy-weir"),
		Name:             "A. Weir",
		SortName:         "Weir, A.",
		Description:      "Local author description.",
		MetadataProvider: providerAudiobookshelf,
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID:   DefaultSourceID,
		LibraryID:  "lib-books",
		EntityType: entityTypeAuthor,
		ExternalID: "author-andy-weir",
		LocalID:    existing.ID,
		ItemID:     "li-project-hail-mary",
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	importer.WithMetadata(metadata.NewAggregator(&stubABSMetadataProvider{
		searchAuthors: []models.Author{{ForeignID: "OL-ANDY", Name: "Andy Weir"}},
		authors: map[string]*models.Author{
			"OL-ANDY": {
				ForeignID:        "OL-ANDY",
				Name:             "Andy Weir",
				SortName:         "Weir, Andy",
				Description:      "Upstream author description.",
				MetadataProvider: "openlibrary",
			},
		},
	}))

	runID := runSingleABSImport(t, importer, sampleABSItem())
	edited, err := authorRepo.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatalf("GetByID after import: %v", err)
	}
	edited.Description = "User edited after import."
	if err := authorRepo.Update(ctx, edited); err != nil {
		t.Fatalf("Update author post-import edit: %v", err)
	}

	if _, err := importer.Rollback(ctx, runID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	restored, err := authorRepo.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if restored.Description != "User edited after import." {
		t.Fatalf("description = %q, want post-import edit preserved", restored.Description)
	}
	if restored.Name != "A. Weir" || restored.ForeignID != existing.ForeignID {
		t.Fatalf("author = %+v, want untouched fields restored", restored)
	}
}

func TestImporter_RollbackSkipsSnapshotWhenProvenanceLocalChanged(t *testing.T) {
	t.Parallel()

	importer, authorRepo, _, _, _, provenanceRepo, runRepo, runEntityRepo, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	stale := &models.Author{
		ForeignID:        "OL-STALE-AFTER",
		Name:             "Imported Name",
		SortName:         "Name, Imported",
		Description:      "Imported description.",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	current := &models.Author{
		ForeignID:        "OL-CURRENT",
		Name:             "Current Canonical",
		SortName:         "Canonical, Current",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, stale); err != nil {
		t.Fatalf("Create stale author: %v", err)
	}
	if err := authorRepo.Create(ctx, current); err != nil {
		t.Fatalf("Create current author: %v", err)
	}
	run := &models.ABSImportRun{
		SourceID:    DefaultSourceID,
		SourceLabel: "Shelf",
		BaseURL:     "https://abs.example.com",
		LibraryID:   "lib-books",
		Status:      runStatusCompleted,
	}
	if err := runRepo.Create(ctx, run); err != nil {
		t.Fatalf("Create run: %v", err)
	}
	before := &authorRollbackSnapshot{
		ForeignID:        "OL-STALE-BEFORE",
		Name:             "Local Name",
		SortName:         "Name, Local",
		Description:      "Local description.",
		MetadataProvider: providerAudiobookshelf,
	}
	after := authorSnapshot(stale)
	metadata, err := authorSnapshotMetadata(nil, before, after)
	if err != nil {
		t.Fatalf("authorSnapshotMetadata: %v", err)
	}
	if err := runEntityRepo.Record(ctx, &models.ABSImportRunEntity{
		RunID:        run.ID,
		SourceID:     DefaultSourceID,
		LibraryID:    "lib-books",
		ItemID:       "li-author",
		EntityType:   entityTypeAuthor,
		ExternalID:   "author-shared",
		LocalID:      stale.ID,
		Outcome:      itemOutcomeLinked,
		MetadataJSON: mustJSONForTest(t, metadata),
	}); err != nil {
		t.Fatalf("Record run entity: %v", err)
	}
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID:    DefaultSourceID,
		LibraryID:   "lib-books",
		EntityType:  entityTypeAuthor,
		ExternalID:  "author-shared",
		LocalID:     current.ID,
		ItemID:      "li-author",
		ImportRunID: ptrInt64(run.ID),
	}); err != nil {
		t.Fatalf("seed moved provenance: %v", err)
	}

	result, err := importer.Rollback(ctx, run.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.Failed != 0 || result.Stats.Skipped == 0 {
		t.Fatalf("rollback result = %+v, want skipped stale snapshot without failure", result)
	}
	got, err := authorRepo.GetByID(ctx, stale.ID)
	if err != nil {
		t.Fatalf("Get stale author: %v", err)
	}
	if got.Name != "Imported Name" || got.ForeignID != "OL-STALE-AFTER" {
		t.Fatalf("stale author = %+v, want no restore after provenance moved", got)
	}
	link, err := provenanceRepo.GetByExternal(ctx, DefaultSourceID, "lib-books", entityTypeAuthor, "author-shared")
	if err != nil {
		t.Fatalf("GetByExternal: %v", err)
	}
	if link == nil || link.LocalID != current.ID {
		t.Fatalf("provenance = %+v, want current canonical link preserved", link)
	}
}

func TestImporter_Rollback_NilReposAreSafe(t *testing.T) {
	t.Parallel()

	importer := &Importer{}
	if _, err := importer.Rollback(context.Background(), 1); err == nil {
		t.Fatal("Rollback with nil repositories returned nil error, want unavailable error")
	}
	if _, err := importer.RollbackPreview(context.Background(), 1); err == nil {
		t.Fatal("RollbackPreview with nil repositories returned nil error, want unavailable error")
	}
}
