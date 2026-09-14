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
// A failed or partial answer never replaces a cached entry. GetAuthor returns
// the error. The works lookups and GetAuthorAudiobooks hand back the cached
// copy instead (the works lookups adding any new works a partial answer
// carried), and GetAuthorWorksForAuthor falls back to the cached catalogue
// when a configured supplement failed, since that supplement drives the
// compilation prune. A refresh is never worse off than the cached answer it
// skipped.
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
