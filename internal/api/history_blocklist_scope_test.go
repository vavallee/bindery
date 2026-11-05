package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestHistoryBlocklist_AnotherUsersEntryDoesNotCountAsBlocked is #3563 for
// the History action. alice already blocklisted a release for her book. When
// bob blocklists the same release from his own book's history, the old global
// check answered "already blocklisted": it told bob about alice's entry and
// wrote nothing for his book, which search now matches per book. bob must get
// his own book scoped entry, and alice's entry must stay as it was.
func TestHistoryBlocklist_AnotherUsersEntryDoesNotCountAsBlocked(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	h, database, _, uBob, eA, eB := seedTwoUserHistory(t)
	ctx := context.Background()
	for _, id := range []int64{eA, eB} {
		if _, err := database.Exec(`UPDATE history SET data = '{"guid":"shared-3563"}' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	hist := db.NewHistoryRepo(database)
	evA, err := hist.GetByID(ctx, eA)
	if err != nil || evA == nil || evA.BookID == nil {
		t.Fatalf("alice's event: %+v, %v", evA, err)
	}
	evB, err := hist.GetByID(ctx, eB)
	if err != nil || evB == nil || evB.BookID == nil {
		t.Fatalf("bob's event: %+v, %v", evB, err)
	}
	blocklist := db.NewBlocklistRepo(database)
	if err := blocklist.Create(ctx, &models.BlocklistEntry{
		BookID: evA.BookID, GUID: "shared-3563", Title: "alice grab", Reason: models.HistoryEventGrabbed,
		Scope: models.BlocklistScopeBook,
	}); err != nil {
		t.Fatal(err)
	}

	id := strconv.FormatInt(eB, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+id+"/blocklist", nil), "id", id)
	rctx := auth.WithUserRole(auth.WithUserID(req.Context(), uBob), "user")
	rec := httptest.NewRecorder()
	h.Blocklist(rec, req.WithContext(rctx))
	if rec.Code != http.StatusCreated {
		t.Fatalf("#3563: bob blocklisting his own release got %d %s, want 201 and an entry for his book", rec.Code, rec.Body.String())
	}

	entries, err := blocklist.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var forBob, forAlice int
	for _, e := range entries {
		switch {
		case e.BookID != nil && *e.BookID == *evB.BookID:
			forBob++
			if e.Scope != models.BlocklistScopeBook {
				t.Errorf("bob's entry scope = %q, want book", e.Scope)
			}
		case e.BookID != nil && *e.BookID == *evA.BookID:
			forAlice++
		}
	}
	if forBob != 1 || forAlice != 1 {
		t.Errorf("entries = %+v, want one for each book", entries)
	}

	// A second press for bob's book is the idempotent answer.
	rec = httptest.NewRecorder()
	h.Blocklist(rec, req.WithContext(rctx))
	if rec.Code != http.StatusOK {
		t.Errorf("repeat blocklist got %d %s, want 200 already blocklisted", rec.Code, rec.Body.String())
	}
}

// TestHistoryBlocklist_ScopeFollowsTheEventType: blocklisting a failed or
// stalled download from History says the release is broken, so the entry
// blocks it for every book, as on main. Blocklisting a grab or an import is a
// judgement about the event's own book and blocks it for that book only. A
// book entry already on file for the event's book does not stop the release
// wide entry.
func TestHistoryBlocklist_ScopeFollowsTheEventType(t *testing.T) {
	for _, tc := range []struct {
		eventType string
		want      string
	}{
		{models.HistoryEventGrabbed, models.BlocklistScopeBook},
		{models.HistoryEventBookImported, models.BlocklistScopeBook},
		{models.HistoryEventDownloadFailed, models.BlocklistScopeRelease},
		{models.HistoryEventDownloadStalled, models.BlocklistScopeRelease},
	} {
		t.Run(tc.eventType, func(t *testing.T) {
			t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
			h, database, _, uBob, _, eB := seedTwoUserHistory(t)
			ctx := context.Background()
			if _, err := database.Exec(`UPDATE history SET event_type = ?, data = '{"guid":"scope-3563"}' WHERE id = ?`, tc.eventType, eB); err != nil {
				t.Fatal(err)
			}
			ev, err := db.NewHistoryRepo(database).GetByID(ctx, eB)
			if err != nil || ev == nil || ev.BookID == nil {
				t.Fatalf("bob's event: %+v, %v", ev, err)
			}
			blocklist := db.NewBlocklistRepo(database)
			if tc.want == models.BlocklistScopeRelease {
				// The book already has a book entry for the release.
				if err := blocklist.Create(ctx, &models.BlocklistEntry{
					BookID: ev.BookID, GUID: "scope-3563", Title: "t", Reason: "format", Scope: models.BlocklistScopeBook,
				}); err != nil {
					t.Fatal(err)
				}
			}

			id := strconv.FormatInt(eB, 10)
			req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+id+"/blocklist", nil), "id", id)
			rctx := auth.WithUserRole(auth.WithUserID(req.Context(), uBob), "user")
			rec := httptest.NewRecorder()
			h.Blocklist(rec, req.WithContext(rctx))
			if rec.Code != http.StatusCreated {
				t.Fatalf("got %d %s, want 201", rec.Code, rec.Body.String())
			}
			entries, err := blocklist.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, e := range entries {
				if e.Reason == tc.eventType {
					found = true
					if e.Scope != tc.want || e.BookID == nil || *e.BookID != *ev.BookID {
						t.Errorf("entry = %+v, want scope %q on bob's book", e, tc.want)
					}
				}
			}
			if !found {
				t.Fatalf("no entry written: %+v", entries)
			}
		})
	}
}
