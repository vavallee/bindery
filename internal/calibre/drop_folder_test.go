package calibre

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeLookup returns canned results for the poller. seq indexes one result
// per call so tests can model "not found → not found → found" sequences.
type fakeLookup struct {
	calls   int
	seq     []fakeLookupResp
	onCall  func(int) // optional spy for call-count assertions
}

type fakeLookupResp struct {
	id    int64
	found bool
	err   error
}

func (f *fakeLookup) fn(_ context.Context, _, _, _ string) (int64, bool, error) {
	n := f.calls
	f.calls++
	if f.onCall != nil {
		f.onCall(n)
	}
	if n >= len(f.seq) {
		// Off-the-end — keep returning "not found" so infinite-poll tests
		// terminate by the attempts budget.
		return 0, false, nil
	}
	r := f.seq[n]
	return r.id, r.found, r.err
}

func makeSrc(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestIngest_HappyPath: file lands in `drop/Author/Title.ext`, lookup hits
// on the first attempt, IngestResult carries the returned id.
func TestIngest_HappyPath(t *testing.T) {
	drop := t.TempDir()
	src := makeSrc(t, "input.epub", "the-bytes")

	w := NewDropFolderWriter(DropFolderConfig{
		DropFolderPath: drop,
		LibraryPath:    "/unused-in-this-test",
		PollInterval:   time.Millisecond,
		PollAttempts:   2,
	})
	fl := &fakeLookup{seq: []fakeLookupResp{{id: 42, found: true}}}
	w.lookup = fl.fn

	res, err := w.Ingest(context.Background(), src, "The Road", "Cormac McCarthy")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found {
		t.Error("Found should be true")
	}
	if res.CalibreID != 42 {
		t.Errorf("CalibreID = %d, want 42", res.CalibreID)
	}
	wantPath := filepath.Join(drop, "Cormac McCarthy", "The Road.epub")
	if res.DroppedPath != wantPath {
		t.Errorf("DroppedPath = %q, want %q", res.DroppedPath, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("file should exist at %q: %v", wantPath, err)
	}
	if fl.calls != 1 {
		t.Errorf("lookup called %d times, want 1", fl.calls)
	}
}

// TestIngest_PollEventuallyFinds verifies the retry loop — the first two
// lookups say "not found", the third hits. The file drop happens once,
// before any lookup; only the lookup count retries.
func TestIngest_PollEventuallyFinds(t *testing.T) {
	drop := t.TempDir()
	src := makeSrc(t, "in.epub", "x")

	w := NewDropFolderWriter(DropFolderConfig{
		DropFolderPath: drop,
		LibraryPath:    "/unused",
		PollInterval:   time.Millisecond,
		PollAttempts:   5,
	})
	fl := &fakeLookup{seq: []fakeLookupResp{
		{}, {}, {id: 7, found: true},
	}}
	w.lookup = fl.fn

	res, err := w.Ingest(context.Background(), src, "T", "A")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || res.CalibreID != 7 {
		t.Errorf("expected found=true id=7, got %+v", res)
	}
	if fl.calls != 3 {
		t.Errorf("lookup called %d times, want 3", fl.calls)
	}
}

// TestIngest_PollExhausted: Calibre never picks up the file. Ingest returns
// nil error (file drop succeeded) but Found=false; caller logs a warning
// and continues the import without a calibre_id mapping.
func TestIngest_PollExhausted(t *testing.T) {
	drop := t.TempDir()
	src := makeSrc(t, "in.epub", "x")

	w := NewDropFolderWriter(DropFolderConfig{
		DropFolderPath: drop,
		LibraryPath:    "/unused",
		PollInterval:   time.Microsecond,
		PollAttempts:   3,
	})
	fl := &fakeLookup{seq: nil} // always not-found
	w.lookup = fl.fn

	res, err := w.Ingest(context.Background(), src, "T", "A")
	if err != nil {
		t.Fatalf("Ingest should not fail on poll timeout, got %v", err)
	}
	if res.Found {
		t.Error("Found should be false when Calibre never picks up")
	}
	if res.CalibreID != 0 {
		t.Errorf("CalibreID should be zero, got %d", res.CalibreID)
	}
	if fl.calls != 3 {
		t.Errorf("lookup called %d times, want 3 (attempts budget)", fl.calls)
	}
	if _, err := os.Stat(res.DroppedPath); err != nil {
		t.Errorf("file should still exist in drop folder: %v", err)
	}
}

// TestIngest_LookupErrorsDontShortCircuit: a transient lookup error in the
// middle of the poll should be logged (see slog.Debug) but must not abort
// the retry loop — Calibre sometimes transiently locks metadata.db.
func TestIngest_LookupErrorsDontShortCircuit(t *testing.T) {
	drop := t.TempDir()
	src := makeSrc(t, "in.epub", "x")

	w := NewDropFolderWriter(DropFolderConfig{
		DropFolderPath: drop,
		LibraryPath:    "/unused",
		PollInterval:   time.Microsecond,
		PollAttempts:   4,
	})
	fl := &fakeLookup{seq: []fakeLookupResp{
		{err: errors.New("db locked")},
		{err: errors.New("db locked")},
		{id: 99, found: true},
	}}
	w.lookup = fl.fn

	res, err := w.Ingest(context.Background(), src, "T", "A")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || res.CalibreID != 99 {
		t.Errorf("expected found=true id=99, got %+v", res)
	}
}

// TestIngest_ContextCancel: a cancelled context during the sleep between
// attempts must break out immediately rather than waiting the full budget.
func TestIngest_ContextCancel(t *testing.T) {
	drop := t.TempDir()
	src := makeSrc(t, "in.epub", "x")

	w := NewDropFolderWriter(DropFolderConfig{
		DropFolderPath: drop,
		LibraryPath:    "/unused",
		PollInterval:   50 * time.Millisecond,
		PollAttempts:   100,
	})
	ctx, cancel := context.WithCancel(context.Background())
	fl := &fakeLookup{
		onCall: func(n int) {
			if n == 1 {
				cancel()
			}
		},
	}
	w.lookup = fl.fn

	start := time.Now()
	res, err := w.Ingest(ctx, src, "T", "A")
	if err != nil {
		t.Fatal(err)
	}
	if res.Found {
		t.Error("Found should be false when ctx cancelled")
	}
	// Shouldn't have burned the full 100 × 50ms budget.
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("context cancel didn't short-circuit: %s", time.Since(start))
	}
}

// TestIngest_RejectsUnconfigured: with drop_folder_path unset we return
// ErrDropFolderNotConfigured so the scanner can log-and-skip.
func TestIngest_RejectsUnconfigured(t *testing.T) {
	src := makeSrc(t, "in.epub", "x")
	w := NewDropFolderWriter(DropFolderConfig{DropFolderPath: ""})
	_, err := w.Ingest(context.Background(), src, "T", "A")
	if !errors.Is(err, ErrDropFolderNotConfigured) {
		t.Errorf("err = %v, want ErrDropFolderNotConfigured", err)
	}
}

// TestIngest_RejectsEmptyTitleOrAuthor: title/author drive both the file
// name and the DB lookup, so blank either must be rejected up front.
func TestIngest_RejectsEmptyTitleOrAuthor(t *testing.T) {
	drop := t.TempDir()
	src := makeSrc(t, "in.epub", "x")
	w := NewDropFolderWriter(DropFolderConfig{DropFolderPath: drop, LibraryPath: "/x"})
	if _, err := w.Ingest(context.Background(), src, "", "A"); err == nil {
		t.Error("empty title should be rejected")
	}
	if _, err := w.Ingest(context.Background(), src, "T", ""); err == nil {
		t.Error("empty author should be rejected")
	}
	if _, err := w.Ingest(context.Background(), "", "T", "A"); err == nil {
		t.Error("empty src should be rejected")
	}
}

// TestIngest_SanitizesPathSegments: path-unsafe characters in title /
// author must not escape into the drop folder. A ":" in the title would
// break Windows, "/" would create an unintended subdirectory.
func TestIngest_SanitizesPathSegments(t *testing.T) {
	drop := t.TempDir()
	src := makeSrc(t, "in.epub", "x")
	w := NewDropFolderWriter(DropFolderConfig{
		DropFolderPath: drop,
		LibraryPath:    "/x",
		PollAttempts:   1,
		PollInterval:   time.Microsecond,
	})
	w.lookup = (&fakeLookup{}).fn

	res, err := w.Ingest(context.Background(), src, "Who: What/Where?", "A/B")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.DroppedPath[len(drop):], ":") {
		t.Errorf("dropped path should not contain ':', got %q", res.DroppedPath)
	}
	rel, _ := filepath.Rel(drop, res.DroppedPath)
	// Must have exactly one separator: Author/Title.ext — no extra dirs
	// from the embedded "/".
	if c := strings.Count(rel, string(filepath.Separator)); c != 1 {
		t.Errorf("expected single separator in %q, got %d", rel, c)
	}
}

// TestIngest_DefaultsAppliedWhenCfgZero: zero-valued PollInterval /
// PollAttempts fall back to the package defaults rather than spinning
// the poll loop zero times or busy-looping.
func TestIngest_DefaultsAppliedWhenCfgZero(t *testing.T) {
	drop := t.TempDir()
	src := makeSrc(t, "in.epub", "x")
	w := NewDropFolderWriter(DropFolderConfig{
		DropFolderPath: drop,
		LibraryPath:    "/x",
		// Leave PollInterval + PollAttempts zero to exercise defaulting.
	})
	fl := &fakeLookup{seq: []fakeLookupResp{{id: 1, found: true}}}
	w.lookup = fl.fn
	res, err := w.Ingest(context.Background(), src, "T", "A")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found {
		t.Error("first-attempt hit should succeed with defaults")
	}
}
