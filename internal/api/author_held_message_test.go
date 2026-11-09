package api

import (
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
