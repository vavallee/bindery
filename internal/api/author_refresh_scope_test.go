package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func waitRefreshIdle(t *testing.T, h *AuthorRefreshHandler) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.Running() {
		t.Fatal("refresh-all did not finish")
	}
}

func refreshStatusAs(t *testing.T, h *AuthorRefreshHandler, ctx context.Context) authorRefreshStatus {
	t.Helper()
	rec := httptest.NewRecorder()
	h.RefreshAllStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/authors/refresh-all/status", nil).WithContext(ctx))
	var st authorRefreshStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status body %q: %v", rec.Body.String(), err)
	}
	return st
}

// Under tenancy a user's Refresh all refreshes only the authors they own: not
// another user's, and not shared ones. Its status reports that run only, and
// the instance status an admin reads is untouched by it.
func TestAuthorRefreshAll_UserRefreshesOnlyOwnAuthors(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedSharedAuthor(t)
	settings := db.NewSettingsRepo(f.database)
	var mu sync.Mutex
	refreshed := map[int64]bool{}
	h := NewAuthorRefreshHandler(f.authors, func(a *models.Author) {
		mu.Lock()
		refreshed[a.ID] = true
		mu.Unlock()
	}).WithSettings(settings)

	bob := withAuthCtx(context.Background(), f.u2, "user")
	rec := httptest.NewRecorder()
	h.RefreshAll(rec, httptest.NewRequest(http.MethodPost, "/api/v1/authors/refresh-all", nil).WithContext(bob))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("refresh-all as bob: %d %s", rec.Code, rec.Body.String())
	}
	waitRefreshIdle(t, h)

	mu.Lock()
	if refreshed[f.a1.ID] {
		t.Errorf("bob's refresh-all refreshed alice's author %d", f.a1.ID)
	}
	if refreshed[f.shared.ID] {
		t.Errorf("bob's refresh-all refreshed the shared author %d", f.shared.ID)
	}
	if !refreshed[f.a2.ID] {
		t.Errorf("bob's refresh-all skipped his own author %d", f.a2.ID)
	}
	mu.Unlock()

	if st := refreshStatusAs(t, h, bob); st.Status != "completed" || st.Total != 1 || st.Done != 1 {
		t.Errorf("bob's status = %+v, want his own completed run of 1 author", st)
	}
	if st := refreshStatusAs(t, h, withAuthCtx(context.Background(), f.u1, "user")); st.Status != "idle" {
		t.Errorf("alice reads bob's run: %+v, want idle", st)
	}
	if st := refreshStatusAs(t, h, withAuthCtx(context.Background(), 99, "admin")); st.Status != "idle" {
		t.Errorf("bob's run overwrote the instance status: %+v", st)
	}
}

// An admin's Refresh all, and anyone's with tenancy off, still covers every
// author including shared ones.
func TestAuthorRefreshAll_AdminAndTenancyOffCoverEveryAuthor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
		role    string
	}{
		{"admin under tenancy", true, "admin"},
		{"user with tenancy off", false, "user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			f := seedSharedAuthor(t)
			var mu sync.Mutex
			refreshed := map[int64]bool{}
			h := NewAuthorRefreshHandler(f.authors, func(a *models.Author) {
				mu.Lock()
				refreshed[a.ID] = true
				mu.Unlock()
			}).WithSettings(db.NewSettingsRepo(f.database))
			ctx := withAuthCtx(context.Background(), f.u2, tc.role)
			rec := httptest.NewRecorder()
			h.RefreshAll(rec, httptest.NewRequest(http.MethodPost, "/api/v1/authors/refresh-all", nil).WithContext(ctx))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("refresh-all: %d %s", rec.Code, rec.Body.String())
			}
			waitRefreshIdle(t, h)
			mu.Lock()
			defer mu.Unlock()
			for _, id := range []int64{f.a1.ID, f.a2.ID, f.shared.ID} {
				if !refreshed[id] {
					t.Errorf("author %d not refreshed", id)
				}
			}
			if st := refreshStatusAs(t, h, ctx); st.Total != 3 {
				t.Errorf("status total = %d, want 3", st.Total)
			}
		})
	}
}

type sharedAuthorFixture struct {
	authzAuthorFixture
	shared *models.Author
}

// seedSharedAuthor is seedTwoUserAuthors plus one unowned (shared) author.
func seedSharedAuthor(t *testing.T) sharedAuthorFixture {
	t.Helper()
	f := seedTwoUserAuthors(t)
	s := &models.Author{ForeignID: "OL-SHARED", Name: "Shared Author", SortName: "Author, Shared", MetadataProvider: "openlibrary"}
	if err := f.authors.Create(context.Background(), s); err != nil {
		t.Fatalf("create shared author: %v", err)
	}
	if _, err := f.database.Exec("UPDATE authors SET owner_user_id=NULL WHERE id=?", s.ID); err != nil {
		t.Fatal(err)
	}
	return sharedAuthorFixture{authzAuthorFixture: f, shared: s}
}
