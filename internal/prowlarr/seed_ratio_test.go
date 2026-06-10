package prowlarr

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func float64Ptr(v float64) *float64 { return &v }

// TestFetchIndexers_ParsesSeedRatio covers how a Prowlarr torrent indexer's
// torrentBaseSettings.seedRatio field maps onto IndexerInfo.SeedRatio.
func TestFetchIndexers_ParsesSeedRatio(t *testing.T) {
	body := `[
		{"id":1,"name":"HasRatio","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.seedRatio","value":2.5}]},
		{"id":2,"name":"NullRatio","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.seedRatio","value":null}]},
		{"id":3,"name":"NoField","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.appMinimumSeeders","value":5}]},
		{"id":4,"name":"ZeroRatio","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.seedRatio","value":0}]},
		{"id":5,"name":"Usenet","protocol":"usenet","supportsSearch":true,"categories":[{"id":7020}]}
	]`
	srv := prowlarrStub(t, body)
	defer srv.Close()

	infos, err := New(srv.URL, "k").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	byName := map[string]*float64{}
	for _, in := range infos {
		byName[in.Name] = in.SeedRatio
	}

	if got := byName["HasRatio"]; got == nil || *got != 2.5 {
		t.Errorf("HasRatio SeedRatio = %v, want 2.5", got)
	}
	for _, name := range []string{"NullRatio", "NoField", "ZeroRatio", "Usenet"} {
		if got := byName[name]; got != nil {
			t.Errorf("%s SeedRatio = %v, want nil", name, got)
		}
	}
}

func TestApplyProwlarrSeedRatio(t *testing.T) {
	cases := []struct {
		name        string
		in          models.Indexer
		prowlarr    *float64
		wantChanged bool
		wantRatio   *float64
		wantSource  string
	}{
		{
			name:        "unset row gets Prowlarr ratio",
			in:          models.Indexer{},
			prowlarr:    float64Ptr(1.5),
			wantChanged: true,
			wantRatio:   float64Ptr(1.5),
			wantSource:  models.SeedRatioSourceProwlarr,
		},
		{
			name:        "user override is never clobbered by a Prowlarr value",
			in:          models.Indexer{SeedRatio: float64Ptr(3), SeedRatioSource: models.SeedRatioSourceUser},
			prowlarr:    float64Ptr(1.5),
			wantChanged: false,
			wantRatio:   float64Ptr(3),
			wantSource:  models.SeedRatioSourceUser,
		},
		{
			name:        "user unlimited sentinel is never clobbered",
			in:          models.Indexer{SeedRatio: float64Ptr(-1), SeedRatioSource: models.SeedRatioSourceUser},
			prowlarr:    float64Ptr(2),
			wantChanged: false,
			wantRatio:   float64Ptr(-1),
			wantSource:  models.SeedRatioSourceUser,
		},
		{
			name:        "user-cleared (null, source=user) is never re-filled",
			in:          models.Indexer{SeedRatio: nil, SeedRatioSource: models.SeedRatioSourceUser},
			prowlarr:    float64Ptr(2),
			wantChanged: false,
			wantRatio:   nil,
			wantSource:  models.SeedRatioSourceUser,
		},
		{
			name:        "prowlarr-sourced value refreshes on a later Prowlarr change",
			in:          models.Indexer{SeedRatio: float64Ptr(1), SeedRatioSource: models.SeedRatioSourceProwlarr},
			prowlarr:    float64Ptr(2),
			wantChanged: true,
			wantRatio:   float64Ptr(2),
			wantSource:  models.SeedRatioSourceProwlarr,
		},
		{
			name:        "prowlarr-sourced value cleared when Prowlarr drops its ratio",
			in:          models.Indexer{SeedRatio: float64Ptr(1), SeedRatioSource: models.SeedRatioSourceProwlarr},
			prowlarr:    nil,
			wantChanged: true,
			wantRatio:   nil,
			wantSource:  models.SeedRatioSourceUnset,
		},
		{
			name:        "no-op when prowlarr-sourced value is unchanged",
			in:          models.Indexer{SeedRatio: float64Ptr(2), SeedRatioSource: models.SeedRatioSourceProwlarr},
			prowlarr:    float64Ptr(2),
			wantChanged: false,
			wantRatio:   float64Ptr(2),
			wantSource:  models.SeedRatioSourceProwlarr,
		},
		{
			name:        "unset row with no Prowlarr ratio stays unset (no-op)",
			in:          models.Indexer{},
			prowlarr:    nil,
			wantChanged: false,
			wantRatio:   nil,
			wantSource:  models.SeedRatioSourceUnset,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := tc.in
			changed := applyProwlarrSeedRatio(&idx, tc.prowlarr)
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if !float64PtrEqual(idx.SeedRatio, tc.wantRatio) {
				t.Errorf("SeedRatio = %v, want %v", deref(idx.SeedRatio), deref(tc.wantRatio))
			}
			if idx.SeedRatioSource != tc.wantSource {
				t.Errorf("SeedRatioSource = %q, want %q", idx.SeedRatioSource, tc.wantSource)
			}
		})
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// TestSyncer_AutoPopulatesSeedRatio exercises the full sync path: a new indexer
// is created with the Prowlarr-sourced ratio recorded as provenance "prowlarr".
func TestSyncer_AutoPopulatesSeedRatio(t *testing.T) {
	srv := prowlarrStub(t, `[{"id":7,"name":"RatioTracker","enable":true,"protocol":"torrent","supportsSearch":true,
		"categories":[{"id":7020}],"fields":[{"name":"torrentBaseSettings.seedRatio","value":1.75}]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{}
	if _, err := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{}).Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %d, want 1", len(store.created))
	}
	got := store.created[0]
	if got.SeedRatio == nil || *got.SeedRatio != 1.75 {
		t.Errorf("SeedRatio = %v, want 1.75", deref(got.SeedRatio))
	}
	if got.SeedRatioSource != models.SeedRatioSourceProwlarr {
		t.Errorf("SeedRatioSource = %q, want %q", got.SeedRatioSource, models.SeedRatioSourceProwlarr)
	}
}

// TestSyncer_DoesNotClobberUserSeedRatio confirms an explicit user override on
// an existing indexer survives a sync that reports a different Prowlarr ratio.
func TestSyncer_DoesNotClobberUserSeedRatio(t *testing.T) {
	pID := 7
	instID := int64(1)
	existing := []models.Indexer{{
		ID:                 10,
		Name:               "RatioTracker",
		Type:               "torznab",
		URL:                "", // forces a different field too, but ratio must stay
		Categories:         []int{7020},
		SeedRatio:          float64Ptr(4),
		SeedRatioSource:    models.SeedRatioSourceUser,
		ProwlarrInstanceID: &instID,
		ProwlarrIndexerID:  &pID,
	}}
	// Build the matching Torznab URL so only the ratio question is under test.
	srv := prowlarrStub(t, `[{"id":7,"name":"RatioTracker","enable":true,"protocol":"torrent","supportsSearch":true,
		"categories":[{"id":7020}],"fields":[{"name":"torrentBaseSettings.seedRatio","value":1.0}]}]`)
	defer srv.Close()
	existing[0].URL = srv.URL + "/7/api"

	store := &fakeIndexerStore{existing: existing}
	if _, err := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{}).Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// Nothing else changed, so the row should not be updated at all.
	for _, u := range store.updated {
		if u.SeedRatio == nil || *u.SeedRatio != 4 || u.SeedRatioSource != models.SeedRatioSourceUser {
			t.Errorf("user override was modified: SeedRatio=%v source=%q",
				deref(u.SeedRatio), u.SeedRatioSource)
		}
	}
}
