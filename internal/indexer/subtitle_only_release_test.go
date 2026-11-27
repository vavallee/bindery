package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// The subtitle only query (#4183) is only worth sending if the relevance
// filter then approves what it finds. It approves the reporter's release, and
// not the same title under another author.
func TestFilterRelevant_SubtitleOnlyRelease(t *testing.T) {
	title := "Path to Transcendence, Book 1: Will of Mana"
	in := []newznab.SearchResult{
		{Title: "Will of Mana by Ryan Arelius [ENG / M4B]"},
		{Title: "Will of Mana by Jane Doe [ENG / M4B]"},
	}
	kept, _ := filterRelevantDetailed(in, title, "Ryan Arelius", nil)
	if len(kept) != 1 || kept[0].Title != in[0].Title {
		t.Fatalf("kept %+v", kept)
	}
}
