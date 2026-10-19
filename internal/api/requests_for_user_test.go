package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Tests for creating a request on another account's behalf with forUser
// (#3337). Only an admin session or the API key may send it; the request is
// then the target account's in every way that matters (owner, pending cap,
// My requests, auto approval) and the row records who actually asked.

// createWithKey posts body the way the API key reaches the handler: admin
// role, the first admin's id, and the verified API key flag.
func (f *requestsFixture) createWithKey(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := auth.WithAPIKeyAuth(auth.WithUserRole(auth.WithUserID(context.Background(), f.admin.ID), auth.RoleAdmin))
	rec := httptest.NewRecorder()
	f.h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/requests", strings.NewReader(body)).WithContext(ctx))
	return rec
}

// createByMode posts body the way an auth-mode grant reaches the handler:
// admin role and the first admin's id, but no admin signed in and no key.
func (f *requestsFixture) createByMode(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := auth.WithAdminByAuthMode(auth.WithUserRole(auth.WithUserID(context.Background(), f.admin.ID), auth.RoleAdmin))
	rec := httptest.NewRecorder()
	f.h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/requests", strings.NewReader(body)).WithContext(ctx))
	return rec
}

func (f *requestsFixture) withdraw(t *testing.T, u *db.User, id int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.Withdraw(rec, withID(asUser(httptest.NewRequest(http.MethodDelete, "/api/v1/requests/x", nil), u), id))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("withdraw %d: %d %s", id, rec.Code, rec.Body.String())
	}
}

func (f *requestsFixture) queue(t *testing.T) []requestResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.Queue(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/v1/requests/queue?status=all", nil), f.admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body.String())
	}
	var out requestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func (f *requestsFixture) listMine(t *testing.T, u *db.User) []requestResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.ListMine(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/v1/requests", nil), u))
	var out requestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func forUserBody(foreignID string, userID int64) string {
	return `{"kind":"book","foreignId":"` + foreignID + `","forUser":` + strconv.FormatInt(userID, 10) + `}`
}

// Anyone but an admin or the API key is refused with 403 for sending forUser,
// whatever its value, and nothing is stored. Other unknown fields are still a
// 400 for them, as before.
func TestRequestsCreate_ForUserRefusedForNonAdmins(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	plain, err := f.users.Create(context.Background(), "plainuser", "x")
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []*db.User{f.requester, plain} {
		for _, value := range []string{strconv.FormatInt(f.other.ID, 10), strconv.FormatInt(caller.ID, 10), "null", `"reader2"`} {
			body := `{"kind":"book","foreignId":"OL27482W","forUser":` + value + `}`
			rec := f.create(t, caller, body)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s (%s) sending forUser %s: %d %s, want 403", caller.Username, caller.Role, value, rec.Code, rec.Body.String())
			}
		}
		if rec := f.create(t, caller, `{"kind":"book","foreignId":"OL27482W","forUser":1,"rootFolderId":1}`); rec.Code != http.StatusBadRequest {
			t.Errorf("%s with forUser and an unknown field: %d, want 400", caller.Username, rec.Code)
		}
	}
	if n, err := f.requests.CountPending(context.Background()); err != nil || n != 0 {
		t.Fatalf("pending requests after refusals = %d (%v), want 0", n, err)
	}
}

// A request an admin session makes for a requester is the requester's: they
// own it and see it on My requests, the admin does not, and the queue names
// the admin as the one who asked.
func TestRequestsCreate_ForUserByAdminSession(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})

	rec := f.create(t, f.admin, forUserBody("OL27482W", f.requester.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create for user: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}

	row, err := f.requests.GetByID(context.Background(), got.ID)
	if err != nil || row == nil {
		t.Fatalf("reload: %v", err)
	}
	if row.OwnerUserID != f.requester.ID {
		t.Fatalf("owner = %d, want the requester %d", row.OwnerUserID, f.requester.ID)
	}
	if row.CreatedVia != models.RequestCreatedViaAdmin || row.CreatedByUserID == nil || *row.CreatedByUserID != f.admin.ID {
		t.Fatalf("created via %q by %v, want admin by %d", row.CreatedVia, row.CreatedByUserID, f.admin.ID)
	}

	if mine := f.listMine(t, f.requester); len(mine) != 1 || mine[0].ID != got.ID {
		t.Fatalf("requester's My requests = %+v, want the request made for them", mine)
	}
	if mine := f.listMine(t, f.admin); len(mine) != 0 {
		t.Fatalf("admin's My requests = %+v, want empty", mine)
	}
	if mine := f.listMine(t, f.requester); mine[0].CreatedVia != "" || mine[0].CreatedBy != "" {
		t.Fatalf("requester's view carries admin fields: %+v", mine[0])
	}

	q := f.queue(t)
	if len(q) != 1 || q[0].Username != f.requester.Username || q[0].CreatedVia != "admin" || q[0].CreatedBy != f.admin.Username {
		t.Fatalf("queue = %+v, want the requester's request created by admin %q", q, f.admin.Username)
	}
}

// The API key is recorded as the API key, not as the first admin account it
// resolves to.
func TestRequestsCreate_ForUserByAPIKey(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})

	rec := f.createWithKey(t, forUserBody("OL27482W", f.requester.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with key: %d %s", rec.Code, rec.Body.String())
	}
	row, err := f.requests.GetByID(context.Background(), decodeRequest(t, rec).ID)
	if err != nil || row == nil {
		t.Fatalf("reload: %v", err)
	}
	if row.OwnerUserID != f.requester.ID {
		t.Fatalf("owner = %d, want the requester %d", row.OwnerUserID, f.requester.ID)
	}
	if row.CreatedVia != models.RequestCreatedViaAPIKey || row.CreatedByUserID != nil {
		t.Fatalf("created via %q by %v, want api_key with no user", row.CreatedVia, row.CreatedByUserID)
	}
	q := f.queue(t)
	if len(q) != 1 || q[0].CreatedVia != "api_key" || q[0].CreatedBy != "" {
		t.Fatalf("queue = %+v, want createdVia api_key and no createdBy", q)
	}
}

// The target's pending cap applies, not the caller's, and the requests made
// for someone else do not count against the admin.
func TestRequestsCreate_ForUserCountsAgainstTargetCap(t *testing.T) {
	stub := wellsRequestStub()
	for _, id := range []string{"OL-A", "OL-B", "OL-C"} {
		stub.getBookByID[id] = &models.Book{ForeignID: id, Title: "Book " + id}
	}
	f := newRequestsFixture(t, stub, &fakeAdder{})
	if err := f.settings.Set(context.Background(), SettingRequestsMaxPendingPerUser, "2"); err != nil {
		t.Fatal(err)
	}
	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL-A"}`); rec.Code != http.StatusCreated {
		t.Fatalf("requester's own request: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.createWithKey(t, forUserBody("OL-B", f.requester.ID)); rec.Code != http.StatusCreated {
		t.Fatalf("second request, for the requester: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.createWithKey(t, forUserBody("OL-C", f.requester.ID))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third pending request for the requester: %d %s, want 429", rec.Code, rec.Body.String())
	}
	if msg := requestErrorBody(rec); !strings.HasPrefix(msg, "This user has 2 requests") {
		t.Errorf("cap sentence = %q, want it to speak of the target user", msg)
	}
	// The requester asking for themselves is held to the same cap.
	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL-C"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("requester at the cap asking themselves: %d, want 429", rec.Code)
	}
	if n, err := f.requests.CountPendingByOwner(context.Background(), f.admin.ID); err != nil || n != 0 {
		t.Fatalf("admin's pending count = %d (%v), want 0", n, err)
	}
	// Another account still has room.
	if rec := f.createWithKey(t, forUserBody("OL-C", f.other.ID)); rec.Code != http.StatusCreated {
		t.Fatalf("request for another user under their own cap: %d %s", rec.Code, rec.Body.String())
	}
}

// With the target's auto approval on, the request approves itself through the
// usual path and what it adds is owned by the target.
func TestRequestsCreate_ForUserAutoApprovesForTarget(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)

	rec := f.createWithKey(t, forUserBody("OL27482W", f.requester.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != models.RequestStatusApproved {
		t.Fatalf("status %q, want approved by the target's setting", got.Status)
	}
	if n := adder.calls.Load(); n != 1 {
		t.Fatalf("add core ran %d times, want once", n)
	}
	adder.mu.Lock()
	addCtx := adder.lastCtx
	adder.mu.Unlock()
	if uid := auth.UserIDFromContext(addCtx); uid != f.requester.ID {
		t.Fatalf("add ran as user %d, want the requester %d", uid, f.requester.ID)
	}
	if auth.UserRoleFromContext(addCtx) != auth.RoleRequester {
		t.Fatalf("add ran with role %q, want requester", auth.UserRoleFromContext(addCtx))
	}
	row, _ := f.requests.GetByID(context.Background(), got.ID)
	if row.DecidedBy != nil || row.CreatedVia != models.RequestCreatedViaAPIKey {
		t.Fatalf("decided_by %v, created_via %q; want an automatic approval of an API key request", row.DecidedBy, row.CreatedVia)
	}
}

// With the target's auto approval off, the request waits for a human even
// when the caller's own account auto approves: the setting read is the
// target's.
func TestRequestsCreate_ForUserWaitsWhenTargetDoesNotAutoApprove(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.admin.ID, true)
	capture := &capturingNotifier{events: make(chan capturedEvent, 4)}
	f.h.WithNotifier(capture)

	got := decodeRequest(t, f.create(t, f.admin, forUserBody("OL27482W", f.requester.ID)))
	if got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending for a target without auto approval", got.Status)
	}
	if n := adder.calls.Load(); n != 0 {
		t.Fatalf("add core ran %d times, want zero", n)
	}
	select {
	case ev := <-capture.events:
		if ev.payload["username"] != f.requester.Username {
			t.Errorf("requestCreated username = %v, want the target %q", ev.payload["username"], f.requester.Username)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no requestCreated event for a request left pending")
	}
	if rec := f.approve(f.admin, got.ID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("manual approval: %d %s", rec.Code, rec.Body.String())
	}
	adder.mu.Lock()
	addCtx := adder.lastCtx
	adder.mu.Unlock()
	if uid := auth.UserIDFromContext(addCtx); uid != f.requester.ID {
		t.Fatalf("approval added as user %d, want the requester %d", uid, f.requester.ID)
	}
}

// forUser takes a positive user id of an account that exists.
func TestRequestsCreate_ForUserValidation(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	for _, value := range []string{`"reader"`, "0", "-1", "1.5", "null", "true", "{}"} {
		body := `{"kind":"book","foreignId":"OL27482W","forUser":` + value + `}`
		if rec := f.createWithKey(t, body); rec.Code != http.StatusBadRequest {
			t.Errorf("forUser %s: %d %s, want 400", value, rec.Code, rec.Body.String())
		}
	}
	rec := f.createWithKey(t, forUserBody("OL27482W", 99999))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown user: %d %s, want 422", rec.Code, rec.Body.String())
	}
	if msg := requestErrorBody(rec); msg != "No user with that id." {
		t.Errorf("unknown user sentence = %q", msg)
	}
	if rec := f.createWithKey(t, `{"kind":"book","foreignId":"OL27482W","forUser":2,"rootFolderId":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("admin with an unknown field: %d, want 400", rec.Code)
	}
	if n, err := f.requests.CountPending(context.Background()); err != nil || n != 0 {
		t.Fatalf("pending requests after refusals = %d (%v), want 0", n, err)
	}
}

// A request without forUser is exactly what it was: the caller owns it and
// nothing records anyone else as having made it.
func TestRequestsCreate_WithoutForUserUnchanged(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})

	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"ebook"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	row, err := f.requests.GetByID(context.Background(), decodeRequest(t, rec).ID)
	if err != nil || row == nil {
		t.Fatalf("reload: %v", err)
	}
	if row.OwnerUserID != f.requester.ID || row.CreatedVia != "" || row.CreatedByUserID != nil {
		t.Fatalf("owner %d, created via %q by %v; want the requester's own request", row.OwnerUserID, row.CreatedVia, row.CreatedByUserID)
	}
	if q := f.queue(t); len(q) != 1 || q[0].CreatedVia != "" || q[0].CreatedBy != "" {
		t.Fatalf("queue = %+v, want no createdVia or createdBy", q)
	}
	// The API key without forUser still asks as the first admin.
	rec = f.createWithKey(t, `{"kind":"author","foreignId":"OL39307A"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("key create: %d %s", rec.Code, rec.Body.String())
	}
	row, _ = f.requests.GetByID(context.Background(), decodeRequest(t, rec).ID)
	if row.OwnerUserID != f.admin.ID || row.CreatedVia != "" {
		t.Fatalf("key request owner %d via %q, want the admin's own", row.OwnerUserID, row.CreatedVia)
	}
	// The strict three field body still holds, with the same sentence.
	rec = f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","title":"x"}`)
	if rec.Code != http.StatusBadRequest || requestErrorBody(rec) != "A request takes only kind, foreignId and mediaType." {
		t.Fatalf("unknown field: %d %q", rec.Code, requestErrorBody(rec))
	}
}

// A caller the auth mode admits as admin, with no admin signed in and no API
// key (disabled mode, or a local client in local-only mode), has nobody to
// record as having asked, so it cannot send forUser. Without forUser it still
// makes a request as before.
func TestRequestsCreate_ForUserRefusedForAuthModeGrant(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})

	rec := f.createByMode(t, forUserBody("OL27482W", f.requester.ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mode grant sending forUser: %d %s, want 403", rec.Code, rec.Body.String())
	}
	if n, err := f.requests.CountPending(context.Background()); err != nil || n != 0 {
		t.Fatalf("pending requests after the refusal = %d (%v), want 0", n, err)
	}
	rec = f.createByMode(t, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mode grant without forUser: %d %s", rec.Code, rec.Body.String())
	}
	row, _ := f.requests.GetByID(context.Background(), decodeRequest(t, rec).ID)
	if row.OwnerUserID != f.admin.ID || row.CreatedVia != "" {
		t.Fatalf("mode grant request owner %d via %q, want the admin's own", row.OwnerUserID, row.CreatedVia)
	}
}

// An admin naming their own id is asking for themselves: the request is an
// ordinary one of theirs, not one made on their behalf, and the answers speak
// to them.
func TestRequestsCreate_ForUserNamingSelfIsOwnRequest(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})

	rec := f.create(t, f.admin, forUserBody("OL27482W", f.admin.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	row, _ := f.requests.GetByID(context.Background(), decodeRequest(t, rec).ID)
	if row.OwnerUserID != f.admin.ID || row.CreatedVia != "" || row.CreatedByUserID != nil {
		t.Fatalf("owner %d via %q by %v, want the admin's own request", row.OwnerUserID, row.CreatedVia, row.CreatedByUserID)
	}
	rec = f.create(t, f.admin, forUserBody("OL27482W", f.admin.ID))
	if msg := requestErrorBody(rec); rec.Code != http.StatusConflict || !strings.HasPrefix(msg, "You have already requested this") {
		t.Fatalf("repeat: %d %q, want 409 addressed to the admin", rec.Code, msg)
	}
}

// Asking again for an approved request whose item has left the library
// reopens the target's row. forUser takes the same path: the target's cap
// applies to the reopen, and the reopened row records who asked this time.
func TestRequestsCreate_ForUserReopensTargetsApprovedRequest(t *testing.T) {
	stub := wellsRequestStub()
	for _, id := range []string{"OL-A", "OL-B"} {
		stub.getBookByID[id] = &models.Book{ForeignID: id, Title: "Book " + id}
	}
	f := newRequestsFixture(t, stub, &fakeAdder{})
	if err := f.settings.Set(context.Background(), SettingRequestsMaxPendingPerUser, "2"); err != nil {
		t.Fatal(err)
	}

	// The requester's own request, approved. The fake adder adds no rows, so
	// the item is not in the library afterwards, as if it had been removed.
	first := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	if rec := f.approve(f.admin, first.ID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL-A"}`); rec.Code != http.StatusCreated {
		t.Fatalf("OL-A: %d %s", rec.Code, rec.Body.String())
	}
	b := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL-B"}`))

	// At the target's cap, the reopen is refused in words about the target.
	rec := f.createWithKey(t, forUserBody("OL27482W", f.requester.ID))
	if msg := requestErrorBody(rec); rec.Code != http.StatusTooManyRequests || !strings.HasPrefix(msg, "This user has 2 requests") {
		t.Fatalf("reopen at the target's cap: %d %q, want 429 about the target", rec.Code, msg)
	}

	f.withdraw(t, f.requester, b.ID)
	rec = f.createWithKey(t, forUserBody("OL27482W", f.requester.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("reopen for the target: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.ID != first.ID || got.Status != models.RequestStatusPending {
		t.Fatalf("reopen answered id %d status %q, want the original row %d back to pending", got.ID, got.Status, first.ID)
	}
	row, _ := f.requests.GetByID(context.Background(), first.ID)
	if row.OwnerUserID != f.requester.ID || row.CreatedVia != models.RequestCreatedViaAPIKey || row.CreatedByUserID != nil {
		t.Fatalf("reopened row owner %d via %q by %v, want the requester's, asked for by the API key", row.OwnerUserID, row.CreatedVia, row.CreatedByUserID)
	}
}
