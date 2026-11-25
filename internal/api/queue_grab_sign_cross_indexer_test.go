package api

import (
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// A grab attributed to indexer A must never get indexer B's API key appended
// to a URL on B's host, whatever path the URL carries.
func TestSignNZBURL_NamedIndexerNeverBorrowsAnotherIndexersKey(t *testing.T) {
	h, database, _, _, _, ctx := queueFixture(t)
	indexers := db.NewIndexerRepo(database)
	h.WithIndexers(indexers)
	a := &models.Indexer{Name: "a", Type: "newznab", URL: "http://indexer-a.example/api", APIKey: "KEY_A", Enabled: true}
	b := &models.Indexer{Name: "b", Type: "newznab", URL: "http://prowlarr:9696/1/api", APIKey: "KEY_B", Enabled: true}
	for _, i := range []*models.Indexer{a, b} {
		if err := indexers.Create(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []string{
		"http://prowlarr:9696/1/download?link=x",
		"http://prowlarr:9696/api/v1/indexer?x=1",
		"http://prowlarr:9696/anything",
	} {
		got, _ := h.signNZBURL(ctx, u, &a.ID)
		if strings.Contains(got, "KEY_B") || strings.Contains(got, "KEY_A") {
			t.Errorf("signNZBURL(%q, indexer A) = %q; a key was appended", u, got)
		}
	}
	// The named indexer's own host, even on a different port, still signs (#2505).
	got, _ := h.signNZBURL(ctx, "http://indexer-a.example:8080/dl?x=1", &a.ID)
	if !strings.Contains(got, "apikey=KEY_A") {
		t.Errorf("own host, other port: got %q, want KEY_A appended", got)
	}
}
