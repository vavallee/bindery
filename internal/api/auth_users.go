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
	users *db.UserRepo
}

func NewUserManagementHandler(users *db.UserRepo) *UserManagementHandler {
	return &UserManagementHandler{users: users}
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
		writeErr(w, http.StatusInternalServerError, "list users: "+err.Error())
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
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash password: "+err.Error())
		return
	}
	u, err := h.users.Create(r.Context(), body.Username, hash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "create user: "+err.Error())
		return
	}
	if body.Role == "admin" {
		if err := h.users.SetRole(r.Context(), u.ID, "admin"); err != nil {
			writeErr(w, http.StatusInternalServerError, "set role: "+err.Error())
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
	if len(body.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash password: "+err.Error())
		return
	}
	if err := h.users.UpdatePassword(r.Context(), id, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "update password: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true})
}
