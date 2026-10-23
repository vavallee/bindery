package abs

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

const unitSource, unitLib, unitItem = DefaultSourceID, "lib-unit", "item-unit"

func unitCfg() ImportConfig {
	return ImportConfig{SourceID: unitSource, LibraryID: unitLib}
}

func closedDB(t *testing.T) *db.ABSSeriesMatchRepo {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	return db.NewABSSeriesMatchRepo(database)
}

func seedMatchState(t *testing.T, imp *Importer, hash, outcome string, checkedAt time.Time) {
	t.Helper()
	err := imp.seriesMatchState.Upsert(context.Background(), &models.ABSSeriesMatchState{
		SourceID: unitSource, LibraryID: unitLib, ItemID: unitItem, InputHash: hash, Outcome: outcome,
	}, checkedAt)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSeriesMatchAlreadyDone_StateReadFailureRematches(t *testing.T) {
	t.Parallel()
	imp, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	imp.seriesMatchState = closedDB(t)
	if imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), &models.Book{}, unitItem, "h") {
		t.Fatal("an unreadable state table must never skip the match")
	}
}

func TestSeriesMatchAlreadyDone_UnknownOutcomeIsNotTrusted(t *testing.T) {
	t.Parallel()
	imp, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	seedMatchState(t, imp, "h", "something-from-the-future", time.Now())
	if imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), &models.Book{}, unitItem, "h") {
		t.Fatal("an outcome this build does not know must re-match")
	}
}

func TestSeriesMatchAlreadyDone_NoStateRepoOrFullRun(t *testing.T) {
	t.Parallel()
	imp, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	seedMatchState(t, imp, "h", seriesMatchOutcomeMatched, time.Now())
	ctx := context.Background()
	if !imp.seriesMatchAlreadyDone(ctx, unitCfg(), &models.Book{}, unitItem, "h") {
		t.Fatal("setup: a matching recorded state must skip")
	}
	full := unitCfg()
	full.Full = true
	if imp.seriesMatchAlreadyDone(ctx, full, &models.Book{}, unitItem, "h") {
		t.Fatal("a Full run must ignore recorded state")
	}
	imp.seriesMatchState = nil
	if imp.seriesMatchAlreadyDone(ctx, unitCfg(), &models.Book{}, unitItem, "h") {
		t.Fatal("without a state repo nothing can be skipped")
	}
}

func TestSeriesMatchAlreadyDone_MissExpiryBoundary(t *testing.T) {
	t.Parallel()
	imp, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	checked := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hash := "ffff"
	seedMatchState(t, imp, hash, seriesMatchOutcomeMiss, checked)
	expiry := seriesMatchMissExpiry(checked, hash)

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"one nanosecond before expiry still skips", expiry.Add(-time.Nanosecond), true},
		{"exactly at expiry is due again", expiry, false},
		{"after expiry is due again", expiry.Add(time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			imp.seriesMatchClock = func() time.Time { return tc.now }
			if got := imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), &models.Book{}, unitItem, hash); got != tc.want {
				t.Fatalf("seriesMatchAlreadyDone = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSeriesMatchAlreadyDone_ChangedHashNeverSkips(t *testing.T) {
	t.Parallel()
	imp, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	seedMatchState(t, imp, "old", seriesMatchOutcomeMatched, time.Now())
	if imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), &models.Book{}, unitItem, "new") {
		t.Fatal("a matched outcome recorded for different inputs must not skip")
	}
}

// linkedSeriesFor makes a series, optionally with a Hardcover link, and records
// ABS series provenance from unitItem to it.
func linkedSeriesFor(t *testing.T, seriesRepo *db.SeriesRepo, prov *db.ABSProvenanceRepo, name string, withLink bool) {
	t.Helper()
	ctx := context.Background()
	s, err := seriesRepo.CreateManual(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if withLink {
		if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: s.ID, HardcoverSeriesID: "hc-series:" + name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := prov.Upsert(ctx, &models.ABSProvenance{
		SourceID: unitSource, LibraryID: unitLib, EntityType: "series", ExternalID: name, LocalID: s.ID, ItemID: unitItem,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSeriesMatchBootstrap_Adoption(t *testing.T) {
	t.Parallel()
	hc := &models.Book{MetadataProvider: providerHardcover}
	ol := &models.Book{MetadataProvider: "openlibrary"}

	t.Run("hardcover book with a linked series is adopted and recorded", func(t *testing.T) {
		imp, _, _, seriesRepo, _, prov, _, _, _, _ := newABSImporterFixture(t)
		linkedSeriesFor(t, seriesRepo, prov, "saga", true)
		if !imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), hc, unitItem, "hash1") {
			t.Fatal("want adoption")
		}
		st, _ := imp.seriesMatchState.Get(context.Background(), unitSource, unitLib, unitItem)
		if st == nil || st.Outcome != seriesMatchOutcomeMatched || st.InputHash != "hash1" {
			t.Fatalf("recorded = %+v, want matched with the current hash", st)
		}
	})

	t.Run("a link on the second series row is still found after an unlinked first row", func(t *testing.T) {
		imp, _, _, seriesRepo, _, prov, _, _, _, _ := newABSImporterFixture(t)
		linkedSeriesFor(t, seriesRepo, prov, "unlinked", false)
		linkedSeriesFor(t, seriesRepo, prov, "linked", true)
		if !imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), hc, unitItem, "h") {
			t.Fatal("want adoption through the second row")
		}
	})

	t.Run("series with no hardcover link is not adopted and nothing is recorded", func(t *testing.T) {
		imp, _, _, seriesRepo, _, prov, _, _, _, _ := newABSImporterFixture(t)
		linkedSeriesFor(t, seriesRepo, prov, "unlinked", false)
		if imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), hc, unitItem, "h") {
			t.Fatal("must not adopt without a link")
		}
		if st, _ := imp.seriesMatchState.Get(context.Background(), unitSource, unitLib, unitItem); st != nil {
			t.Fatalf("recorded %+v for a refused adoption", st)
		}
	})

	t.Run("non hardcover book is not adopted", func(t *testing.T) {
		imp, _, _, seriesRepo, _, prov, _, _, _, _ := newABSImporterFixture(t)
		linkedSeriesFor(t, seriesRepo, prov, "saga", true)
		if imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), ol, unitItem, "h") {
			t.Fatal("book without a Hardcover identity must not be adopted")
		}
	})

	t.Run("nil book is not adopted", func(t *testing.T) {
		imp, _, _, seriesRepo, _, prov, _, _, _, _ := newABSImporterFixture(t)
		linkedSeriesFor(t, seriesRepo, prov, "saga", true)
		if imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), nil, unitItem, "h") {
			t.Fatal("nil book must not be adopted")
		}
	})

	t.Run("another item's series membership is not evidence", func(t *testing.T) {
		imp, _, _, seriesRepo, _, prov, _, _, _, _ := newABSImporterFixture(t)
		linkedSeriesFor(t, seriesRepo, prov, "saga", true)
		if imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), hc, "some-other-item", "h") {
			t.Fatal("provenance of a different item must not be adopted")
		}
	})

	t.Run("provenance read failure falls through to a normal match", func(t *testing.T) {
		imp, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
		database, err := db.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		imp.provenance = db.NewABSProvenanceRepo(database)
		database.Close()
		if imp.seriesMatchAlreadyDone(context.Background(), unitCfg(), hc, unitItem, "h") {
			t.Fatal("a failed provenance read must not adopt")
		}
	})
}
