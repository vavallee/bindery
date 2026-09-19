package hardcoverlistsyncer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

func TestDailyQuotaListSyncStopsBeforeWrites(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	token := "example-only"
	if err := settings.Set(ctx, "auth.hardcover_daily_holds", fmt.Sprintf(`{"%x":%q}`, sha256.Sum256([]byte(token)), time.Now().Add(time.Hour).UTC().Format(time.RFC3339))); err != nil {
		t.Fatal(err)
	}
	s := New(db.NewImportListRepo(database), db.NewAuthorRepo(database), db.NewBookRepo(database)).WithDailyQuota(hardcover.NewDailyQuota(settings))
	il := testImportList("Held", "hardcover", true)
	il.APIKey = token
	// First prove the real factory inherits the hold before any network call.
	var daily *metadata.DailyQuotaError
	if err := s.syncList(ctx, il); !errors.As(err, &daily) {
		t.Fatalf("factory hold: %v", err)
	}
	// A fetched batch must also stop before writing books, even if it arrived
	// from an already in-flight request when the hold was recorded.
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL}},
			books: []models.Book{bookWithSeriesRef("hc:123", "Example", nil)},
		}
	})
	if err := s.syncList(ctx, il); !errors.As(err, &daily) {
		t.Fatalf("batch hold: %v", err)
	}
	books, err := s.books.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	authors, err := s.authors.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 || len(authors) != 0 {
		t.Fatalf("wrote %d books, %d authors", len(books), len(authors))
	}
}
