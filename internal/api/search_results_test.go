package api

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/indexer/newznab"
)

func TestSearchResultRegistry_RecordsWhatWasReturned(t *testing.T) {
	r := NewSearchResultRegistry()
	r.remember([]newznab.SearchResult{
		{GUID: "g1", IndexerID: 3, Title: "T", Size: 9, Protocol: "torrent", NZBURL: "http://idx/dl?apikey=K&id=1"},
		{GUID: "", NZBURL: "http://idx/dl?id=2"},
	})
	e, ok := r.lookup("g1")
	if !ok {
		t.Fatal("g1 should be recorded")
	}
	if e.NZBURL != "http://idx/dl?apikey=K&id=1" {
		t.Errorf("the recorded URL must be the raw one, credentials included, got %q", e.NZBURL)
	}
	book := int64(5)
	req := grabRequest{GUID: "g1", NZBURL: "http://elsewhere/", Title: "posted", BookID: &book, MediaType: "audiobook"}
	e.apply(&req)
	if req.NZBURL != e.NZBURL || req.Title != "T" || req.Size != 9 || req.Protocol != "torrent" || req.IndexerID == nil || *req.IndexerID != 3 {
		t.Errorf("apply must take the release fields from the record, got %+v", req)
	}
	if req.BookID == nil || *req.BookID != 5 || req.MediaType != "audiobook" {
		t.Errorf("apply must keep the caller's book and media type, got %+v", req)
	}
	if _, ok := r.lookup(""); ok {
		t.Error("an empty GUID is never recorded")
	}
	var nilReg *SearchResultRegistry
	nilReg.remember([]newznab.SearchResult{{GUID: "g"}})
	if _, ok := nilReg.lookup("g"); ok {
		t.Error("a nil registry knows no releases")
	}
}

func TestSearchResultRegistry_ExpiresAndEvicts(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := NewSearchResultRegistry()
	r.now = func() time.Time { return now }
	r.max = 3

	r.remember([]newznab.SearchResult{{GUID: "a"}, {GUID: "b"}})
	now = now.Add(time.Minute)
	// Re-recording a refreshes it, so b is now the oldest.
	r.remember([]newznab.SearchResult{{GUID: "a"}, {GUID: "c"}, {GUID: "d"}})
	if _, ok := r.lookup("b"); ok {
		t.Error("b was the oldest past the cap and should be evicted")
	}
	for _, g := range []string{"a", "c", "d"} {
		if _, ok := r.lookup(g); !ok {
			t.Errorf("%s should still be recorded", g)
		}
	}

	now = now.Add(searchResultTTL + time.Second)
	if _, ok := r.lookup("a"); ok {
		t.Error("an entry past the TTL must not be grabbable")
	}
	r.remember([]newznab.SearchResult{{GUID: "e"}})
	if len(r.entries) != 1 {
		t.Errorf("expired entries should be dropped on the next record, have %d", len(r.entries))
	}
}

func TestSearchResultRegistry_OrderStaysBounded(t *testing.T) {
	r := NewSearchResultRegistry()
	for i := 0; i < 1000; i++ {
		r.remember([]newznab.SearchResult{{GUID: "same"}, {GUID: "g" + strconv.Itoa(i%5)}})
	}
	if len(r.entries) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(r.entries))
	}
	if len(r.order) > 2*len(r.entries)+64+2 {
		t.Errorf("re-recorded GUIDs must not grow the order list without bound, it has %d records", len(r.order))
	}
}

func TestCallerMayPostDownloadURL(t *testing.T) {
	bg := context.Background()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"api key", auth.WithAPIKeyAuth(auth.WithUserRole(auth.WithUserID(bg, 1), auth.RoleAdmin)), true},
		{"admin session", auth.WithUserRole(auth.WithUserID(bg, 1), auth.RoleAdmin), false},
		{"local or disabled mode admin, no user", auth.WithUserRole(bg, auth.RoleAdmin), false},
		{"no identity at all", bg, true},
		{"user session", auth.WithUserRole(auth.WithUserID(bg, 2), auth.RoleUser), false},
		{"session whose role could not be read", auth.WithUserID(bg, 2), false},
		{"requester", auth.WithUserRole(auth.WithUserID(bg, 3), auth.RoleRequester), false},
	} {
		if got := callerMayPostDownloadURL(tc.ctx); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
