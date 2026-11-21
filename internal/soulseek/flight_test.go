package soulseek

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/downloader/slskd"
)

type funcSearcher func(ctx context.Context, text string) ([]slskd.SearchResponse, error)

func (f funcSearcher) Search(ctx context.Context, text string) ([]slskd.SearchResponse, error) {
	return f(ctx, text)
}

// A panic in the client must not leave the flight in the table: every later
// search with the same text would wait on it until its own context ended.
func TestSearchPanicReleasesTheFlight(t *testing.T) {
	var calls atomic.Int32
	s := &Source{name: "S", client: funcSearcher(func(context.Context, string) ([]slskd.SearchResponse, error) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return []slskd.SearchResponse{{Username: "u"}}, nil
	})}
	if _, err := s.search(context.Background(), "dune"); err == nil {
		t.Fatal("the panicking search returned no error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := s.search(ctx, "dune")
	if err != nil || len(resp) != 1 {
		t.Fatalf("second search: %v %v", resp, err)
	}
	if time.Since(start) > time.Second {
		t.Error("the second search waited on a dead flight")
	}
}

// A waiter whose own context is live must not inherit the owner's cancellation.
func TestWaiterRetriesWhenTheOwnerWasCancelled(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	s := &Source{name: "S", client: funcSearcher(func(ctx context.Context, _ string) ([]slskd.SearchResponse, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []slskd.SearchResponse{{Username: "u"}}, nil
	})}
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerDone := make(chan error, 1)
	go func() {
		_, err := s.search(ownerCtx, "dune")
		ownerDone <- err
	}()
	<-started
	type result struct {
		resp []slskd.SearchResponse
		err  error
	}
	waiter := make(chan result, 1)
	go func() {
		r, err := s.search(context.Background(), "dune")
		waiter <- result{r, err}
	}()
	// Let the waiter join the flight before the owner is cancelled.
	for {
		s.mu.Lock()
		_, ok := s.inflight["dune"]
		s.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	cancelOwner()
	if err := <-ownerDone; err == nil {
		t.Error("the cancelled owner returned no error")
	}
	select {
	case r := <-waiter:
		if r.err != nil || len(r.resp) != 1 {
			t.Fatalf("waiter got %v, %v; want its own successful search", r.resp, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter never finished")
	}
	if calls.Load() != 2 {
		t.Errorf("searches = %d, want 2 (the waiter became the owner)", calls.Load())
	}
}
