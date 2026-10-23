package db

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// #3368: one Hardcover id must not end up on two series, whichever path sees
// it second.

func hcDupRepo(t *testing.T) *SeriesRepo {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewSeriesRepo(database)
}

func hcDupManual(t *testing.T, repo *SeriesRepo, foreignID, hcID string) *models.Series {
	t.Helper()
	ctx := context.Background()
	s := &models.Series{ForeignID: foreignID, Title: "Stormlight"}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if hcID != "" {
		if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: s.ID, HardcoverSeriesID: hcID, Confidence: 1, LinkedBy: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func seriesCount(t *testing.T, repo *SeriesRepo) int {
	t.Helper()
	all, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

// A sync that sees hc-series:N after a hand-added series linked to N reuses
// that series instead of creating a second row.
func TestCreateOrGetReusesSeriesLinkedToHardcoverID(t *testing.T) {
	repo := hcDupRepo(t)
	ctx := context.Background()
	manual := hcDupManual(t, repo, "manual:series:mine", "hc-series:42")

	got := &models.Series{ForeignID: "hc-series:42", Title: "The Stormlight Archive"}
	if err := repo.CreateOrGet(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got.ID != manual.ID {
		t.Fatalf("CreateOrGet id = %d, want the linked series %d", got.ID, manual.ID)
	}
	if n := seriesCount(t, repo); n != 1 {
		t.Fatalf("series rows = %d, want 1", n)
	}
	// The sync's follow-up link write must not take the id over or fail.
	linked, err := repo.EnsureHardcoverLinkFromForeignID(ctx, got.ID, "hc-series:42", "The Stormlight Archive")
	if err != nil || linked {
		t.Fatalf("ensure = %v, %v; want false, nil (link already there)", linked, err)
	}
}

// The bare spelling a client may have stored still counts as the same id.
func TestCreateOrGetReusesSeriesLinkedToBareHardcoverID(t *testing.T) {
	repo := hcDupRepo(t)
	manual := hcDupManual(t, repo, "manual:series:mine", "42")
	got := &models.Series{ForeignID: "hc-series:42", Title: "Stormlight"}
	if err := repo.CreateOrGet(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if got.ID != manual.ID || seriesCount(t, repo) != 1 {
		t.Fatalf("id = %d rows = %d, want the bare-linked series %d only", got.ID, seriesCount(t, repo), manual.ID)
	}
}

// Another provider's id that merely ends in the same digits is not a match.
func TestCreateOrGetIgnoresLinkForOtherProviderIDs(t *testing.T) {
	repo := hcDupRepo(t)
	manual := hcDupManual(t, repo, "manual:series:mine", "hc-series:42")
	got := &models.Series{ForeignID: "ol-series:42", Title: "Other"}
	if err := repo.CreateOrGet(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if got.ID == manual.ID || seriesCount(t, repo) != 2 {
		t.Fatalf("ol-series:42 was folded into the hardcover series (id %d)", got.ID)
	}
}

// The usual case keeps working: an unseen hc id creates, a seen one is found.
func TestCreateOrGetStillCreatesUnlinkedHardcoverID(t *testing.T) {
	repo := hcDupRepo(t)
	ctx := context.Background()
	hcDupManual(t, repo, "manual:series:mine", "hc-series:42")
	a := &models.Series{ForeignID: "hc-series:7", Title: "Other"}
	b := &models.Series{ForeignID: "hc-series:7", Title: "Other"}
	if err := repo.CreateOrGet(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateOrGet(ctx, b); err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.ID != b.ID || seriesCount(t, repo) != 2 {
		t.Fatalf("ids %d %d rows %d, want one new series reused", a.ID, b.ID, seriesCount(t, repo))
	}
}

// Concurrent syncs and a manual link racing for one id leave one series.
func TestCreateOrGetConcurrentWithLinkLeavesOneSeries(t *testing.T) {
	repo := hcDupRepo(t)
	ctx := context.Background()
	manual := hcDupManual(t, repo, "manual:series:mine", "hc-series:42")
	var wg sync.WaitGroup
	ids := make([]int64, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &models.Series{ForeignID: "hc-series:42", Title: "Stormlight"}
			if err := repo.CreateOrGet(ctx, s); err != nil {
				t.Error(err)
			}
			ids[i] = s.ID
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != manual.ID {
			t.Fatalf("ids = %v, want all %d", ids, manual.ID)
		}
	}
	if n := seriesCount(t, repo); n != 1 {
		t.Fatalf("series rows = %d, want 1", n)
	}
}

func TestUpsertHardcoverLinkRejectsIDHeldByAnotherSeries(t *testing.T) {
	repo := hcDupRepo(t)
	ctx := context.Background()
	first := hcDupManual(t, repo, "manual:series:a", "hc-series:42")
	second := hcDupManual(t, repo, "manual:series:b", "")

	err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: second.ID, HardcoverSeriesID: "hc-series:42", Confidence: 1})
	var conflict *HardcoverLinkConflictError
	if !errors.Is(err, ErrHardcoverIDLinked) || !errors.As(err, &conflict) || conflict.SeriesID != first.ID {
		t.Fatalf("err = %v, want a conflict naming series %d", err, first.ID)
	}
	if link, _ := repo.GetHardcoverLink(ctx, second.ID); link != nil {
		t.Fatalf("second series got a link anyway: %+v", link)
	}
	// Bare spelling of the same id is the same id.
	err = repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: second.ID, HardcoverSeriesID: "42"})
	if !errors.Is(err, ErrHardcoverIDLinked) {
		t.Fatalf("bare id err = %v, want a conflict", err)
	}
	// A different id is fine, and so is the owner rewriting its own link.
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: second.ID, HardcoverSeriesID: "hc-series:43"}); err != nil {
		t.Fatalf("other id: %v", err)
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: first.ID, HardcoverSeriesID: "hc-series:42", HardcoverTitle: "renamed"}); err != nil {
		t.Fatalf("owner rewrite: %v", err)
	}
}

// A series that carries the id as its foreign id holds it even with no link.
func TestUpsertHardcoverLinkRejectsIDOwnedByForeignIDOrAlias(t *testing.T) {
	repo := hcDupRepo(t)
	ctx := context.Background()
	owner := hcDupManual(t, repo, "hc-series:42", "")
	other := hcDupManual(t, repo, "manual:series:b", "")
	err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: other.ID, HardcoverSeriesID: "hc-series:42"})
	var conflict *HardcoverLinkConflictError
	if !errors.As(err, &conflict) || conflict.SeriesID != owner.ID {
		t.Fatalf("foreign id: err = %v, want conflict with %d", err, owner.ID)
	}

	if _, err := repo.db.ExecContext(ctx, "INSERT INTO series_aliases (foreign_id, series_id) VALUES ('hc-series:99', ?)", owner.ID); err != nil {
		t.Fatal(err)
	}
	err = repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: other.ID, HardcoverSeriesID: "hc-series:99"})
	if !errors.As(err, &conflict) || conflict.SeriesID != owner.ID {
		t.Fatalf("alias: err = %v, want conflict with %d", err, owner.ID)
	}
}

// A library that already holds a duplicate can still refresh the link metadata
// of either series; only moving onto a new id is refused.
func TestUpsertHardcoverLinkAllowsRewriteInExistingDuplicate(t *testing.T) {
	repo := hcDupRepo(t)
	ctx := context.Background()
	a := hcDupManual(t, repo, "manual:series:a", "")
	b := hcDupManual(t, repo, "manual:series:b", "")
	// Plant the legacy state directly, past the guard.
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := repo.db.ExecContext(ctx, "INSERT INTO series_hardcover_links (series_id, hardcover_series_id) VALUES (?, 'hc-series:42')", id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{a.ID, b.ID} {
		if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: id, HardcoverSeriesID: "hc-series:42", HardcoverSlug: "s"}); err != nil {
			t.Fatalf("refresh of series %d: %v", id, err)
		}
	}
}

func TestEnsureHardcoverLinkFromForeignIDLeavesIDWithOwner(t *testing.T) {
	repo := hcDupRepo(t)
	ctx := context.Background()
	owner := hcDupManual(t, repo, "manual:series:a", "hc-series:42")
	legacy := hcDupManual(t, repo, "hc-series:42", "")
	linked, err := repo.EnsureHardcoverLinkFromForeignID(ctx, legacy.ID, "hc-series:42", "Stormlight")
	if err != nil || linked {
		t.Fatalf("ensure = %v, %v; want false, nil", linked, err)
	}
	if link, _ := repo.GetHardcoverLink(ctx, owner.ID); link == nil {
		t.Fatal("owner lost its link")
	}
}

func TestListHardcoverDuplicates(t *testing.T) {
	repo := hcDupRepo(t)
	ctx := context.Background()
	a := hcDupManual(t, repo, "manual:series:a", "")
	b := hcDupManual(t, repo, "hc-series:42", "")
	c := hcDupManual(t, repo, "manual:series:c", "")
	hcDupManual(t, repo, "manual:series:d", "hc-series:7")
	for _, row := range []struct {
		id int64
		hc string
	}{{a.ID, "hc-series:42"}, {c.ID, "42"}} {
		if _, err := repo.db.ExecContext(ctx, "INSERT INTO series_hardcover_links (series_id, hardcover_series_id) VALUES (?, ?)", row.id, row.hc); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.ListHardcoverDuplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].HardcoverSeriesID != "hc-series:42" || len(got[0].SeriesIDs) != 3 ||
		got[0].SeriesIDs[0] != a.ID || got[0].SeriesIDs[1] != b.ID || got[0].SeriesIDs[2] != c.ID {
		t.Fatalf("duplicates = %+v, want one group of %d %d %d", got, a.ID, b.ID, c.ID)
	}
}
