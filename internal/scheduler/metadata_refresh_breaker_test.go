package scheduler

import (
	"fmt"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
)

// Three authors that fail with the provider unavailable on every call trip the
// breaker every tick. They must stop heading the queue after a few trips so the
// healthy authors behind them are refreshed. 24 ticks cannot cover 93 authors
// at 4 a tick once ticks are lost to the trips, so the run is 30 ticks.
func TestRefreshMetadata_PersistentlyFailingTripleDoesNotStarveLibrary(t *testing.T) {
	f := newRefreshFixture(t, 96) // batch 4
	f.provider.fn = func(fid string) error {
		if fid == "OL000A" || fid == "OL001A" || fid == "OL002A" {
			return fmt.Errorf("get author: %w", metadata.ErrUnavailable)
		}
		return nil
	}
	for i := 0; i < 30; i++ {
		f.s.refreshMetadata()
	}
	healthy := 0
	for fid := range f.ids {
		if fid == "OL000A" || fid == "OL001A" || fid == "OL002A" {
			continue
		}
		if f.stamped(fid) != nil {
			healthy++
		}
	}
	if healthy != 93 {
		t.Errorf("%d of the 93 healthy authors refreshed in 30 ticks, want 93", healthy)
	}
}

// A provider outage that trips the breaker and then recovers cools nobody: the
// same authors head the next tick and are refreshed at once. This holds for an
// outage of one tick and for one of two ticks, one short of the threshold.
func TestRefreshMetadata_ShortOutageCoolsNobodyAndRecovers(t *testing.T) {
	for _, outageTicks := range []int{1, metadataRefreshBreakerTripsToCool - 1} {
		t.Run(fmt.Sprintf("%d ticks", outageTicks), func(t *testing.T) {
			f := newRefreshFixture(t, 96) // batch 4
			down := true
			f.provider.fn = func(string) error {
				if down {
					return fmt.Errorf("get author: %w", metadata.ErrUnavailable)
				}
				return nil
			}
			for i := 0; i < outageTicks; i++ {
				f.s.refreshMetadata()
			}
			if cooling := f.s.coolingAuthors(time.Now()); len(cooling) != 0 {
				t.Fatalf("an outage of %d ticks cooled %d authors, want none", outageTicks, len(cooling))
			}
			down = false
			f.provider.reset()
			f.s.refreshMetadata()
			got := f.provider.called()
			want := []string{"OL000A", "OL001A", "OL002A", "OL003A"}
			if len(got) != 4 {
				t.Fatalf("recovered tick refreshed %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] || f.stamped(want[i]) == nil {
					t.Fatalf("recovered tick refreshed %v, want %v stamped", got, want)
				}
			}
		})
	}
}

// The trip count of an author is cleared when it later succeeds, so trips
// separated by a success never add up to a cooldown.
func TestRefreshMetadata_BreakerTripCountResetsOnSuccess(t *testing.T) {
	f := newRefreshFixture(t, 96) // batch 4
	down := true
	f.provider.fn = func(string) error {
		if down {
			return fmt.Errorf("get author: %w", metadata.ErrUnavailable)
		}
		return nil
	}
	f.s.refreshMetadata()
	f.s.refreshMetadata()
	for _, fid := range []string{"OL000A", "OL001A", "OL002A"} {
		if got := f.s.breakerTripCount(f.ids[fid]); got != 2 {
			t.Fatalf("%s trip count after two tripped ticks = %d, want 2", fid, got)
		}
	}
	down = false
	f.s.refreshMetadata()
	for _, fid := range []string{"OL000A", "OL001A", "OL002A"} {
		if got := f.s.breakerTripCount(f.ids[fid]); got != 0 {
			t.Errorf("%s trip count after a success = %d, want 0", fid, got)
		}
	}
}
