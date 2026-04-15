package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

func notificationFixture(t *testing.T) (*NotificationHandler, *db.NotificationRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewNotificationRepo(database)
	n := notifier.New(repo)
	// Disable SSRF validation so httptest.NewServer (loopback) works in tests.
	n.SetValidator(nil)
	return NewNotificationHandler(repo, n), repo
}

func TestNotificationList_Empty(t *testing.T) {
	h, _ := notificationFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/notification", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}
}

func TestNotificationCRUD(t *testing.T) {
	h, _ := notificationFixture(t)

	// RFC5737 TEST-NET-3 IP literal: public range, not blocked by Strict policy,
	// and skips DNS entirely in the test environment.
	body := `{"name":"webhook","url":"http://203.0.113.1/hook","enabled":true,"onGrab":true}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/notification", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.Notification
	json.NewDecoder(rec.Body).Decode(&created)
	if created.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// Get
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/notification/1", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", rec.Code)
	}

	// Get — bad id
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/notification/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("get bad id: expected 400, got %d", rec.Code)
	}

	// Get — missing
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/notification/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get missing: expected 404, got %d", rec.Code)
	}

	// Update
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/notification/1",
			bytes.NewBufferString(`{"name":"renamed","url":"http://203.0.113.2/"}`)),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Errorf("update: expected 200, got %d", rec.Code)
	}

	// Update — bad id
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/notification/abc", bytes.NewBufferString(`{}`)),
		"id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update bad id: expected 400, got %d", rec.Code)
	}

	// Update — missing
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/notification/999", bytes.NewBufferString(`{"name":"x"}`)),
		"id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("update missing: expected 404, got %d", rec.Code)
	}

	// Update — invalid body on existing row
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/notification/1", bytes.NewBufferString(`not-json`)),
		"id", "1"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update bad body: expected 400, got %d", rec.Code)
	}

	// Delete
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/notification/1", nil), "id", "1"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", rec.Code)
	}

	// Delete — bad id
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/notification/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("delete bad id: expected 400, got %d", rec.Code)
	}
}

func TestNotificationCreate_Validation(t *testing.T) {
	h, _ := notificationFixture(t)
	for _, tc := range []struct {
		body, desc string
	}{
		{`not-json`, "invalid json"},
		{`{}`, "missing name and url"},
		{`{"name":"x"}`, "missing url"},
		{`{"url":"http://x"}`, "missing name"},
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/notification", bytes.NewBufferString(tc.body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", tc.desc, rec.Code)
		}
	}
}

func TestNotificationTest_NotFound(t *testing.T) {
	h, _ := notificationFixture(t)

	// Bad id
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/notification/abc/test", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: expected 400, got %d", rec.Code)
	}

	// Missing
	rec = httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/notification/999/test", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing: expected 404, got %d", rec.Code)
	}
}

// TestNotificationTest_Webhook runs a real HTTP test against a local server so
// Notifier.send is exercised and the 200 path is covered.
func TestNotificationTest_Webhook(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	h, repo := notificationFixture(t)
	n := &models.Notification{
		Name:    "webhook",
		URL:     srv.URL,
		Enabled: true,
	}
	if err := repo.Create(contextBackground(), n); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(
		httptest.NewRequest(http.MethodPost, "/api/v1/notification/1/test", nil),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, "Bindery notification test") {
		t.Errorf("expected test payload to be delivered, got %q", gotBody)
	}
}
