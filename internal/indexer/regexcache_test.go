package indexer

import (
	"regexp"
	"strconv"
	"testing"
)

// withCompileCounter swaps compileRegex for a counting wrapper and hands back
// the counter. Not parallel-safe (compileRegex is a package variable), so the
// tests below must not call t.Parallel.
func withCompileCounter(t *testing.T) *int {
	t.Helper()
	var n int
	orig := compileRegex
	compileRegex = func(pattern string) *regexp.Regexp {
		n++
		return orig(pattern)
	}
	t.Cleanup(func() { compileRegex = orig })
	return &n
}

// withFreshRegexCache isolates a test from patterns other tests have cached.
func withFreshRegexCache(t *testing.T, max int) {
	t.Helper()
	orig := regexCache
	regexCache = newBoundedRegexCache(max)
	t.Cleanup(func() { regexCache = orig })
}

// TestPhraseRegexCompilesOncePerPattern is the regression test for #2341. The
// old form was
//
//	re, _ := regexCache.LoadOrStore(pattern, regexp.MustCompile(pattern))
//
// which compiles the pattern before the lookup and throws the result away on a
// hit, so this test saw one compile per call instead of one per pattern.
func TestPhraseRegexCompilesOncePerPattern(t *testing.T) {
	withFreshRegexCache(t, regexCacheMaxEntries)
	compiles := withCompileCounter(t)

	phrase := []string{"the", "lord", "of", "the", "rings"}
	for i := 0; i < 50; i++ {
		if !ContainsPhrase("the lord of the rings fellowship epub", phrase) {
			t.Fatalf("ContainsPhrase should match on iteration %d", i)
		}
	}
	if *compiles != 1 {
		t.Errorf("phraseRegex compiled %d times for one pattern, want 1", *compiles)
	}

	// A different phrase is a different pattern, so exactly one more compile.
	for i := 0; i < 50; i++ {
		containsInOrder("the lord of the rings fellowship epub", []string{"lord", "rings"})
	}
	if *compiles != 2 {
		t.Errorf("compiles = %d after adding one in-order pattern, want 2", *compiles)
	}

	// WordBoundaryRegex already did Load-then-Store; make sure it stayed that
	// way through the rewrite.
	for i := 0; i < 50; i++ {
		WordBoundaryRegex("fellowship")
	}
	if *compiles != 3 {
		t.Errorf("compiles = %d after adding one word-boundary pattern, want 3", *compiles)
	}
}

// TestInOrderRegexCompilesOncePerPattern is the containsInOrder half of #2341.
func TestInOrderRegexCompilesOncePerPattern(t *testing.T) {
	withFreshRegexCache(t, regexCacheMaxEntries)
	compiles := withCompileCounter(t)

	seq := []string{"secrets", "body"}
	for i := 0; i < 25; i++ {
		if !containsInOrder("the secrets of the human body epub", seq) {
			t.Fatalf("containsInOrder should match on iteration %d", i)
		}
	}
	if *compiles != 1 {
		t.Errorf("inOrderRegex compiled %d times for one pattern, want 1", *compiles)
	}
}

// TestRegexCacheIsBounded covers #2344: the cache used to be an unbounded
// sync.Map, so every distinct token an instance ever searched stayed resident
// with its compiled regex for the life of the process.
func TestRegexCacheIsBounded(t *testing.T) {
	const max = 8
	withFreshRegexCache(t, max)

	for i := 0; i < max*10; i++ {
		WordBoundaryRegex(string(rune('a'+i%26)) + "token" + strconv.Itoa(i))
	}
	if got := regexCache.len(); got != max {
		t.Errorf("cache holds %d entries, want the cap of %d", got, max)
	}
}

// TestRegexCacheEvictsOldestFirst pins the FIFO policy: after overflowing by
// one, the first key inserted is the one that is gone and the rest survive.
func TestRegexCacheEvictsOldestFirst(t *testing.T) {
	const max = 4
	withFreshRegexCache(t, max)

	keys := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for _, k := range keys {
		regexCache.store(k, regexp.MustCompile(regexp.QuoteMeta(k)))
	}

	if _, ok := regexCache.load("alpha"); ok {
		t.Error("alpha was inserted first and should have been evicted")
	}
	for _, k := range keys[1:] {
		if _, ok := regexCache.load(k); !ok {
			t.Errorf("%s should still be cached", k)
		}
	}
	if got := regexCache.len(); got != max {
		t.Errorf("cache holds %d entries, want %d", got, max)
	}
}

// TestRegexCacheStoreIsIdempotent guards the ring bookkeeping: re-storing a key
// already present must not consume a second slot, or the ring and the map drift
// apart and the cache silently under-fills.
func TestRegexCacheStoreIsIdempotent(t *testing.T) {
	const max = 3
	withFreshRegexCache(t, max)

	re := regexp.MustCompile("x")
	for i := 0; i < 10; i++ {
		regexCache.store("same", re)
	}
	if got := regexCache.len(); got != 1 {
		t.Fatalf("cache holds %d entries after 10 stores of one key, want 1", got)
	}

	regexCache.store("other", re)
	regexCache.store("third", re)
	if got := regexCache.len(); got != 3 {
		t.Errorf("cache holds %d entries, want 3", got)
	}
	if _, ok := regexCache.load("same"); !ok {
		t.Error("the repeatedly-stored key should still be cached")
	}
}

// BenchmarkContainsPhraseCached measures the cache-hit path ContainsPhrase runs
// once per release in the search filter loop.
func BenchmarkContainsPhraseCached(b *testing.B) {
	phrase := []string{"the", "lord", "of", "the", "rings"}
	haystack := "the lord of the rings the fellowship of the ring 2001 retail epub"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !ContainsPhrase(haystack, phrase) {
			b.Fatal("expected a match")
		}
	}
}
