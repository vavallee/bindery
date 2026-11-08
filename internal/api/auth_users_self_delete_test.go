package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// selfDeleteFixture seeds two admins so the last admin guard is out of the
// picture: what is being tested is the caller deleting their own account.
// "me" owns an author, so the purge path has something to destroy.
func selfDeleteFixture(t *testing.T) (*UserManagementHandler, *db.UserRepo, *db.AuthorRepo, int64, int64) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	users := db.NewUserRepo(database)
	authors := db.NewAuthorRepo(database)

	me, err := users.Create(ctx, "me", "h")
	if err != nil {
		t.Fatalf("create me: %v", err)
	}
	other, err := users.Create(ctx, "other", "h")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	for _, id := range []int64{me.ID, other.ID} {
		if err := users.SetRole(ctx, id, auth.RoleAdmin); err != nil {
			t.Fatalf("promote %d: %v", id, err)
		}
	}
	a := &models.Author{ForeignID: "OL1A", Name: "Mine", SortName: "Mine"}
	if err := authors.CreateForUser(ctx, a, me.ID); err != nil {
		t.Fatalf("create author: %v", err)
	}
	return NewUserManagementHandler(users), users, authors, me.ID, a.ID
}

// sessionCtx is what the auth middleware hands a signed in admin's request.
func sessionCtx(userID int64) context.Context {
	return auth.WithUserRole(auth.WithUserID(context.Background(), userID), auth.RoleAdmin)
}

// An admin's own session cannot delete its own account, under no strategy and
// under purge, and the refusal comes before any library data is touched.
func TestUserMgmt_Delete_RefusesOwnAccount(t *testing.T) {
	for _, query := range []string{"", "?strategy=purge", "?strategy=reassign"} {
		t.Run("query="+query, func(t *testing.T) {
			h, users, authors, meID, authorID := selfDeleteFixture(t)

			req := jsonReqWithID(http.MethodDelete, "/api/v1/auth/users/x"+query, "", meID, sessionCtx(meID))
			rec := httptest.NewRecorder()
			h.Delete(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("self delete status=%d, want 409; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "your own account") {
				t.Errorf("self delete body=%s, want it to say why", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `"counts"`) {
				t.Errorf("self delete body=%s, want no owned data counts (that would open the strategy dialog)", rec.Body.String())
			}
			if u, _ := users.GetByID(context.Background(), meID); u == nil {
				t.Fatal("own account was deleted")
			}
			if a, _ := authors.GetByID(context.Background(), authorID); a == nil {
				t.Fatal("own library was purged although the delete was refused")
			}
		})
	}
}

// The API key and the auth mode act as the install, not as a person. The
// middleware stamps the operator's id on those requests, so that id says
// nothing about who is asking and must not block deleting the operator while
// another admin remains.
func TestUserMgmt_Delete_InstallCallerMayDeleteOperator(t *testing.T) {
	cases := map[string]func(context.Context) context.Context{
		"api key":      auth.WithAPIKeyAuth,
		"auth by mode": auth.WithAdminByAuthMode,
	}
	for name, mark := range cases {
		t.Run(name, func(t *testing.T) {
			h, users, _, meID, _ := selfDeleteFixture(t)

			req := jsonReqWithID(http.MethodDelete, "/api/v1/auth/users/x?strategy=purge", "", meID, mark(sessionCtx(meID)))
			rec := httptest.NewRecorder()
			h.Delete(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("install delete of operator status=%d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if u, _ := users.GetByID(context.Background(), meID); u != nil {
				t.Fatal("operator account still present")
			}
		})
	}
}

// Deleting someone else from your own session still works.
func TestUserMgmt_Delete_OtherAdminFromSession(t *testing.T) {
	h, users, _, meID, _ := selfDeleteFixture(t)
	other, _ := users.GetByUsername(context.Background(), "other")

	req := jsonReqWithID(http.MethodDelete, "/api/v1/auth/users/x", "", other.ID, sessionCtx(meID))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete other admin status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}
