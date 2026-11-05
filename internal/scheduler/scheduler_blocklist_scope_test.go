package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestSearchAndGrabFormat_BlocklistEntryForAnotherBookDoesNotBlock is #3563
// for automatic search. alice's book for the same work had the release
// blocklisted for it, for example because her quality profile refused the
// format at import. bob's book must still grab it. An entry the download
// client earned (release scoped) must keep blocking it for bob too.
func TestSearchAndGrabFormat_BlocklistEntryForAnotherBookDoesNotBlock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  string
		reason string
		grabs  int32
	}{
		{"book scoped entry of another book", models.BlocklistScopeBook, "format mobi is not allowed", 1},
		{"release scoped entry of another book", models.BlocklistScopeRelease, "downloadFailed: FAILURE/PAR", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRegrabFixture(t)
			ctx := context.Background()
			aliceBook := models.Book{
				ForeignID: "OL-3563-ALICE", AuthorID: f.book.AuthorID, Title: f.book.Title,
				SortTitle: f.book.SortTitle, Status: models.BookStatusWanted,
				Genres: []string{}, MetadataProvider: "ol", Monitored: true,
				MediaType: models.MediaTypeEbook, OwnerUserID: f.alice,
			}
			if err := db.NewBookRepo(f.database).Create(ctx, &aliceBook); err != nil {
				t.Fatal(err)
			}
			if err := f.s.blocklist.Create(ctx, &models.BlocklistEntry{
				BookID: &aliceBook.ID, GUID: regrabGUID, Title: "Regrab Book.epub",
				Reason: tc.reason, Scope: tc.scope,
			}); err != nil {
				t.Fatal(err)
			}

			f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)

			if n := f.adds.Load(); n != tc.grabs {
				t.Errorf("#3563: client got %d requests for bob's book, want %d", n, tc.grabs)
			}
		})
	}
}
