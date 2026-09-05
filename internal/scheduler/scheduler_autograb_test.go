package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// autoGrabScheduler builds a Scheduler whose only observable behaviour is how
// many indexer searches it starts, with the autoGrab.enabled row seeded to
// value. An empty value leaves the key unset, which is the shape of an install
// that never touched the switch.
func autoGrabScheduler(t *testing.T, value string) (*Scheduler, *stubSearcher) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	settings := db.NewSettingsRepo(database)
	if value != "" {
		if err := settings.Set(context.Background(), "autoGrab.enabled", value); err != nil {
			t.Fatalf("seed autoGrab.enabled=%q: %v", value, err)
		}
	}
	ss := &stubSearcher{}
	return &Scheduler{searcher: ss, settings: settings}, ss
}

// TestSearchAndGrabBook_HonoursAutoGrabSwitch is the #2256 chokepoint test.
//
// autoGrab.enabled used to be enforced by each caller, so three dispatch sites
// (bulk fan-out, add-book, add-from-recommendations) grabbed with the switch
// off. The check now lives at the single point where a grab is dispatched, so
// the switch holds no matter who called.
//
// A 'both' book is used deliberately: it costs two searches when the switch is
// on, so "off" asserting zero is not confused with a book that had nothing to
// search for.
func TestSearchAndGrabBook_HonoursAutoGrabSwitch(t *testing.T) {
	book := models.Book{
		ID:        7,
		Title:     "The Martian",
		MediaType: models.MediaTypeBoth,
		// Neither file path is set, so both formats still need a search.
	}

	cases := []struct {
		name      string
		setting   string
		wantCalls int
	}{
		{"switch off", "false", 0},
		{"switch on", "true", 2},
		{"key unset defaults to on", "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ss := autoGrabScheduler(t, tc.setting)
			s.SearchAndGrabBook(context.Background(), book)
			if got := int(ss.calls.Load()); got != tc.wantCalls {
				t.Fatalf("searches started with autoGrab.enabled=%q: got %d, want %d",
					tc.setting, got, tc.wantCalls)
			}
		})
	}
}

// TestAutoGrabEnabled_FailsOpenWithoutSettings pins the nil-repo behaviour.
// Every pre-#2256 call site defaulted to "enabled" when it could not read the
// setting, and a lot of scheduler tests construct a Scheduler with no settings
// repo at all; flipping that default to "disabled" would silently stop grabs
// for anyone whose settings read fails.
func TestAutoGrabEnabled_FailsOpenWithoutSettings(t *testing.T) {
	s := &Scheduler{}
	if !s.autoGrabEnabled(context.Background()) {
		t.Fatal("autoGrabEnabled with no settings repo = false, want true (fail open)")
	}
}

// ---------------------------------------------------------------------------
// Drift guard
// ---------------------------------------------------------------------------

// schedulerSourceDir is this package's directory. go test runs with the
// package dir as cwd, so the guard reads the sources it is guarding.
const schedulerSourceDir = "."

// funcBody returns the source text of a top-level func whose declaration line
// starts with decl, along with the 1-based line range it spans. It relies on
// gofmt's guarantee that a top-level func closes on a line that is exactly "}".
func funcBody(t *testing.T, src, decl string) (body string, firstLine, lastLine int) {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, decl) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("could not find %q in the scheduler source; if it was renamed or moved, update this drift guard to follow it", decl)
	}
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "}" {
			return strings.Join(lines[start:i+1], "\n"), start + 1, i + 1
		}
	}
	t.Fatalf("could not find the closing brace of %q", decl)
	return "", 0, 0
}

// TestAutoGrabChokepointHolds is the #2256 drift guard, modelled on the #1700
// vocabulary guard: it reads the source and asserts a structural property, so
// it fires whichever side of the property changes.
//
// The fix for #2256 is not "these three call sites now check the switch", it
// is "there is exactly one place a grab is dispatched from and it checks the
// switch". Two things have to stay true for that to keep holding:
//
//  1. searchAndGrabFormats consults autoGrabEnabled before it dispatches
//     anything, and
//  2. nothing calls searchAndGrabFormat except searchAndGrabFormats, so no new
//     dispatch site can route around the check.
//
// searchAndGrabFormats is where the check moved to when the wanted sweep gained
// the ability to search a subset of a book's formats (#2365); SearchAndGrabBook
// is now one of its callers rather than the chokepoint itself. A new caller of
// either needs no guard of its own and this guard stays quiet. A new caller of
// searchAndGrabFormat, or a searchAndGrabFormats that stops checking, fails
// here.
func TestAutoGrabChokepointHolds(t *testing.T) {
	const mainFile = "scheduler.go"
	raw, err := os.ReadFile(filepath.Join(schedulerSourceDir, mainFile))
	if err != nil {
		t.Fatalf("reading %s: %v (this guard needs the full repo checkout)", mainFile, err)
	}
	src := string(raw)

	body, first, last := funcBody(t, src, "func (s *Scheduler) searchAndGrabFormats(")

	checkAt := strings.Index(body, "s.autoGrabEnabled(ctx)")
	if checkAt < 0 {
		t.Fatalf("searchAndGrabFormats does not call s.autoGrabEnabled(ctx); the auto-grab kill switch (#2256) is only enforced there, so removing it un-guards every caller at once")
	}
	dispatchAt := strings.Index(body, "s.searchAndGrabFormat(")
	if dispatchAt < 0 {
		t.Fatal("searchAndGrabFormats no longer calls s.searchAndGrabFormat; update this drift guard to follow the new dispatch call")
	}
	if checkAt > dispatchAt {
		t.Fatal("searchAndGrabFormats dispatches a search before checking s.autoGrabEnabled(ctx); the check must gate the dispatch (#2256)")
	}

	// autoGrabEnabled must actually read the switch it is named after.
	helper, _, _ := funcBody(t, src, "func (s *Scheduler) autoGrabEnabled(")
	if !strings.Contains(helper, `"autoGrab.enabled"`) {
		t.Fatal(`Scheduler.autoGrabEnabled no longer reads the "autoGrab.enabled" setting`)
	}

	// Every other call of searchAndGrabFormat in production code would be a
	// dispatch site that skips the check.
	entries, err := os.ReadDir(schedulerSourceDir)
	if err != nil {
		t.Fatalf("reading %s: %v", schedulerSourceDir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fileRaw, err := os.ReadFile(filepath.Join(schedulerSourceDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for i, line := range strings.Split(string(fileRaw), "\n") {
			if !strings.Contains(line, "searchAndGrabFormat(") {
				continue
			}
			if strings.HasPrefix(line, "func (s *Scheduler) searchAndGrabFormat(") {
				continue // the declaration itself
			}
			lineNo := i + 1
			if name == mainFile && lineNo >= first && lineNo <= last {
				continue // inside searchAndGrabFormats, which is the guarded path
			}
			t.Errorf("%s:%d calls searchAndGrabFormat outside searchAndGrabFormats, bypassing the auto-grab kill switch (#2256). Dispatch through searchAndGrabFormats instead.", name, lineNo)
		}
	}
}
