package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/models"
)

// Applying a conflict value is a user choice with no provider fetch, so it
// must not count as a profile refresh: stamping freshness there would make the
// staggered refresh skip the author for a day (#2595).
func TestABSConflictHandler_ResolveAuthorConflictDoesNotStampProfileRefresh(t *testing.T) {
	t.Parallel()

	_, h, conflicts, authors, _ := absConflictFixture(t)
	author := &models.Author{ForeignID: "OL-ANDY2", Name: "Andy Weir", SortName: "Weir, Andy", Description: "Old", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(context.Background(), author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	conflict := &models.ABSMetadataConflict{
		SourceID: "default", LibraryID: "lib-books", ItemID: "li-author",
		EntityType: "author", LocalID: author.ID, FieldName: "description",
		ABSValue: "ABS bio", UpstreamValue: "Upstream bio",
		AppliedSource: abs.MetadataSourceUpstream, ResolutionStatus: "unresolved",
	}
	if err := conflicts.Upsert(context.Background(), conflict); err != nil {
		t.Fatalf("Upsert conflict: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abs/conflicts/1/resolve", bytes.NewBufferString(`{"source":"abs"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	h.Resolve(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Resolve status = %d body=%s", rec.Code, rec.Body.String())
	}

	got, err := authors.GetByID(context.Background(), author.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Description != "ABS bio" {
		t.Fatalf("description = %q, want the ABS value applied", got.Description)
	}
	if got.LastMetadataRefreshAt != nil {
		t.Errorf("applying a conflict stamped profile freshness %v, want nil", got.LastMetadataRefreshAt)
	}
}
