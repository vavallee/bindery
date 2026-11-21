package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

var streakClasses = []string{"indexer", "release", "neverArrived"}

type streakFixture struct {
	t    *testing.T
	repo *HistoryRepo
	now  time.Time
}

func newStreakFixture(t *testing.T) *streakFixture {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &streakFixture{t: t, repo: NewHistoryRepo(database), now: time.Now().UTC()}
}

// add writes an event `ago` in the past.
func (f *streakFixture) add(eventType, title string, ago time.Duration, data map[string]string) {
	f.t.Helper()
	raw, _ := json.Marshal(data)
	e := &models.HistoryEvent{EventType: eventType, SourceTitle: title, Data: string(raw)}
	if err := f.repo.Create(context.Background(), e); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.repo.db.Exec(`UPDATE history SET created_at = ? WHERE id = ?`, f.now.Add(-ago), e.ID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *streakFixture) fail(class string, ago time.Duration) {
	f.t.Helper()
	f.add(models.HistoryEventDownloadFailed, "T", ago, map[string]string{"guid": "g", "class": class})
}

func (f *streakFixture) streak() (int, time.Time) {
	f.t.Helper()
	n, last, _, err := f.repo.ReleaseFailureStreak(context.Background(), "g", "T", nil,
		f.now.Add(-7*24*time.Hour), streakClasses, "neverArrived", time.Hour)
	if err != nil {
		f.t.Fatal(err)
	}
	return n, last
}

func TestReleaseFailureStreak_WindowAndClassFilter(t *testing.T) {
	f := newStreakFixture(t)
	f.fail("indexer", 8*24*time.Hour)  // older than the window
	f.fail("local", 3*time.Hour)       // not a counted class
	f.fail("clientError", 4*time.Hour) // not a counted class
	f.fail("indexerWide", 5*time.Hour) // not a counted class
	f.fail("release", 6*time.Hour)     // counts
	f.add(models.HistoryEventDownloadFailed, "T", time.Hour, map[string]string{"guid": "other", "class": "indexer"})
	if n, _ := f.streak(); n != 1 {
		t.Fatalf("streak = %d, want 1: only the in-window failure of a counted class and this guid counts", n)
	}
}

func TestReleaseFailureStreak_SpacingAndImportReset(t *testing.T) {
	f := newStreakFixture(t)
	f.fail("indexer", 10*time.Hour)
	f.fail("indexer", 9*time.Hour+50*time.Minute) // inside the gap of the first
	f.fail("indexer", 5*time.Hour)
	if n, _ := f.streak(); n != 2 {
		t.Fatalf("streak = %d, want 2: a failure inside the gap is recorded but not counted", n)
	}

	// An import of the release, keyed on its guid, resets the run.
	f.add(models.HistoryEventBookImported, "unrelated title", 4*time.Hour, map[string]string{"guid": "g"})
	f.fail("indexer", time.Hour)
	if n, _ := f.streak(); n != 1 {
		t.Fatalf("streak = %d after an import, want 1", n)
	}
}

func TestReleaseFailureStreak_ImportKeyedOnGUIDNotTitle(t *testing.T) {
	f := newStreakFixture(t)
	f.fail("indexer", 10*time.Hour)
	// Same title, another release: must not reset this one.
	f.add(models.HistoryEventBookImported, "T", 5*time.Hour, map[string]string{"guid": "someone-else"})
	if n, _ := f.streak(); n != 1 {
		t.Fatalf("streak = %d, want 1: an import of another release with the same title reset the run", n)
	}
	// An old import with no guid falls back to title (and book), here both match.
	f.add(models.HistoryEventBookImported, "T", 4*time.Hour, map[string]string{})
	if n, _ := f.streak(); n != 0 {
		t.Fatalf("streak = %d, want 0 after a guid-less import of the same title", n)
	}
	// An empty title never matches by fallback.
	g := newStreakFixture(t)
	g.add(models.HistoryEventDownloadFailed, "", 10*time.Hour, map[string]string{"guid": "g", "class": "indexer"})
	g.add(models.HistoryEventBookImported, "", 5*time.Hour, map[string]string{})
	n, _, _, err := g.repo.ReleaseFailureStreak(context.Background(), "g", "", nil,
		g.now.Add(-7*24*time.Hour), streakClasses, "neverArrived", time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("empty title: streak = %d (%v), want 1", n, err)
	}
}

func TestReleaseFailureStreak_GrabResets(t *testing.T) {
	f := newStreakFixture(t)
	f.fail("indexer", 20*time.Hour)
	f.fail("indexer", 14*time.Hour)
	// A send that went through, then another with nothing between: the first
	// attempt did not fail, so the run is over.
	f.add(models.HistoryEventGrabbed, "T", 8*time.Hour, map[string]string{"guid": "g"})
	f.add(models.HistoryEventGrabbed, "T", 2*time.Hour, map[string]string{"guid": "g"})
	f.fail("indexer", time.Hour)
	if n, _ := f.streak(); n != 1 {
		t.Fatalf("streak = %d after successful sends, want 1", n)
	}
}

// A send that went through without a failure after it breaks a never-arrived
// run too: the second grab shows the first attempt did not fail.
func TestReleaseFailureStreak_GrabWithoutFailureResetsNeverArrived(t *testing.T) {
	f := newStreakFixture(t)
	f.fail("neverArrived", 30*time.Hour)
	f.fail("neverArrived", 20*time.Hour)
	f.add(models.HistoryEventGrabbed, "T", 8*time.Hour, map[string]string{"guid": "g"})
	f.add(models.HistoryEventGrabbed, "T", 3*time.Hour, map[string]string{"guid": "g"})
	f.fail("neverArrived", time.Hour)
	if n, _ := f.streak(); n != 1 {
		t.Fatalf("streak = %d, want 1", n)
	}
}

// The grab right before a never-arrived failure is that failure's own attempt
// and must not reset the run, or never-arrived could never be counted.
func TestReleaseFailureStreak_NeverArrivedKeepsItsOwnGrab(t *testing.T) {
	f := newStreakFixture(t)
	for _, ago := range []time.Duration{30 * time.Hour, 20 * time.Hour, 10 * time.Hour} {
		f.add(models.HistoryEventGrabbed, "T", ago+time.Hour, map[string]string{"guid": "g"})
		f.fail("neverArrived", ago)
	}
	if n, _ := f.streak(); n != 3 {
		t.Fatalf("streak = %d, want 3", n)
	}
}

func TestIndexerFailuresElsewhere(t *testing.T) {
	f := newStreakFixture(t)
	ctx := context.Background()
	f.add(models.HistoryEventDownloadFailed, "A", time.Hour, map[string]string{"guid": "a", "class": "indexer", "indexerId": "7"})
	f.add(models.HistoryEventDownloadFailed, "B", time.Hour, map[string]string{"guid": "b", "class": "indexer", "indexerId": "8"})
	f.add(models.HistoryEventDownloadFailed, "C", 30*time.Hour, map[string]string{"guid": "c", "class": "indexer", "indexerId": "7"})
	f.add(models.HistoryEventDownloadFailed, "D", time.Hour, map[string]string{"guid": "d", "class": "local", "indexerId": "7"})
	since := f.now.Add(-24 * time.Hour)
	if n, err := f.repo.IndexerFailuresElsewhere(ctx, 7, "g", []string{"indexer"}, since); err != nil || n != 1 {
		t.Fatalf("elsewhere(7, g) = %d (%v), want 1", n, err)
	}
	if n, _ := f.repo.IndexerFailuresElsewhere(ctx, 7, "a", []string{"indexer"}, since); n != 0 {
		t.Fatalf("a release's own failure counted as elsewhere: %d", n)
	}
}

func TestIndexerServedElsewhere(t *testing.T) {
	f := newStreakFixture(t)
	ctx := context.Background()
	f.add(models.HistoryEventGrabbed, "A", 2*time.Hour, map[string]string{"guid": "a", "indexerId": "7"})
	f.add(models.HistoryEventGrabbed, "B", time.Hour, map[string]string{"guid": "b", "indexerId": "8"})
	f.add(models.HistoryEventGrabbed, "G", time.Hour, map[string]string{"guid": "g", "indexerId": "7"})
	if ok, err := f.repo.IndexerServedElsewhere(ctx, 7, "g", f.now.Add(-3*time.Hour)); err != nil || !ok {
		t.Fatalf("served = %v (%v), want true", ok, err)
	}
	if ok, _ := f.repo.IndexerServedElsewhere(ctx, 7, "g", f.now.Add(-time.Hour/2)); ok {
		t.Error("a grab before `since` counted")
	}
	if ok, _ := f.repo.IndexerServedElsewhere(ctx, 7, "a", f.now.Add(-3*time.Hour)); ok {
		// only g (excluded=a) remains: g is another release, so it counts
		t.Log("g counts for a")
	}
	if ok, _ := f.repo.IndexerServedElsewhere(ctx, 9, "g", f.now.Add(-3*time.Hour)); ok {
		t.Error("another indexer's grab counted")
	}
}
