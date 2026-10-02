package prowlarr

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func intPtr(v int) *int { return &v }

func derefInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// TestFetchIndexers_ParsesSeedTime covers how Prowlarr's
// torrentBaseSettings.seedTime (a nullable int, minutes) maps onto
// IndexerInfo.SeedTimeMinutes (#2206). Prowlarr warns on anything <= 0, so
// only a positive whole number counts as a configured seed time.
func TestFetchIndexers_ParsesSeedTime(t *testing.T) {
	body := `[
		{"id":1,"name":"HasTime","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.seedRatio","value":1.0},{"name":"torrentBaseSettings.seedTime","value":4320}]},
		{"id":2,"name":"NullTime","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.seedTime","value":null}]},
		{"id":3,"name":"ZeroTime","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.seedTime","value":0}]},
		{"id":4,"name":"PackOnly","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.packSeedTime","value":600}]},
		{"id":5,"name":"Fractional","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}],
		 "fields":[{"name":"torrentBaseSettings.seedTime","value":1.5}]},
		{"id":6,"name":"Usenet","protocol":"usenet","supportsSearch":true,"categories":[{"id":7020}]}
	]`
	srv := prowlarrStub(t, body)
	defer srv.Close()

	infos, err := New(srv.URL, "k").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	byName := map[string]*int{}
	for _, in := range infos {
		byName[in.Name] = in.SeedTimeMinutes
	}
	if got := byName["HasTime"]; got == nil || *got != 4320 {
		t.Errorf("HasTime SeedTimeMinutes = %v, want 4320", derefInt(got))
	}
	for _, name := range []string{"NullTime", "ZeroTime", "PackOnly", "Fractional", "Usenet"} {
		if got := byName[name]; got != nil {
			t.Errorf("%s SeedTimeMinutes = %v, want nil", name, *got)
		}
	}
}

func TestApplyProwlarrSeedTime(t *testing.T) {
	cases := []struct {
		name        string
		in          models.Indexer
		prowlarr    *int
		wantChanged bool
		wantTime    *int
		wantSource  string
	}{
		{
			name:        "unset row gets Prowlarr seed time",
			in:          models.Indexer{},
			prowlarr:    intPtr(1440),
			wantChanged: true,
			wantTime:    intPtr(1440),
			wantSource:  models.SeedRatioSourceProwlarr,
		},
		{
			name:        "user value is never touched",
			in:          models.Indexer{SeedTimeMinutes: intPtr(60), SeedTimeSource: models.SeedRatioSourceUser},
			prowlarr:    intPtr(1440),
			wantChanged: false,
			wantTime:    intPtr(60),
			wantSource:  models.SeedRatioSourceUser,
		},
		{
			name:        "user clear sticks",
			in:          models.Indexer{SeedTimeMinutes: nil, SeedTimeSource: models.SeedRatioSourceUser},
			prowlarr:    intPtr(1440),
			wantChanged: false,
			wantTime:    nil,
			wantSource:  models.SeedRatioSourceUser,
		},
		{
			name:        "Prowlarr change refreshes a Prowlarr value",
			in:          models.Indexer{SeedTimeMinutes: intPtr(60), SeedTimeSource: models.SeedRatioSourceProwlarr},
			prowlarr:    intPtr(120),
			wantChanged: true,
			wantTime:    intPtr(120),
			wantSource:  models.SeedRatioSourceProwlarr,
		},
		{
			name:        "removed in Prowlarr clears a Prowlarr value",
			in:          models.Indexer{SeedTimeMinutes: intPtr(60), SeedTimeSource: models.SeedRatioSourceProwlarr},
			prowlarr:    nil,
			wantChanged: true,
			wantTime:    nil,
			wantSource:  models.SeedRatioSourceUnset,
		},
		{
			name:        "same value is not a change",
			in:          models.Indexer{SeedTimeMinutes: intPtr(60), SeedTimeSource: models.SeedRatioSourceProwlarr},
			prowlarr:    intPtr(60),
			wantChanged: false,
			wantTime:    intPtr(60),
			wantSource:  models.SeedRatioSourceProwlarr,
		},
		{
			name:        "seed time ignores a user owned ratio",
			in:          models.Indexer{SeedRatio: float64Ptr(2), SeedRatioSource: models.SeedRatioSourceUser},
			prowlarr:    intPtr(600),
			wantChanged: true,
			wantTime:    intPtr(600),
			wantSource:  models.SeedRatioSourceProwlarr,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := tc.in
			changed := applyProwlarrSeedTime(&idx, tc.prowlarr)
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if !intPtrEqual(idx.SeedTimeMinutes, tc.wantTime) {
				t.Errorf("SeedTimeMinutes = %v, want %v", derefInt(idx.SeedTimeMinutes), derefInt(tc.wantTime))
			}
			if idx.SeedTimeSource != tc.wantSource {
				t.Errorf("SeedTimeSource = %q, want %q", idx.SeedTimeSource, tc.wantSource)
			}
			// The ratio half is a separate decision and must not move.
			if idx.SeedRatioSource != tc.in.SeedRatioSource || !float64PtrEqual(idx.SeedRatio, tc.in.SeedRatio) {
				t.Errorf("seed time reconcile touched the ratio: %v %q", deref(idx.SeedRatio), idx.SeedRatioSource)
			}
		})
	}
}

// TestSyncer_SeedTime runs the whole sync: a new indexer gets Prowlarr's seed
// time with provenance, an existing row whose ratio the user owns still gets
// the seed time filled (the two provenances are independent), and an existing
// row whose seed time the user owns is left alone.
func TestSyncer_SeedTime(t *testing.T) {
	srv := prowlarrStub(t, `[
		{"id":7,"name":"New","enable":true,"protocol":"torrent","supportsSearch":true,
		 "categories":[{"id":7020}],"fields":[{"name":"torrentBaseSettings.seedTime","value":2880}]},
		{"id":8,"name":"RatioOwned","enable":true,"protocol":"torrent","supportsSearch":true,
		 "categories":[{"id":7020}],"fields":[{"name":"torrentBaseSettings.seedTime","value":600}]},
		{"id":9,"name":"TimeOwned","enable":true,"protocol":"torrent","supportsSearch":true,
		 "categories":[{"id":7020}],"fields":[{"name":"torrentBaseSettings.seedTime","value":600}]}
	]`)
	defer srv.Close()

	instID := int64(1)
	p8, p9 := 8, 9
	existing := []models.Indexer{
		{
			ID: 20, Name: "RatioOwned", Type: "torznab", URL: srv.URL + "/8/api", Categories: []int{7020},
			SeedRatio: float64Ptr(3), SeedRatioSource: models.SeedRatioSourceUser,
			ProwlarrInstanceID: &instID, ProwlarrIndexerID: &p8,
		},
		{
			ID: 21, Name: "TimeOwned", Type: "torznab", URL: srv.URL + "/9/api", Categories: []int{7020},
			SeedTimeMinutes: intPtr(45), SeedTimeSource: models.SeedRatioSourceUser,
			ProwlarrInstanceID: &instID, ProwlarrIndexerID: &p9,
		},
	}
	store := &fakeIndexerStore{existing: existing}
	if _, err := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{}).Sync(context.Background(), instID); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(store.created) != 1 {
		t.Fatalf("created = %d, want 1", len(store.created))
	}
	if c := store.created[0]; c.SeedTimeMinutes == nil || *c.SeedTimeMinutes != 2880 || c.SeedTimeSource != models.SeedRatioSourceProwlarr {
		t.Errorf("new indexer: SeedTimeMinutes=%v source=%q, want 2880 prowlarr", derefInt(c.SeedTimeMinutes), c.SeedTimeSource)
	}

	var sawRatioOwned bool
	for _, u := range store.updated {
		switch u.ID {
		case 20:
			sawRatioOwned = true
			if u.SeedTimeMinutes == nil || *u.SeedTimeMinutes != 600 || u.SeedTimeSource != models.SeedRatioSourceProwlarr {
				t.Errorf("RatioOwned: SeedTimeMinutes=%v source=%q, want 600 prowlarr", derefInt(u.SeedTimeMinutes), u.SeedTimeSource)
			}
			if u.SeedRatio == nil || *u.SeedRatio != 3 || u.SeedRatioSource != models.SeedRatioSourceUser {
				t.Errorf("RatioOwned: user ratio was modified: %v %q", deref(u.SeedRatio), u.SeedRatioSource)
			}
		case 21:
			t.Errorf("TimeOwned was updated although nothing it owns changed: SeedTimeMinutes=%v source=%q",
				derefInt(u.SeedTimeMinutes), u.SeedTimeSource)
		}
	}
	if !sawRatioOwned {
		t.Error("RatioOwned was not updated; a Prowlarr seed time alone must be enough to write the row")
	}
}
