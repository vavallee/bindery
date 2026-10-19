package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestRequestRepo_CreateCapIsAtomic: the pending cap is enforced inside the
// INSERT, so concurrent creates for one owner cannot all see room. Before,
// the handler counted, called the provider, then inserted, and 24 concurrent
// creates took a requester to 15 pending against a cap of 3. Run with -race.
func TestRequestRepo_CreateCapIsAtomic(t *testing.T) {
	_, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	owner := mustUser(t, users, "reader")
	other := mustUser(t, users, "reader2")

	const racers, limit = 24, 3
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, capped := 0, 0
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := &models.LibraryRequest{OwnerUserID: owner.ID, Kind: models.RequestKindBook, ForeignID: fmt.Sprintf("OL%dW", i), PayloadJSON: "{}"}
			err := repo.Create(ctx, req, limit)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, ErrRequestCapReached):
				capped++
			default:
				t.Errorf("create %d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	n, err := repo.CountPendingByOwner(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != limit || created != limit || capped != racers-limit {
		t.Fatalf("pending %d, created %d, capped %d; want %d, %d, %d", n, created, capped, limit, limit, racers-limit)
	}
	// The cap is per owner.
	if err := repo.Create(ctx, &models.LibraryRequest{OwnerUserID: other.ID, Kind: models.RequestKindBook, ForeignID: "OL1W", PayloadJSON: "{}"}, limit); err != nil {
		t.Fatalf("other owner: %v", err)
	}
	// A duplicate is still a duplicate, not a cap refusal, while under cap.
	if err := repo.Create(ctx, &models.LibraryRequest{OwnerUserID: other.ID, Kind: models.RequestKindBook, ForeignID: "OL1W", PayloadJSON: "{}"}, limit); !errors.Is(err, ErrRequestExists) {
		t.Fatalf("duplicate: %v, want ErrRequestExists", err)
	}
}

func TestRequestRepo_ReopenRespectsCap(t *testing.T) {
	_, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	admin := mustUser(t, users, "admin")
	owner := mustUser(t, users, "reader")
	approved := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL1W")
	c, err := repo.Claim(ctx, approved.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, approved.ID, c.ClaimToken, nil, nil); err != nil {
		t.Fatal(err)
	}
	mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL2W")
	if err := repo.Reopen(ctx, approved.ID, owner.ID, "", "{}", "", nil, 1); !errors.Is(err, ErrRequestCapReached) {
		t.Fatalf("reopen at the cap: %v, want ErrRequestCapReached", err)
	}
	if err := repo.Reopen(ctx, approved.ID, owner.ID, "", "{}", "", nil, 2); err != nil {
		t.Fatalf("reopen under the cap: %v", err)
	}
}

// TestRequestRepo_CapCountsRequestsBeingApproved: a request an admin is
// approving still counts against its owner's cap, for creates and reopens.
// The second review found that counting only 'pending' survived every test.
func TestRequestRepo_CapCountsRequestsBeingApproved(t *testing.T) {
	_, repo, users := newRequestsFixture(t)
	ctx := context.Background()
	admin := mustUser(t, users, "admin")
	owner := mustUser(t, users, "reader")
	claimedReq := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL1W")
	if _, err := repo.Claim(ctx, claimedReq.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	next := &models.LibraryRequest{OwnerUserID: owner.ID, Kind: models.RequestKindBook, ForeignID: "OL2W", PayloadJSON: "{}"}
	if err := repo.Create(ctx, next, 1); !errors.Is(err, ErrRequestCapReached) {
		t.Fatalf("create at the cap with one request being approved: %v, want ErrRequestCapReached", err)
	}

	// Reopen: an approved request, with another one being approved.
	approved := mustRequest(t, repo, owner.ID, models.RequestKindBook, "OL3W")
	c, err := repo.Claim(ctx, approved.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, approved.ID, c.ClaimToken, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reopen(ctx, approved.ID, owner.ID, "", "{}", "", nil, 1); !errors.Is(err, ErrRequestCapReached) {
		t.Fatalf("reopen at the cap with one request being approved: %v, want ErrRequestCapReached", err)
	}
}
