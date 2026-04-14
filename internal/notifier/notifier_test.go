package notifier

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// testNotifier creates a Notifier with a nil repo and a custom HTTP client.
// Safe for tests that only exercise send() and Test() since those don't use the repo.
func testNotifier(httpClient *http.Client) *Notifier {
	return &Notifier{
		repo: nil,
		http: httpClient,
	}
}

func TestMatchesEvent(t *testing.T) {
	n := &Notifier{}
	tests := []struct {
		notif     models.Notification
		eventType string
		want      bool
	}{
		{models.Notification{OnGrab: true}, EventGrabbed, true},
		{models.Notification{OnGrab: false}, EventGrabbed, false},
		{models.Notification{OnImport: true}, EventBookImported, true},
		{models.Notification{OnImport: false}, EventBookImported, false},
		{models.Notification{OnFailure: true}, EventDownloadFailed, true},
		{models.Notification{OnFailure: false}, EventDownloadFailed, false},
		{models.Notification{OnHealth: true}, EventHealth, true},
		{models.Notification{OnHealth: false}, EventHealth, false},
		{models.Notification{OnUpgrade: true}, EventUpgrade, true},
		{models.Notification{OnUpgrade: false}, EventUpgrade, false},
		// Unknown event type always returns false.
		{models.Notification{OnGrab: true, OnImport: true}, "unknown", false},
	}

	for _, tt := range tests {
		got := n.matchesEvent(&tt.notif, tt.eventType)
		if got != tt.want {
			t.Errorf("matchesEvent(event=%q, onGrab=%v, onImport=%v, onFailure=%v, onHealth=%v, onUpgrade=%v) = %v, want %v",
				tt.eventType, tt.notif.OnGrab, tt.notif.OnImport, tt.notif.OnFailure,
				tt.notif.OnHealth, tt.notif.OnUpgrade, got, tt.want)
		}
	}
}

func TestSend_Success(t *testing.T) {
	var gotBody []byte
	var gotMethod string
	var gotContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{
		URL:    srv.URL,
		Method: "POST",
	}
	payload := map[string]interface{}{"event": "grabbed", "title": "Dune"}

	if err := n.send(context.Background(), notif, payload); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("Method: want POST, got %q", gotMethod)
	}
	if !strings.Contains(gotContentType, "application/json") {
		t.Errorf("Content-Type: want application/json, got %q", gotContentType)
	}
	if !strings.Contains(string(gotBody), "grabbed") {
		t.Errorf("body missing event field: %s", gotBody)
	}
}

func TestSend_DefaultsPOST(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: srv.URL, Method: ""} // empty → defaults to POST
	_ = n.send(context.Background(), notif, map[string]interface{}{})
	if gotMethod != "POST" {
		t.Errorf("expected POST default method, got %q", gotMethod)
	}
}

func TestSend_CustomHeaders(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom-Header")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{
		URL:     srv.URL,
		Method:  "POST",
		Headers: `{"X-Custom-Header": "my-secret"}`,
	}
	_ = n.send(context.Background(), notif, map[string]interface{}{})
	if gotHeader != "my-secret" {
		t.Errorf("custom header: want 'my-secret', got %q", gotHeader)
	}
}

func TestSend_EmptyHeaders(t *testing.T) {
	// Headers = "{}" should not cause an error (empty JSON object, no custom headers).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: srv.URL, Headers: "{}"}
	if err := n.send(context.Background(), notif, map[string]interface{}{}); err != nil {
		t.Fatalf("send with empty headers: %v", err)
	}
}

func TestSend_Non2xxStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: srv.URL}
	if err := n.send(context.Background(), notif, map[string]interface{}{}); err == nil {
		t.Fatal("expected error on 400 response")
	}
}

func TestSend_500Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: srv.URL}
	if err := n.send(context.Background(), notif, map[string]interface{}{}); err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestSend_NoURL(t *testing.T) {
	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: ""}
	if err := n.send(context.Background(), notif, map[string]interface{}{}); err == nil {
		t.Fatal("expected error when URL is empty")
	}
}

func TestSend_InvalidHeaders(t *testing.T) {
	// Invalid JSON in Headers should be silently skipped, not crash.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: srv.URL, Headers: "not-json"}
	// Should not error — invalid headers are silently ignored.
	if err := n.send(context.Background(), notif, map[string]interface{}{}); err != nil {
		t.Fatalf("send with invalid headers JSON: %v", err)
	}
}

func TestTest_Success(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Test() doesn't use repo — safe with nil.
	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: srv.URL, Method: "POST"}

	if err := n.Test(context.Background(), notif); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !strings.Contains(string(gotBody), "test") {
		t.Errorf("expected 'test' in body, got: %s", gotBody)
	}
}

func TestTest_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: srv.URL}
	if err := n.Test(context.Background(), notif); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestUserAgentHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testNotifier(&http.Client{})
	notif := &models.Notification{URL: srv.URL}
	_ = n.send(context.Background(), notif, map[string]interface{}{})

	if gotUA != "Bindery/1.0" {
		t.Errorf("User-Agent: want 'Bindery/1.0', got %q", gotUA)
	}
}
