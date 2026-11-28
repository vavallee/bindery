package releasefail

import (
	"fmt"
	"testing"
	"time"
)

// A time the indexer gave that is only a minute away is still not retried
// sooner than the floor.
func TestIndexerBackoffFloor(t *testing.T) {
	soon := fmt.Sprintf("indexer returned HTTP 429: disabled till %s", now.Add(time.Minute).Format("1/2/2006 15:04:05"))
	if got := IndexerBackoffUntil(ClassifySend(soon, now), now); !got.Equal(now.Add(MinIndexerBackoff)) {
		t.Errorf("backoff until %v, want the %v floor", got, MinIndexerBackoff)
	}
}

// The two errors #4111 quotes, as the torrent adapters word them: the ban
// notice behind a 429 is the indexer's and never counts; a bare 500 saying
// "Download failed" is the indexer failing to fetch that release, and can.
func TestClassifySend_IssueErrors(t *testing.T) {
	for msg, want := range map[string]Class{
		"download client error: fetch torrent content: fetch torrent: indexer returned HTTP 429: Indexer is disabled till 11/20/2026 02:16:12 due to recent failures.": ClassIndexer,
		"download client error: fetch torrent content: fetch torrent: indexer returned HTTP 500: Download failed":                                                      ClassServerError,
	} {
		if got := ClassifySend(msg, now); got.Class != want {
			t.Errorf("%q classified %q, want %q", msg, got.Class, want)
		}
	}
}

func TestClassCounts(t *testing.T) {
	for c, want := range map[Class]bool{
		ClassIndexer: false, ClassRelease: true, ClassServerError: true, ClassNeverArrived: true,
		ClassClientError: false, ClassClientRemoved: false, ClassLocal: false, ClassIndexerWide: false,
	} {
		if c.Counts() != want {
			t.Errorf("%q.Counts() = %v, want %v", c, c.Counts(), want)
		}
	}
}
