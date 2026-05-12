package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func authorAliasRequest(method, path string, authorID, aliasID int64) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(authorID, 10))
	rctx.URLParams.Add("aliasID", strconv.FormatInt(aliasID, 10))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestAuthorAliasHandler_Delete(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (*AuthorAliasHandler, *db.AuthorAliasRepo, *models.Author, *models.Author, *models.AuthorAlias) {
		t.Helper()
		database, err := db.OpenMemory()
		if err != nil {
			t.Fatalf("open memory db: %v", err)
		}
		t.Cleanup(func() { database.Close() })

		authorRepo := db.NewAuthorRepo(database)
		aliasRepo := db.NewAuthorAliasRepo(database)
		ctx := context.Background()
		target := &models.Author{ForeignID: "OL-TARGET", Name: "Target Author", SortName: "Author, Target", Monitored: true}
		if err := authorRepo.Create(ctx, target); err != nil {
			t.Fatalf("Create target: %v", err)
		}
		other := &models.Author{ForeignID: "OL-OTHER", Name: "Other Author", SortName: "Author, Other", Monitored: true}
		if err := authorRepo.Create(ctx, other); err != nil {
			t.Fatalf("Create other: %v", err)
		}
		alias := &models.AuthorAlias{AuthorID: target.ID, Name: "Pen Name"}
		if err := aliasRepo.Create(ctx, alias); err != nil {
			t.Fatalf("Create alias: %v", err)
		}
		return NewAuthorAliasHandler(authorRepo, aliasRepo), aliasRepo, target, other, alias
	}

	t.Run("success", func(t *testing.T) {
		h, aliasRepo, target, _, alias := setup(t)
		rec := httptest.NewRecorder()
		h.Delete(rec, authorAliasRequest(http.MethodDelete, "/api/v1/author/1/aliases/1", target.ID, alias.ID))

		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d body = %s, want 204", rec.Code, rec.Body.String())
		}
		aliases, err := aliasRepo.ListByAuthor(context.Background(), target.ID)
		if err != nil {
			t.Fatalf("ListByAuthor: %v", err)
		}
		if len(aliases) != 0 {
			t.Fatalf("aliases = %+v, want deleted", aliases)
		}
	})

	t.Run("missing author", func(t *testing.T) {
		h, _, _, _, alias := setup(t)
		rec := httptest.NewRecorder()
		h.Delete(rec, authorAliasRequest(http.MethodDelete, "/api/v1/author/999/aliases/1", 999, alias.ID))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d body = %s, want 404", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing alias", func(t *testing.T) {
		h, _, target, _, _ := setup(t)
		rec := httptest.NewRecorder()
		h.Delete(rec, authorAliasRequest(http.MethodDelete, "/api/v1/author/1/aliases/999", target.ID, 999))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d body = %s, want 404", rec.Code, rec.Body.String())
		}
	})

	t.Run("wrong author", func(t *testing.T) {
		h, _, _, other, alias := setup(t)
		rec := httptest.NewRecorder()
		h.Delete(rec, authorAliasRequest(http.MethodDelete, "/api/v1/author/2/aliases/1", other.ID, alias.ID))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d body = %s, want 404", rec.Code, rec.Body.String())
		}
	})
}
