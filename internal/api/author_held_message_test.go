package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With tenancy on, an author another account holds is invisible to the caller
// but still owns the instance wide unique foreign id, so the add collides. The
// 409 must say why in plain words and must not name the other account (#3555).
func assertAuthorHeldAnswer(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	msg, _ := body["error"].(string)
	for _, want := range []string{"another account's library", "requester role"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	raw := strings.ToLower(rec.Body.String())
	for _, leak := range []string{"bob", "canonicalauthor", "ownerid", "owner_user_id"} {
		if strings.Contains(raw, leak) {
			t.Errorf("body leaks %q: %s", leak, rec.Body.String())
		}
	}
	if _, ok := body["canonicalAuthorId"]; ok {
		t.Errorf("body names the other account's author: %s", rec.Body.String())
	}
}

func TestCreateAuthor_HeldByAnotherAccountUnderTenancySaysSo(t *testing.T) {
	f := newTenancyFixture(t, true, &stubMetaProvider{})
	f.seedBobsHerbert(t, "OL-HERBERT")

	_, err := f.h.createAuthorCore(f.userCtx(f.alice), createAuthorParams{
		ForeignID: "OL-HERBERT", Name: "Frank Herbert", SkipCatalogueSync: true,
	})
	if err == nil {
		t.Fatal("alice added an author bob holds")
	}
	rec := httptest.NewRecorder()
	f.h.writeCreateAuthorError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", nil), err)
	assertAuthorHeldAnswer(t, rec)
}

func TestAddBook_AuthorHeldByAnotherAccountUnderTenancySaysSo(t *testing.T) {
	f := newTenancyFixture(t, true, duneAudiobookProvider())
	f.seedBobsHerbert(t, "OL-SHARED-A")

	_, err := f.h.addBookCore(f.userCtx(f.alice), duneAddParams)
	if err == nil {
		t.Fatal("alice filed a book under an author bob holds")
	}
	rec := httptest.NewRecorder()
	f.h.writeAddBookError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/book", nil), err)
	assertAuthorHeldAnswer(t, rec)
}

// seedOrphanIdentifier plants an author_identifiers row whose author is gone.
// The unique foreign id is taken, yet no author row is visible to any caller,
// so the old text, not "another account's library", is the honest answer.
func seedOrphanIdentifier(t *testing.T, f tenancyFixture, foreignID string) {
	t.Helper()
	ctx := context.Background()
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO author_identifiers (author_id, provider, foreign_id) VALUES (999999, 'openlibrary', ?)`, foreignID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAuthor_UniqueFailureKeepsOldTextUnlessNonAdminUnderTenancy(t *testing.T) {
	cases := []struct {
		name    string
		tenancy bool
		admin   bool
		want    string
	}{
		{"tenancy off", false, false, "author already exists"},
		{"admin under tenancy", true, true, "author already exists"},
		{"non admin under tenancy", true, false, "another account's library"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTenancyFixture(t, tc.tenancy, &stubMetaProvider{})
			seedOrphanIdentifier(t, f, "OL-ORPHAN")

			_, err := f.h.createAuthorCore(f.callerCtx(tc.admin), createAuthorParams{
				ForeignID: "OL-ORPHAN", Name: "Orphan Author", SkipCatalogueSync: true,
			})
			if err == nil {
				t.Fatal("expected a conflict")
			}
			rec := httptest.NewRecorder()
			f.h.writeCreateAuthorError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", nil), err)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
			}
			var body map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			msg, _ := body["error"].(string)
			if tc.want == "author already exists" && msg != tc.want {
				t.Errorf("error = %q, want %q", msg, tc.want)
			}
			if tc.want != "author already exists" && !strings.Contains(msg, tc.want) {
				t.Errorf("error = %q, want it to contain %q", msg, tc.want)
			}
		})
	}
}

func TestAddBook_UniqueFailureKeepsOldTextUnlessNonAdminUnderTenancy(t *testing.T) {
	cases := []struct {
		name    string
		tenancy bool
		admin   bool
		held    bool
	}{
		{"tenancy off", false, false, false},
		{"admin under tenancy", true, true, false},
		{"non admin under tenancy", true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTenancyFixture(t, tc.tenancy, duneAudiobookProvider())
			seedOrphanIdentifier(t, f, "OL-SHARED-A")

			_, err := f.h.addBookCore(f.callerCtx(tc.admin), duneAddParams)
			if err == nil {
				t.Fatal("expected a conflict")
			}
			rec := httptest.NewRecorder()
			f.h.writeAddBookError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/book", nil), err)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
			}
			isHeld := strings.Contains(rec.Body.String(), "another account's library")
			if isHeld != tc.held {
				t.Errorf("body %s: held message = %v, want %v", rec.Body.String(), isHeld, tc.held)
			}
			if !tc.held && !strings.Contains(rec.Body.String(), "author already exists") {
				t.Errorf("body %s: want the old text", rec.Body.String())
			}
		})
	}
}
