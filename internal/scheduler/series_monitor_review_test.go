package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestResolveSeriesMonitorInterval(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	settings := db.NewSettingsRepo(database)
	s := &Scheduler{settings: settings}
	if _, on := s.resolveSeriesMonitorInterval(); on {
		t.Error("unset must be off")
	}
	cases := []struct {
		value string
		on    bool
		want  time.Duration
	}{
		{"off", false, 0},
		{"", false, 0},
		{"24h", true, 24 * time.Hour},
		{"720h", true, 720 * time.Hour},
		{"1h", true, defaultDiscoveryInterval},      // out of bounds falls back
		{"garbage", true, defaultDiscoveryInterval}, // unparseable falls back
	}
	for _, tc := range cases {
		if err := settings.Set(context.Background(), settingSeriesMonitoringInterval, tc.value); err != nil {
			t.Fatal(err)
		}
		d, on := s.resolveSeriesMonitorInterval()
		if on != tc.on || (on && d != tc.want) {
			t.Errorf("%q: got %v, %v, want %v, %v", tc.value, d, on, tc.want, tc.on)
		}
	}
	if _, on := (&Scheduler{}).resolveSeriesMonitorInterval(); on {
		t.Error("no settings repo must be off")
	}
}

// A series with no link, or with the provider switched off, is looked at again
// in hours, not after a whole interval, so linking it takes effect soon.
func TestSeriesMonitorJob_NoLinkRetriesSoon(t *testing.T) {
	old := discoveryPace
	discoveryPace = 0
	defer func() { discoveryPace = old }()
	for _, status := range []string{models.SeriesCheckNoLink, models.SeriesCheckDisabled, models.SeriesCheckAmbiguous} {
		f := newSeriesJobFixture(t, 1)
		f.out = func(*models.Series) SeriesCheckOutcome { return SeriesCheckOutcome{Status: status} }
		f.job.tick(context.Background())
		mon, _ := f.repo.GetMonitoring(context.Background(), f.ids[0])
		if want := f.now.Add(discoveryRetryAfter); mon == nil || !mon.NextCheckAt.Equal(want) {
			t.Errorf("%s: next check = %v, want %v", status, mon, want)
		}
	}
}
