package scheduler

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// A result no indexer produced (a Soulseek peer, #1717) has a nil IndexerID.
// The bookkeeping that keys on the id must treat it as "none", and a stored
// download row must get NULL, because downloads.indexer_id is a foreign key
// and 0 is not a row.
func TestResultIndexerID_NilIsNone(t *testing.T) {
	if got := resultIndexerID(nil); got != 0 {
		t.Errorf("resultIndexerID(nil) = %d", got)
	}
	if got := resultIndexerID(&newznab.SearchResult{}); got != 0 {
		t.Errorf("resultIndexerID(no indexer) = %d", got)
	}
	if got := resultIndexerID(&newznab.SearchResult{IndexerID: idPtr(7)}); got != 7 {
		t.Errorf("resultIndexerID(7) = %d", got)
	}
}

func TestCopyIndexerID(t *testing.T) {
	if copyIndexerID(nil) != nil {
		t.Error("nil became non nil")
	}
	if copyIndexerID(idPtr(0)) != nil {
		t.Error("0 became a stored id; the foreign key would reject it")
	}
	src := idPtr(5)
	got := copyIndexerID(src)
	if got == nil || *got != 5 {
		t.Fatalf("copy = %v", got)
	}
	*src = 6
	if *got != 5 {
		t.Error("the copy aliases the search result")
	}
}

func TestCheckPin_ResultWithoutIndexerPassesQuietly(t *testing.T) {
	f := newSchedPinFixture(t)
	skipped := map[int64]bool{}
	res := &newznab.SearchResult{GUID: "slskd:1", Protocol: "soulseek"}
	c, skip, err := f.s.checkPin(f.ctx, f.book, res, skipped)
	if c != nil || skip || err != nil {
		t.Fatalf("checkPin = %v, %v, %v; want nil, false, nil", c, skip, err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v", skipped)
	}
}
