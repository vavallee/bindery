package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
)

// failureClasses are the two ways one author's refresh can fail without the
// whole provider being down: for the author's own reasons, or transiently in a
// way the breaker also counts (server error, timeout, network error).
var failureClasses = []struct {
	name string
	err  func() error
}{
	{"own reason", func() error { return errors.New("author not found") }},
	{"unavailable", func() error { return fmt.Errorf("get author: %w", metadata.ErrUnavailable) }},
	{"timeout", func() error { return fmt.Errorf("get author: %w", context.DeadlineExceeded) }},
	{"net error", func() error { return &net.OpError{Op: "dial", Err: errors.New("connection refused")} }},
}

// One author that keeps failing must not hold the front of the queue: with a
// batch of one, the other authors still get their turn, and the failing one is
// retried once, not every tick.
func TestRefreshMetadata_FailingAuthorDoesNotStarveOthers_BatchOfOne(t *testing.T) {
	for _, class := range failureClasses {
		t.Run(class.name, func(t *testing.T) {
			f := newRefreshFixture(t, 3) // batch 1
			f.provider.fn = func(fid string) error {
				if fid == "OL000A" {
					return class.err()
				}
				return nil
			}
			for i := 0; i < 6; i++ {
				f.s.refreshMetadata()
			}
			calls := f.provider.called()
			failing := 0
			for _, fid := range calls {
				if fid == "OL000A" {
					failing++
				}
			}
			if failing != 1 {
				t.Errorf("failing author called %d times in 6 ticks, want once (calls %v)", failing, calls)
			}
			for _, fid := range []string{"OL001A", "OL002A"} {
				if f.stamped(fid) == nil {
					t.Errorf("%s starved behind the failing author; calls %v", fid, calls)
				}
			}
		})
	}
}

// With a batch of two and two authors failing, a day of ticks still refreshes
// the other 46.
func TestRefreshMetadata_FailingAuthorsDoNotStarveOthers_DayOfTicks(t *testing.T) {
	for _, class := range failureClasses {
		t.Run(class.name, func(t *testing.T) {
			f := newRefreshFixture(t, 48) // batch 2
			f.provider.fn = func(fid string) error {
				if fid == "OL000A" || fid == "OL001A" {
					return class.err()
				}
				return nil
			}
			for i := 0; i < 24; i++ {
				f.s.refreshMetadata()
			}
			refreshed := 0
			for fid := range f.ids {
				if f.stamped(fid) != nil {
					refreshed++
				}
			}
			if refreshed != 46 {
				t.Errorf("%d of the 46 healthy authors refreshed in 24 ticks, want 46", refreshed)
			}
		})
	}
}

// A provider wide outage still stops the batch at the breaker, and the authors
// it failed on are not cooled: they head the next tick, so a recovered
// provider reaches them at once.
func TestRefreshMetadata_ProviderOutageStopsBatchWithoutCoolingAuthors(t *testing.T) {
	f := newRefreshFixture(t, 96) // batch 4
	down := true
	f.provider.fn = func(string) error {
		if down {
			return fmt.Errorf("get author: %w", metadata.ErrUnavailable)
		}
		return nil
	}

	f.s.refreshMetadata()
	first := f.provider.called()
	if len(first) != metadataRefreshFailureBreaker {
		t.Fatalf("outage tick made %d calls %v, want the batch stopped at %d", len(first), first, metadataRefreshFailureBreaker)
	}
	if cooling := f.s.coolingAuthors(time.Now()); len(cooling) != 0 {
		t.Errorf("an outage cooled %d authors, want none", len(cooling))
	}

	down = false
	f.provider.reset()
	f.s.refreshMetadata()
	second := f.provider.called()
	if len(second) != 4 || second[0] != first[0] || second[1] != first[1] || second[2] != first[2] {
		t.Fatalf("recovered tick refreshed %v, want it to start with %v", second, first)
	}
}
