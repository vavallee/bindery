package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type requestsFixture struct {
	database  *sql.DB
	h         *RequestHandler
	requests  *db.RequestRepo
	books     *db.BookRepo
	authors   *db.AuthorRepo
	settings  *db.SettingsRepo
	users     *db.UserRepo
	author    *AuthorHandler
	admin     *db.User
	requester *db.User
	other     *db.User
}

// newRequestsFixture wires the requests handler over an in memory database.
// With adder nil the real AuthorHandler (over provider) does the adds.
func newRequestsFixture(t *testing.T, provider metadata.Provider, adder requestAdder) *requestsFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)
	ctx := context.Background()

	f := &requestsFixture{
		database: database,
		requests: db.NewRequestRepo(database),
		books:    db.NewBookRepo(database),
		authors:  db.NewAuthorRepo(database),
		settings: db.NewSettingsRepo(database),
		users:    db.NewUserRepo(database),
	}
	agg := metadata.NewAggregator(provider)
	group := jobs.NewGroup(context.Background())
	t.Cleanup(func() { group.Shutdown(5 * time.Second) })
	f.author = NewAuthorHandler(f.authors, nil, f.books, nil, agg, f.settings, db.NewMetadataProfileRepo(database), nil).WithJobs(group)
	if adder == nil {
		adder = f.author
	}
	// A private, generous provider bucket so tests in this package do not
	// share the process wide one; the limit tests set their own.
	f.h = NewRequestHandler(f.requests, f.books, f.authors, f.settings, f.users, agg, adder).
		WithProviderLimiter(auth.NewRequesterLimiter(1<<20, 1<<20, time.Minute, 64))

	mk := func(name, role string) *db.User {
		u, err := f.users.Create(ctx, name, "x")
		if err != nil {
			t.Fatal(err)
		}
		if role != auth.RoleUser {
			if err := f.users.SetRole(ctx, u.ID, role); err != nil {
				t.Fatal(err)
			}
			u.Role = role
		}
		return u
	}
	f.admin = mk("admin", auth.RoleAdmin)
	f.requester = mk("reader", auth.RoleRequester)
	f.other = mk("reader2", auth.RoleRequester)
	return f
}

func asUser(r *http.Request, u *db.User) *http.Request {
	return r.WithContext(auth.WithUserRole(auth.WithUserID(r.Context(), u.ID), u.Role))
}

func withID(r *http.Request, id int64) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func (f *requestsFixture) create(t *testing.T, u *db.User, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.Create(rec, asUser(httptest.NewRequest(http.MethodPost, "/api/v1/requests", strings.NewReader(body)), u))
	return rec
}

func (f *requestsFixture) approve(u *db.User, id int64, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.h.Approve(rec, withID(asUser(httptest.NewRequest(http.MethodPost, "/api/v1/requests/x/approve", strings.NewReader(body)), u), id))
	return rec
}

func decodeRequest(t *testing.T, rec *httptest.ResponseRecorder) requestResponse {
	t.Helper()
	var out requestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func requestErrorBody(rec *httptest.ResponseRecorder) string {
	var m map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m["error"]
}

// wellsRequestStub is the War of the Worlds stub with an author on the book
// record, the way OpenLibrary and Hardcover answer GetBook.
func wellsRequestStub() *stubMetaProvider {
	stub := addBookBackCatalogueStub(false)
	picked := *stub.getBookByID["OL27482W"]
	picked.Author = &models.Author{ForeignID: "OL39307A", Name: "H. G. Wells"}
	stub.getBookByID["OL27482W"] = &picked
	stub.author = &models.Author{ForeignID: "OL39307A", Name: "H. G. Wells", SortName: "Wells, H. G.", MetadataProvider: "openlibrary"}
	return stub
}

// TestRequestsCreate_ServerBuildsPayload is security review item S2: the body
// carries only kind, foreignId and mediaType; title and author come from the
// provider, and any other field is refused rather than ignored.
func TestRequestsCreate_ServerBuildsPayload(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)

	for _, body := range []string{
		`{"kind":"book","foreignId":"OL27482W","title":"@everyone free nitro"}`,
		`{"kind":"book","foreignId":"OL27482W","rootFolderId":1}`,
		`{"kind":"book","foreignId":"OL27482W","searchOnAdd":true}`,
		`{"kind":"book","foreignId":"OL27482W","qualityProfileId":2}`,
		`{"kind":"book","foreignId":"OL27482W"} {"kind":"author"}`,
		`[]`,
		``,
	} {
		if rec := f.create(t, f.requester, body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status %d, want 400", body, rec.Code)
		}
	}
	for _, body := range []string{
		`{"kind":"movie","foreignId":"OL27482W"}`,
		`{"kind":"book","foreignId":""}`,
		`{"kind":"book","foreignId":"OL 27482W"}`,
		`{"kind":"book","foreignId":"` + strings.Repeat("x", 200) + `"}`,
		`{"kind":"book","foreignId":"OL27482W","mediaType":"vinyl"}`,
	} {
		if rec := f.create(t, f.requester, body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status %d, want 400", body, rec.Code)
		}
	}

	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"audiobook"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Title != "The War of the Worlds" || got.AuthorName != "H. G. Wells" || got.Status != "pending" || got.MediaType != "audiobook" {
		t.Fatalf("created request = %+v", got)
	}
	row, _ := f.requests.GetByID(context.Background(), got.ID)
	var p requestPayload
	if err := json.Unmarshal([]byte(row.PayloadJSON), &p); err != nil {
		t.Fatal(err)
	}
	want := requestPayload{Kind: "book", ForeignID: "OL27482W", ForeignAuthorID: "OL39307A", AuthorName: "H. G. Wells", MediaType: "audiobook"}
	if p != want {
		t.Fatalf("stored payload = %+v, want %+v", p, want)
	}
	if got.Username != "" || got.ResultBookID != nil {
		t.Fatalf("requester response carries admin fields: %+v", got)
	}
}

func TestRequestsCreate_BodySizeLimit(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	body := `{"kind":"book","foreignId":"OL27482W","mediaType":"` + strings.Repeat(" ", requestCreateMaxBody) + `"}`
	if rec := f.create(t, f.requester, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: status %d, want 400", rec.Code)
	}
}

func TestRequestsCreate_Conflicts(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	ctx := context.Background()

	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(requestErrorBody(rec), "already requested") {
		t.Fatalf("repeat: %d %q, want 409 already requested", rec.Code, requestErrorBody(rec))
	}
	// Another requester can ask for the same book.
	if rec := f.create(t, f.other, `{"kind":"book","foreignId":"OL27482W"}`); rec.Code != http.StatusCreated {
		t.Fatalf("other requester: %d %s", rec.Code, rec.Body.String())
	}

	// Already in the library.
	author := seedWells(t, f.authors)
	book := &models.Book{ForeignID: "OL-INLIB", AuthorID: author.ID, Title: "In Library", SortTitle: "in library", Status: models.BookStatusImported}
	if err := f.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	rec = f.create(t, f.requester, `{"kind":"book","foreignId":"OL-INLIB"}`)
	if rec.Code != http.StatusConflict || requestErrorBody(rec) != "This book is already in the library." {
		t.Fatalf("book in library: %d %q", rec.Code, requestErrorBody(rec))
	}
	rec = f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`)
	if rec.Code != http.StatusConflict || requestErrorBody(rec) != "This author is already in the library." {
		t.Fatalf("author in library: %d %q", rec.Code, requestErrorBody(rec))
	}

	// Declined earlier.
	pending, _ := f.requests.GetForOwner(ctx, f.other.ID, "book", "OL27482W")
	if err := f.requests.Decline(ctx, pending.ID, f.admin.ID, ""); err != nil {
		t.Fatal(err)
	}
	rec = f.create(t, f.other, `{"kind":"book","foreignId":"OL27482W"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(requestErrorBody(rec), "declined") {
		t.Fatalf("after decline: %d %q", rec.Code, requestErrorBody(rec))
	}
}

func TestRequestsCreate_PendingCap(t *testing.T) {
	stub := wellsRequestStub()
	for _, id := range []string{"OL-A", "OL-B", "OL-C"} {
		stub.getBookByID[id] = &models.Book{ForeignID: id, Title: "Book " + id}
	}
	f := newRequestsFixture(t, stub, nil)
	if err := f.settings.Set(context.Background(), SettingRequestsMaxPendingPerUser, "2"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"OL-A", "OL-B"} {
		if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"`+id+`"}`); rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL-C"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third pending request: %d, want 429", rec.Code)
	}
	// The cap is per user.
	if rec := f.create(t, f.other, `{"kind":"book","foreignId":"OL-C"}`); rec.Code != http.StatusCreated {
		t.Fatalf("other user under their own cap: %d", rec.Code)
	}
}

func TestRequestsCreate_ProviderFailures(t *testing.T) {
	stub := wellsRequestStub()
	stub.getBookErrByID = map[string]error{"OL-DOWN": errors.New("upstream 502")}
	f := newRequestsFixture(t, stub, nil)
	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL-DOWN"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("provider error: %d, want 502", rec.Code)
	}
	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL-MISSING"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown book: %d, want 404", rec.Code)
	}
	if n, _ := f.requests.CountPendingByOwner(context.Background(), f.requester.ID); n != 0 {
		t.Fatalf("failed lookups stored %d requests", n)
	}
}

func TestRequestsListAndWithdraw_OwnerIsolation(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	mine := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	theirs := decodeRequest(t, f.create(t, f.other, `{"kind":"author","foreignId":"OL39307A"}`))

	rec := httptest.NewRecorder()
	f.h.ListMine(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/v1/requests", nil), f.requester))
	var list requestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != mine.ID {
		t.Fatalf("requester list = %+v, want only their own request", list)
	}

	rec = httptest.NewRecorder()
	f.h.Withdraw(rec, withID(asUser(httptest.NewRequest(http.MethodDelete, "/", nil), f.requester), theirs.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("withdrawing another user's request: %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	f.h.Withdraw(rec, withID(asUser(httptest.NewRequest(http.MethodDelete, "/", nil), f.requester), mine.ID))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("withdrawing own request: %d", rec.Code)
	}
}

// TestRequestsApprove_CreatesRowsOwnedByRequester runs the real add core: the
// approval creates the book and its author owned by the requester, not by the
// approving admin, and the request reads as approved.
func TestRequestsApprove_CreatesRowsOwnedByRequester(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	ctx := context.Background()
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"ebook"}`))

	rec := f.approve(f.admin, created.ID, `{"searchOnAdd":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != "approved" || got.ResultBookID == nil || got.Username != "reader" {
		t.Fatalf("approved response = %+v", got)
	}
	book, err := f.books.GetByForeignID(ctx, "OL27482W")
	if err != nil || book == nil {
		t.Fatalf("book not added: %v", err)
	}
	if book.OwnerUserID != f.requester.ID {
		t.Errorf("book owner = %d, want the requester %d (admin is %d)", book.OwnerUserID, f.requester.ID, f.admin.ID)
	}
	author, err := f.authors.GetByID(ctx, book.AuthorID)
	if err != nil || author == nil || author.OwnerUserID != f.requester.ID {
		t.Errorf("author = %+v err %v, want owned by the requester", author, err)
	}
	if book.MediaType != models.MediaTypeEbook || !book.Monitored {
		t.Errorf("book media %q monitored %v, want ebook and monitored", book.MediaType, book.Monitored)
	}

	// A second approval of the same request is refused.
	if rec := f.approve(f.admin, created.ID, `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("second approve: %d, want 409", rec.Code)
	}
	// Once imported, the requester sees it fulfilled.
	book.Status = models.BookStatusImported
	if err := f.books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	f.h.ListMine(rec, asUser(httptest.NewRequest(http.MethodGet, "/", nil), f.requester))
	var list requestListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 1 || !list.Items[0].Fulfilled {
		t.Fatalf("after import the request list = %+v, want fulfilled", list.Items)
	}
}

func TestRequestsApprove_AuthorRequestUsesAdminChoices(t *testing.T) {
	stub := wellsRequestStub()
	f := newRequestsFixture(t, stub, nil)
	ctx := context.Background()
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`))
	if created.Title != "H. G. Wells" {
		t.Fatalf("author request title = %q", created.Title)
	}
	rec := f.approve(f.admin, created.ID, `{"monitorMode":"future","monitorNewItems":"none","searchOnAdd":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	author, err := f.authors.GetByForeignID(ctx, "OL39307A")
	if err != nil || author == nil {
		t.Fatalf("author not created: %v", err)
	}
	if author.OwnerUserID != f.requester.ID || author.MonitorMode != "future" || author.MonitorNewItems != "none" {
		t.Fatalf("author = owner %d mode %q new %q, want requester, future, none", author.OwnerUserID, author.MonitorMode, author.MonitorNewItems)
	}
}

// fakeAdder records what an approval asked for and can hold the call open.
type fakeAdder struct {
	calls      atomic.Int32
	hold       time.Duration
	err        error
	lastCtx    context.Context
	mu         sync.Mutex
	last       addBookParams
	lastAuthor createAuthorParams
	// panicWith, when set, makes addBookCore panic with its value.
	panicWith atomic.Pointer[string]
}

func (a *fakeAdder) addBookCore(ctx context.Context, req addBookParams) (addBookResult, error) {
	a.calls.Add(1)
	a.mu.Lock()
	a.lastCtx, a.last = ctx, req
	a.mu.Unlock()
	time.Sleep(a.hold)
	if p := a.panicWith.Load(); p != nil {
		panic(*p)
	}
	if a.err != nil {
		return addBookResult{}, a.err
	}
	// No ids: the fake creates no rows for result_book_id to reference.
	return addBookResult{}, nil
}

func (a *fakeAdder) createAuthorCore(ctx context.Context, req createAuthorParams) (createAuthorResult, error) {
	a.calls.Add(1)
	a.mu.Lock()
	a.lastCtx, a.lastAuthor = ctx, req
	a.mu.Unlock()
	time.Sleep(a.hold)
	if a.err != nil {
		return createAuthorResult{}, a.err
	}
	return createAuthorResult{Created: true}, nil
}

// TestRequestsApprove_ConcurrentApprovalsAddOnce is plan item T3: two admins
// approving the same request at once produce exactly one add. Run with -race.
func TestRequestsApprove_ConcurrentApprovalsAddOnce(t *testing.T) {
	adder := &fakeAdder{hold: 100 * time.Millisecond}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	// A real approval would have left rows; the fake adds nothing, so the
	// in library recheck passes for both racers.

	codes := make([]int, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i] = f.approve(f.admin, created.ID, `{}`).Code
		}(i)
	}
	close(start)
	wg.Wait()
	sort.Ints(codes)
	if codes[0] != http.StatusOK || codes[1] != http.StatusConflict {
		t.Fatalf("status codes = %v, want one 200 and one 409", codes)
	}
	if n := adder.calls.Load(); n != 1 {
		t.Fatalf("add core called %d times, want exactly once", n)
	}
}

func TestRequestsApprove_RunsAsRequesterAndReleasesOnFailure(t *testing.T) {
	adder := &fakeAdder{err: errAddBookAuthorMetadataUnavailable}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	ctx := context.Background()
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W","mediaType":"both"}`))

	rec := f.approve(f.admin, created.ID, `{"mediaType":"audiobook","searchOnAdd":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("failed add: %d %s, want the core's 422", rec.Code, rec.Body.String())
	}
	adder.mu.Lock()
	gotUID, gotRole := auth.UserIDFromContext(adder.lastCtx), auth.UserRoleFromContext(adder.lastCtx)
	last := adder.last
	adder.mu.Unlock()
	if gotUID != f.requester.ID || gotRole != auth.RoleRequester {
		t.Fatalf("core ran as user %d role %q, want the requester %d", gotUID, gotRole, f.requester.ID)
	}
	if last.MediaType != "audiobook" || !last.SearchOnAdd || last.ForeignAuthorID != "OL39307A" {
		t.Fatalf("core params = %+v, want the admin's media type and search choice over the stored payload", last)
	}
	row, _ := f.requests.GetByID(ctx, created.ID)
	if row.Status != models.RequestStatusPending {
		t.Fatalf("after a failed approval the request is %q, want pending again", row.Status)
	}
	adder.err = nil
	if rec := f.approve(f.admin, created.ID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("retry after release: %d", rec.Code)
	}
}

// TestRequestsApprove_RevalidatesPayload: a stored payload that no longer
// matches its row is refused, and so is a request for something that has
// reached the library since it was made.
func TestRequestsApprove_RevalidatesPayload(t *testing.T) {
	adder := &fakeAdder{}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	ctx := context.Background()
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))

	for _, payload := range []string{
		`{"kind":"book","foreignId":"OL-SOMETHING-ELSE"}`,
		`{"kind":"author","foreignId":"OL27482W"}`,
		`{"kind":"book","foreignId":"OL27482W","foreignAuthorId":"bad id"}`,
		`{"kind":"book","foreignId":"OL27482W","mediaType":"vinyl"}`,
		`not json`,
	} {
		if _, err := f.database.ExecContext(ctx, "UPDATE requests SET payload_json = ? WHERE id = ?", payload, created.ID); err != nil {
			t.Fatal(err)
		}
		if rec := f.approve(f.admin, created.ID, `{}`); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("payload %s: status %d, want 422", payload, rec.Code)
		}
	}
	if adder.calls.Load() != 0 {
		t.Fatalf("add core ran %d times on invalid payloads", adder.calls.Load())
	}

	good := `{"kind":"book","foreignId":"OL27482W"}`
	if _, err := f.database.ExecContext(ctx, "UPDATE requests SET payload_json = ? WHERE id = ?", good, created.ID); err != nil {
		t.Fatal(err)
	}
	author := seedWells(t, f.authors)
	if err := f.books.Create(ctx, &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war"}); err != nil {
		t.Fatal(err)
	}
	rec := f.approve(f.admin, created.ID, `{}`)
	if rec.Code != http.StatusConflict || requestErrorBody(rec) != "This book is already in the library." {
		t.Fatalf("approve after it reached the library: %d %q", rec.Code, requestErrorBody(rec))
	}
	if row, _ := f.requests.GetByID(ctx, created.ID); row.Status != models.RequestStatusPending {
		t.Fatalf("status after refused approval = %q, want pending", row.Status)
	}
	if rec := f.approve(f.admin, created.ID, `{"rootFolderId":1,"bogus":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("approval body with an unknown field: %d, want 400", rec.Code)
	}
	if rec := f.approve(f.admin, 99999, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d, want 404", rec.Code)
	}
}

func TestRequestsDecline(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	decline := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.h.Decline(rec, withID(asUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), f.admin), created.ID))
		return rec
	}
	rec := decline(`{"reason":"Not in\u0007 our \u202Ecollection` + strings.Repeat("!", 600) + `"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeRequest(t, rec)
	if got.Status != "declined" || !strings.HasPrefix(got.DeclineReason, "Not in our collection") ||
		len([]rune(got.DeclineReason)) > requestReasonMaxRunes || strings.ContainsAny(got.DeclineReason, "\u0007\u202E") {
		t.Fatalf("declined = %+v", got)
	}
	if rec := decline(`{}`); rec.Code != http.StatusConflict {
		t.Fatalf("second decline: %d, want 409", rec.Code)
	}
	if rec := f.approve(f.admin, created.ID, `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("approve after decline: %d, want 409", rec.Code)
	}
}

// TestRequestsApprove_AuthorSearchOnAddKeepsTheSync: search on add runs inside
// the author's catalogue sync, and createAuthorCore refuses SearchOnAdd with
// SkipCatalogueSync. Approval must never send that pair, with the real core
// (which would answer errCreateAuthorSearchNeedsSync) and as recorded by the
// fake; and if the sentinel ever surfaces, the admin gets a sentence.
func TestRequestsApprove_AuthorSearchOnAddKeepsTheSync(t *testing.T) {
	t.Run("real core", func(t *testing.T) {
		f := newRequestsFixture(t, wellsRequestStub(), nil)
		created := decodeRequest(t, f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`))
		rec := f.approve(f.admin, created.ID, `{"searchOnAdd":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("approve with search on add: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("params", func(t *testing.T) {
		adder := &fakeAdder{}
		f := newRequestsFixture(t, wellsRequestStub(), adder)
		created := decodeRequest(t, f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`))
		if rec := f.approve(f.admin, created.ID, `{"searchOnAdd":true}`); rec.Code != http.StatusOK {
			t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
		}
		adder.mu.Lock()
		p := adder.lastAuthor
		adder.mu.Unlock()
		if !p.SearchOnAdd || p.SkipCatalogueSync {
			t.Fatalf("author params SearchOnAdd=%v SkipCatalogueSync=%v, want search with the sync", p.SearchOnAdd, p.SkipCatalogueSync)
		}
	})
	t.Run("sentinel is explained", func(t *testing.T) {
		adder := &fakeAdder{err: errCreateAuthorSearchNeedsSync}
		f := newRequestsFixture(t, wellsRequestStub(), adder)
		created := decodeRequest(t, f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`))
		rec := f.approve(f.admin, created.ID, `{"searchOnAdd":true}`)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(requestErrorBody(rec), "Search on add needs") {
			t.Fatalf("sentinel: %d %q, want 500 with the admin sentence", rec.Code, requestErrorBody(rec))
		}
		if row, _ := f.requests.GetByID(context.Background(), created.ID); row.Status != models.RequestStatusPending {
			t.Fatalf("status after the sentinel = %q, want pending", row.Status)
		}
	})
}

// requesterLibraryBookFields is the S1 allow list. Adding a field to
// requesterLibraryBook without adding it here fails the test below, which is
// the point: every field a requester can see is a decision.
var requesterLibraryBookFields = map[string]string{
	"ID":             "id",
	"Title":          "title",
	"AuthorName":     "authorName",
	"Series":         "series",
	"SeriesPosition": "seriesPosition",
	"CoverURL":       "coverUrl",
	"Status":         "status",
	"Formats":        "formats",
}

func TestRequesterLibraryBook_FieldAllowList(t *testing.T) {
	typ := reflect.TypeOf(requesterLibraryBook{})
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		fld := typ.Field(i)
		wantJSON, ok := requesterLibraryBookFields[fld.Name]
		if !ok {
			t.Errorf("requesterLibraryBook has field %s, which is not on the requester allow list", fld.Name)
			continue
		}
		if tag := strings.Split(fld.Tag.Get("json"), ",")[0]; tag != wantJSON {
			t.Errorf("field %s serialises as %q, want %q", fld.Name, tag, wantJSON)
		}
		if fld.Anonymous {
			t.Errorf("field %s is embedded, which would carry another type's fields", fld.Name)
		}
		seen[fld.Name] = true
	}
	for name := range requesterLibraryBookFields {
		if !seen[name] {
			t.Errorf("allow list names %s, which the type no longer has", name)
		}
	}
	// The response wrapper must carry only the projection.
	items, _ := reflect.TypeOf(requesterLibraryResponse{}).FieldByName("Items")
	if items.Type != reflect.TypeOf([]requesterLibraryBook{}) {
		t.Errorf("requesterLibraryResponse.Items is %v", items.Type)
	}
}

// TestRequestsLibrary_ProjectionCarriesNoPaths seeds a book with files under
// a marker path and asserts the marker appears nowhere in the response.
func TestRequestsLibrary_ProjectionCarriesNoPaths(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	ctx := context.Background()
	author := seedWells(t, f.authors)
	book := &models.Book{ForeignID: "OL27482W", AuthorID: author.ID, Title: "The War of the Worlds", SortTitle: "war of the worlds",
		Status: models.BookStatusImported, ImageURL: "https://covers.example/w.jpg"}
	if err := f.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	const marker = "/srv/secret-library-root"
	for _, s := range []string{
		`INSERT INTO book_files (book_id, format, path) VALUES (?, 'ebook', '` + marker + `/Wells/war.epub')`,
		`INSERT INTO book_files (book_id, format, path) VALUES (?, 'audiobook', '` + marker + `/Wells/war')`,
	} {
		if _, err := f.database.ExecContext(ctx, s, book.ID); err != nil {
			t.Fatal(err)
		}
	}
	other := &models.Book{ForeignID: "OL-TM", AuthorID: author.ID, Title: "The Time Machine", SortTitle: "time machine", Status: models.BookStatusWanted}
	if err := f.books.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	f.h.Library(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/v1/requests/library?search=war", nil), f.requester))
	if rec.Code != http.StatusOK {
		t.Fatalf("library: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{marker, "filePath", "ebookFilePath", "foreignBookId", "OL27482W", "monitored", "owner"} {
		if strings.Contains(body, leak) {
			t.Errorf("projection contains %q: %s", leak, body)
		}
	}
	var got requesterLibraryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("search war = %+v, want one book", got)
	}
	item := got.Items[0]
	if item.Title != "The War of the Worlds" || item.AuthorName != "H. G. Wells" || item.Status != "imported" ||
		!reflect.DeepEqual(item.Formats, []string{"ebook", "audiobook"}) || !strings.Contains(item.CoverURL, "/api/v1/images?url=") {
		t.Fatalf("item = %+v", item)
	}
}

func TestCleanRequestText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  The   War\tof\nthe Worlds ", "The War of the Worlds"},
		{"Evil\u202Egnirts\u200B", "Evilgnirts"},
		{"bell\u0007ring", "bellring"},
		{strings.Repeat("a", 400), strings.Repeat("a", 300)},
	}
	for _, c := range cases {
		if got := cleanRequestText(c.in, requestTitleMaxRunes); got != c.want {
			t.Errorf("cleanRequestText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
