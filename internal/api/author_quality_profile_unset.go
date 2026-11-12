package api

import (
	"encoding/json"
	"net/http"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// AuthorQualityProfileUnsetHandler serves the opt in fix for authors that have
// no quality profile (#3831). Such an author has no format filter at all, so a
// pdf only download imports under what the user thinks is an epub only setup.
// Until the default quality profile setting existed, every author added from
// the UI ended up that way.
//
// It never runs on its own and no migration does this: the Settings page shows
// the count and the admin picks a profile and confirms.
//
// Mounted behind auth.RequireAdmin (registerAuthorQualityProfileUnsetRoutes in
// cmd/bindery). The only screen that offers it is the admin only Quality tab,
// and a bulk write across many authors is the kind of new endpoint this
// project keeps admin only. A non admin can still set a profile on each of
// their own authors from Edit Author.
type AuthorQualityProfileUnsetHandler struct {
	authors  *db.AuthorRepo
	profiles *db.QualityProfileRepo
}

func NewAuthorQualityProfileUnsetHandler(authors *db.AuthorRepo, profiles *db.QualityProfileRepo) *AuthorQualityProfileUnsetHandler {
	return &AuthorQualityProfileUnsetHandler{authors: authors, profiles: profiles}
}

// qualityProfileUnsetScope is the set of authors the count and the apply act
// on: the caller's own authors, plus the unowned shared ones when the caller
// could already edit those one at a time. Another account's authors are never
// included, admin or not, since authors are per account (#3555) and this is a
// bulk change nobody on that account asked for.
//
// A caller with no user identity (API key, local only, auth disabled) is
// admin equivalent everywhere else and owns nothing, so it covers every
// author, the same unscoped view auth.ListScopeUserID gives it.
func qualityProfileUnsetScope(r *http.Request) db.AuthorOwnerScope {
	ctx := r.Context()
	uid := auth.UserIDFromContext(ctx)
	if uid == 0 {
		return db.AuthorOwnerScope{AllOwners: true}
	}
	return db.AuthorOwnerScope{
		OwnerUserID:    uid,
		IncludeUnowned: auth.CheckWriteOwnership(ctx, 0),
	}
}

// Count answers how many authors in scope have no quality profile.
// GET /api/v1/authors/without-quality-profile
func (h *AuthorQualityProfileUnsetHandler) Count(w http.ResponseWriter, r *http.Request) {
	n, err := h.authors.CountWithoutQualityProfile(r.Context(), qualityProfileUnsetScope(r))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// Apply gives the chosen quality profile to exactly the authors in scope that
// have none, and answers how many changed. Authors that already have a
// profile are left alone.
// POST /api/v1/authors/without-quality-profile {"qualityProfileId": N}
func (h *AuthorQualityProfileUnsetHandler) Apply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QualityProfileID *int64 `json:"qualityProfileId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.QualityProfileID == nil || *req.QualityProfileID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "qualityProfileId is required"})
		return
	}
	profile, err := h.profiles.GetByID(r.Context(), *req.QualityProfileID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if profile == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "quality profile not found"})
		return
	}
	n, err := h.authors.ApplyQualityProfileToUnset(r.Context(), qualityProfileUnsetScope(r), profile.ID)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"updated": n})
}
