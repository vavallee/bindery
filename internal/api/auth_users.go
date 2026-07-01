package api

import (
	"encoding/json"
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
type UserManagementHandler struct {
	users            *db.UserRepo
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
}

func toUserResponse(u db.User) userResponse {
	return userResponse{
		ID:          u.ID,
		Username:    u.Username,
		Role:        u.Role,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		CreatedAt:   u.CreatedAt.Format("2006-01-02T15:04:05Z"),
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
	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResponse(u))
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
	if body.Role == "admin" {
		if err := h.users.SetRole(r.Context(), u.ID, "admin"); err != nil {
			writeServerError(w, r, err)
			return
		}
		u.Role = "admin"
	}
	w.WriteHeader(http.StatusCreated)
	writeOK(w, toUserResponse(*u))
}

// Delete removes a user (admin-only). Cannot delete the last admin.
// DELETE /api/v1/auth/users/:id
func (h *UserManagementHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.users.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := h.users.SetRole(r.Context(), id, body.Role); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true})
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
