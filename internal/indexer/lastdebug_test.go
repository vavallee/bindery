package indexer

import "testing"

func allow(int64) bool { return true }

// One user's interactive search is theirs alone; a caller that sees every
// interactive search gets the newest of them (#1859, #2154).
func TestDebugLog_InteractiveIsPerUserUnlessSeeAll(t *testing.T) {
	l := NewDebugLog()
	l.RecordInteractive(1, &SearchDebug{BookID: 10})
	l.RecordInteractive(2, &SearchDebug{BookID: 20})

	if got := l.Latest(1, false, allow); got == nil || got.BookID != 10 {
		t.Fatalf("user 1 read %+v, want book 10", got)
	}
	if got := l.Latest(3, false, allow); got != nil {
		t.Fatalf("user 3 read %+v, want nothing", got)
	}
	if got := l.Latest(3, true, allow); got == nil || got.BookID != 20 {
		t.Fatalf("see all read %+v, want the newest, book 20", got)
	}
}

// The newest visible entry wins across both kinds, and an automatic search the
// reader may not see is passed over rather than hiding an older visible one.
func TestDebugLog_NewestVisibleWins(t *testing.T) {
	l := NewDebugLog()
	l.RecordBackground(5, &SearchDebug{BookID: 1})
	l.RecordInteractive(5, &SearchDebug{BookID: 2})
	if got := l.Latest(5, false, allow); got.BookID != 2 {
		t.Fatalf("got book %d, want the newer interactive book 2", got.BookID)
	}
	l.RecordBackground(5, &SearchDebug{BookID: 3})
	l.RecordBackground(6, &SearchDebug{BookID: 4})
	onlyOwn := func(owner int64) bool { return owner == 5 }
	if got := l.Latest(5, false, onlyOwn); got.BookID != 3 {
		t.Fatalf("got book %d, want book 3, skipping owner 6's newer search", got.BookID)
	}
	if got := l.Latest(5, false, nil); got.BookID != 2 {
		t.Fatalf("got book %d, want the interactive book 2 when no automatic search is visible", got.BookID)
	}
}

// A sweep over a large library must not grow the log without bound, and one
// owner's sweep must not evict another owner's entries.
func TestDebugLog_BackgroundIsBoundedPerOwner(t *testing.T) {
	l := NewDebugLog()
	l.RecordBackground(2, &SearchDebug{BookID: 999})
	for i := 0; i < debugLogBackgroundPerOwner*50; i++ {
		l.RecordBackground(1, &SearchDebug{BookID: int64(i)})
	}
	if n := len(l.background[1]); n != debugLogBackgroundPerOwner {
		t.Fatalf("kept %d entries for owner 1, want %d", n, debugLogBackgroundPerOwner)
	}
	if got := l.Latest(0, false, allow); got.BookID != int64(debugLogBackgroundPerOwner*50-1) {
		t.Fatalf("latest book %d, want the last recorded", got.BookID)
	}
	onlyOwner2 := func(owner int64) bool { return owner == 2 }
	if got := l.Latest(0, false, onlyOwner2); got == nil || got.BookID != 999 {
		t.Fatalf("owner 2 read %+v, want its entry to survive owner 1's sweep", got)
	}
}

// A search that rejected thousands of releases is stored with its rejection
// list capped, and the caller's own value (which an interactive search sends
// back in full) is left alone.
func TestDebugLog_CapsStoredFilters(t *testing.T) {
	d := &SearchDebug{BookID: 1}
	for i := 0; i < debugLogMaxFilters+50; i++ {
		d.Filters = append(d.Filters, FilterDebug{Stage: "relevance"})
	}
	l := NewDebugLog()
	l.RecordInteractive(1, d)
	l.RecordBackground(1, d)
	if len(d.Filters) != debugLogMaxFilters+50 {
		t.Fatalf("caller's filters changed to %d", len(d.Filters))
	}
	for _, got := range []*SearchDebug{l.interactive[1].dbg, l.background[1][0].dbg} {
		if len(got.Filters) != debugLogMaxFilters+1 {
			t.Fatalf("stored %d filters, want %d plus one summary", len(got.Filters), debugLogMaxFilters)
		}
		if last := got.Filters[debugLogMaxFilters]; last.Stage != "truncated" || last.Reason != "50 more rejected release(s) not kept in the stored copy" {
			t.Fatalf("summary entry = %+v", last)
		}
	}
}

func TestDebugLog_NilIsSafe(t *testing.T) {
	var l *DebugLog
	l.RecordInteractive(1, &SearchDebug{})
	l.RecordBackground(1, &SearchDebug{})
	if l.Latest(1, true, allow) != nil {
		t.Fatal("nil log returned an entry")
	}
}
