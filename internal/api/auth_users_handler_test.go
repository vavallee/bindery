package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// These tests cover the admin-only user-management mutations in auth_users.go
// (Create, Delete, SetRole), which were at 0% handler coverage. They exercise
// the handlers directly through httptest, mirroring the construction pattern in
// authorization_test.go.
//
// AUTHORIZATION NOTE: the admin gate for these endpoints lives in the router
// (cmd/bindery/main.go:701, `r.Use(auth.RequireAdmin)`), NOT inside the handler
// methods. Calling the handler method directly therefore bypasses the gate by
// construction. To still assert the authorization contract, TestUserMgmt_Create
// _NonAdminForbidden mounts the handler behind auth.RequireAdmin and proves a
// non-admin caller is rejected with 403 before the handler ever runs.

func newUserMgmtFixture(t *testing.T) (*UserManagementHandler, *db.UserRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	users := db.NewUserRepo(database)
	return NewUserManagementHandler(users), users
}

// jsonReq builds a request with a JSON body and the supplied context.
func jsonReq(method, path, body string, ctx context.Context) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	return req
}

// jsonReqWithID is jsonReq plus a chi {id} URL param.
func jsonReqWithID(method, path, body string, id int64, ctx context.Context) *http.Request {
	req := jsonReq(method, path, body, ctx)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	base := req.Context()
	req = req.WithContext(context.WithValue(base, chi.RouteCtxKey, rctx))
	return req
}

// --- Create ---------------------------------------------------------------

func TestUserMgmt_Create_Success(t *testing.T) {
	h, users := newUserMgmtFixture(t)

	rec := httptest.NewRecorder()
	h.Create(rec, jsonReq(http.MethodPost, "/api/v1/auth/users",
		`{"username":"newuser","password":"hunter2pass"}`, nil))

	if rec.Code != http.StatusCreated {
		t.Fatalf("Create status=%d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got userResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse body: %v (body=%s)", err, rec.Body.String())
	}
	if got.Username != "newuser" {
		t.Errorf("username=%q, want newuser", got.Username)
	}
	if got.Role != "user" {
		t.Errorf("role=%q, want user (default)", got.Role)
	}
	if got.ID == 0 {
		t.Error("expected a non-zero id for the created user")
	}
	// Verify the row actually persisted.
	if u, err := users.GetByUsername(context.Background(), "newuser"); err != nil || u == nil {
		t.Fatalf("created user not found in db: %v", err)
	}
}

func TestUserMgmt_Create_AdminRole(t *testing.T) {
	h, users := newUserMgmtFixture(t)

	rec := httptest.NewRecorder()
	h.Create(rec, jsonReq(http.MethodPost, "/api/v1/auth/users",
		`{"username":"adminuser","password":"hunter2pass","role":"admin"}`, nil))

	if rec.Code != http.StatusCreated {
		t.Fatalf("Create status=%d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got userResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Role != "admin" {
		t.Errorf("role=%q, want admin", got.Role)
	}
	u, _ := users.GetByUsername(context.Background(), "adminuser")
	if u == nil || u.Role != "admin" {
		t.Fatalf("admin role not persisted, got %+v", u)
	}
}

// TestUserMgmt_Create_DuplicateUsername asserts the durable invariant: a
// duplicate username is rejected with an error status and the original row is
// untouched. It deliberately does NOT pin the exact code — the handler currently
// surfaces the repo's UNIQUE-constraint error as 500, whereas 409 would be more
// correct (flagged as a follow-up). Accepting either keeps this test correct
// both today and after that fix, rather than enshrining the wart.
func TestUserMgmt_Create_DuplicateUsername(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	seed, err := users.Create(context.Background(), "dupe", "hash")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Create(rec, jsonReq(http.MethodPost, "/api/v1/auth/users",
		`{"username":"dupe","password":"hunter2pass"}`, nil))

	// Must be rejected. 409 is the target status; 500 is current behaviour.
	if rec.Code != http.StatusConflict && rec.Code != http.StatusInternalServerError {
		t.Fatalf("duplicate username status=%d, want 409 (preferred) or 500 (current); body=%s", rec.Code, rec.Body.String())
	}

	// The original row must be untouched (no overwrite, still the seeded user).
	got, err := users.GetByUsername(context.Background(), "dupe")
	if err != nil || got == nil {
		t.Fatalf("seeded user missing after duplicate create: %v", err)
	}
	if got.ID != seed.ID {
		t.Fatalf("duplicate create altered the existing row: id %d -> %d", seed.ID, got.ID)
	}
}

func TestUserMgmt_Create_MissingUsername(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, jsonReq(http.MethodPost, "/api/v1/auth/users",
		`{"username":"","password":"hunter2pass"}`, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty username status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUserMgmt_Create_MissingPassword(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, jsonReq(http.MethodPost, "/api/v1/auth/users",
		`{"username":"someone","password":""}`, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty password status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUserMgmt_Create_InvalidJSON(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, jsonReq(http.MethodPost, "/api/v1/auth/users", `{not json`, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUserMgmt_Create_LocalAuthDisabled(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	h = h.WithLocalAuthEnabled(false)
	rec := httptest.NewRecorder()
	h.Create(rec, jsonReq(http.MethodPost, "/api/v1/auth/users",
		`{"username":"someone","password":"hunter2pass"}`, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("local-auth-disabled status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestUserMgmt_Create_NonAdminForbidden asserts the AUTHORIZATION contract: the
// endpoint must reject non-admin callers. The gate is the router's RequireAdmin
// middleware, so we mount the handler behind it (as cmd/bindery/main.go does)
// and confirm a "user"-role caller never reaches the handler.
func TestUserMgmt_Create_NonAdminForbidden(t *testing.T) {
	h, users := newUserMgmtFixture(t)

	r := chi.NewRouter()
	r.With(auth.RequireAdmin).Post("/auth/users", h.Create)

	ctx := withAuthCtx(context.Background(), 7, "user")
	req := jsonReq(http.MethodPost, "/auth/users",
		`{"username":"sneaky","password":"hunter2pass"}`, ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin Create status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	// And the user must NOT have been created.
	if u, _ := users.GetByUsername(context.Background(), "sneaky"); u != nil {
		t.Error("non-admin caller managed to create a user despite 403")
	}
}

// --- Delete ---------------------------------------------------------------

func TestUserMgmt_Delete_Success(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	// Seed an admin (so deleting the target is not a last-admin situation) and
	// a plain user to delete.
	admin, _ := users.Create(ctx, "admin", "h")
	_ = users.SetRole(ctx, admin.ID, "admin")
	victim, _ := users.Create(ctx, "victim", "h")

	rec := httptest.NewRecorder()
	h.Delete(rec, jsonReqWithID(http.MethodDelete, "/api/v1/auth/users/x", "", victim.ID, ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("Delete status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if u, _ := users.GetByID(ctx, victim.ID); u != nil {
		t.Error("user still present after Delete")
	}
}

// TestUserMgmt_Delete_NotFound documents that deleting a non-existent id is a
// no-op success (the repo returns nil for ErrNoRows), so the handler returns
// 200. This pins the current "already gone" semantics.
func TestUserMgmt_Delete_NotFound(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	rec := httptest.NewRecorder()
	h.Delete(rec, jsonReqWithID(http.MethodDelete, "/api/v1/auth/users/x", "", 999999, context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("Delete missing id status=%d, want 200 (no-op); body=%s", rec.Code, rec.Body.String())
	}
}

func TestUserMgmt_Delete_InvalidID(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	req := jsonReq(http.MethodDelete, "/api/v1/auth/users/abc", "", context.Background())
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric id status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestUserMgmt_Delete_LastAdmin asserts the last-admin guard: the sole admin
// cannot be deleted (repo returns an error, handler maps it to 400).
func TestUserMgmt_Delete_LastAdmin(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	admin, _ := users.Create(ctx, "onlyadmin", "h")
	_ = users.SetRole(ctx, admin.ID, "admin")

	rec := httptest.NewRecorder()
	h.Delete(rec, jsonReqWithID(http.MethodDelete, "/api/v1/auth/users/x", "", admin.ID, ctx))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete-last-admin status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if u, _ := users.GetByID(ctx, admin.ID); u == nil {
		t.Error("last admin was deleted despite the guard")
	}
}

// --- SetRole --------------------------------------------------------------

func TestUserMgmt_SetRole_Promote(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	u, _ := users.Create(ctx, "promoteme", "h")

	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"admin"}`, u.ID, ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("SetRole status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := users.GetByID(ctx, u.ID)
	if got == nil || got.Role != "admin" {
		t.Fatalf("role not updated to admin, got %+v", got)
	}
}

func TestUserMgmt_SetRole_Demote(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	// Two admins so demoting one is allowed.
	a1, _ := users.Create(ctx, "admin1", "h")
	_ = users.SetRole(ctx, a1.ID, "admin")
	a2, _ := users.Create(ctx, "admin2", "h")
	_ = users.SetRole(ctx, a2.ID, "admin")

	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"user"}`, a2.ID, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("demote status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := users.GetByID(ctx, a2.ID)
	if got == nil || got.Role != "user" {
		t.Fatalf("role not demoted, got %+v", got)
	}
}

func TestUserMgmt_SetRole_InvalidRole(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	u, _ := users.Create(ctx, "u", "h")

	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"superadmin"}`, u.ID, ctx))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid role status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUserMgmt_SetRole_InvalidJSON(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{bad`, 1, context.Background()))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUserMgmt_SetRole_InvalidID(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	req := jsonReq(http.MethodPut, "/api/v1/auth/users/abc/role", `{"role":"admin"}`, context.Background())
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.SetRole(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric id status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestUserMgmt_SetRole_DemoteLastAdmin asserts the last-admin demotion guard.
func TestUserMgmt_SetRole_DemoteLastAdmin(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	admin, _ := users.Create(ctx, "onlyadmin", "h")
	_ = users.SetRole(ctx, admin.ID, "admin")

	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"user"}`, admin.ID, ctx))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("demote-last-admin status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := users.GetByID(ctx, admin.ID)
	if got == nil || got.Role != "admin" {
		t.Fatalf("last admin was demoted despite the guard, got %+v", got)
	}
}
