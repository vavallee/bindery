package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// ptrFloat is a test helper for building *float64 literals inline.
func ptrFloat(v float64) *float64 { return &v }

// TestResolveSeedRatio covers each branch of resolveSeedRatio: the nil-repo
// guard, the zero-id guard, a missing indexer, an indexer with no override
// (nil SeedRatio), and an indexer carrying an explicit override (including the
// -1 "unlimited" sentinel).
func TestResolveSeedRatio(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	indexers := db.NewIndexerRepo(database)

	// Indexer with an explicit positive override.
	idxOverride := &models.Indexer{
		Name: "withRatio", Type: "newznab", URL: "http://x", APIKey: "k",
		Categories: []int{}, SeedRatio: ptrFloat(2.5), SeedRatioSource: models.SeedRatioSourceUser,
	}
	if err := indexers.Create(ctx, idxOverride); err != nil {
		t.Fatalf("create override indexer: %v", err)
	}

	// Indexer with the unlimited sentinel (-1).
	idxUnlimited := &models.Indexer{
		Name: "unlimited", Type: "newznab", URL: "http://y", APIKey: "k",
		Categories: []int{}, SeedRatio: ptrFloat(-1), SeedRatioSource: models.SeedRatioSourceProwlarr,
	}
	if err := indexers.Create(ctx, idxUnlimited); err != nil {
		t.Fatalf("create unlimited indexer: %v", err)
	}

	// Indexer with no override stored (SeedRatio nil).
	idxNoOverride := &models.Indexer{
		Name: "noRatio", Type: "newznab", URL: "http://z", APIKey: "k",
		Categories: []int{}, SeedRatio: nil,
	}
	if err := indexers.Create(ctx, idxNoOverride); err != nil {
		t.Fatalf("create no-override indexer: %v", err)
	}

	tests := []struct {
		name    string
		repo    *db.IndexerRepo
		id      int64
		wantNil bool
		wantVal float64
	}{
		{
			name:    "nil indexer repo returns nil",
			repo:    nil,
			id:      idxOverride.ID,
			wantNil: true,
		},
		{
			name:    "zero indexer id returns nil",
			repo:    indexers,
			id:      0,
			wantNil: true,
		},
		{
			name:    "unknown indexer id returns nil",
			repo:    indexers,
			id:      999999,
			wantNil: true,
		},
		{
			name:    "indexer with no override returns nil",
			repo:    indexers,
			id:      idxNoOverride.ID,
			wantNil: true,
		},
		{
			name:    "indexer with positive override returns the ratio",
			repo:    indexers,
			id:      idxOverride.ID,
			wantNil: false,
			wantVal: 2.5,
		},
		{
			name:    "indexer with unlimited sentinel returns -1",
			repo:    indexers,
			id:      idxUnlimited.ID,
			wantNil: false,
			wantVal: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Scheduler{indexers: tt.repo}
			got := s.resolveSeedRatio(ctx, tt.id)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil ratio, got %v", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected ratio %v, got nil", tt.wantVal)
			}
			if *got != tt.wantVal {
				t.Fatalf("expected ratio %v, got %v", tt.wantVal, *got)
			}
		})
	}
}

// TestStorePending verifies storePending persists a delay-rejected release into
// pending_releases with the expected scalar fields, the format scoping, the
// indexer-id pointer, and the round-trippable release JSON.
func TestStorePending(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	pending := db.NewPendingReleaseRepo(database)

	// pending_releases.book_id has a NOT NULL FK to books(id) (migration 021),
	// and OpenMemory enables PRAGMA foreign_keys, so a real book row is required.
	a := &models.Author{ForeignID: "OL-A1", Name: "Auth", SortName: "Auth", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := &models.Book{
		ForeignID: "OL-B1", AuthorID: a.ID, Title: "Pending Book",
		SortTitle: "Pending Book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatalf("book create: %v", err)
	}

	s := &Scheduler{pending: pending}

	res := newznab.SearchResult{
		GUID:      "guid-pending-1",
		IndexerID: 42,
		Title:     "Pending Book by Auth EPUB",
		Size:      123456,
		PubDate:   "Mon, 02 Jan 2006 15:04:05 +0000",
		Protocol:  "torrent",
	}
	reason := "torrent delay not met"

	s.storePending(ctx, book.ID, models.MediaTypeEbook, res, reason)

	got, err := pending.ListByBookAndMediaType(ctx, book.ID, models.MediaTypeEbook)
	if err != nil {
		t.Fatalf("ListByBookAndMediaType: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 pending release, got %d", len(got))
	}
	pr := got[0]

	if pr.BookID != book.ID {
		t.Errorf("BookID: want %d, got %d", book.ID, pr.BookID)
	}
	if pr.MediaType != models.MediaTypeEbook {
		t.Errorf("MediaType: want %q, got %q", models.MediaTypeEbook, pr.MediaType)
	}
	if pr.Title != res.Title {
		t.Errorf("Title: want %q, got %q", res.Title, pr.Title)
	}
	if pr.GUID != res.GUID {
		t.Errorf("GUID: want %q, got %q", res.GUID, pr.GUID)
	}
	if pr.Protocol != res.Protocol {
		t.Errorf("Protocol: want %q, got %q", res.Protocol, pr.Protocol)
	}
	if pr.Size != res.Size {
		t.Errorf("Size: want %d, got %d", res.Size, pr.Size)
	}
	if pr.Reason != reason {
		t.Errorf("Reason: want %q, got %q", reason, pr.Reason)
	}
	if pr.IndexerID == nil {
		t.Errorf("IndexerID: want pointer to %d, got nil", res.IndexerID)
	} else if *pr.IndexerID != res.IndexerID {
		t.Errorf("IndexerID: want %d, got %d", res.IndexerID, *pr.IndexerID)
	}
	if pr.ReleaseJSON == "" {
		t.Errorf("ReleaseJSON should be persisted, got empty string")
	}

	// The stored entry must be scoped to its format: the audiobook query must
	// not see the ebook entry (issue #707).
	other, err := pending.ListByBookAndMediaType(ctx, book.ID, models.MediaTypeAudiobook)
	if err != nil {
		t.Fatalf("ListByBookAndMediaType audiobook: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("expected 0 audiobook pending releases, got %d", len(other))
	}
}

// TestStorePending_ZeroIndexerIDLeavesPointerNil verifies that a release with
// IndexerID==0 stores a NULL indexer_id (nil pointer), not a zero value.
func TestStorePending_ZeroIndexerIDLeavesPointerNil(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	pending := db.NewPendingReleaseRepo(database)

	a := &models.Author{ForeignID: "OL-A2", Name: "Auth2", SortName: "Auth2", MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("author create: %v", err)
	}
	book := &models.Book{
		ForeignID: "OL-B2", AuthorID: a.ID, Title: "Pending Book 2",
		SortTitle: "Pending Book 2", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatalf("book create: %v", err)
	}

	s := &Scheduler{pending: pending}

	res := newznab.SearchResult{
		GUID:     "guid-pending-2",
		Title:    "No Indexer Release",
		Protocol: "usenet",
		// IndexerID intentionally left 0.
	}
	s.storePending(ctx, book.ID, models.MediaTypeAudiobook, res, "usenet delay not met")

	got, err := pending.ListByBookAndMediaType(ctx, book.ID, models.MediaTypeAudiobook)
	if err != nil {
		t.Fatalf("ListByBookAndMediaType: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 pending release, got %d", len(got))
	}
	if got[0].IndexerID != nil {
		t.Errorf("IndexerID: want nil for zero IndexerID, got %d", *got[0].IndexerID)
	}
}
