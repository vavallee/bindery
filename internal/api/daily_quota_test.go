package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

func TestDailyQuotaAPIClientsShareHold(t *testing.T) {
	h, settings, ctx := settingsFixture(t)
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
	token := "example-only"
	if err := settings.Set(ctx, SettingHardcoverAPIToken, token); err != nil {
		t.Fatal(err)
	}
	// Example-only install secret so the test can compute the fingerprint.
	secret := strings.Repeat("ab", 32)
	if err := settings.Set(ctx, "auth.hardcover_daily_hold_secret", secret); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(secret)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	if err := settings.Set(ctx, "auth.hardcover_daily_holds", fmt.Sprintf(`{"%x":%q}`, mac.Sum(nil), until)); err != nil {
		t.Fatal(err)
	}
	hold := hardcover.NewDailyQuota(settings)
	h.WithDailyQuota(hold)
	rec := httptest.NewRecorder()
	h.TestHardcover(rec, httptest.NewRequest(http.MethodPost, "/api/v1/hardcover/test", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), until) || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("test result: %d %s", rec.Code, rec.Body.String())
	}
	lists := NewImportListHandler(nil, settings, nil, nil).WithDailyQuota(hold)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/importlist/hardcover", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	lists.HardcoverLists(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), until) {
		t.Fatalf("list result: %d %s", rec.Code, rec.Body.String())
	}
}

type pausedCatalogueProvider struct {
	metadata.Provider
	daily error
}

func (p *pausedCatalogueProvider) CheckQuota(context.Context) error { return p.daily }

func TestDailyQuotaCatalogueStopsBeforeProviderCalls(t *testing.T) {
	daily := &metadata.DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
	// Any metadata call beyond the quota check would panic on the embedded nil provider.
	f := newRelinkUpstreamFixture(t, &pausedCatalogueProvider{daily: daily})
	author := f.createAuthor(t, &models.Author{Name: "Example", ForeignID: "hc:123", Monitored: true})
	added, err := f.handler.runCatalogueSync(f.ctx, author, catalogueSyncOptions{})
	if added != 0 || !errors.Is(err, daily) {
		t.Fatalf("added=%d err=%v", added, err)
	}
	// The fire-and-forget entry point must also stop safely.
	f.handler.fetchAuthorBooks(f.ctx, author, catalogueSyncOptions{})
}

func TestDailyQuotaHoldHiddenFromAdminSettings(t *testing.T) {
	h, settings, ctx := settingsFixture(t)
	key := "auth.hardcover_daily_holds"
	if err := settings.Set(ctx, key, `{"example-fingerprint":"2026-09-19T01:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, adminReq(httptest.NewRequest(http.MethodGet, "/api/v1/setting", nil)))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), key) || strings.Contains(rec.Body.String(), "example-fingerprint") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.Get(rec, adminReq(withKey(httptest.NewRequest(http.MethodGet, "/api/v1/setting/"+key, nil), key)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
}

// holdAfterProvider admits the first n quota checks, then reports a hold.
type holdAfterProvider struct {
	*stubMetaProvider
	admit int
	daily error
}

func (p *holdAfterProvider) CheckQuota(context.Context) error {
	if p.admit == 0 {
		return p.daily
	}
	p.admit--
	return nil
}

func TestDailyQuotaCatalogueHoldMidRunFinishesCreatedBooks(t *testing.T) {
	daily := &metadata.DailyQuotaError{ResetAt: time.Now().Add(time.Hour)}
	// One check at the start of the run, one for the first work.
	provider := &holdAfterProvider{stubMetaProvider: &stubMetaProvider{works: missingDateTestWorks()[:2]}, admit: 2, daily: daily}
	f := newRelinkUpstreamFixture(t, provider)
	author := f.createAuthor(t, &models.Author{Name: "Example", ForeignID: "OL930A", MetadataProvider: "openlibrary"})
	added, err := f.handler.runCatalogueSync(f.ctx, author, catalogueSyncOptions{})
	if added != 1 || !errors.Is(err, daily) {
		t.Fatalf("added=%d err=%v", added, err)
	}
	books, err := f.books.ListByAuthor(f.ctx, author.ID)
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%d err=%v", len(books), err)
	}
	// The created book still went through the post-create pass.
	if summary := f.handler.syncSummaries.get(author.ID); summary == nil || summary.Added != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}
