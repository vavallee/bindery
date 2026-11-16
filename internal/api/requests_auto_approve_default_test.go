package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Tests for the system wide auto approve default and the tri-state account
// setting (#3900).

func setAutoApproveDefault(t *testing.T, repo *db.SettingsRepo, v string) {
	t.Helper()
	if err := repo.Set(context.Background(), SettingRequestsAutoApproveDefault, v); err != nil {
		t.Fatal(err)
	}
}

func bptr(b bool) *bool { return &b }

// The effective value is the account's own when it has one, otherwise the
// system default; and the default is off when unset.
func TestEffectiveAutoApprove_Table(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	settings := db.NewSettingsRepo(database)
	ctx := context.Background()

	if effectiveAutoApprove(ctx, settings, auth.RoleRequester, nil) {
		t.Fatal("an unset account with no default stored must not auto approve")
	}
	for _, tc := range []struct {
		def  string
		own  *bool
		want bool
	}{
		{"false", nil, false},
		{"false", bptr(true), true},
		{"false", bptr(false), false},
		{"true", nil, true},
		{"true", bptr(true), true},
		{"true", bptr(false), false},
		{"", nil, false},
		{"garbage", nil, false},
	} {
		setAutoApproveDefault(t, settings, tc.def)
		if got := effectiveAutoApprove(ctx, settings, auth.RoleRequester, tc.own); got != tc.want {
			t.Errorf("default %q own %v: got %v, want %v", tc.def, tc.own, got, tc.want)
		}
	}
}

// An unreadable default is no auto approval for an unset account, and never
// overrides an account's own value.
func TestEffectiveAutoApprove_FailsClosed(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	settings := db.NewSettingsRepo(database)
	setAutoApproveDefault(t, settings, "true")
	// A separate connection that fails every read stands in for a lookup error.
	broken, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	brokenSettings := db.NewSettingsRepo(broken)
	broken.Close()
	database.Close()

	ctx := context.Background()
	if effectiveAutoApprove(ctx, brokenSettings, auth.RoleRequester, nil) {
		t.Fatal("an unreadable default auto approved an unset account")
	}
	if effectiveAutoApprove(ctx, nil, auth.RoleRequester, nil) {
		t.Fatal("a missing settings repo auto approved an unset account")
	}
	if !effectiveAutoApprove(ctx, brokenSettings, auth.RoleRequester, bptr(true)) {
		t.Fatal("an explicit Always was lost to a settings read error")
	}
	if effectiveAutoApprove(ctx, brokenSettings, auth.RoleRequester, bptr(false)) {
		t.Fatal("an explicit Never was lost to a settings read error")
	}
}

// A user lookup error answers false even with the default on.
func TestAutoApproveRequests_UserLookupErrorFailsClosed(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	setAutoApproveDefault(t, f.settings, "true")
	if !f.h.autoApproveRequests(context.Background(), f.requester.ID) {
		t.Fatal("precondition: default on should approve an unset account")
	}
	if f.h.autoApproveRequests(context.Background(), 987654) {
		t.Fatal("an unknown owner auto approved")
	}
	f.database.Close()
	if f.h.autoApproveRequests(context.Background(), f.requester.ID) {
		t.Fatal("a failing user lookup auto approved")
	}
}

// With the default on, a request from an account with no setting of its own
// is added by the create call; with the default off it waits.
func TestRequestsCreate_UnsetAccountFollowsDefault(t *testing.T) {
	for _, tc := range []struct {
		name       string
		def        string
		own        *bool
		wantStatus string
	}{
		{"default on, unset", "true", nil, models.RequestStatusApproved},
		{"default off, unset", "false", nil, models.RequestStatusPending},
		{"default unset, unset", "", nil, models.RequestStatusPending},
		{"default on, Never", "true", bptr(false), models.RequestStatusPending},
		{"default off, Always", "false", bptr(true), models.RequestStatusApproved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adder := &fakeAdder{}
			f := newRequestsFixture(t, wellsRequestStub(), adder)
			if tc.def != "" {
				setAutoApproveDefault(t, f.settings, tc.def)
			}
			if err := f.users.SetRequestsAutoApprove(context.Background(), f.requester.ID, tc.own); err != nil {
				t.Fatal(err)
			}
			rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			if got := decodeRequest(t, rec); got.Status != tc.wantStatus {
				t.Fatalf("status %q, want %q", got.Status, tc.wantStatus)
			}
			wantCalls := int32(0)
			if tc.wantStatus == models.RequestStatusApproved {
				wantCalls = 1
			}
			if n := adder.calls.Load(); n != wantCalls {
				t.Fatalf("add core ran %d times, want %d", n, wantCalls)
			}
		})
	}
}

// The toggle takes true, false and null, and refuses everything else.
func TestUserMgmt_SetAutoApprove_TriStateAndStrictBody(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	u, err := users.Create(ctx, "reader", "h")
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) int {
		rec := httptest.NewRecorder()
		h.SetAutoApprove(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/auto-approve", body, u.ID, ctx))
		return rec.Code
	}
	state := func() *bool {
		got, _ := users.GetByID(ctx, u.ID)
		return got.RequestsAutoApprove
	}

	if put(`{"enabled":true}`) != http.StatusOK || state() == nil || !*state() {
		t.Fatal("true did not set Always")
	}
	if put(`{"enabled":false}`) != http.StatusOK || state() == nil || *state() {
		t.Fatal("false did not set Never")
	}
	if put(`{"enabled":null}`) != http.StatusOK || state() != nil {
		t.Fatal("null did not clear the account back to the default")
	}

	// Junk leaves the stored value untouched.
	if put(`{"enabled":true}`) != http.StatusOK {
		t.Fatal("setup")
	}
	for _, body := range []string{
		`{"enabled":"true"}`, `{"enabled":1}`, `{"enabled":0}`, `{"enabled":"null"}`,
		`{"enabled":[]}`, `{"enabled":{}}`, `{}`, ``, `null`, `[]`, `true`,
		`{"enabled":true,"extra":1}`, `{"enable":true}`, `{"enabled":true} {"enabled":false}`,
	} {
		if code := put(body); code != http.StatusBadRequest {
			t.Errorf("body %q answered %d, want 400", body, code)
		}
	}
	if s := state(); s == nil || !*s {
		t.Fatal("a refused body changed the stored value")
	}
}

// The users list shows the account's own value (null, true, false) and the
// effective one, which follows the default for an unset account only.
func TestUserMgmt_List_ShowsOwnAndEffective(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	users := db.NewUserRepo(database)
	settings := db.NewSettingsRepo(database)
	h := NewUserManagementHandler(users).WithSettings(settings)
	ctx := context.Background()
	unset, _ := users.Create(ctx, "unset", "h")
	always, _ := users.Create(ctx, "always", "h")
	never, _ := users.Create(ctx, "never", "h")
	unsetUser, _ := users.Create(ctx, "unsetuser", "h")
	unsetAdmin, _ := users.Create(ctx, "unsetadmin", "h")
	for _, id := range []int64{unset.ID, always.ID, never.ID} {
		if err := users.SetRole(ctx, id, auth.RoleRequester); err != nil {
			t.Fatal(err)
		}
	}
	if err := users.SetRole(ctx, unsetAdmin.ID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := users.SetRequestsAutoApprove(ctx, always.ID, bptr(true)); err != nil {
		t.Fatal(err)
	}
	if err := users.SetRequestsAutoApprove(ctx, never.ID, bptr(false)); err != nil {
		t.Fatal(err)
	}

	list := func() map[int64]map[string]any {
		rec := httptest.NewRecorder()
		h.List(rec, jsonReq(http.MethodGet, "/api/v1/auth/users", "", nil))
		var rows []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		out := map[int64]map[string]any{}
		for _, r := range rows {
			out[int64(r["id"].(float64))] = r
		}
		return out
	}
	for _, def := range []bool{false, true} {
		v := "false"
		if def {
			v = "true"
		}
		setAutoApproveDefault(t, settings, v)
		got := list()
		if got[unset.ID]["autoApproveRequests"] != nil {
			t.Errorf("unset account own value = %v, want null", got[unset.ID]["autoApproveRequests"])
		}
		if got[unset.ID]["autoApproveEffective"] != def {
			t.Errorf("default %v: unset effective = %v", def, got[unset.ID]["autoApproveEffective"])
		}
		// The default never reaches a user or admin account with no value.
		for _, id := range []int64{unsetUser.ID, unsetAdmin.ID} {
			if got[id]["autoApproveEffective"] != false {
				t.Errorf("default %v: non requester %d effective = %v, want false", def, id, got[id]["autoApproveEffective"])
			}
		}
		if got[always.ID]["autoApproveRequests"] != true || got[always.ID]["autoApproveEffective"] != true {
			t.Errorf("default %v: Always row = %v", def, got[always.ID])
		}
		if got[never.ID]["autoApproveRequests"] != false || got[never.ID]["autoApproveEffective"] != false {
			t.Errorf("default %v: Never row = %v", def, got[never.ID])
		}
	}
}

// The default is admin only to read and to write.
func TestSettings_AutoApproveDefaultIsAdminOnly(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if !isAdminOnlySetting(SettingRequestsAutoApproveDefault) {
		t.Fatal("requests.auto_approve_default must not be readable by a non admin")
	}
	if err := repo.Set(ctx, SettingRequestsAutoApproveDefault, "true"); err != nil {
		t.Fatal(err)
	}
	if _, ok := listAs(t, h, "user")[SettingRequestsAutoApproveDefault]; ok {
		t.Fatal("a requester's settings list carries requests.auto_approve_default")
	}
	if _, ok := listAs(t, h, "admin")[SettingRequestsAutoApproveDefault]; !ok {
		t.Fatal("an admin cannot read requests.auto_approve_default")
	}
	if rec := getAs(h, "user", SettingRequestsAutoApproveDefault); rec.Code != http.StatusNotFound {
		t.Fatalf("GET as non admin = %d, want 404", rec.Code)
	}

	gated := auth.RequireAdmin(http.HandlerFunc(h.Set))
	req := userReq(withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingRequestsAutoApproveDefault, bytes.NewBufferString(`{"value":"false"}`)), SettingRequestsAutoApproveDefault))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT as non admin = %d, want 403", rec.Code)
	}
	if got, _ := repo.Get(ctx, SettingRequestsAutoApproveDefault); got == nil || got.Value != "true" {
		t.Fatalf("a refused write changed the stored value: %+v", got)
	}

	// Admin writes validate: only true or false.
	for _, v := range []string{"yes", "1", "TRUE", "on"} {
		req := adminReq(withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingRequestsAutoApproveDefault, bytes.NewBufferString(`{"value":"`+v+`"}`)), SettingRequestsAutoApproveDefault))
		rec := httptest.NewRecorder()
		h.Set(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("value %q answered %d, want 400", v, rec.Code)
		}
	}
	req = adminReq(withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingRequestsAutoApproveDefault, bytes.NewBufferString(`{"value":"false"}`)), SettingRequestsAutoApproveDefault))
	rec = httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin write false = %d %s", rec.Code, rec.Body.String())
	}
}

// The role matrix (#3900): the system default reaches a requester only. An
// explicit Always or Never is honoured on any role.
func TestRequestsCreate_RoleMatrix(t *testing.T) {
	owns := map[string]*bool{"null": nil, "always": bptr(true), "never": bptr(false)}
	for _, role := range []string{auth.RoleRequester, auth.RoleUser, auth.RoleAdmin} {
		for ownName, own := range owns {
			for _, def := range []string{"true", "false"} {
				name := role + "/" + ownName + "/default " + def
				t.Run(name, func(t *testing.T) {
					adder := &fakeAdder{}
					f := newRequestsFixture(t, wellsRequestStub(), adder)
					ctx := context.Background()
					acct, err := f.users.Create(ctx, "acct", "x")
					if err != nil {
						t.Fatal(err)
					}
					if err := f.users.SetRole(ctx, acct.ID, role); err != nil {
						t.Fatal(err)
					}
					acct.Role = role
					setAutoApproveDefault(t, f.settings, def)
					if err := f.users.SetRequestsAutoApprove(ctx, acct.ID, own); err != nil {
						t.Fatal(err)
					}
					want := false
					switch {
					case own != nil:
						want = *own
					case role == auth.RoleRequester:
						want = def == "true"
					}
					rec := f.create(t, acct, `{"kind":"book","foreignId":"OL27482W"}`)
					if rec.Code != http.StatusCreated {
						t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
					}
					wantStatus := models.RequestStatusPending
					wantCalls := int32(0)
					if want {
						wantStatus, wantCalls = models.RequestStatusApproved, 1
					}
					if got := decodeRequest(t, rec); got.Status != wantStatus {
						t.Fatalf("status %q, want %q", got.Status, wantStatus)
					}
					if n := adder.calls.Load(); n != wantCalls {
						t.Fatalf("add core ran %d times, want %d", n, wantCalls)
					}
				})
			}
		}
	}
}

// With the default on, an integration using the API key makes its requests as
// the first admin, and they queue, whether made for the admin or for another
// account that follows the default as a non requester.
func TestRequestsCreate_APIKeyAsAdminQueuesWithDefaultOn(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApproveDefault(t, f.settings, "true")

	rec := f.createWithKey(t, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with key: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusPending {
		t.Fatalf("first admin via API key: status %q, want pending", got.Status)
	}
	if n := adder.calls.Load(); n != 0 {
		t.Fatalf("add core ran %d times for an admin's request, want 0", n)
	}
}

// forUser naming an admin account with no setting queues with the default on.
func TestRequestsCreate_ForUserAdminQueuesWithDefaultOn(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApproveDefault(t, f.settings, "true")
	rec := f.createWithKey(t, forUserBody("OL27482W", f.admin.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRequest(t, rec); got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}
	if n := adder.calls.Load(); n != 0 {
		t.Fatalf("add core ran %d times, want 0", n)
	}
}

// A service account admin set to Always keeps working, with the default off
// or on, through the API key and forUser.
func TestRequestsCreate_ServiceAccountAdminAlwaysAutoApproves(t *testing.T) {
	for _, def := range []string{"false", "true"} {
		t.Run("default "+def, func(t *testing.T) {
			adder := &fakeAdder{}
			f := newRequestsFixture(t, wellsRequestStub(), adder)
			setAutoApproveDefault(t, f.settings, def)
			svc, err := f.users.Create(context.Background(), "seerr", "x")
			if err != nil {
				t.Fatal(err)
			}
			if err := f.users.SetRole(context.Background(), svc.ID, auth.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			if err := f.users.SetRequestsAutoApprove(context.Background(), svc.ID, bptr(true)); err != nil {
				t.Fatal(err)
			}
			rec := f.createWithKey(t, forUserBody("OL27482W", svc.ID))
			if rec.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			if got := decodeRequest(t, rec); got.Status != models.RequestStatusApproved {
				t.Fatalf("status %q, want approved", got.Status)
			}
			if n := adder.calls.Load(); n != 1 {
				t.Fatalf("add core ran %d times, want 1", n)
			}
		})
	}
}
