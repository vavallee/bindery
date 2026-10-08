package hardcover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
)

func TestAggregatorCacheScopesLiveCredentials(t *testing.T) {
	var token atomic.Value
	token.Store("account-one")
	var calls atomic.Int32
	c := newUnconfiguredMockClient(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if strings.Contains(req.Query, "GetEditions") {
			return gqlResponse(t, 200, map[string]any{"editions": []map[string]any{{"id": 1, "title": account}}}), nil
		}
		return gqlResponse(t, 200, map[string]any{"search": map[string]any{"results": map[string]any{"hits": []map[string]any{{"document": map[string]any{"id": 1, "slug": "dune", "title": account}}}}}}), nil
	}).WithTokenSource(func(context.Context) string { return token.Load().(string) })
	a := metadata.NewAggregator(c)
	ctx := context.Background()
	for _, account := range []string{"account-one", "account-two"} {
		token.Store(account)
		for range 2 {
			b, err := a.SearchBooks(ctx, "Dune")
			if err != nil || len(b) != 1 || b[0].Title != account {
				t.Fatalf("account search: %v %v", b, err)
			}
			e, err := a.GetEditions(ctx, "hc:dune")
			if err != nil || len(e) != 1 || e[0].Title != account {
				t.Fatalf("account editions: %v %v", e, err)
			}
			e, err = a.GetEditionsFromProvider(ctx, "hc", "dune")
			if err != nil || len(e) != 1 || e[0].Title != account {
				t.Fatalf("explicit editions: %v %v", e, err)
			}
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("HTTP requests = %d, want 4", calls.Load())
	}
	token.Store("")
	if _, err := a.GetEditions(ctx, "hc:dune"); !errors.Is(err, metadata.ErrProviderNotConfigured) {
		t.Fatalf("removed credential reused cache: %v", err)
	}
	// Direct provider calls remain an uncached upstream refresh path.
	token.Store("account-two")
	if _, err := c.GetEditions(ctx, "dune"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 {
		t.Fatalf("direct refresh did not fetch: %d", calls.Load())
	}
}

func TestCacheScopeBindsTokenAcrossPagination(t *testing.T) {
	var token atomic.Value
	token.Store("first")
	var calls atomic.Int32
	c := newUnconfiguredMockClient(func(r *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer first" {
			t.Errorf("page %d used changed token: %q", n, got)
		}
		token.Store("second")
		var editions []map[string]any
		if n == 1 {
			for i := range editionsPageSize {
				editions = append(editions, map[string]any{"id": i + 1, "title": fmt.Sprint(i)})
			}
		} else {
			editions = append(editions, map[string]any{"id": 101, "title": "last"})
		}
		return gqlResponse(t, 200, map[string]any{"editions": editions}), nil
	}).WithTokenSource(func(context.Context) string { return token.Load().(string) })
	bound, scope := c.ResolveCacheProvider(context.Background())
	if strings.Contains(scope, "first") {
		t.Fatal("scope contains token")
	}
	editions, err := bound.GetEditions(context.Background(), "dune")
	if err != nil || len(editions) != 101 || calls.Load() != 2 {
		t.Fatalf("incomplete snapshot: count=%d calls=%d err=%v", len(editions), calls.Load(), err)
	}
	_, nextScope := c.ResolveCacheProvider(context.Background())
	if nextScope == scope {
		t.Fatal("credential change retained namespace")
	}
}

func TestEditionCacheCombinesFullPaginatedFetch(t *testing.T) {
	c := newMockClient(nil)
	a := metadata.NewAggregator(c)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		c.http.Transport = &testTransport{handler: func(r *http.Request) (*http.Response, error) {
			n := calls.Add(1)
			if n == 1 {
				<-release
			}
			var editions []map[string]any
			if n == 1 {
				for i := range editionsPageSize {
					editions = append(editions, map[string]any{"id": i + 1, "title": "edition"})
				}
			} else {
				editions = append(editions, map[string]any{"id": 101, "title": "last"})
			}
			return gqlResponse(t, 200, map[string]any{"editions": editions}), nil
		}}
		done := make(chan int, 2)
		go func() {
			e, err := a.GetEditions(context.Background(), "hc:dune")
			if err != nil {
				t.Error(err)
			}
			done <- len(e)
		}()
		synctest.Wait()
		go func() {
			e, err := a.GetEditionsFromProvider(context.Background(), "hardcover", "dune")
			if err != nil {
				t.Error(err)
			}
			done <- len(e)
		}()
		synctest.Wait()
		close(release)
		synctest.Wait()
		if first, second := <-done, <-done; first != 101 || second != 101 || calls.Load() != 2 {
			t.Fatalf("counts=%d,%d requests=%d", first, second, calls.Load())
		}
	})
}

func TestEditionCacheDoesNotStorePartialFailure(t *testing.T) {
	var calls atomic.Int32
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n == 2 {
			return gqlResponse(t, 200, `{"errors":[{"message":"upstream unavailable"}]}`), nil
		}
		var editions []map[string]any
		if n == 1 || n == 3 {
			for i := range editionsPageSize {
				editions = append(editions, map[string]any{"id": i + 1, "title": "edition"})
			}
		} else {
			editions = append(editions, map[string]any{"id": 101, "title": "last"})
		}
		return gqlResponse(t, 200, map[string]any{"editions": editions}), nil
	})
	a := metadata.NewAggregator(c)
	if editions, err := a.GetEditions(context.Background(), "hc:dune"); err == nil || len(editions) != 0 {
		t.Fatalf("partial failure: %v %v", editions, err)
	}
	editions, err := a.GetEditionsFromProvider(context.Background(), "hardcover", "dune")
	if err != nil || len(editions) != 101 || calls.Load() != 4 {
		t.Fatalf("retry incomplete: count=%d calls=%d err=%v", len(editions), calls.Load(), err)
	}
}

func TestEditionCacheSeparatesConcurrentAccounts(t *testing.T) {
	c := newMockClient(nil)
	var token atomic.Value
	token.Store("first")
	c = c.WithTokenSource(func(context.Context) string { return token.Load().(string) })
	a := metadata.NewAggregator(c)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		c.http.Transport = &testTransport{handler: func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			<-release
			return gqlResponse(t, 200, map[string]any{"editions": []map[string]any{{"id": 1, "title": account}}}), nil
		}}
		result := make(chan string, 2)
		fetch := func() {
			e, err := a.GetEditions(context.Background(), "hc:dune")
			if err != nil || len(e) != 1 {
				t.Errorf("editions: %v %v", e, err)
				result <- "error"
				return
			}
			result <- e[0].Title
		}
		go fetch()
		synctest.Wait()
		token.Store("second")
		go fetch()
		synctest.Wait()
		if calls.Load() != 2 {
			t.Errorf("accounts shared flight: calls=%d", calls.Load())
		}
		close(release)
		synctest.Wait()
		seen := map[string]bool{<-result: true, <-result: true}
		if !seen["first"] || !seen["second"] {
			t.Fatalf("account results mixed: %v", seen)
		}
	})
}

func TestAggregatorCachePreservesThrottleDeadline(t *testing.T) {
	c := newMockClient(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected upstream request")
		return nil, errors.New("unexpected request")
	})
	a := metadata.NewAggregator(c)
	synctest.Test(t, func(t *testing.T) {
		c.throttle = newThrottle()
		c.throttle.penalize(20 * time.Second)
		before := c.throttle.next
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := a.GetEditions(ctx, "hc:dune")
		synctest.Wait()
		if !errors.Is(err, ErrRateLimited) {
			t.Errorf("want classifiable ErrRateLimited, got %v", err)
		}
		c.throttle.mu.Lock()
		after := c.throttle.next
		c.throttle.mu.Unlock()
		if !after.Equal(before) {
			t.Errorf("impossible request consumed throttle reservation: queue advanced %v", after.Sub(before))
		}
	})
}
