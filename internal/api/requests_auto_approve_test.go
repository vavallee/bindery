package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// Tests for per-account request auto approval (#2718). A requester account
// whose admin turned the setting on gets its request added straight away; an
// account without it still waits for a human; turning the setting off puts the
// manual path back.

func setAutoApprove(t *testing.T, f *requestsFixture, userID int64, enabled bool) {
	t.Helper()
	if err := f.users.SetRequestsAutoApprove(context.Background(), userID, enabled); err != nil {
		t.Fatalf("set auto approve: %v", err)
	}
}

// An auto approving account's request is added and marked approved by the
// create call itself, with no admin involved. decided_by stays NULL so the
// queue can tell it apart from a human approval.
func TestRequestsCreate_AutoApproveNeedsNoAdmin(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)

	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != models.RequestStatusApproved {
		t.Fatalf("status %q, want approved without an admin", got.Status)
	}
	if n := adder.calls.Load(); n != 1 {
		t.Fatalf("add core ran %d times, want once", n)
	}
	adder.mu.Lock()
	params := adder.last
	adder.mu.Unlock()
	if !params.SearchOnAdd {
		t.Error("an auto approved book request did not search on add")
	}

	row, err := f.requests.GetByID(context.Background(), got.ID)
	if err != nil || row == nil {
		t.Fatalf("reload request: %v", err)
	}
	if row.Status != models.RequestStatusApproved {
		t.Fatalf("stored status %q, want approved", row.Status)
	}
	if row.DecidedBy != nil {
		t.Fatalf("decided_by = %d, want NULL for an automatic approval", *row.DecidedBy)
	}
	if row.DecidedAt == nil {
		t.Error("decided_at not set on an auto approved request")
	}
}

// The same create from an account without the setting still lands in the queue
// and touches no adder. This is the behaviour every existing install has.
func TestRequestsCreate_WithoutAutoApproveStillWaits(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)

	got := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	if got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}
	if n := adder.calls.Load(); n != 0 {
		t.Fatalf("add core ran %d times, want zero", n)
	}
}

// Turning the setting off puts the manual path back: the next request waits,
// and an admin can still approve it by hand.
func TestRequestsCreate_AutoApproveOffRestoresTheManualPath(t *testing.T) {
	stub := wellsRequestStub()
	stub.getBookByID["OL-SECOND"] = &models.Book{ForeignID: "OL-SECOND", Title: "Second"}
	adder := &fakeAdder{}
	f := newRequestsFixture(t, stub, adder)

	setAutoApprove(t, f, f.requester.ID, true)
	if got := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)); got.Status != models.RequestStatusApproved {
		t.Fatalf("first request status %q, want approved", got.Status)
	}

	setAutoApprove(t, f, f.requester.ID, false)
	second := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL-SECOND"}`))
	if second.Status != models.RequestStatusPending {
		t.Fatalf("second request status %q, want pending after the setting went off", second.Status)
	}
	if n := adder.calls.Load(); n != 1 {
		t.Fatalf("add core ran %d times, want only the auto approved first request", n)
	}
	if rec := f.approve(f.admin, second.ID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("manual approval after the setting went off: %d %s", rec.Code, rec.Body.String())
	}
	if n := adder.calls.Load(); n != 2 {
		t.Fatalf("add core ran %d times, want 2 after the manual approval", n)
	}
}

// Switching the setting on applies to the next request only. A request already
// waiting when the admin flips it stays in the queue for a human.
func TestRequestsCreate_AutoApproveLeavesQueuedRequestsAlone(t *testing.T) {
	stub := wellsRequestStub()
	stub.getBookByID["OL-LATER"] = &models.Book{ForeignID: "OL-LATER", Title: "Later"}
	adder := &fakeAdder{}
	f := newRequestsFixture(t, stub, adder)

	queued := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	setAutoApprove(t, f, f.requester.ID, true)

	fresh := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL-LATER"}`))
	if fresh.Status != models.RequestStatusApproved {
		t.Fatalf("new request after switching the setting on: %q, want approved", fresh.Status)
	}
	row, err := f.requests.GetByID(context.Background(), queued.ID)
	if err != nil || row == nil {
		t.Fatalf("reload queued request: %v", err)
	}
	if row.Status != models.RequestStatusPending {
		t.Fatalf("already queued request became %q, want it left pending", row.Status)
	}
	if n := adder.calls.Load(); n != 1 {
		t.Fatalf("add core ran %d times, want once for the new request only", n)
	}
}

// A failed auto approval must not lose the request. It stays pending and an
// admin can still approve it once the provider is back.
func TestRequestsCreate_AutoApproveFailureLeavesRequestPending(t *testing.T) {
	adder := &fakeAdder{err: errors.New("provider exploded")}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)

	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != models.RequestStatusPending {
		t.Fatalf("status after a failed auto approval %q, want pending", got.Status)
	}
	row, err := f.requests.GetByID(context.Background(), got.ID)
	if err != nil || row == nil {
		t.Fatalf("reload request: %v", err)
	}
	if row.Status != models.RequestStatusPending {
		t.Fatalf("stored status %q, want pending", row.Status)
	}

	adder.err = nil
	if rec := f.approve(f.admin, got.ID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("manual approval after the failure: %d %s", rec.Code, rec.Body.String())
	}
}

// An author request auto approves too, through the ordinary catalogue sync. It
// does not search on add, matching the approve form's own starting point for
// authors.
func TestRequestsCreate_AutoApproveAuthorRequest(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	setAutoApprove(t, f, f.requester.ID, true)

	got := decodeRequest(t, f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`))
	if got.Status != models.RequestStatusApproved {
		t.Fatalf("status %q, want approved", got.Status)
	}
	adder.mu.Lock()
	params := adder.lastAuthor
	adder.mu.Unlock()
	if params.SearchOnAdd {
		t.Error("an auto approved author request searched on add")
	}
	if params.SkipCatalogueSync {
		t.Error("an auto approved author request skipped the catalogue sync")
	}
}

// Asking again for something approved earlier reopens the row; when the
// account auto approves, that reopen is approved too rather than left waiting.
func TestRequestsCreate_AutoApproveReopenedRequest(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)

	first := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	if rec := f.approve(f.admin, first.ID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("seed approval: %d %s", rec.Code, rec.Body.String())
	}
	setAutoApprove(t, f, f.requester.ID, true)

	again := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	if again.ID != first.ID {
		t.Fatalf("re-request made a new row %d, want the reopened %d", again.ID, first.ID)
	}
	if again.Status != models.RequestStatusApproved {
		t.Fatalf("reopened request status %q, want approved", again.Status)
	}
	if n := adder.calls.Load(); n != 2 {
		t.Fatalf("add core ran %d times, want 2 (seed plus auto approved reopen)", n)
	}
}

// The daily quota is per account: once one requester has auto approved their
// allowance, another account with the setting on still auto approves. It is
// also the only brake on the path that removes the pending cap, so the count is
// taken from the same requests.max_pending_per_user limit.
func TestRequestsCreate_AutoApproveDailyQuota(t *testing.T) {
	stub := wellsRequestStub()
	stub.getBookByID["OL-SECOND"] = &models.Book{ForeignID: "OL-SECOND", Title: "Second"}
	stub.getBookByID["OL-THIRD"] = &models.Book{ForeignID: "OL-THIRD", Title: "Third"}
	adder := &fakeAdder{}
	f := newRequestsFixture(t, stub, adder)
	if err := f.settings.Set(context.Background(), SettingRequestsMaxPendingPerUser, "2"); err != nil {
		t.Fatal(err)
	}
	setAutoApprove(t, f, f.requester.ID, true)
	setAutoApprove(t, f, f.other.ID, true)

	for _, id := range []string{"OL27482W", "OL-SECOND"} {
		got := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"`+id+`"}`))
		if got.Status != models.RequestStatusApproved {
			t.Fatalf("%s status %q, want approved while under the quota", id, got.Status)
		}
	}

	// Past the quota the request falls back to the queue, exactly as it would
	// with the setting off, and the adder is not run for it.
	third := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL-THIRD"}`))
	if third.Status != models.RequestStatusPending {
		t.Fatalf("request past the daily quota status %q, want pending", third.Status)
	}
	if n := adder.calls.Load(); n != 2 {
		t.Fatalf("add core ran %d times, want 2 (the daily quota)", n)
	}
	row, err := f.requests.GetByID(context.Background(), third.ID)
	if err != nil || row == nil {
		t.Fatalf("reload the queued request: %v", err)
	}
	if row.Status != models.RequestStatusPending {
		t.Fatalf("stored status past the quota %q, want pending", row.Status)
	}

	// The quota is per account, so another auto approving requester is
	// unaffected by the first one having reached it.
	if got := decodeRequest(t, f.create(t, f.other, `{"kind":"book","foreignId":"OL27482W"}`)); got.Status != models.RequestStatusApproved {
		t.Fatalf("second requester status %q, want approved (the quota is per account)", got.Status)
	}

	// A human can still take the queued one, which is what "fall back to
	// queuing" has to mean.
	if rec := f.approve(f.admin, third.ID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("manual approval past the quota: %d %s", rec.Code, rec.Body.String())
	}
	if n := adder.calls.Load(); n != 4 {
		t.Fatalf("add core ran %d times, want 4 after the second requester and the manual approval", n)
	}
}

// An auto approved request must not send requestCreated: the point of the
// issue is that a household member's requests stop pinging the admin. The
// audit trail is still there, because the approved row is in the queue.
func TestRequestsCreate_AutoApproveSendsNoRequestCreated(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	capture := &capturingNotifier{events: make(chan capturedEvent, 4)}
	f.h.WithNotifier(capture)
	setAutoApprove(t, f, f.requester.ID, true)

	got := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	if got.Status != models.RequestStatusApproved {
		t.Fatalf("status %q, want approved", got.Status)
	}
	select {
	case ev := <-capture.events:
		t.Fatalf("auto approved request sent %s: %+v", ev.event, ev.payload)
	case <-time.After(300 * time.Millisecond):
	}
}

// Suppression tracks the request actually being approved, not the setting: a
// request the auto approval leaves pending still sends requestCreated, so a
// failed add is not silently invisible to the admin.
func TestRequestsCreate_AutoApproveFailureStillNotifies(t *testing.T) {
	adder := &fakeAdder{err: errors.New("provider exploded")}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	capture := &capturingNotifier{events: make(chan capturedEvent, 4)}
	f.h.WithNotifier(capture)
	setAutoApprove(t, f, f.requester.ID, true)

	got := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	if got.Status != models.RequestStatusPending {
		t.Fatalf("status %q, want pending", got.Status)
	}
	select {
	case ev := <-capture.events:
		if ev.event != notifier.EventRequestCreated {
			t.Fatalf("event = %q, want %q", ev.event, notifier.EventRequestCreated)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a request left pending by a failed auto approval sent no requestCreated event")
	}
}

// The admin route sets the flag and the users list reports it.
func TestUserMgmt_SetAutoApprove(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	u, err := users.Create(ctx, "reader", "h")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.SetAutoApprove(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/auto-approve", `{"enabled":true}`, u.ID, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("SetAutoApprove status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := users.GetByID(ctx, u.ID)
	if got == nil || !got.RequestsAutoApprove {
		t.Fatalf("auto approve not persisted, got %+v", got)
	}

	rec = httptest.NewRecorder()
	h.List(rec, jsonReq(http.MethodGet, "/api/v1/auth/users", "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var listed []userResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].AutoApproveRequests {
		t.Fatalf("users list did not report auto approve: %+v", listed)
	}

	rec = httptest.NewRecorder()
	h.SetAutoApprove(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/auto-approve", `{"enabled":false}`, u.ID, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("SetAutoApprove off status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got, _ = users.GetByID(ctx, u.ID); got.RequestsAutoApprove {
		t.Fatal("auto approve still on after being turned off")
	}
}

func TestUserMgmt_SetAutoApprove_BadRequests(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	ctx := context.Background()

	rec := httptest.NewRecorder()
	h.SetAutoApprove(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/auto-approve", `{"enabled":true}`, 0, ctx))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status=%d, want 404 (an admin typo must not report success); body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.SetAutoApprove(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/auto-approve", `{bad`, 1, ctx))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	req := jsonReq(http.MethodPut, "/api/v1/auth/users/abc/auto-approve", `{"enabled":true}`, ctx)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	h.SetAutoApprove(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric id status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
