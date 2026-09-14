package metadata

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// CacheScopedProvider binds live configuration to a request. The returned
// provider must use that snapshot for the entire fetch (including pagination),
// and scope must change whenever credentials or query options change. Never
// put credentials themselves in scope. The bound provider must retain optional
// capabilities such as author catalogues and cover lookup. Immutable providers
// need not implement it.
type CacheScopedProvider interface {
	ResolveCacheProvider(context.Context) (Provider, string)
}

type requestCache struct {
	mu       sync.Mutex
	searches *ttlCache
	flights  map[string]*metadataFlight
}

type metadataFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters map[*context.Context]struct{}
	value   any
	err     error
}

type boundCacheProvider struct {
	provider Provider
	scope    string
}
type boundCacheProvidersKey struct{}

// bindCacheProviders pins configuration for composed reads: every nested fetch
// and derived cache key uses the same provider snapshots, even across workers.
func (a *Aggregator) bindCacheProviders(ctx context.Context) (context.Context, string) {
	bound := make(map[string]boundCacheProvider)
	var scope strings.Builder
	for _, p := range a.providers() {
		if p == nil {
			continue
		}
		provider, key := resolveCacheProvider(ctx, p)
		bound[fmt.Sprintf("%T:%p", p, p)] = boundCacheProvider{provider, key}
		fmt.Fprintf(&scope, "%q;", key)
	}
	return context.WithValue(ctx, boundCacheProvidersKey{}, bound), scope.String()
}

func resolveCacheProvider(ctx context.Context, p Provider) (Provider, string) {
	// Provider instances carry immutable configuration (endpoint, API key, etc.).
	key := fmt.Sprintf("%T:%p", p, p)
	if bound, ok := ctx.Value(boundCacheProvidersKey{}).(map[string]boundCacheProvider); ok {
		if snapshot, found := bound[key]; found {
			return snapshot.provider, snapshot.scope
		}
	}
	if scoped, ok := p.(CacheScopedProvider); ok {
		bound, scope := scoped.ResolveCacheProvider(ctx)
		return bound, key + ":" + scope
	}
	return p, key
}

type schedulingWaitersKey struct{}

// SchedulingDeadline reports the budget available for reserving a provider slot.
// Shared fetches use the latest deadline among their remaining callers, or no
// deadline if any caller is unbounded. This is a scheduling hint, not a context
// deadline: cancellation still belongs to each caller and the last waiter.
func SchedulingDeadline(ctx context.Context) (time.Time, bool) {
	deadline, bounded := ctx.Deadline()
	waiters, ok := ctx.Value(schedulingWaitersKey{}).(func() []context.Context)
	if !ok {
		return deadline, bounded
	}
	latest := time.Now() // No live callers means no remaining scheduling budget.
	for _, waiter := range waiters() {
		if waiter.Err() != nil {
			continue
		}
		end, limited := SchedulingDeadline(waiter)
		if !limited {
			return deadline, bounded
		}
		if end.After(latest) {
			latest = end
		}
	}
	if bounded && deadline.Before(latest) {
		return deadline, true
	}
	return latest, true
}

// cachedRequest shares only successful snapshots. Each waiter owns its return
// value and cancellation; the upstream request is canceled when nobody needs it.
func cachedRequest[T any](ctx context.Context, a *Aggregator, cache *ttlCache, key string, fetch func(context.Context) (T, error), clone func(T) T) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	r := &a.requests
	r.mu.Lock()
	if cache == nil {
		if r.searches == nil {
			r.searches = newTTLCacheWithCap(5*time.Minute, 1000)
		}
		cache = r.searches
	}
	if value, ok := cache.get(key); ok {
		r.mu.Unlock()
		return clone(value.(T)), nil
	}
	if r.flights == nil {
		r.flights = make(map[string]*metadataFlight)
	}
	f := r.flights[key]
	if f == nil {
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &metadataFlight{done: make(chan struct{}), cancel: cancel, waiters: make(map[*context.Context]struct{})}
		workCtx = context.WithValue(workCtx, schedulingWaitersKey{}, func() []context.Context {
			r.mu.Lock()
			defer r.mu.Unlock()
			callers := make([]context.Context, 0, len(f.waiters))
			for caller := range f.waiters {
				callers = append(callers, *caller)
			}
			return callers
		})
		r.flights[key] = f
		go func() {
			value, err := fetch(workCtx)
			if err == nil {
				value = clone(value)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			// An abandoned fetch must not overwrite a newer request or fill the cache.
			if r.flights[key] == f {
				if err == nil && workCtx.Err() == nil {
					cache.set(key, value)
				}
				delete(r.flights, key)
			}
			f.value, f.err = value, err
			close(f.done)
			cancel()
		}()
	}
	f.waiters[&ctx] = struct{}{}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		r.mu.Lock()
		delete(f.waiters, &ctx)
		if len(f.waiters) == 0 && r.flights[key] == f {
			delete(r.flights, key)
			f.cancel()
		}
		r.mu.Unlock()
		return zero, ctx.Err()
	case <-f.done:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if f.err != nil {
			return zero, f.err
		}
		return clone(f.value.(T)), nil
	}
}

func (a *Aggregator) searchProviderBooks(ctx context.Context, p Provider, query string) ([]models.Book, error) {
	p, scope := resolveCacheProvider(ctx, p)
	return a.searchBoundProviderBooks(ctx, p, scope, query)
}

func (a *Aggregator) searchBoundProviderBooks(ctx context.Context, p Provider, scope, query string) ([]models.Book, error) {
	return cachedRequest(ctx, a, nil, "search-books:"+scope+":"+query, func(ctx context.Context) ([]models.Book, error) {
		return p.SearchBooks(ctx, query)
	}, cloneBooks)
}

func (a *Aggregator) searchProviderAuthors(ctx context.Context, p Provider, query string) ([]models.Author, error) {
	p, scope := resolveCacheProvider(ctx, p)
	return cachedRequest(ctx, a, nil, "search-authors:"+scope+":"+query, func(ctx context.Context) ([]models.Author, error) {
		return p.SearchAuthors(ctx, query)
	}, cloneAuthors)
}

func (a *Aggregator) providerEditions(ctx context.Context, p Provider, id string) ([]models.Edition, error) {
	p, scope := resolveCacheProvider(ctx, p)
	// Match Hardcover's exact slug-first interpretation. In particular, do not
	// parse numbers, lowercase slugs, or map a slug to a numeric book ID.
	cacheID := id
	if normalizedProviderName(p.Name()) == "hardcover" {
		cacheID = strings.TrimSpace(strings.TrimPrefix(id, "hc:"))
	}
	return cachedRequest(ctx, a, a.cache, "editions:"+scope+":"+cacheID, func(ctx context.Context) ([]models.Edition, error) {
		return p.GetEditions(ctx, id)
	}, cloneEditions)
}
