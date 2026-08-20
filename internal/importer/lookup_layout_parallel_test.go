package importer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// TestLookupBatchLayout_PreservesOrderAcrossFanOut is the regression guard for
// #1638's fix: LookupBatchLayout now matches units through a bounded worker
// pool instead of a sequential loop, and its documented contract is that
// "results are aligned with the input paths". A pool that wrote results in
// completion order rather than by index would still return the right SET of
// matches, so every existing single-path test would keep passing while the
// wizard silently attached each match to the wrong file.
//
// Uses three times lookupBatchConcurrency paths so the pool genuinely recycles
// slots rather than launching everything at once.
func TestLookupBatchLayout_PreservesOrderAcrossFanOut(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	const n = lookupBatchConcurrency * 3
	root := t.TempDir()
	paths := make([]string, n)
	wantTitles := make([]string, n)
	for i := range paths {
		// Distinct author per book so each unit has exactly one corroborated
		// catalogue match and lands on "confident".
		author := fmt.Sprintf("Author %02d", i)
		title := fmt.Sprintf("Title %02d", i)
		seedLayoutBook(t, books, authors, ctx, author, title)
		paths[i] = filepath.Join(root, author, title+".epub")
		wantTitles[i] = title
		writeFileAt(t, paths[i])
	}

	res, err := s.LookupBatchLayout(ctx, root, paths)
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if len(res) != n {
		t.Fatalf("len(results) = %d, want %d", len(res), n)
	}
	for i := range res {
		if res[i].Match == "" {
			t.Fatalf("results[%d].Match is empty: a slot was never written", i)
		}
		if res[i].Book == nil {
			t.Fatalf("results[%d].Book = nil, want a match for %q", i, wantTitles[i])
		}
		if res[i].Book.Title != wantTitles[i] {
			t.Errorf("results[%d].Book.Title = %q, want %q (results are misaligned with paths)",
				i, res[i].Book.Title, wantTitles[i])
		}
	}
}

// TestLookupBatchLayout_CanceledContextReturnsError pins the contract that a
// canceled batch fails outright rather than returning a partially populated
// slice. The fan-out stops launching on cancellation, so without an explicit
// error the tail of the results slice would be zero-value LookupResults whose
// Match is "" — neither confident, ambiguous nor none — and the wizard would
// render them as silently empty rows.
func TestLookupBatchLayout_CanceledContextReturnsError(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	seedLayoutBook(t, books, authors, ctx, "Andy Weir", "Project Hail Mary")

	root := t.TempDir()
	p := filepath.Join(root, "Andy Weir", "Project Hail Mary.epub")
	writeFileAt(t, p)

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	res, err := s.LookupBatchLayout(canceled, root, []string{p})
	if err == nil {
		t.Fatalf("LookupBatchLayout returned nil error for a canceled context, results = %+v", res)
	}
	if res != nil {
		t.Errorf("results = %+v, want nil so no partially matched batch reaches the caller", res)
	}
}
