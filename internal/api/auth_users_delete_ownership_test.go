package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// These cover the ownership half of DELETE /auth/users/:id (#1899). Before the
// fix the handler ran a bare delete and handed the caller SQLite's own
// "FOREIGN KEY constraint failed" text as a 400, which is both unactionable and
// indistinguishable from the last-admin refusal.

// ownedUserFixture returns the handler, the repos, and a user who owns one
// author and one book.
func ownedUserFixture(t *testing.T) (*UserManagementHandler, *db.UserRepo, *db.AuthorRepo, int64, int64) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	users := db.NewUserRepo(database)
	authors := db.NewAuthorRepo(database)

	admin, err := users.Create(ctx, "root", "pw")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	victim, err := users.Create(ctx, "victim", "pw")
	if err != nil {
		t.Fatalf("create victim: %v", err)
	}
	a := &models.Author{ForeignID: "OL1A", Name: "Owned Author", SortName: "Author, Owned"}
	if err := authors.CreateForUser(ctx, a, victim.ID); err != nil {
		t.Fatalf("create author: %v", err)
	}
	if _, err := database.Exec(
		"INSERT INTO books (foreign_id, author_id, title, sort_title, owner_user_id) VALUES ('OL1W', ?, 'B', 'B', ?)",
		a.ID, victim.ID,
	); err != nil {
		t.Fatalf("seed book: %v", err)
	}
	return NewUserManagementHandler(users), users, authors, admin.ID, victim.ID
}

// TestUserMgmt_Delete_OwnedRowsConflict is the reported bug's replacement
// behaviour: instead of a 400 carrying a raw SQLite message, the admin gets a
// 409 naming what stands in the way.
func TestUserMgmt_Delete_OwnedRowsConflict(t *testing.T) {
	h, users, _, _, victimID := ownedUserFixture(t)

	rr := httptest.NewRecorder()
	h.Delete(rr, jsonReqWithID(http.MethodDelete, "/auth/users/2", "", victimID, nil))

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error  string           `json:"error"`
		Counts db.UserOwnedRows `json:"counts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Counts.Authors != 1 || body.Counts.Books != 1 {
		t.Errorf("counts = %+v, want 1 author and 1 book", body.Counts)
	}
	if body.Error == "" {
		t.Error("conflict body carries no message")
	}
	if u, _ := users.GetByID(context.Background(), victimID); u == nil {
		t.Error("user was deleted by the request that only asked what would happen")
	}
}

// TestUserMgmt_Delete_ReassignStrategy carries the delete out once the admin has
// chosen an inheritor.
func TestUserMgmt_Delete_ReassignStrategy(t *testing.T) {
	h, users, authors, adminID, victimID := ownedUserFixture(t)
	ctx := context.Background()

	rr := httptest.NewRecorder()
	h.Delete(rr, jsonReqWithID(http.MethodDelete,
		"/auth/users/2?strategy=reassign&reassignTo=1", "", victimID, nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rr.Code, rr.Body.String())
	}
	if u, _ := users.GetByID(ctx, victimID); u != nil {
		t.Error("user still present")
	}
	list, err := authors.ListByUser(ctx, adminID)
	if err != nil {
		t.Fatalf("list authors: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("inheritor should see 1 author; got %d", len(list))
	}
}

// TestUserMgmt_Delete_PurgeStrategy removes the library along with the user.
func TestUserMgmt_Delete_PurgeStrategy(t *testing.T) {
	h, users, authors, adminID, victimID := ownedUserFixture(t)
	ctx := context.Background()

	rr := httptest.NewRecorder()
	h.Delete(rr, jsonReqWithID(http.MethodDelete, "/auth/users/2?strategy=purge", "", victimID, nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rr.Code, rr.Body.String())
	}
	if u, _ := users.GetByID(ctx, victimID); u != nil {
		t.Error("user still present")
	}
	list, err := authors.ListByUser(ctx, adminID)
	if err != nil {
		t.Fatalf("list authors: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("purge should have removed the author; admin sees %d", len(list))
	}
}

// TestUserMgmt_Delete_BadStrategyArgs covers the caller getting the query wrong.
func TestUserMgmt_Delete_BadStrategyArgs(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"unknown strategy", "?strategy=evaporate", http.StatusBadRequest},
		{"unparseable inheritor", "?strategy=reassign&reassignTo=abc", http.StatusBadRequest},
		{"inheritor does not exist", "?strategy=reassign&reassignTo=9999", http.StatusBadRequest},
		{"inheritor is the deleted user", "?strategy=reassign&reassignTo=2", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, users, _, _, victimID := ownedUserFixture(t)
			rr := httptest.NewRecorder()
			h.Delete(rr, jsonReqWithID(http.MethodDelete, "/auth/users/2"+tc.query, "", victimID, nil))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rr.Code, tc.want, rr.Body.String())
			}
			if u, _ := users.GetByID(context.Background(), victimID); u == nil {
				t.Error("user deleted despite a rejected request")
			}
		})
	}
}

// TestUserMgmt_Delete_UnusedAccountNeedsNoStrategy keeps the easy path easy: an
// account that never touched the app still deletes in one call.
func TestUserMgmt_Delete_UnusedAccountNeedsNoStrategy(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	if _, err := users.Create(ctx, "root", "pw"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	spare, err := users.Create(ctx, "spare", "pw")
	if err != nil {
		t.Fatalf("create spare: %v", err)
	}

	rr := httptest.NewRecorder()
	h.Delete(rr, jsonReqWithID(http.MethodDelete, "/auth/users/2", "", spare.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rr.Code, rr.Body.String())
	}
	if u, _ := users.GetByID(ctx, spare.ID); u != nil {
		t.Error("user still present")
	}
}
