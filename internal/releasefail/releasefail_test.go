package releasefail

import (
	"fmt"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestClassifySend(t *testing.T) {
	tests := []struct {
		name  string
		msg   string
		class Class
	}{
		{"429", "download client error: fetch torrent content: fetch torrent: indexer returned HTTP 429: slow down", ClassIndexer},
		{"500", "fetch torrent content: fetch torrent: indexer returned HTTP 500: Download failed", ClassIndexer},
		{"503", "fetch torrent content: fetch torrent: indexer returned HTTP 503", ClassIndexer},
		{"disabled till", "fetch torrent content: fetch torrent: indexer returned HTTP 429: Indexer is disabled till 11/21/2026 02:16:12 due to recent failures.", ClassIndexer},
		{"404", "fetch torrent content: fetch torrent: indexer returned HTTP 404: not found", ClassRelease},
		{"410", "fetch torrent content: fetch torrent: indexer returned HTTP 410", ClassRelease},
		{"401 is a credential, not the release", "fetch torrent: indexer returned HTTP 401", ClassLocal},
		{"qBittorrent own 500", "add torrent HTTP 500: boom", ClassLocal},
		{"qBittorrent own 429", "add torrent HTTP 429: boom", ClassLocal},
		{"unreachable", "add torrent: dial tcp 10.0.0.5:8080: connect: connection refused", ClassLocal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifySend(tt.msg, now).Class; got != tt.class {
				t.Errorf("class = %q, want %q", got, tt.class)
			}
		})
	}
}

// The detail is built from the class and the status, never from the error,
// which can carry an API key or the client's address.
func TestClassifySend_DetailHoldsNothingFromTheError(t *testing.T) {
	msg := "fetch torrent: indexer returned HTTP 429: https://idx.example/dl?apikey=SECRET123 at 10.0.0.5:8080"
	d := ClassifySend(msg, now).Detail
	if d != "indexer HTTP 429" {
		t.Errorf("detail = %q", d)
	}
}

func TestIndexerBackoffUntil(t *testing.T) {
	future := now.Add(3 * time.Hour)
	disabled := fmt.Sprintf("indexer returned HTTP 429: Indexer is disabled till %s due to recent failures", future.Format("1/2/2006 15:04:05"))
	if got := IndexerBackoffUntil(ClassifySend(disabled, now), now); !got.Equal(future) {
		t.Errorf("disabled till: backoff until %v, want the indexer's own %v", got, future)
	}

	past := fmt.Sprintf("indexer returned HTTP 429: disabled till %s", now.Add(-time.Hour).Format("1/2/2006 15:04:05"))
	if got := IndexerBackoffUntil(ClassifySend(past, now), now); !got.Equal(now.Add(DefaultIndexerBackoff)) {
		t.Errorf("a ban already over: backoff until %v, want the default", got)
	}

	if got := IndexerBackoffUntil(ClassifySend("indexer returned HTTP 500", now), now); !got.Equal(now.Add(DefaultIndexerBackoff)) {
		t.Errorf("a bare 500: backoff until %v, want the default", got)
	}

	far := fmt.Sprintf("indexer returned HTTP 429: disabled till %s", now.Add(400*time.Hour).Format("1/2/2006 15:04:05"))
	if got := IndexerBackoffUntil(ClassifySend(far, now), now); !got.Equal(now.Add(MaxIndexerBackoff)) {
		t.Errorf("an absurd ban: backoff until %v, want the cap", got)
	}

	ra := "indexer returned HTTP 429: Retry-After: 1800"
	if got := IndexerBackoffUntil(ClassifySend(ra, now), now); !got.Equal(now.Add(30 * time.Minute)) {
		t.Errorf("Retry-After: backoff until %v, want +30m", got)
	}

	if got := IndexerBackoffUntil(ClassifySend("add torrent: connection refused", now), now); !got.IsZero() {
		t.Errorf("a local failure backs no indexer off, got %v", got)
	}
	if got := IndexerBackoffUntil(ClassifySend("indexer returned HTTP 404", now), now); !got.IsZero() {
		t.Errorf("a dead link is the release's, not the indexer's: got %v", got)
	}
}

func TestParseRetryAtGarbage(t *testing.T) {
	for _, msg := range []string{
		"disabled till 99/99/2026 99:99:99",
		"disabled till",
		"Retry-After: soon",
		"Retry-After: 0",
	} {
		if got := ClassifySend("indexer returned HTTP 429: "+msg, now).RetryAt; !got.IsZero() {
			t.Errorf("%q parsed to %v, want none", msg, got)
		}
	}
}
