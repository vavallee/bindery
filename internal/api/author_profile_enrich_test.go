package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// The manual Refresh Metadata always asks the enrichers, even right after the
// staggered refresh stamped the author, and stamps a full answer (#3287).
func TestRefreshAuthorProfile_EnrichesWhateverTheStamp(t *testing.T) {
	provider := &stubMetaProvider{author: &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann"}}
	f := newCoreFixture(t, provider)
	e := &countingEnricher{}
	f.h.meta.SetAuthorEnrichers(e)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "openlibrary"}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	earlier := time.Now().Add(-time.Hour)
	if err := f.authors.StampEnrichment(ctx, author.ID, earlier); err != nil {
		t.Fatal(err)
	}

	f.h.refreshAuthorProfile(ctx, author, true)

	got, err := f.authors.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.calls() != 1 || got.ImageURL != "https://commons.example/k.jpg" || got.ImageSource == nil {
		t.Errorf("%d enricher calls, photo %q from %+v; want the enriched photo", e.calls(), got.ImageURL, got.ImageSource)
	}
	if ts, err := f.authors.LastEnrichmentAt(ctx, author.ID); err != nil || ts == nil || !ts.After(earlier.Add(time.Minute)) {
		t.Errorf("enrichment stamp %v, %v; want it moved to now", ts, err)
	}

	// Nothing new on a second click: the profile is unchanged, the answer is
	// still stamped.
	if err := f.authors.StampEnrichment(ctx, author.ID, earlier); err != nil {
		t.Fatal(err)
	}
	f.h.refreshAuthorProfile(ctx, got, true)
	if ts, _ := f.authors.LastEnrichmentAt(ctx, author.ID); ts == nil || !ts.After(earlier.Add(time.Minute)) {
		t.Errorf("an unchanged refresh did not stamp the enrichment: %v", ts)
	}
}

// An add and the Refresh button always enrich. The other refresh-path syncs
// (bulk refresh, Refresh all, scheduled discovery) enrich once the author's
// enrichment is due: they stamp the profile fresh, which takes the author out
// of the staggered refresh, so leaving it to that refresh postponed it by a
// day each time they ran (#3398). An import never asks, however due.
func TestCatalogueSyncEnrichesOnlyWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name        string
		opts        catalogueSyncOptions
		enrichedAgo time.Duration // 0: never enriched
		want        int
	}{
		{"bulk refresh, never enriched", catalogueSyncOptions{discovery: true}, 0, 1},
		{"bulk refresh, enrichment aged", catalogueSyncOptions{discovery: true}, db.AuthorEnrichmentInterval + time.Hour, 1},
		{"bulk refresh, enriched recently", catalogueSyncOptions{discovery: true}, time.Hour, 0},
		{"import, never enriched", catalogueSyncOptions{}, 0, 0},
		{"add or Refresh button, enriched recently", catalogueSyncOptions{discovery: true, enrichProfile: true}, time.Hour, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stubMetaProvider{author: &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann"}}
			f := newCoreFixture(t, provider)
			e := &countingEnricher{}
			f.h.meta.SetAuthorEnrichers(e)
			ctx := context.Background()
			author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "openlibrary", Monitored: true}
			if err := f.authors.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			before := time.Now().Add(-tc.enrichedAgo)
			if tc.enrichedAgo > 0 {
				if err := f.authors.StampEnrichment(ctx, author.ID, before); err != nil {
					t.Fatal(err)
				}
			}
			f.h.fetchAuthorBooks(ctx, author, tc.opts)
			if e.calls() != tc.want {
				t.Errorf("%d enricher calls, want %d", e.calls(), tc.want)
			}
			ts, _ := f.authors.LastEnrichmentAt(ctx, author.ID)
			restamped := ts != nil && (tc.enrichedAgo == 0 || ts.After(before.Add(time.Minute)))
			if restamped != (tc.want == 1) {
				t.Errorf("enrichment stamp %v, want restamped=%v", ts, tc.want == 1)
			}
		})
	}
}

// waitForStamp waits for the background sync to stamp the author's
// enrichment, then drains the fixture's jobs. drain cancels the sync's
// context, so it must not run before the stamp is written.
func waitForStamp(t *testing.T, f coreFixture, authorID int64, after time.Time) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ts, _ := f.authors.LastEnrichmentAt(context.Background(), authorID); ts != nil && ts.After(after) {
			break
		}
	}
	f.drain(t)
}

// Adding an author through the handler enriches it straight away (#3287):
// startAddSync sets enrichProfile.
func TestCreateHandlerEnrichesTheNewAuthor(t *testing.T) {
	provider := &stubMetaProvider{author: &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann"}}
	f := newCoreFixture(t, provider)
	e := &countingEnricher{}
	f.h.meta.SetAuthorEnrichers(e)
	body, _ := json.Marshal(map[string]any{"foreignAuthorId": "OL1A", "authorName": "Kari Nordmann", "monitored": true})
	rec := httptest.NewRecorder()
	f.h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var created models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	waitForStamp(t, f, created.ID, time.Time{})
	if e.calls() != 1 {
		t.Errorf("adding an author asked the enrichers %d times, want 1", e.calls())
	}
	if ts, _ := f.authors.LastEnrichmentAt(context.Background(), created.ID); ts == nil {
		t.Error("adding an author did not stamp its enrichment")
	}
}

// The author page's Refresh button enriches too, whatever the stamp says.
func TestRefreshHandlerEnrichesTheAuthor(t *testing.T) {
	provider := &stubMetaProvider{author: &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann"}}
	f := newCoreFixture(t, provider)
	e := &countingEnricher{}
	f.h.meta.SetAuthorEnrichers(e)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "openlibrary", Monitored: true}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	earlier := time.Now().Add(-time.Hour)
	if err := f.authors.StampEnrichment(ctx, author.ID, earlier); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(author.ID, 10)
	rec := httptest.NewRecorder()
	f.h.Refresh(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/author/"+id+"/refresh", nil), "id", id))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	waitForStamp(t, f, author.ID, earlier.Add(time.Minute))
	if e.calls() != 1 {
		t.Errorf("Refresh asked the enrichers %d times, want 1", e.calls())
	}
}

// A refresh whose save fails stamps no enrichment, so the next refresh asks
// again rather than holding a month for an answer that was never kept.
func TestRefreshAuthorProfile_FailedSaveDoesNotStampEnrichment(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authors := db.NewAuthorRepo(database)
	agg := metadata.NewAggregator(&stubMetaProvider{author: &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann"}})
	agg.SetAuthorEnrichers(&countingEnricher{})
	h := NewAuthorHandler(authors, nil, db.NewBookRepo(database), nil, agg, nil, db.NewMetadataProfileRepo(database), nil)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Refuse the whole row save, which writes image_url, but not the single
	// column enrichment stamp, so a stamp would still land if one were made.
	if _, err := database.Exec(`CREATE TRIGGER refuse_profile_save BEFORE UPDATE OF image_url ON authors BEGIN SELECT RAISE(ABORT, 'read only'); END`); err != nil {
		t.Fatal(err)
	}
	h.refreshAuthorProfile(ctx, author, true)
	if ts, err := authors.LastEnrichmentAt(ctx, author.ID); err != nil || ts != nil {
		t.Errorf("a failed save stamped the enrichment: %v, %v", ts, err)
	}
}

// The many-author syncs share an hourly budget of enrichments, so Refresh all
// on a large library asks about the first due authors and leaves the rest due
// for the staggered refresh (#3398). The Refresh button is outside it.
func TestSyncEnrichmentIsBudgeted(t *testing.T) {
	prev := syncEnrichmentsPerHour
	syncEnrichmentsPerHour = 2
	t.Cleanup(func() { syncEnrichmentsPerHour = prev })

	provider := &stubMetaProvider{author: &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann"}}
	f := newCoreFixture(t, provider)
	e := &countingEnricher{}
	f.h.meta.SetAuthorEnrichers(e)
	ctx := context.Background()
	var ids []int64
	for i, id := range []string{"OL1A", "OL2A", "OL3A", "OL4A"} {
		a := &models.Author{ForeignID: id, Name: fmt.Sprintf("Kari Nordmann %d", i), SortName: "Nordmann, Kari", MetadataProvider: "openlibrary", Monitored: true}
		if err := f.authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
		if i < 3 {
			f.h.fetchAuthorBooks(ctx, a, catalogueSyncOptions{discovery: true})
		} else {
			f.h.fetchAuthorBooks(ctx, a, catalogueSyncOptions{discovery: true, enrichProfile: true})
		}
	}
	if e.calls() != 3 {
		t.Errorf("%d enricher calls, want 2 from the budget and 1 from the Refresh button", e.calls())
	}
	if !f.authors.EnrichmentDue(ctx, ids[2]) {
		t.Error("the author past the budget should stay due for the staggered refresh")
	}
}

// The budget is per hour: a new hour gives the syncs a fresh one.
func TestTakeSyncEnrichmentWindow(t *testing.T) {
	prev := syncEnrichmentsPerHour
	syncEnrichmentsPerHour = 1
	t.Cleanup(func() { syncEnrichmentsPerHour = prev })
	h := &AuthorHandler{}
	start := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	if !h.takeSyncEnrichment(start) || h.takeSyncEnrichment(start.Add(59*time.Minute)) {
		t.Fatal("want one enrichment in the first hour")
	}
	if !h.takeSyncEnrichment(start.Add(time.Hour)) {
		t.Error("want a fresh budget an hour later")
	}
}
