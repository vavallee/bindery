package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// UserManagementHandler owns admin-only user CRUD endpoints.
// GET    /api/v1/auth/users
// POST   /api/v1/auth/users
// DELETE /api/v1/auth/users/:id
// PUT    /api/v1/auth/users/:id/role
// PUT    /api/v1/auth/users/:id/auto-approve
type UserManagementHandler struct {
	users            *db.UserRepo
	settings         *db.SettingsRepo
	localAuthEnabled bool
}

// minPasswordLength is the minimum length enforced for locally-managed
// passwords (admin Create and ResetPassword), matching the login/registration
// flows in auth.go.
const minPasswordLength = 8

// validatePassword returns a user-facing error message when pw is too short,
// or "" when it is acceptable.
func validatePassword(pw string) string {
	if len(pw) < minPasswordLength {
		return "password must be at least 8 characters"
	}
	return ""
}

func NewUserManagementHandler(users *db.UserRepo) *UserManagementHandler {
	return &UserManagementHandler{users: users, localAuthEnabled: true}
}

// WithSettings gives the handler the settings store so a user's effective
// auto approve value can include requests.auto_approve_default (#3900).
func (h *UserManagementHandler) WithSettings(s *db.SettingsRepo) *UserManagementHandler {
	h.settings = s
	return h
}

// WithLocalAuthEnabled controls whether local password accounts may be
// created via the admin API. When false, POST /auth/users returns 403.
func (h *UserManagementHandler) WithLocalAuthEnabled(v bool) *UserManagementHandler {
	h.localAuthEnabled = v
	return h
}

type userResponse struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	Role        string  `json:"role"`
	Email       *string `json:"email,omitempty"`
	DisplayName *string `json:"displayName,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	// AutoApproveRequests is the account's own setting: true (Always), false
	// (Never) or null (use requests.auto_approve_default) (#2718, #3900).
	AutoApproveRequests *bool `json:"autoApproveRequests"`
	// AutoApproveEffective is what a request from this account actually gets
	// today: its own value, or the system default when it has none.
	AutoApproveEffective bool `json:"autoApproveEffective"`
}

// toUserResponse shapes a user row. autoDefault is the system default,
// read once by the caller so a list does not read the setting per row.
func toUserResponse(u db.User, autoDefault bool) userResponse {
	effective := autoDefault
	if u.RequestsAutoApprove != nil {
		effective = *u.RequestsAutoApprove
	}
	return userResponse{
		ID:                   u.ID,
		Username:             u.Username,
		Role:                 u.Role,
		Email:                u.Email,
		DisplayName:          u.DisplayName,
		CreatedAt:            u.CreatedAt.Format("2006-01-02T15:04:05Z"),
		AutoApproveRequests:  u.RequestsAutoApprove,
		AutoApproveEffective: effective,
	}
}

// List returns all users (admin-only).
// GET /api/v1/auth/users
func (h *UserManagementHandler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.users.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	autoDefault := autoApproveDefault(r.Context(), h.settings)
	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResponse(u, autoDefault))
	}
	writeOK(w, out)
}

// Create adds a new user (admin-only).
// POST /api/v1/auth/users
func (h *UserManagementHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !h.localAuthEnabled {
		writeErr(w, http.StatusForbidden, "local accounts are disabled")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Username == "" || body.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	if msg := validatePassword(body.Password); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	u, err := h.users.Create(r.Context(), body.Username, hash)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// Create always inserts a user; any other valid role is applied after.
	// An unrecognised role keeps the historical behaviour of creating a
	// plain user rather than refusing.
	if body.Role != auth.RoleUser && auth.ValidRole(body.Role) {
		if err := h.users.SetRole(r.Context(), u.ID, body.Role); err != nil {
			writeServerError(w, r, err)
			return
		}
		u.Role = body.Role
	}
	w.WriteHeader(http.StatusCreated)
	writeOK(w, toUserResponse(*u, autoApproveDefault(r.Context(), h.settings)))
}

// Delete removes a user (admin-only). Cannot delete the last admin, and a
// signed in admin cannot delete their own account (409).
// DELETE /api/v1/auth/users/:id[?strategy=reassign|purge][&reassignTo=<id>]
//
// A user who owns rows cannot be deleted without saying what happens to them
// (#1899). Called without a strategy on such a user, this answers 409 with the
// per-table counts so the caller can put a real choice in front of the admin:
//
//	{"error": "...", "counts": {"authors": 12, "books": 240, ...}}
//
// Re-issue the same request with strategy=reassign (reassignTo omitted means
// global, visible to every user) or strategy=purge to carry it out. A user who
// owns nothing deletes on the first call.
func (h *UserManagementHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}

	// A signed in admin cannot delete their own account: the session would
	// end on the next request with nobody having chosen to hand over the
	// install. Checked before the owned rows count and before any strategy
	// runs. The exemption follows the credential, not the id: a request
	// authenticated by the API key, or admitted as admin by the auth mode,
	// acts as the install rather than as a person and skips this guard, so it
	// may still remove the operator while another admin remains (the last
	// admin guard in the repo still applies). Without a session the
	// middleware stamps the operator's id on such requests; with one, the id
	// stays the session user's, so a signed in admin who also sends a valid
	// API key skips the guard too.
	if ctx := r.Context(); !auth.AuthedViaAPIKey(ctx) && !auth.AdminByAuthMode(ctx) &&
		auth.UserIDFromContext(ctx) == id {
		writeErr(w, http.StatusConflict, "you cannot delete your own account; sign in as another admin to remove it")
		return
	}

	strategy := db.UserDeleteStrategy(r.URL.Query().Get("strategy"))
	if strategy == "" {
		counts, err := h.users.OwnedRows(r.Context(), id)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
		if counts.Total() > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":  "this user owns library data; choose what happens to it",
				"counts": counts,
			})
			return
		}
		// Nothing to inherit, so the strategies are equivalent. Reassign is the
		// non-destructive one.
		strategy = db.ReassignOwnedRows
	}

	plan := db.UserDeletePlan{Strategy: strategy}
	if raw := r.URL.Query().Get("reassignTo"); raw != "" {
		to, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid reassignTo")
			return
		}
		plan.ReassignTo = &to
	}

	switch err := h.users.Delete(r.Context(), id, plan); {
	case err == nil:
	case errors.Is(err, db.ErrLastAdmin),
		errors.Is(err, db.ErrUnknownDeleteStrategy),
		errors.Is(err, db.ErrReassignToSelf),
		errors.Is(err, db.ErrReassignTargetMissing):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, db.ErrUserStillReferenced):
		writeErr(w, http.StatusConflict, err.Error())
		return
	default:
		writeServerError(w, r, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// SetRole changes a user's role (admin-only). Cannot demote the last admin.
// PUT /api/v1/auth/users/:id/role
func (h *UserManagementHandler) SetRole(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch err := h.users.SetRole(r.Context(), id, body.Role); {
	case err == nil:
	case errors.Is(err, db.ErrInvalidRole), errors.Is(err, db.ErrLastAdminDemote):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, db.ErrUserNotFound):
		writeErr(w, http.StatusNotFound, "No user with that id.")
		return
	default:
		writeServerError(w, r, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// parseAutoApproveBody reads {"enabled": true|false|null}. The key is
// required and must be a JSON boolean or null: a string, a number, a missing
// key or an extra key is refused rather than guessed at, because a misread
// "off" or "on" changes who gets books added without an admin. null clears the
// account's own setting so it follows requests.auto_approve_default.
func parseAutoApproveBody(r io.Reader) (*bool, bool) {
	var body struct {
		Enabled json.RawMessage `json:"enabled"`
	}
	dec := json.NewDecoder(io.LimitReader(r, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() {
		return nil, false
	}
	switch string(bytes.TrimSpace(body.Enabled)) {
	case "true":
		v := true
		return &v, true
	case "false":
		v := false
		return &v, true
	case "null":
		return nil, true
	}
	return nil, false
}

// SetAutoApprove sets a user's own request auto approval (admin-only): true
// is Always, false is Never, null is use the system default (#3900).
// PUT /api/v1/auth/users/:id/auto-approve
//
// It changes what happens to the account's next request; requests already in
// the queue stay there for a human.
func (h *UserManagementHandler) SetAutoApprove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	enabled, ok := parseAutoApproveBody(r.Body)
	if !ok {
		writeErr(w, http.StatusBadRequest, "enabled must be true, false or null")
		return
	}
	if err := h.users.SetRequestsAutoApprove(r.Context(), id, enabled); err != nil {
		if errors.Is(err, db.ErrUserNotFound) {
			writeErr(w, http.StatusNotFound, "No user with that id.")
			return
		}
		writeServerError(w, r, err)
		return
	}
	// Answer with what the account now has, so the UI shows the effective
	// result without a second request. The write has already landed, so a
	// failed re-read still answers ok, just without the values.
	out := map[string]any{"ok": true}
	if u, err := h.users.GetByID(r.Context(), id); err == nil && u != nil {
		resp := toUserResponse(*u, autoApproveDefault(r.Context(), h.settings))
		out["autoApproveRequests"] = resp.AutoApproveRequests
		out["autoApproveEffective"] = resp.AutoApproveEffective
	}
	writeOK(w, out)
}

// ResetPassword sets a new password for a user (admin-only).
// PUT /api/v1/auth/users/:id/reset-password
func (h *UserManagementHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if msg := validatePassword(body.Password); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if err := h.users.UpdatePassword(r.Context(), id, hash); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}
