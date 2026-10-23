package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func TestMetadataRefreshBatchSize(t *testing.T) {
	cases := []struct{ eligible, want int }{
		{-5, 1}, {0, 1}, {1, 1}, {24, 1}, {25, 2}, {48, 2}, {49, 3},
		{2400, 100}, {2401, 100}, {1000000, 100},
	}
	for _, tc := range cases {
		if got := metadataRefreshBatchSize(tc.eligible); got != tc.want {
			t.Errorf("metadataRefreshBatchSize(%d) = %d, want %d", tc.eligible, got, tc.want)
		}
	}
}

func TestCoolingAuthors_CooldownBoundaryAndPruning(t *testing.T) {
	s := &Scheduler{}
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s.coolAuthor(1, base)
	until := base.Add(metadataRefreshAuthorCooldown)

	if got := s.coolingAuthors(until.Add(-time.Nanosecond)); !got[1] {
		t.Fatal("author must still be cooling one nanosecond before the end")
	}
	if got := s.coolingAuthors(until); got[1] {
		t.Fatal("author must be released exactly at the end of the cooldown")
	}
	s.metadataCooldownMu.Lock()
	_, kept := s.metadataCooldown[1]
	s.metadataCooldownMu.Unlock()
	if kept {
		t.Fatal("an expired cooldown must be forgotten, not kept forever")
	}
}

func TestCoolingAuthors_PrunesStaleBreakerTrips(t *testing.T) {
	s := &Scheduler{}
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s.noteBreakerTrip(1, base)
	s.noteBreakerTrip(2, base.Add(time.Hour))

	s.coolingAuthors(base.Add(breakerTripMemory))
	if s.breakerTripCount(1) != 1 {
		t.Fatal("a trip exactly breakerTripMemory old is still remembered")
	}
	s.coolingAuthors(base.Add(breakerTripMemory + time.Second))
	if s.breakerTripCount(1) != 0 {
		t.Fatal("a trip older than breakerTripMemory must be pruned")
	}
	if s.breakerTripCount(2) != 1 {
		t.Fatal("a newer trip must survive the pruning of an older one")
	}
}

func TestNoteBreakerTrip_CoolsOnTheThirdAndRestartsCount(t *testing.T) {
	s := &Scheduler{}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for i := 1; i < metadataRefreshBreakerTripsToCool; i++ {
		s.noteBreakerTrip(7, now)
		if s.coolingAuthors(now)[7] {
			t.Fatalf("author cooled after only %d trips", i)
		}
		if s.breakerTripCount(7) != i {
			t.Fatalf("trip count = %d, want %d", s.breakerTripCount(7), i)
		}
	}
	s.noteBreakerTrip(7, now)
	if !s.coolingAuthors(now)[7] {
		t.Fatal("author must be cooled on the third consecutive trip")
	}
	if s.breakerTripCount(7) != 0 {
		t.Fatal("cooling must restart the trip count")
	}
	s.noteBreakerTrip(8, now)
	s.clearBreakerTrips(8)
	if s.breakerTripCount(8) != 0 {
		t.Fatal("clearBreakerTrips must forget the author's trips")
	}
}

// refreshProvider answers GetAuthor through fn so a test can return a sparse
// record, nothing at all, or act mid call.
type refreshProvider struct {
	mockMetaProvider
	calls []string
	fn    func(foreignID string) (*models.Author, error)
}

func (p *refreshProvider) GetAuthor(_ context.Context, foreignID string) (*models.Author, error) {
	p.calls = append(p.calls, foreignID)
	return p.fn(foreignID)
}

func newRefreshEdgeFixture(t *testing.T, n int, fn func(string) (*models.Author, error)) (*refreshFixture, *refreshProvider) {
	t.Helper()
	f := newRefreshFixture(t, n)
	p := &refreshProvider{fn: fn}
	f.s.meta = metadata.NewAggregator(p)
	return f, p
}

func TestRefreshMetadata_NothingEligibleCallsNoProvider(t *testing.T) {
	f, p := newRefreshEdgeFixture(t, 0, func(string) (*models.Author, error) { return &models.Author{}, nil })
	f.s.refreshMetadata()
	if len(p.calls) != 0 {
		t.Fatalf("provider called %v with no eligible authors", p.calls)
	}
}

// An author unmonitored while the pass was running is skipped without a
// provider call and without using one of the batch slots.
func TestRefreshMetadata_AuthorUnmonitoredMidPassIsSkippedWithoutUsingASlot(t *testing.T) {
	var f *refreshFixture
	var p *refreshProvider
	f, p = newRefreshEdgeFixture(t, 60, func(fid string) (*models.Author, error) {
		if fid == "OL000A" {
			a, err := f.authors.GetByID(context.Background(), f.ids["OL001A"])
			if err != nil || a == nil {
				t.Fatalf("reload: %v", err)
			}
			a.Monitored = false
			if err := f.authors.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
		}
		return &models.Author{ForeignID: fid, Description: "bio"}, nil
	})
	// batch = ceil(60/24) = 3. OL000A runs first and unmonitors OL001A.
	f.s.refreshMetadata()

	want := []string{"OL000A", "OL002A", "OL003A"}
	if len(p.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", p.calls, want)
	}
	for i := range want {
		if p.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", p.calls, want)
		}
	}
	if f.stamped("OL001A") != nil {
		t.Fatal("the unmonitored author must not be stamped")
	}
}

func TestRefreshMetadata_CancelledContextMidPassStopsBeforeTheNextAuthor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, p := newRefreshEdgeFixture(t, 60, func(fid string) (*models.Author, error) {
		cancel()
		return &models.Author{ForeignID: fid, Description: "bio"}, nil
	})
	f.s.appCtx = ctx
	f.s.refreshMetadata()
	if len(p.calls) != 1 {
		t.Fatalf("calls = %v, want the pass to stop after the author in flight", p.calls)
	}
}

// A provider that owns no prefix answers (nil, nil): the author is not
// stamped and is cooled so it does not head every tick.
func TestRefreshMetadata_NoProviderForAuthorCoolsItWithoutStamping(t *testing.T) {
	f, p := newRefreshEdgeFixture(t, 3, func(fid string) (*models.Author, error) {
		if fid == "OL000A" {
			return nil, nil
		}
		return &models.Author{ForeignID: fid, Description: "bio"}, nil
	})
	f.s.refreshMetadata()
	f.s.refreshMetadata()
	var first int
	for _, c := range p.calls {
		if c == "OL000A" {
			first++
		}
	}
	if first != 1 {
		t.Fatalf("OL000A asked %d times over two ticks, want once (calls %v)", first, p.calls)
	}
	if f.stamped("OL000A") != nil {
		t.Fatal("an author nobody answered for must not be stamped")
	}
	if f.stamped("OL001A") == nil {
		t.Fatal("the next author should have been reached on the second tick")
	}
}

// A sparse provider answer must not wipe enrichment already stored (#1463).
func TestRefreshMetadata_SparseAnswerKeepsStoredProfile(t *testing.T) {
	f, _ := newRefreshEdgeFixture(t, 1, func(fid string) (*models.Author, error) {
		return &models.Author{ForeignID: fid}, nil
	})
	ctx := context.Background()
	a, _ := f.authors.GetByID(ctx, f.ids["OL000A"])
	a.Description, a.ImageURL, a.AverageRating, a.RatingsCount = "stored bio", "http://img/a.jpg", 4.5, 120
	if err := f.authors.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	f.s.refreshMetadata()

	got, _ := f.authors.GetByID(ctx, f.ids["OL000A"])
	if got.Description != "stored bio" || got.ImageURL != "http://img/a.jpg" || got.AverageRating != 4.5 || got.RatingsCount != 120 {
		t.Fatalf("sparse refresh clobbered the profile: %+v", got)
	}
	if got.LastMetadataRefreshAt == nil {
		t.Fatal("a successful refresh must stamp even when the answer is sparse")
	}
}

func TestRefreshMetadata_RichAnswerOverwritesEveryField(t *testing.T) {
	f, _ := newRefreshEdgeFixture(t, 1, func(fid string) (*models.Author, error) {
		return &models.Author{ForeignID: fid, Description: "new bio", ImageURL: "http://img/new.jpg", AverageRating: 3.9, RatingsCount: 7}, nil
	})
	ctx := context.Background()
	a, _ := f.authors.GetByID(ctx, f.ids["OL000A"])
	a.Description, a.ImageURL, a.AverageRating, a.RatingsCount = "old", "http://img/old.jpg", 1, 1
	if err := f.authors.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	f.s.refreshMetadata()
	got, _ := f.authors.GetByID(ctx, f.ids["OL000A"])
	if got.Description != "new bio" || got.ImageURL != "http://img/new.jpg" || got.AverageRating != 3.9 || got.RatingsCount != 7 {
		t.Fatalf("profile = %+v, want the provider's values", got)
	}
}

// An edit saved while the provider was being asked wins: the refresh is
// dropped, the edit survives and the author stays unstamped for a later tick.
func TestRefreshMetadata_EditDuringProviderCallWinsAndAuthorStaysStale(t *testing.T) {
	var f *refreshFixture
	f, _ = newRefreshEdgeFixture(t, 1, func(fid string) (*models.Author, error) {
		ctx := context.Background()
		a, _ := f.authors.GetByID(ctx, f.ids["OL000A"])
		a.Description = "edited by the user"
		if err := f.authors.Update(ctx, a); err != nil {
			t.Fatal(err)
		}
		return &models.Author{ForeignID: fid, Description: "from provider"}, nil
	})
	f.s.refreshMetadata()
	got, _ := f.authors.GetByID(context.Background(), f.ids["OL000A"])
	if got.Description != "edited by the user" {
		t.Fatalf("description = %q, the user's edit was overwritten", got.Description)
	}
	if got.LastMetadataRefreshAt != nil {
		t.Fatal("a dropped refresh must not stamp the author")
	}
}
