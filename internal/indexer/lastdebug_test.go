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

// A sweep over a large library must not grow the log without bound.
func TestDebugLog_BackgroundIsBounded(t *testing.T) {
	l := NewDebugLog()
	for i := 0; i < debugLogBackgroundCap*3; i++ {
		l.RecordBackground(0, &SearchDebug{BookID: int64(i)})
	}
	if n := len(l.background); n != debugLogBackgroundCap {
		t.Fatalf("kept %d entries, want %d", n, debugLogBackgroundCap)
	}
	if got := l.Latest(0, false, allow); got.BookID != int64(debugLogBackgroundCap*3-1) {
		t.Fatalf("latest book %d, want the last recorded", got.BookID)
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
