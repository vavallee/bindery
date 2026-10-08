package downloader

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestHealthStoreAdvisorySurvivesProbe pins that the 15 minute path probe,
// which rewrites the client's health with Set, does not erase an advisory,
// and that advisories from several sources are all shown (#3024).
func TestHealthStoreAdvisorySurvivesProbe(t *testing.T) {
	store := NewHealthStore()
	store.SetAdvisory(7, AdvisoryBlocklist, models.DownloadClientHealth{Status: HealthError, Message: "paused"})
	store.Set(7, models.DownloadClientHealth{Status: HealthOK, Message: "paths fine"})
	if h := store.Get(7); h == nil || h.Status != HealthError || h.Message != "paused" {
		t.Fatalf("after a passing probe = %+v, want the advisory", h)
	}
	store.Set(7, models.DownloadClientHealth{Status: HealthError, Message: "path missing."})
	if h := store.Get(7); h == nil || h.Message != "path missing. paused" {
		t.Fatalf("after a failing probe = %+v, want both messages", h)
	}
	store.SetAdvisory(7, AdvisoryUnpackers, models.DownloadClientHealth{Status: HealthError, Message: "no unrar"})
	if h := store.Get(7); h == nil || h.Message != "path missing. paused. no unrar" {
		t.Fatalf("with two advisories = %+v, want all three messages", h)
	}
	store.ClearAdvisory(7, AdvisoryBlocklist)
	store.ClearAdvisory(7, AdvisoryUnpackers)
	if h := store.Get(7); h == nil || h.Message != "path missing." {
		t.Fatalf("after ClearAdvisory = %+v, want the probe result", h)
	}
	store.SetAdvisory(7, AdvisoryBlocklist, models.DownloadClientHealth{Status: HealthError, Message: "paused"})
	store.ForgetClient(7)
	if h := store.Get(7); h == nil || h.Message != "path missing." {
		t.Fatalf("after ForgetClient = %+v, want the probe result", h)
	}
}

// TestHealthStoreAdvisoryNotifiesOnce pins that an advisory publishes the
// health event on entry only, like Set's entry into HealthError.
func TestHealthStoreAdvisoryNotifiesOnce(t *testing.T) {
	spy := &healthSpy{}
	store := NewHealthStore().WithNotifier(spy)
	store.SetAdvisory(7, AdvisoryBlocklist, models.DownloadClientHealth{Status: HealthError, Message: "paused"})
	store.SetAdvisory(7, AdvisoryBlocklist, models.DownloadClientHealth{Status: HealthError, Message: "paused"})
	if len(spy.calls) != 1 || spy.calls[0].eventType != notifierEventHealth {
		t.Fatalf("health events = %+v, want exactly one", spy.calls)
	}
}

// unpackerFixture is a HealthStore whose sysinfo call and clock are scripted.
type unpackerFixture struct {
	store   *HealthStore
	now     time.Time
	calls   int
	missing []string
	err     error
}

func newUnpackerFixture() *unpackerFixture {
	f := &unpackerFixture{store: NewHealthStore(), now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	f.store.now = func() time.Time { return f.now }
	f.store.sysinfo = func(context.Context, *models.DownloadClient) ([]string, error) {
		f.calls++
		return f.missing, f.err
	}
	return f
}

var nzbgetClient7 = &models.DownloadClient{ID: 7, Name: "nzbget", Type: "nzbget"}

// TestNZBGetUnpackersIsLazyAndCached pins the #3024 review point that sysinfo
// is not cheap (NZBGet fetches ip.nzbget.com and runs which, unrar and 7z):
// it is asked once, reused for unpackerCheckTTL, and asked again on force.
func TestNZBGetUnpackersIsLazyAndCached(t *testing.T) {
	f := newUnpackerFixture()
	ctx := context.Background()
	f.missing = []string{"UnRAR"}
	for i := 0; i < 5; i++ {
		if got := f.store.NZBGetUnpackers(ctx, nzbgetClient7, false); len(got) != 1 {
			t.Fatalf("call %d = %v", i, got)
		}
	}
	if f.calls != 1 {
		t.Fatalf("sysinfo calls = %d for five lookups inside the TTL, want 1", f.calls)
	}
	f.now = f.now.Add(unpackerCheckTTL + time.Minute)
	f.store.NZBGetUnpackers(ctx, nzbgetClient7, false)
	f.store.NZBGetUnpackers(ctx, nzbgetClient7, true)
	if f.calls != 3 {
		t.Fatalf("sysinfo calls = %d after the TTL and a forced check, want 3", f.calls)
	}
	if got := f.store.NZBGetUnpackers(ctx, &models.DownloadClient{ID: 8, Type: "sabnzbd"}, true); got != nil || f.calls != 3 {
		t.Fatalf("a SABnzbd client asked sysinfo: %v, calls %d", got, f.calls)
	}
}

// TestNZBGetUnpackersKeepsLastKnownOnFailure: a timeout must not make a
// missing UnRAR look found, and a failing NZBGet is not asked on every call.
func TestNZBGetUnpackersKeepsLastKnownOnFailure(t *testing.T) {
	f := newUnpackerFixture()
	ctx := context.Background()
	f.missing = []string{"UnRAR"}
	f.store.NZBGetUnpackers(ctx, nzbgetClient7, false)

	f.now = f.now.Add(unpackerCheckTTL + time.Minute)
	f.missing, f.err = nil, errors.New("timeout")
	if got := f.store.NZBGetUnpackers(ctx, nzbgetClient7, false); len(got) != 1 || got[0] != "UnRAR" {
		t.Fatalf("after a failed sysinfo = %v, want the last known [UnRAR]", got)
	}
	if h := f.store.Get(7); h == nil || !strings.Contains(h.Message, "cannot find UnRAR") {
		t.Fatalf("health after a failed sysinfo = %+v, want the UnRAR error kept", h)
	}
	calls := f.calls
	f.now = f.now.Add(time.Minute)
	f.store.NZBGetUnpackers(ctx, nzbgetClient7, false)
	if f.calls != calls {
		t.Fatal("a failing sysinfo was asked again a minute later")
	}
	f.now = f.now.Add(unpackerRetryAfterFailure)
	f.store.NZBGetUnpackers(ctx, nzbgetClient7, false)
	if f.calls != calls+1 {
		t.Fatal("a failing sysinfo was not retried after the back off")
	}
}

// TestNZBGetUnpackersAdvisory: a missing UnRAR is an error on the client; a
// missing 7-Zip alone is not, as NZBGet's default SevenZipCmd is often absent.
func TestNZBGetUnpackersAdvisory(t *testing.T) {
	f := newUnpackerFixture()
	ctx := context.Background()
	f.store.Set(7, models.DownloadClientHealth{Status: HealthOK, Message: "paths fine"})

	f.missing = []string{"7-Zip"}
	f.store.NZBGetUnpackers(ctx, nzbgetClient7, true)
	if h := f.store.Get(7); h == nil || h.Status != HealthOK {
		t.Fatalf("missing 7-Zip alone turned the client %+v, want it left ok", h)
	}

	f.missing = []string{"7-Zip", "UnRAR"}
	f.store.NZBGetUnpackers(ctx, nzbgetClient7, true)
	if h := f.store.Get(7); h == nil || h.Status != HealthError || !strings.Contains(h.Message, "UnRAR") {
		t.Fatalf("missing UnRAR: %+v, want an error naming it", h)
	}

	f.missing = nil
	f.store.NZBGetUnpackers(ctx, nzbgetClient7, true)
	if h := f.store.Get(7); h == nil || h.Status != HealthOK {
		t.Fatalf("after UnRAR was found again: %+v, want ok", h)
	}
}
