package metadata_test

import (
	"context"
	"os"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/hardcover"
)

func TestLiveHardcoverUserListsIncludeShelfCounts(t *testing.T) {
	skipUnlessIntegration(t)
	token := os.Getenv(binderyHardcoverAPITokenEnv)
	if token == "" {
		t.Skipf("skipping live Hardcover list lookup; set %s", binderyHardcoverAPITokenEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveProviderTestTimeout)
	t.Cleanup(cancel)

	lists, err := hardcover.New().WithToken(token).GetUserLists(ctx)
	if err != nil {
		skipIfLiveProviderUnavailableError(t, "hardcover", err)
		t.Fatalf("GetUserLists: %v", err)
	}
	if len(lists) < 4 {
		t.Fatalf("GetUserLists returned %d lists, want at least the four built-in shelves", len(lists))
	}
	want := []struct {
		id   int
		slug string
	}{
		{id: -1, slug: "want-to-read"},
		{id: -2, slug: "currently-reading"},
		{id: -3, slug: "read"},
		{id: -4, slug: "did-not-finish"},
	}
	for i, shelf := range want {
		got := lists[i]
		if got.ID != shelf.id || got.Slug != shelf.slug {
			t.Fatalf("shelf[%d] = %+v, want id=%d slug=%q", i, got, shelf.id, shelf.slug)
		}
		if got.BooksCount < 0 {
			t.Fatalf("shelf[%d] BooksCount = %d, want non-negative", i, got.BooksCount)
		}
	}
}
