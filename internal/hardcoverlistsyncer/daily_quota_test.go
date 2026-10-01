package hardcoverlistsyncer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
	// Example-only install secret so the test can compute the fingerprint.
	secret := strings.Repeat("ab", 32)
	if err := settings.Set(ctx, "auth.hardcover_daily_hold_secret", secret); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(secret)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	if err := settings.Set(ctx, "auth.hardcover_daily_holds", fmt.Sprintf(`{"%x":%q}`, mac.Sum(nil), time.Now().Add(time.Hour).UTC().Format(time.RFC3339))); err != nil {
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

// holdAfter admits the first n checks, then reports a daily hold.
type holdAfter struct {
	n     int
	daily *metadata.DailyQuotaError
}

func (h *holdAfter) Check(context.Context, string) error {
	if h.n == 0 {
		return h.daily
	}
	h.n--
	return nil
}

func TestDailyQuotaMidPassHoldStillSearchesWantedBooks(t *testing.T) {
	s, repo, searcher := newSearchingSyncer(t)
	ctx := context.Background()
	il := testImportList("Held mid-pass", "hardcover", true)
	il.MonitorNew = true
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{ForeignID: "hc:list-author", Name: "List Author", MetadataProvider: "hardcover"}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 7, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:before-hold", Title: "Before Hold", MetadataProvider: "hardcover", Author: author},
				{ForeignID: "hc:after-hold", Title: "After Hold", MetadataProvider: "hardcover", Author: author},
			},
		}
	})
	daily := &metadata.DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
	s.dailyQuota = &holdAfter{n: 1, daily: daily}

	if err := s.syncList(ctx, il); !errors.Is(err, daily) {
		t.Fatalf("syncList = %v, want the daily hold", err)
	}
	created, err := s.books.GetByForeignID(ctx, "hc:before-hold")
	if err != nil || created == nil {
		t.Fatalf("book before hold not created: %v", err)
	}
	if skipped, _ := s.books.GetByForeignID(ctx, "hc:after-hold"); skipped != nil {
		t.Fatal("book written after the hold")
	}
	if call := searcher.waitForCall(t, time.Second); call.ID != created.ID {
		t.Errorf("search started for book %d, want %d", call.ID, created.ID)
	}
}
