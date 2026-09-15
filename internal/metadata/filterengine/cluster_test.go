package filterengine

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestClusterKey_FoldsLeadingArticleAndCase(t *testing.T) {
	tests := []struct{ a, b string }{
		{"The Hobbit", "the hobbit"},
		{"The Hobbit", "Hobbit"},
		{"A Study in Scarlet", "Study in Scarlet"},
		{"An Ember in the Ashes", "Ember in the Ashes"},
	}
	for _, tt := range tests {
		ka, kb := ClusterKey(tt.a), ClusterKey(tt.b)
		if ka != kb {
			t.Errorf("ClusterKey(%q)=%q != ClusterKey(%q)=%q, want equal", tt.a, ka, tt.b, kb)
		}
	}
}

func TestClusterKey_DistinctTitlesDontCollide(t *testing.T) {
	if ClusterKey("The Way of Kings") == ClusterKey("Words of Radiance") {
		t.Error("distinct series entries collided into one cluster key")
	}
}

func TestBuildClusters_GroupsAndAggregates(t *testing.T) {
	books := []models.Book{
		{ForeignID: "ol1", Title: "The Hobbit", EditionCount: 5},
		{ForeignID: "ol2", Title: "Hobbit", EditionCount: 40},
		{ForeignID: "ol3", Title: "The Silmarillion", EditionCount: 12},
	}
	clusters, byID := BuildClusters(books)

	if len(clusters) != 2 {
		t.Fatalf("clusters = %d, want 2", len(clusters))
	}

	hobbit := byID["ol1"]
	if hobbit == nil {
		t.Fatal("ol1 has no cluster")
	}
	if hobbit != byID["ol2"] {
		t.Error("ol1 and ol2 (\"The Hobbit\" / \"Hobbit\") should share one cluster")
	}
	if hobbit.Size != 2 {
		t.Errorf("hobbit cluster size = %d, want 2", hobbit.Size)
	}
	if hobbit.MaxEditionCount != 40 {
		t.Errorf("hobbit cluster MaxEditionCount = %d, want 40 (the max across members)", hobbit.MaxEditionCount)
	}
	if hobbit.CanonicalID != "ol2" {
		t.Errorf("hobbit cluster CanonicalID = %q, want %q (the higher edition-count member)", hobbit.CanonicalID, "ol2")
	}

	silmarillion := byID["ol3"]
	if silmarillion == nil {
		t.Fatal("ol3 has no cluster")
	}
	if silmarillion.Size != 1 {
		t.Errorf("silmarillion cluster size = %d, want 1", silmarillion.Size)
	}
	if silmarillion.CanonicalID != "ol3" {
		t.Errorf("solo cluster CanonicalID = %q, want %q", silmarillion.CanonicalID, "ol3")
	}
}

func TestBuildClusters_TieKeepsFirstSeen(t *testing.T) {
	books := []models.Book{
		{ForeignID: "first", Title: "Dune", EditionCount: 10},
		{ForeignID: "second", Title: "Dune", EditionCount: 10},
	}
	_, byID := BuildClusters(books)
	if byID["first"].CanonicalID != "first" {
		t.Errorf("CanonicalID = %q, want %q (tie should keep first-seen)", byID["first"].CanonicalID, "first")
	}
}

// TestBuildClusters_EmptyForeignIDFirstSeenDoesNotReSeed pins a fix: the
// first-seen member of a cluster having an empty ForeignID (not reachable
// via today's only real caller, OpenLibrary, which filters empty ForeignIDs
// before constructing a Book — but BuildClusters is exported and
// provider-agnostic) must not let a later, lower-EditionCount member
// silently overwrite CanonicalID/the internal canonicalEC tracker outside
// the normal ">" comparison. MaxEditionCount was never affected by this bug
// (computed independently every iteration); only CanonicalID selection was.
func TestBuildClusters_EmptyForeignIDFirstSeenDoesNotReSeed(t *testing.T) {
	books := []models.Book{
		{ForeignID: "", Title: "X", EditionCount: 5},
		{ForeignID: "A", Title: "X", EditionCount: 1},
	}
	clusters, _ := BuildClusters(books)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	c := clusters[0]
	if c.MaxEditionCount != 5 {
		t.Errorf("MaxEditionCount = %d, want 5", c.MaxEditionCount)
	}
	if c.CanonicalID != "" {
		t.Errorf("CanonicalID = %q, want \"\" (the higher-EditionCount member seen first, even with an empty ForeignID)", c.CanonicalID)
	}
}

func TestBuildClusters_EmptyInput(t *testing.T) {
	clusters, byID := BuildClusters(nil)
	if len(clusters) != 0 || len(byID) != 0 {
		t.Errorf("expected empty results for empty input, got %d clusters, %d entries", len(clusters), len(byID))
	}
}
