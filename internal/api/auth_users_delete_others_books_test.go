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

// TestUserMgmt_Delete_ConflictCountsOtherAccountsBooks checks the 409 body
// names the books and files purge would take from other accounts (#3559 part 1).
func TestUserMgmt_Delete_ConflictCountsOtherAccountsBooks(t *testing.T) {
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
	seed := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (10, 'OL1W', ?, 'Mine', 'Mine', ?)", []any{a.ID, victim.ID}},
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (11, 'OL2W', ?, 'Theirs', 'Theirs', ?)", []any{a.ID, admin.ID}},
		{"INSERT INTO book_files (book_id, format, path) VALUES (11, 'ebook', '/lib/t.epub')", nil},
	}
	for _, s := range seed {
		if _, err := database.Exec(s.query, s.args...); err != nil {
			t.Fatalf("seed %q: %v", s.query, err)
		}
	}

	h := NewUserManagementHandler(users)
	rr := httptest.NewRecorder()
	h.Delete(rr, jsonReqWithID(http.MethodDelete, "/auth/users/2", "", victim.ID, nil))

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rr.Code, rr.Body.String())
	}
	var raw struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if raw.Counts["books"] != 1 {
		t.Errorf("books = %d, want 1 (own only)", raw.Counts["books"])
	}
	if raw.Counts["othersBooks"] != 1 {
		t.Errorf("othersBooks = %d, want 1; counts %v", raw.Counts["othersBooks"], raw.Counts)
	}
	if raw.Counts["othersBookFiles"] != 1 {
		t.Errorf("othersBookFiles = %d, want 1; counts %v", raw.Counts["othersBookFiles"], raw.Counts)
	}
}
