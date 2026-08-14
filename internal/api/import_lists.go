package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/hardcoverlistsyncer"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// HardcoverListSyncer is the narrow surface ImportListHandler needs for the
// manual "Sync now" affordance: launch the sync in the background, and report
// what the current or most recent run did. Implemented by
// *hardcoverlistsyncer.ListSyncer.
type HardcoverListSyncer interface {
	StartOne(ctx context.Context, id int64) error
	Progress() hardcoverlistsyncer.SyncProgress
}

type hardcoverUserListClient interface {
	GetUserLists(ctx context.Context) ([]hardcover.HCList, error)
	GetUsername(ctx context.Context) (string, error)
}

// userLookup is the narrow surface the handler needs to validate that a list's
// owner_user_id points at a real user before persisting it. Satisfied by
// *db.UserRepo.
type userLookup interface {
	GetByID(ctx context.Context, id int64) (*db.User, error)
}

type ImportListHandler struct {
	repo         *db.ImportListRepo
	settings     *db.SettingsRepo
	hcSync       HardcoverListSyncer
	users        userLookup
	hcListClient func(token string) hardcoverUserListClient
}

func NewImportListHandler(repo *db.ImportListRepo, settings *db.SettingsRepo, hcSync HardcoverListSyncer, users userLookup) *ImportListHandler {
	return &ImportListHandler{
		repo:     repo,
		settings: settings,
		hcSync:   hcSync,
		users:    users,
		hcListClient: func(token string) hardcoverUserListClient {
			return hardcover.NewAuthenticated(token)
		},
	}
}

// errInvalidOwner marks an owner_user_id the client supplied that cannot be
// accepted (non-positive, or not an existing user). The handler maps it to 400;
// a wrapped lookup failure (not this sentinel) maps to 500.
var errInvalidOwner = errors.New("invalid ownerUserId")

// validateOwner reports an error when ownerID is set (non-nil) but does not
// reference an existing user. nil is always valid — it means "global", the
// shared-shelf default. A nil users lookup skips validation (single-user
// deployments that never wire tenancy).
func (h *ImportListHandler) validateOwner(ctx context.Context, ownerID *int64) error {
	if ownerID == nil || h.users == nil {
		return nil
	}
	if *ownerID <= 0 {
		return fmt.Errorf("%w: must be a positive user id or null", errInvalidOwner)
	}
	u, err := h.users.GetByID(ctx, *ownerID)
	if err != nil {
		return fmt.Errorf("look up owner user %d: %w", *ownerID, err)
	}
	if u == nil {
		return fmt.Errorf("%w: user %d does not exist", errInvalidOwner, *ownerID)
	}
	return nil
}

// writeOwnerError maps a validateOwner error to the right status: a bad client
// value is 400, an internal lookup failure is 500.
func writeOwnerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errInvalidOwner) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeServerError(w, r, err)
}

func (h *ImportListHandler) List(w http.ResponseWriter, r *http.Request) {
	lists, err := h.repo.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if lists == nil {
		lists = []models.ImportList{}
	}
	writeJSON(w, http.StatusOK, importListResponses(lists))
}

func (h *ImportListHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	il, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if il == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "import list not found"})
		return
	}
	writeJSON(w, http.StatusOK, importListResponse(*il))
}

func (h *ImportListHandler) Create(w http.ResponseWriter, r *http.Request) {
	var il models.ImportList
	if err := json.NewDecoder(r.Body).Decode(&il); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if il.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	if il.Type == "" {
		il.Type = "csv"
	}
	if !validImportListMediaType(il.MediaType) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mediaType must be one of: ebook, audiobook, both (or empty)"})
		return
	}
	if err := h.validateOwner(r.Context(), il.OwnerUserID); err != nil {
		writeOwnerError(w, r, err)
		return
	}
	il.APIKey = hardcover.NormalizeAPIToken(il.APIKey)
	if err := h.repo.Create(r.Context(), &il); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, importListResponse(il))
}

func (h *ImportListHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "import list not found"})
		return
	}
	il := *existing
	raw := map[string]json.RawMessage{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := applyImportListPatch(&il, raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := h.validateOwner(r.Context(), il.OwnerUserID); err != nil {
		writeOwnerError(w, r, err)
		return
	}
	il.ID = id
	if err := h.repo.Update(r.Context(), &il); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, importListResponse(il))
}

func (h *ImportListHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HardcoverLists returns the authenticated user's Hardcover reading lists. A
// request Authorization header can supply a one-off token; otherwise admins can
// use the saved global Hardcover token. The saved-token path is admin-only
// because it reveals the configured account's list names.
func (h *ImportListHandler) HardcoverLists(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	token := hardcover.NormalizeAPIToken(authHeader)
	if token == "" {
		if !requestHasAdminSemantics(r, h.settings) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
			return
		}
		token = GetHardcoverAPIToken(r.Context(), h.settings)
		if token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Hardcover API token is not configured"})
			return
		}
	}
	client := h.hcListClient(token)
	lists, err := client.GetUserLists(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	// account identifies whose lists these are (#1489): built-in shelves share
	// one slug per account, so the frontend needs the username to keep two
	// accounts' "Want to Read" apart. Best-effort — an empty account degrades
	// to the legacy slug-only matching.
	account, err := client.GetUsername(r.Context())
	if err != nil {
		slog.Debug("hardcover lists: username lookup failed", "error", err)
		account = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account, "lists": lists})
}

// --- Exclusions ---

func (h *ImportListHandler) ListExclusions(w http.ResponseWriter, r *http.Request) {
	exclusions, err := h.repo.ListExclusions(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if exclusions == nil {
		exclusions = []models.ImportListExclusion{}
	}
	writeJSON(w, http.StatusOK, exclusions)
}

func (h *ImportListHandler) CreateExclusion(w http.ResponseWriter, r *http.Request) {
	var e models.ImportListExclusion
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if e.ForeignID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "foreignId required"})
		return
	}
	if err := h.repo.CreateExclusion(r.Context(), &e); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (h *ImportListHandler) DeleteExclusion(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.repo.DeleteExclusion(r.Context(), id); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Sync triggers a manual sync of a single import list. Hardcover lists run the
// full hardcoverlistsyncer path; other list types are rejected with 400 since
// no other type has a syncer wired here yet.
//
// The sync used to run inside this request, which capped it at the server's
// 60s request timeout: on a 1,660-book shelf that meant 519 books imported and
// 1,110 "context deadline exceeded" errors (#1854). It now starts a background
// job on a context that outlives the request and answers 202 with the initial
// progress snapshot, so the UI can poll SyncStatus instead of holding a
// connection open. Preconditions (unknown id, wrong type, disabled list,
// missing token) are still checked synchronously and still answer 4xx.
// POST /api/v1/importlist/{id}/sync
func (h *ImportListHandler) Sync(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if h.hcSync == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "hardcover syncer not configured"})
		return
	}

	// context.WithoutCancel: the response returning must not cancel the job.
	// The syncer prefers the process-wide jobs group when one is wired, so the
	// run is still cancelled and drained on shutdown.
	switch err := h.hcSync.StartOne(context.WithoutCancel(r.Context()), id); {
	case err == nil:
		writeJSON(w, http.StatusAccepted, h.hcSync.Progress())
	case errors.Is(err, hardcoverlistsyncer.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, hardcoverlistsyncer.ErrWrongType):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, hardcoverlistsyncer.ErrDisabled):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, hardcoverlistsyncer.ErrMissingToken):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, hardcoverlistsyncer.ErrSyncAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, hardcoverlistsyncer.ErrShuttingDown):
		// The process is draining and refused to start new work. 503 rather
		// than 502: nothing is broken, the request just arrived too late.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}

// SyncStatus reports the current (or most recent) Hardcover list sync so the
// Import tab can show that a sync is running and when it finished, whether it
// was started from "Sync now" or by the scheduler.
// GET /api/v1/importlist/sync/status
func (h *ImportListHandler) SyncStatus(w http.ResponseWriter, _ *http.Request) {
	if h.hcSync == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "hardcover syncer not configured"})
		return
	}
	writeJSON(w, http.StatusOK, h.hcSync.Progress())
}

func importListResponses(lists []models.ImportList) []models.ImportList {
	out := make([]models.ImportList, 0, len(lists))
	for _, il := range lists {
		out = append(out, importListResponse(il))
	}
	return out
}

func importListResponse(il models.ImportList) models.ImportList {
	il.APIKeyConfigured = hardcover.NormalizeAPIToken(il.APIKey) != ""
	il.APIKey = ""
	return il
}

// validImportListMediaType reports whether v is an accepted per-list media
// type. Empty is valid and means "unset" — synced books keep the media type
// derived from the source (e.g. Hardcover edition availability).
func validImportListMediaType(v string) bool {
	switch v {
	case "", models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
		return true
	default:
		return false
	}
}

func applyImportListPatch(il *models.ImportList, raw map[string]json.RawMessage) error {
	apply := func(key string, dest any) error {
		if value, ok := raw[key]; ok {
			return json.Unmarshal(value, dest)
		}
		return nil
	}
	if err := apply("name", &il.Name); err != nil {
		return err
	}
	if err := apply("type", &il.Type); err != nil {
		return err
	}
	if err := apply("url", &il.URL); err != nil {
		return err
	}
	if err := apply("rootFolderId", &il.RootFolderID); err != nil {
		return err
	}
	if err := apply("qualityProfileId", &il.QualityProfileID); err != nil {
		return err
	}
	if err := apply("monitorNew", &il.MonitorNew); err != nil {
		return err
	}
	if err := apply("autoAdd", &il.AutoAdd); err != nil {
		return err
	}
	if err := apply("enabled", &il.Enabled); err != nil {
		return err
	}
	if err := apply("mediaType", &il.MediaType); err != nil {
		return err
	}
	// ownerUserId patch semantics: an explicit null clears the owner (back to
	// global), a number sets it, an absent key leaves the current owner intact.
	// Existence against the users table is validated by the handler afterwards.
	if err := apply("ownerUserId", &il.OwnerUserID); err != nil {
		return err
	}
	if !validImportListMediaType(il.MediaType) {
		return errors.New("mediaType must be one of: ebook, audiobook, both (or empty)")
	}
	clearAPIKey := false
	if value, ok := raw["clearApiKey"]; ok {
		if err := json.Unmarshal(value, &clearAPIKey); err != nil {
			return err
		}
	}
	apiKey := ""
	if value, ok := raw["apiKey"]; ok {
		var apiKeyValue string
		if err := json.Unmarshal(value, &apiKeyValue); err != nil {
			return err
		}
		apiKey = hardcover.NormalizeAPIToken(apiKeyValue)
	}
	if clearAPIKey && apiKey != "" {
		return errors.New("apiKey and clearApiKey cannot both be set")
	}
	if clearAPIKey {
		il.APIKey = ""
	} else if apiKey != "" {
		il.APIKey = apiKey
	}
	return nil
}
