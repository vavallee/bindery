package api

import (
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestSignNZBURL_ReturnsTheIndexerItResolved covers #2053. The host-match
// fallback exists for API clients that post {guid, nzbUrl} with no indexer id,
// and it used to return only the signed URL. The grab then persisted a download
// row with a nil IndexerID, so the queue could not say where the release came
// from and resolveSeedRatio had no id to look an override up against.
//
// Asserting the id and the URL together matters: returning the URL was never
// broken, so a test that only checked signing would have passed throughout.
func TestSignNZBURL_ReturnsTheIndexerItResolved(t *testing.T) {
	h, database, _, _, _, ctx := queueFixture(t)
	indexers := db.NewIndexerRepo(database)
	h.WithIndexers(indexers)

	idx := &models.Indexer{Name: "prowlarr-1", Type: "newznab", URL: "http://prowlarr:9696/3/api", APIKey: "SECRET", Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	const dl = "http://prowlarr:9696/3/download?file=Lee+Child&link=abc"
	const signed = "http://prowlarr:9696/3/download?apikey=SECRET&file=Lee+Child&link=abc"

	got, matched := h.signNZBURL(ctx, dl, nil)
	if got != signed {
		t.Fatalf("url = %q, want %q", got, signed)
	}
	if matched == nil {
		t.Fatal("indexer id = nil, want the indexer whose key signed the URL")
	}
	if *matched != idx.ID {
		t.Errorf("indexer id = %d, want %d", *matched, idx.ID)
	}
}

// TestSignNZBURL_AttributionOnlyWhenUnambiguous pins the cases where the
// fallback must NOT attribute.
//
// The seed-ratio override is per indexer row, so two indexers on one host that
// happen to share an apikey sign identically but are not interchangeable for
// attribution. Picking whichever List returned last would be a guess. The
// caller's own id needs no help, and an ambiguous host signs nothing at all.
func TestSignNZBURL_AttributionOnlyWhenUnambiguous(t *testing.T) {
	h, database, _, _, _, ctx := queueFixture(t)
	indexers := db.NewIndexerRepo(database)
	h.WithIndexers(indexers)

	first := &models.Indexer{Name: "prowlarr-1", Type: "newznab", URL: "http://prowlarr:9696/3/api", APIKey: "SECRET", Enabled: true}
	if err := indexers.Create(ctx, first); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	const dl = "http://prowlarr:9696/3/download?file=Lee+Child&link=abc"
	const signed = "http://prowlarr:9696/3/download?apikey=SECRET&file=Lee+Child&link=abc"

	// The caller named the indexer, so the row already carries the right id and
	// the fallback has nothing to add.
	if _, matched := h.signNZBURL(ctx, dl, &first.ID); matched != nil {
		t.Errorf("caller-supplied id: matched = %d, want nil", *matched)
	}

	// Two indexers on the host with the same key: still signs, no attribution.
	if err := indexers.Create(ctx, &models.Indexer{
		Name: "prowlarr-2", Type: "newznab", URL: "http://prowlarr:9696/4/api", APIKey: "SECRET", Enabled: true,
	}); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	got, matched := h.signNZBURL(ctx, dl, nil)
	if got != signed {
		t.Errorf("two indexers sharing a key: url = %q, want it still signed", got)
	}
	if matched != nil {
		t.Errorf("two indexers sharing a key: matched = %d, want nil (their seed ratios can differ)", *matched)
	}

	// A third with a different key makes the host ambiguous: nothing signed,
	// nothing attributed.
	if err := indexers.Create(ctx, &models.Indexer{
		Name: "prowlarr-3", Type: "newznab", URL: "http://prowlarr:9696/5/api", APIKey: "OTHER", Enabled: true,
	}); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	got, matched = h.signNZBURL(ctx, dl, nil)
	if got != dl {
		t.Errorf("ambiguous host: url = %q, want it unchanged", got)
	}
	if matched != nil {
		t.Errorf("ambiguous host: matched = %d, want nil", *matched)
	}

	// A URL on a host no indexer serves attributes nothing either.
	const foreign = "http://uploader.example.com/dl?id=abc"
	if _, matched := h.signNZBURL(ctx, foreign, nil); matched != nil {
		t.Errorf("foreign host: matched = %d, want nil", *matched)
	}
}

// TestSignNZBURL_StaleIdIsReplacedByTheRealOne is the case that produced a
// dangling reference rather than a nil one: the caller named an indexer that no
// longer exists, the host match signed the URL anyway, and the row kept the id
// of a deleted indexer. The id recorded should be the one that actually signed.
func TestSignNZBURL_StaleIdIsReplacedByTheRealOne(t *testing.T) {
	h, database, _, _, _, ctx := queueFixture(t)
	indexers := db.NewIndexerRepo(database)
	h.WithIndexers(indexers)

	idx := &models.Indexer{Name: "prowlarr-1", Type: "newznab", URL: "http://prowlarr:9696/3/api", APIKey: "SECRET", Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	const dl = "http://prowlarr:9696/3/download?file=Lee+Child&link=abc"
	stale := int64(9999)

	_, matched := h.signNZBURL(ctx, dl, &stale)
	if matched == nil {
		t.Fatal("stale id: matched = nil, want the indexer that signed")
	}
	if *matched != idx.ID {
		t.Errorf("stale id: matched = %d, want %d", *matched, idx.ID)
	}
}
