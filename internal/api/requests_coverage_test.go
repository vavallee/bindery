package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
)

// TestRequestsCreateCoverageValidation walks the refusals in front of the
// provider lookup: no identity, unknown fields, bad kind, bad foreign id and
// bad media type. None of them may store a request.
func TestRequestsCreateCoverageValidation(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})

	// A context with no user id (an install API key reaching the handler) is
	// refused: requests belong to people.
	rec := httptest.NewRecorder()
	f.h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/requests", strings.NewReader(`{"kind":"book","foreignId":"OL27482W"}`)))
	if rec.Code != http.StatusForbidden || !strings.Contains(requestErrorBody(rec), "Sign in") {
		t.Fatalf("anonymous create: %d %q, want 403", rec.Code, requestErrorBody(rec))
	}

	cases := []struct {
		name, body, want string
	}{
		{"unknown field", `{"kind":"book","foreignId":"OL27482W","title":"spoofed"}`, "A request takes only kind, foreignId and mediaType."},
		{"trailing data", `{"kind":"book","foreignId":"OL27482W"} {}`, "A request takes only kind, foreignId and mediaType."},
		{"bad kind", `{"kind":"series","foreignId":"OL27482W"}`, "kind must be book or author."},
		{"empty foreign id", `{"kind":"book","foreignId":"   "}`, "foreignId is missing or not a provider id."},
		{"control char in foreign id", `{"kind":"book","foreignId":"OL\u0001X"}`, "foreignId is missing or not a provider id."},
		{"overlong foreign id", `{"kind":"book","foreignId":"` + strings.Repeat("A", requestForeignIDMaxLen+1) + `"}`, "foreignId is missing or not a provider id."},
		{"bad media type", `{"kind":"book","foreignId":"OL27482W","mediaType":"vinyl"}`, "mediaType must be ebook, audiobook, both or empty."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := f.create(t, f.requester, c.body)
			if rec.Code != http.StatusBadRequest || requestErrorBody(rec) != c.want {
				t.Fatalf("%d %q, want 400 %q", rec.Code, requestErrorBody(rec), c.want)
			}
		})
	}
	if n, _ := f.requests.CountPendingByOwner(context.Background(), f.requester.ID); n != 0 {
		t.Fatalf("refused creates stored %d requests", n)
	}
}

// TestRequestsCreateCoverageProviderRateLimit: the provider bucket is spent
// by requesters only. Once it is empty a requester gets 429 with Retry-After,
// while an admin making the same call is not charged.
func TestRequestsCreateCoverageProviderRateLimit(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	f.h.WithProviderLimiter(auth.NewRequesterLimiter(1, 0.0001, time.Minute, 8))

	if rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second create: %d %s, want 429", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	if n, _ := f.requests.CountPendingByOwner(context.Background(), f.requester.ID); n != 1 {
		t.Fatalf("pending = %d, want only the first request", n)
	}
	// Admins are never charged from the requester bucket.
	if rec := f.create(t, f.admin, `{"kind":"book","foreignId":"OL27482W"}`); rec.Code != http.StatusCreated {
		t.Fatalf("admin create: %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// TestRequestsQueueCoverage covers the admin queue's status filter and its
// paging envelope.
func TestRequestsQueueCoverage(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	first := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	if rec := f.create(t, f.other, `{"kind":"book","foreignId":"OL27482W"}`); rec.Code != http.StatusCreated {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body.String())
	}
	if err := f.requests.Decline(context.Background(), first.ID, f.admin.ID, "no"); err != nil {
		t.Fatal(err)
	}

	queue := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.h.Queue(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/v1/requests/queue"+query, nil), f.admin))
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) requestListResponse {
		t.Helper()
		var out requestListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		return out
	}

	for _, c := range []struct {
		query      string
		wantTotal  int
		wantStatus string
	}{
		{"", 1, "pending"},
		{"?status=pending", 1, "pending"},
		{"?status=declined", 1, "declined"},
		{"?status=approved", 0, ""},
		{"?status=all", 2, ""},
	} {
		rec := queue(c.query)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: %d %s", c.query, rec.Code, rec.Body.String())
		}
		got := decode(rec)
		if got.Total != c.wantTotal || len(got.Items) != c.wantTotal {
			t.Fatalf("%q: total=%d items=%d, want %d", c.query, got.Total, len(got.Items), c.wantTotal)
		}
		if c.wantStatus != "" && got.Items[0].Status != c.wantStatus {
			t.Fatalf("%q: status = %q, want %q", c.query, got.Items[0].Status, c.wantStatus)
		}
	}

	paged := decode(queue("?status=all&limit=1&offset=1"))
	if paged.Total != 2 || len(paged.Items) != 1 || paged.Limit != 1 || paged.Offset != 1 {
		t.Fatalf("paged = %+v, want total 2, one item, limit 1 offset 1", paged)
	}

	rec := queue("?status=bogus")
	if rec.Code != http.StatusBadRequest || !strings.Contains(requestErrorBody(rec), "status must be") {
		t.Fatalf("bad status: %d %q", rec.Code, requestErrorBody(rec))
	}
}

// TestRequestsDeclineCoverageValidation covers the id, body and existence
// checks on decline.
func TestRequestsDeclineCoverageValidation(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))

	rec := httptest.NewRecorder()
	req := withURLParam(asUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)), f.admin), "id", "abc")
	f.h.Decline(rec, req)
	if rec.Code != http.StatusBadRequest || requestErrorBody(rec) != "invalid id" {
		t.Fatalf("bad id: %d %q", rec.Code, requestErrorBody(rec))
	}

	rec = httptest.NewRecorder()
	f.h.Decline(rec, withID(asUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"reason":"x","extra":1}`)), f.admin), created.ID))
	if rec.Code != http.StatusBadRequest || requestErrorBody(rec) != "invalid decline body" {
		t.Fatalf("bad body: %d %q", rec.Code, requestErrorBody(rec))
	}

	rec = httptest.NewRecorder()
	f.h.Decline(rec, withID(asUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)), f.admin), created.ID+999))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d %s, want 404", rec.Code, rec.Body.String())
	}

	// The refused calls left the request pending.
	got, err := f.requests.GetByID(context.Background(), created.ID)
	if err != nil || got == nil || got.Status != "pending" {
		t.Fatalf("request after refused declines = %+v, %v; want pending", got, err)
	}
}

// TestRequestsPendingCountCoverage: the admin badge counts pending requests
// across every user and drops a request once it is decided.
func TestRequestsPendingCountCoverage(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	count := func() int {
		t.Helper()
		rec := httptest.NewRecorder()
		f.h.PendingCount(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/v1/requests/pending-count", nil), f.admin))
		if rec.Code != http.StatusOK {
			t.Fatalf("pending count: %d %s", rec.Code, rec.Body.String())
		}
		var out map[string]int
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out["count"]
	}
	if n := count(); n != 0 {
		t.Fatalf("empty count = %d", n)
	}
	first := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	if rec := f.create(t, f.other, `{"kind":"book","foreignId":"OL27482W"}`); rec.Code != http.StatusCreated {
		t.Fatalf("second request: %d", rec.Code)
	}
	if n := count(); n != 2 {
		t.Fatalf("count = %d, want 2 across users", n)
	}
	if err := f.requests.Decline(context.Background(), first.ID, f.admin.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("count after decline = %d, want 1", n)
	}
	f.database.Close()
	rec := httptest.NewRecorder()
	f.h.PendingCount(rec, asUser(httptest.NewRequest(http.MethodGet, "/", nil), f.admin))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("repo error: %d, want 500", rec.Code)
	}
}
