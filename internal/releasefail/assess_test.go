package releasefail

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// assessFixture writes history rows at chosen ages and asks Assess about a
// release on indexer 7.
type assessFixture struct {
	t    *testing.T
	hist *db.HistoryRepo
	exec func(ago time.Duration, id int64)
	now  time.Time
	dl   *models.Download
}

func newAssessFixture(t *testing.T) *assessFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &assessFixture{t: t, hist: db.NewHistoryRepo(database), now: time.Now().UTC()}
	f.exec = func(ago time.Duration, id int64) {
		if _, err := database.Exec(`UPDATE history SET created_at = ? WHERE id = ?`, f.now.Add(-ago), id); err != nil {
			t.Fatal(err)
		}
	}
	idx := int64(7)
	f.dl = &models.Download{GUID: "g", Title: "T", IndexerID: &idx}
	return f
}

func (f *assessFixture) add(eventType string, ago time.Duration, data map[string]string) {
	f.t.Helper()
	raw, _ := json.Marshal(data)
	e := &models.HistoryEvent{EventType: eventType, SourceTitle: "T", Data: string(raw)}
	if err := f.hist.Create(context.Background(), e); err != nil {
		f.t.Fatal(err)
	}
	f.exec(ago, e.ID)
}

func (f *assessFixture) fail(class Class, ago time.Duration) {
	f.add(models.HistoryEventDownloadFailed, ago, map[string]string{"guid": "g", "class": string(class), "indexerId": "7"})
}

func (f *assessFixture) serve(ago time.Duration) {
	f.add(models.HistoryEventGrabbed, ago, map[string]string{"guid": "other", "indexerId": "7"})
}

func (f *assessFixture) assess(class Class) Verdict {
	return Assess(context.Background(), f.hist, f.dl, class, f.now)
}

// Only the window Assess passes bounds the run: failures 8 days old do not
// count towards it.
func TestAssess_WindowBoundsTheRun(t *testing.T) {
	f := newAssessFixture(t)
	f.serve(time.Hour)
	f.fail(ClassRelease, 8*24*time.Hour)
	f.fail(ClassRelease, 8*24*time.Hour+3*time.Hour)
	if v := f.assess(ClassRelease); v.Count != 1 || v.Block {
		t.Fatalf("verdict = %+v, want a count of 1: failures older than the window must not count", v)
	}
}

// A grab that went through after the last failure ends the run for a 404, but
// not for a never-arrived failure, whose own grab precedes it.
func TestAssess_TrailingGrabResetsOnlyNonNeverArrived(t *testing.T) {
	f := newAssessFixture(t)
	f.fail(ClassRelease, 20*time.Hour)
	f.fail(ClassRelease, 12*time.Hour)
	f.add(models.HistoryEventGrabbed, 6*time.Hour, map[string]string{"guid": "g", "indexerId": "7"})
	f.serve(5 * time.Hour)
	if v := f.assess(ClassRelease); v.Count != 1 {
		t.Fatalf("a 404 after a successful send: %+v, want a count of 1", v)
	}

	g := newAssessFixture(t)
	g.fail(ClassNeverArrived, 20*time.Hour)
	g.fail(ClassNeverArrived, 12*time.Hour)
	g.add(models.HistoryEventGrabbed, 6*time.Hour, map[string]string{"guid": "g", "indexerId": "7"})
	g.serve(5 * time.Hour)
	if v := g.assess(ClassNeverArrived); v.Count != 3 || !v.Block {
		t.Fatalf("never arrived after its own grab: %+v, want 3 and a block", v)
	}
}

// The three guards that make a failure indexer wide evidence, and the verdict
// they give: nothing counts and the class is stored as indexerWide.
func TestAssess_GuardsAgainstIndexerWideFaults(t *testing.T) {
	t.Run("no indexer", func(t *testing.T) {
		f := newAssessFixture(t)
		f.serve(time.Hour)
		f.dl.IndexerID = nil
		if v := f.assess(ClassRelease); v.Class != ClassIndexerWide || v.Count != 0 || v.Block {
			t.Fatalf("verdict = %+v", v)
		}
		zero := int64(0)
		f.dl.IndexerID = &zero
		if v := f.assess(ClassRelease); v.Class != ClassIndexerWide {
			t.Fatalf("an indexer id of 0 counted: %+v", v)
		}
	})
	t.Run("other releases failing", func(t *testing.T) {
		f := newAssessFixture(t)
		f.serve(time.Hour)
		f.add(models.HistoryEventDownloadFailed, 2*time.Hour, map[string]string{"guid": "other", "class": "release", "indexerId": "7"})
		v := f.assess(ClassRelease)
		if v.Class != ClassIndexerWide || !v.OtherReleasesFailing {
			t.Fatalf("verdict = %+v, want indexer wide with the burst flag", v)
		}
	})
	t.Run("indexer served nothing", func(t *testing.T) {
		f := newAssessFixture(t)
		f.serve(30 * time.Hour) // outside the day
		if v := f.assess(ClassRelease); v.Class != ClassIndexerWide || v.Count != 0 || v.OtherReleasesFailing {
			t.Fatalf("verdict = %+v, want indexer wide without the burst flag", v)
		}
	})
	t.Run("healthy indexer", func(t *testing.T) {
		f := newAssessFixture(t)
		f.serve(time.Hour)
		if v := f.assess(ClassRelease); v.Class != ClassRelease || v.Count != 1 || v.Block {
			t.Fatalf("verdict = %+v, want a first counted failure", v)
		}
	})
	t.Run("indexer errors are never counted", func(t *testing.T) {
		f := newAssessFixture(t)
		f.serve(time.Hour)
		if v := f.assess(ClassIndexer); v.Class != ClassIndexer || v.Count != 0 || v.Block {
			t.Fatalf("verdict = %+v, want nothing counted", v)
		}
		if v := f.assess(ClassClientError); v.Count != 0 || v.Block {
			t.Fatalf("a client error counted: %+v", v)
		}
	})
}

// The indexer must have served something since THIS release last failed, not
// merely at some point in the last day: a grab from before the previous
// failure says nothing about the indexer between then and now.
func TestAssess_ServedBeforeTheLastFailureDoesNotCount(t *testing.T) {
	f := newAssessFixture(t)
	f.fail(ClassRelease, 14*time.Hour)
	f.serve(13 * time.Hour) // after the first failure, before the second
	f.fail(ClassRelease, 7*time.Hour)
	// Nothing served since the second failure: this one is not evidence.
	if v := f.assess(ClassRelease); v.Class != ClassIndexerWide || v.Block || v.Count != 2 {
		t.Fatalf("verdict = %+v, want indexer wide, count 2, no block", v)
	}
	// The same run with a grab after the second failure does block.
	f.serve(3 * time.Hour)
	if v := f.assess(ClassRelease); v.Count != 3 || !v.Block {
		t.Fatalf("verdict = %+v, want 3 and a block once the indexer served again", v)
	}
}

// Three failures a retry apart on a serving indexer block; the verdict says so.
func TestAssess_ThirdSpacedFailureBlocks(t *testing.T) {
	f := newAssessFixture(t)
	f.fail(ClassRelease, 20*time.Hour)
	f.serve(19 * time.Hour)
	f.fail(ClassRelease, 12*time.Hour)
	f.serve(11 * time.Hour)
	if v := f.assess(ClassRelease); v.Count != 3 || !v.Block {
		t.Fatalf("verdict = %+v, want 3 and a block", v)
	}
}

// A 5xx on the link counts like a 404 (#4111): three a retry apart on a
// serving indexer block, and a mix of the two is one run.
func TestAssess_ServerErrorCountsLikeNotFound(t *testing.T) {
	f := newAssessFixture(t)
	f.fail(ClassServerError, 20*time.Hour)
	f.serve(19 * time.Hour)
	f.fail(ClassRelease, 12*time.Hour)
	f.serve(11 * time.Hour)
	if v := f.assess(ClassServerError); v.Class != ClassServerError || v.Count != 3 || !v.Block {
		t.Fatalf("verdict = %+v, want 3 and a block", v)
	}
}

// Another release of the indexer answering 5xx makes this one indexer wide:
// an indexer failing everything is paused, never held against a release.
func TestAssess_ServerErrorElsewhereIsIndexerWide(t *testing.T) {
	f := newAssessFixture(t)
	f.serve(time.Hour)
	f.add(models.HistoryEventDownloadFailed, 2*time.Hour,
		map[string]string{"guid": "other-release", "class": string(ClassServerError), "indexerId": "7"})
	if v := f.assess(ClassServerError); v.Class != ClassIndexerWide || v.Count != 0 || v.Block || !v.OtherReleasesFailing {
		t.Fatalf("verdict = %+v, want indexer wide with the burst flag", v)
	}
}
