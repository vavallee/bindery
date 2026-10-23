package api

import (
	"context"
	"net/http"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/telemetry"
)

// SetupState reports how far an install has progressed through first-run
// setup, backing the onboarding checklist. Every field is derived
// server-side so the UI needs one round-trip instead of probing several
// list endpoints.
//
// The two halves deliberately use different sources:
//
//   - HasIndexer/HasClient are CURRENT state (rows that exist right now).
//     A user who deletes their only indexer has a broken pipeline again
//     and must be told, so a "you did this once" marker would be wrong.
//   - HasAuthor/HasGrab/HasImport are EVER-happened, read from the
//     setup-funnel markers. History is prunable and books get deleted;
//     "you have completed a download" stays true regardless.
type SetupState struct {
	HasIndexer bool `json:"hasIndexer"`
	HasClient  bool `json:"hasClient"`
	HasAuthor  bool `json:"hasAuthor"`
	HasGrab    bool `json:"hasGrab"`
	HasImport  bool `json:"hasImport"`
	// Complete is true once the pipeline is configured AND has produced an
	// import — the point at which the checklist has nothing left to say.
	Complete bool `json:"complete"`

	// Blocked is true when this install has imported through Bindery before
	// but cannot import right now, and has been that way for a couple of
	// minutes. See setup_blocked.go for exactly what is checked. Only admins
	// see it set. BlockedReason names the problem and the fix but never a
	// path. BlockedArea is one of the blockedArea* constants.
	Blocked       bool   `json:"blocked"`
	BlockedReason string `json:"blockedReason,omitempty"`
	BlockedArea   string `json:"blockedArea,omitempty"`
}

// SetupStateHandler serves GET /api/v1/system/setup-state.
type SetupStateHandler struct {
	indexers *db.IndexerRepo
	clients  *db.DownloadClientRepo
	authors  *db.AuthorRepo
	settings *db.SettingsRepo

	blocked blockedChecker
}

func NewSetupStateHandler(indexers *db.IndexerRepo, clients *db.DownloadClientRepo, authors *db.AuthorRepo, settings *db.SettingsRepo) *SetupStateHandler {
	return &SetupStateHandler{indexers: indexers, clients: clients, authors: authors, settings: settings}
}

// settingPresent reports whether a settings key holds a non-empty value.
// Read errors report false: the checklist showing an outstanding step it
// has already completed is a far smaller harm than failing the request.
func settingPresent(ctx context.Context, settings *db.SettingsRepo, key string) bool {
	if settings == nil {
		return false
	}
	v, err := settings.Get(ctx, key)
	return err == nil && v != nil && v.Value != ""
}

func (h *SetupStateHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	state := SetupState{
		HasAuthor: settingPresent(ctx, h.settings, telemetry.SettingFirstAuthorAt),
		HasGrab:   settingPresent(ctx, h.settings, telemetry.SettingFirstGrabAt),
		HasImport: settingPresent(ctx, h.settings, telemetry.SettingFirstImportAt),
	}

	// Enabled-only, matching what the pipeline actually uses: a disabled
	// indexer is never searched and a disabled client never receives grabs,
	// so neither counts as a completed setup step.
	if list, err := h.indexers.List(ctx); err == nil {
		for _, ix := range list {
			if ix.Enabled {
				state.HasIndexer = true
				break
			}
		}
	}
	if list, err := h.clients.List(ctx); err == nil {
		for _, c := range list {
			if c.Enabled {
				state.HasClient = true
				break
			}
		}
	}

	// The funnel marker for "first author" only exists on installs that
	// added one after the marker shipped. Fall back to current library
	// contents so an older install doesn't see a permanently unchecked box.
	if !state.HasAuthor && h.authors != nil {
		// limit 1: only the total count matters, not the rows.
		if _, total, err := h.authors.ListPage(ctx, 0, 1, 0); err == nil && total > 0 {
			state.HasAuthor = true
		}
	}

	state.Complete = state.HasIndexer && state.HasClient && state.HasAuthor && state.HasImport

	// Blocked is admin-only: it describes folders and client health, which
	// this endpoint's non-admin callers are not otherwise shown. Auth
	// disabled and API key callers are stamped admin by the middleware.
	if auth.UserRoleFromContext(ctx) == "admin" {
		h.blocked.apply(ctx, h.clients, &state)
	}
	writeJSON(w, http.StatusOK, state)
}
