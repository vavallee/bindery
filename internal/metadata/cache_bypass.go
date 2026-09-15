package metadata

import "context"

// cacheBypassKey is the context key WithCacheBypass sets. Unexported so only
// this package can read or strip it.
type cacheBypassKey struct{}

// WithCacheBypass marks ctx as an explicit user refresh: the author profile
// and catalogue lookups it reaches (GetAuthor, GetAuthorWorks,
// GetAuthorWorksUnenriched, GetAuthorWorksForAuthor, GetAuthorAudiobooks)
// skip the aggregator's 24 hour cache, ask the provider, and write the answer
// back so the next ordinary read sees it.
//
// An answer the aggregator can tell is failed or short never replaces a
// cached entry. GetAuthor returns the error. The works lookups and
// GetAuthorAudiobooks hand back the cached copy instead (the works lookups
// adding any new works a short answer carried), and GetAuthorWorksForAuthor
// falls back to the cached catalogue when a configured supplement failed,
// since that supplement drives the compilation prune.
//
// What counts as short depends on the provider. Any error is caught on every
// provider. OpenLibrary also reports a works answer cut short by a failed
// later page or search call (GetAuthorWorksForRefresh). For the other primary
// providers an empty works list is treated as short while works are cached,
// because DNB answers some failures with an empty list and no error; a non
// empty list from them is trusted as a cache miss would trust it, so a list
// that lost some works to an upstream fault they swallow can still replace
// the cache.
//
// Only those entry points honour it, and each one consumes it before making
// nested lookups, so per work cover enrichment, edition and ISBN lookups keep
// their cache. Without that a refresh of a prolific author would repeat
// thousands of enrichment round trips (#2578) on every click.
//
// The provider clients sit below the cache, so a bypassed call still goes
// through the Hardcover throttle and the OpenLibrary retry and backoff.
//
// Background work (the scheduled metadata refresh, bulk and "refresh all"
// author refreshes) must not set it: the cache is what keeps those from
// refetching the whole library on every run (#2601).
func WithCacheBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, cacheBypassKey{}, true)
}

// CacheBypassed reports whether ctx carries WithCacheBypass.
func CacheBypassed(ctx context.Context) bool {
	v, _ := ctx.Value(cacheBypassKey{}).(bool)
	return v
}

// consumeCacheBypass reports whether ctx asked for a cache bypass and returns
// a context without it for the nested lookups the caller makes. See
// WithCacheBypass for why the bypass stops at the entry point.
func consumeCacheBypass(ctx context.Context) (context.Context, bool) {
	if !CacheBypassed(ctx) {
		return ctx, false
	}
	return context.WithValue(ctx, cacheBypassKey{}, false), true
}
