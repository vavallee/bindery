package migrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type dailyProvider struct {
	stubProvider
	held error
}

func (p *dailyProvider) CheckQuota(context.Context) error { return p.held }

func TestDailyQuotaStopsCSVAtFirstExhaustedRow(t *testing.T) {
	database := newTestDB(t)
	p := &dailyProvider{}
	calls := 0
	p.searchAuthorsFn = func(context.Context, string) ([]models.Author, error) {
		calls++
		p.held = &metadata.DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
		return nil, p.held
	}
	_, err := ImportCSVAuthors(context.Background(), strings.NewReader("name\nFirst Author\nSecond Author\nThird Author\n"), db.NewAuthorRepo(database), db.NewSettingsRepo(database), metadata.NewAggregator(p), nil)
	var daily *metadata.DailyQuotaError
	if !errors.As(err, &daily) || calls != 1 {
		t.Fatalf("calls %d err %v", calls, err)
	}
}

func TestDailyQuotaImmediatelyTripsImportBreaker(t *testing.T) {
	daily := &metadata.DailyQuotaError{ResetAt: time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)}
	p := &primaryOutage{}
	p.observe("goodreads", metadata.SearchOutcome{Primary: "hardcover", PrimaryFailed: true, FirstErr: fmt.Errorf("lookup: %w", daily)})
	if !p.down() || !strings.Contains(p.reason(), "2026-09-19T01:00:00Z") {
		t.Fatalf("down %v reason %s", p.down(), p.reason())
	}
}

func TestDailyQuotaHeldEnricherDoesNotTripImportBreaker(t *testing.T) {
	daily := &metadata.DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
	p := &primaryOutage{}
	// OpenLibrary answered; only the Hardcover enricher was held.
	held := metadata.SearchOutcome{
		Primary: "openlibrary", Answered: []string{"openlibrary"}, FailedProviders: []string{"hardcover"},
		FirstErr: fmt.Errorf("hardcover: %w", daily),
	}
	for range primaryOutageThreshold + 1 {
		p.observe("csv", held)
	}
	if p.down() {
		t.Fatalf("held enricher tripped the breaker: %s", p.reason())
	}
}

func TestDailyQuotaRerunQueuesCommittedEmptyCatalogues(t *testing.T) {
	database := newTestDB(t)
	authors, settings := db.NewAuthorRepo(database), db.NewSettingsRepo(database)
	p := &dailyProvider{stubProvider: stubProvider{name: "hardcover"}}
	p.searchAuthorsFn = func(_ context.Context, name string) ([]models.Author, error) {
		return []models.Author{{Name: name, ForeignID: "hc:" + name}}, nil
	}
	fetched := 0
	p.getAuthorFn = func(_ context.Context, id string) (*models.Author, error) {
		fetched++
		if fetched == 2 {
			p.held = &metadata.DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
		}
		return &models.Author{Name: strings.TrimPrefix(id, "hc:"), ForeignID: id}, nil
	}
	ctx := context.Background()
	agg := metadata.NewAggregator(p)
	oldPace := catalogueFetchPaceInterval
	catalogueFetchPaceInterval = 0
	defer func() { catalogueFetchPaceInterval = oldPace }()
	blockedCallback := make(chan struct{}, 1)
	res, err := ImportCSVAuthors(ctx, strings.NewReader("First\nSecond\nThird\n"), authors, settings, agg, func(*models.Author) { blockedCallback <- struct{}{} })
	if err == nil || res.Added != 2 {
		t.Fatalf("prefix: %+v %v", res, err)
	}
	select {
	case <-blockedCallback:
	case <-time.After(time.Second):
		t.Fatal("committed prefix not dispatched")
	}
	p.held = nil
	callbacks := make(chan string, 3)
	res, err = ImportCSVAuthors(ctx, strings.NewReader("First\nSecond\nThird\n"), authors, settings, agg, func(a *models.Author) { callbacks <- a.Name })
	if err != nil || res.Added != 1 || res.Skipped != 2 {
		t.Fatalf("rerun: %+v %v", res, err)
	}
	seen := make(map[string]bool)
	for range 3 {
		select {
		case name := <-callbacks:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatal("empty catalogue stranded after rerun")
		}
	}
	if len(seen) != 3 {
		t.Fatalf("callbacks: %v", seen)
	}
	// A deliberately emptied catalogue must remain skipped.
	existing, err := authors.GetByAnyForeignID(ctx, "hc:First")
	if err != nil {
		t.Fatal(err)
	}
	if err := authors.MarkCataloguePopulated(ctx, existing.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	result := newResult()
	if got := resolveAndCreateAuthor(ctx, "csv", "First", true, authors, settings, agg, &primaryOutage{}, newLibraryAuthors(authors), result); got != nil {
		t.Fatal("previously populated author requeued")
	}
}

func TestDailyQuotaFailedCatalogueReadCountsOnce(t *testing.T) {
	database := newTestDB(t)
	authors, settings := db.NewAuthorRepo(database), db.NewSettingsRepo(database)
	ctx := context.Background()
	if err := authors.Create(ctx, &models.Author{Name: "Existing", ForeignID: "hc:Existing", MetadataProvider: "hardcover"}); err != nil {
		t.Fatal(err)
	}
	// Only the catalogue marker read uses this column, so this fails just that read.
	if _, err := database.Exec(`ALTER TABLE authors RENAME COLUMN catalogue_populated_at TO catalogue_populated_at_gone`); err != nil {
		t.Fatal(err)
	}
	p := &dailyProvider{stubProvider: stubProvider{name: "hardcover"}}
	p.searchAuthorsFn = func(_ context.Context, name string) ([]models.Author, error) {
		return []models.Author{{Name: name, ForeignID: "hc:" + name}}, nil
	}
	res := newResult()
	if got := resolveAndCreateAuthor(ctx, "csv", "Existing", true, authors, settings, metadata.NewAggregator(p), &primaryOutage{}, newLibraryAuthors(authors), res); got != nil {
		t.Fatal("author requeued despite failed marker read")
	}
	if res.Errors != 1 || res.Skipped != 0 {
		t.Fatalf("errors=%d skipped=%d, want 1 and 0", res.Errors, res.Skipped)
	}
}
