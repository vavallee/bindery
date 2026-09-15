package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

func TestHardcoverQuotaStatusAndSettings(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(context.Background(), hardcover.SettingDailyRequestLimit, "50000"); err != nil {
		t.Fatal(err)
	}
	h := NewSettingsHandler(settings).WithHardcoverQuota(hardcover.NewQuota(settings))
	w := httptest.NewRecorder()
	h.HardcoverQuota(w, httptest.NewRequest(http.MethodGet, "/api/v1/hardcover/quota", nil))
	var status hardcover.QuotaStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || status.Allowance != 50000 || status.Reserve != 5000 || status.Deferred {
		t.Fatalf("status: %d %+v", w.Code, status)
	}
	for _, value := range []string{"5000", "50000", "123456", ""} {
		if err := validateSettingValue(hardcover.SettingDailyRequestLimit, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"0", "-1", "invalid", "1000000001"} {
		if err := validateSettingValue(hardcover.SettingDailyRequestLimit, value); err == nil {
			t.Fatal(value)
		}
	}
	for _, key := range []string{"auth.hardcover_quota.key.hash", "auth.hardcover_deferred_editions.1"} {
		if !isSecretSetting(key) || isWritableSecretSetting(key) {
			t.Fatalf("internal state exposed: %s", key)
		}
	}
	h.WithHardcoverQuota(nil)
	w = httptest.NewRecorder()
	h.HardcoverQuota(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code)
	}
}

func TestQuotaDeferredHydrationSiblingRecovery(t *testing.T) {
	for _, path := range []string{"book rebind", "series pinned", "recommendation"} {
		t.Run(path, func(t *testing.T) {
			h, author, books := prefetchFixture(t, newConcurrentEditionProvider(nil, 1))
			ctx := context.Background()
			book := &models.Book{ForeignID: "hc:123", Title: "Deferred", SortTitle: "deferred", AuthorID: author.ID, MetadataProvider: "hardcover", MediaType: models.MediaTypeEbook}
			if err := books.Create(ctx, book); err != nil {
				t.Fatal(err)
			}
			deferred := func(context.Context, string) ([]models.Edition, error) { return nil, metadata.ErrProviderDeferred }
			switch path {
			case "book rebind":
				b := &BookHandler{books: books, editions: h.editions, settings: h.settings, editionFetcher: deferred}
				b.hydrateHardcoverEditions(ctx, book, "hardcover")
			case "series pinned":
				s := &SeriesHandler{books: books, editions: h.editions, settings: h.settings, editionFetcher: deferred}
				s.hydrateHardcoverEditions(ctx, book, true)
			case "recommendation":
				r := (&RecommendationHandler{books: books, editions: h.editions, editionFetcher: deferred}).WithSettings(h.settings)
				r.hydrateHardcoverEditions(ctx, book)
			}
			pending, err := h.settings.GetDeferredHardcoverEditions(ctx, book.ID)
			if err != nil || pending == nil {
				t.Fatalf("lost deferred hydration: %+v %v", pending, err)
			}
			var work bookhydrate.DeferredEditions
			if err := json.Unmarshal([]byte(*pending), &work); err != nil {
				t.Fatal(err)
			}
			if work.ForeignID != "hc:123" || work.MediaTypePinned != (path == "series pinned") {
				t.Fatalf("wrong replay options: %+v", work)
			}
			calls := 0
			h.editionFetcher = func(_ context.Context, id string) ([]models.Edition, error) {
				calls++
				if id != "hc:123" {
					t.Fatalf("replayed wrong provider identity: %s", id)
				}
				return []models.Edition{{ForeignID: "hc-edition:456", Title: "Recovered", Format: "Audiobook", Language: "en"}}, nil
			}
			// Recreate the handler: only persisted work is available to this refresh.
			resumed := &AuthorHandler{books: books, editions: h.editions, settings: h.settings, editionFetcher: h.editionFetcher}
			resumed.retryDeferredEditions(ctx, book)
			if calls != 1 {
				t.Fatalf("recovery calls: %d", calls)
			}
			if pending, err = h.settings.GetDeferredHardcoverEditions(ctx, book.ID); err != nil || pending != nil {
				t.Fatalf("completed marker: %+v %v", pending, err)
			}
			stored, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantMedia := models.MediaTypeBoth
			if path == "series pinned" {
				wantMedia = models.MediaTypeEbook
			}
			if stored.ForeignID != "hc:123" || stored.MetadataProvider != "hardcover" || stored.MediaType != wantMedia || stored.Language != "en" {
				t.Fatalf("wrong recovered book: %+v", stored)
			}
			// Stale work must never attach the old provider's editions after a rebind.
			if err := h.settings.SetDeferredHardcoverEditions(ctx, book.ID, pendingJSON(t, work)); err != nil {
				t.Fatal(err)
			}
			book.ForeignID = "hc:999"
			resumed.retryDeferredEditions(ctx, book)
			if calls != 1 {
				t.Fatal("replayed stale provider target")
			}
		})
	}
}

func pendingJSON(t *testing.T, work bookhydrate.DeferredEditions) string {
	t.Helper()
	raw, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
