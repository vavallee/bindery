package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// testNotifier creates a Notifier with a nil repo and a custom HTTP client.
// Safe for tests that only exercise send() and Test() since those don't use the repo.
// validate is left nil so loopback URLs from httptest.NewServer are accepted.
func testNotifier(httpClient *http.Client) *Notifier {
	return &Notifier{
		repo:     nil,
		http:     httpClient,
		validate: nil,
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

// TestSend_AppriseFields verifies the payload sent over the wire carries a
// "body" (and a "title") so Apprise's REST API accepts it, while leaving the
// caller's original fields untouched for other webhook consumers.
func TestSend_AppriseFields(t *testing.T) {
	tests := []struct {
		name      string
		payload   map[string]interface{}
		wantBody  string
		wantTitle string
	}{
		{
			name:      "title only (grab/import) → body falls back to title",
			payload:   map[string]interface{}{"title": "Dune", "author": "Herbert"},
			wantBody:  "Dune",
			wantTitle: "Dune",
		},
		{
			name:      "message only (test/health) → body from message, default title",
			payload:   map[string]interface{}{"eventType": "test", "message": "hello"},
			wantBody:  "hello",
			wantTitle: "Bindery",
		},
		{
			name:      "explicit body is preserved",
			payload:   map[string]interface{}{"body": "explicit", "message": "ignored"},
			wantBody:  "explicit",
			wantTitle: "Bindery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			n := testNotifier(&http.Client{})
			notif := &models.Notification{URL: srv.URL, Method: "POST"}
			if err := n.send(context.Background(), notif, tt.payload); err != nil {
				t.Fatalf("send: %v", err)
			}

			var got map[string]interface{}
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Fatalf("unmarshal sent body: %v (%s)", err, gotBody)
			}
			if got["body"] != tt.wantBody {
				t.Errorf("body = %v, want %q", got["body"], tt.wantBody)
			}
			if got["title"] != tt.wantTitle {
				t.Errorf("title = %v, want %q", got["title"], tt.wantTitle)
			}
			// Caller's original keys must survive untouched.
			for k, v := range tt.payload {
				if got[k] != v {
					t.Errorf("original key %q = %v, want %v", k, got[k], v)
				}
			}
		})
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

func TestNew_DefaultValidator(t *testing.T) {
	// New() wires in the production SSRF validator (strict policy). Confirm
	// that a loopback URL is rejected, proving the validator is plumbed.
	n := New(nil)
	if n == nil {
		t.Fatal("New returned nil")
		return
	}
	if n.validate == nil {
		t.Fatal("New did not install a validator")
	}
	if err := n.validate("http://127.0.0.1/hook"); err == nil {
		t.Error("default validator should reject loopback")
	}
}

func TestSetValidator_Override(t *testing.T) {
	n := New(nil)
	called := 0
	n.SetValidator(func(string) error {
		called++
		return nil
	})
	// send() with a no-op validator should now accept loopback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	notif := &models.Notification{URL: srv.URL}
	if err := n.send(context.Background(), notif, map[string]interface{}{}); err != nil {
		t.Fatalf("send with override: %v", err)
	}
	if called != 1 {
		t.Errorf("overridden validator calls: want 1, got %d", called)
	}
}

func TestSend_ValidatorRejects(t *testing.T) {
	// send() must return the validator's error without making an HTTP call.
	n := New(nil)
	// Point at a public URL so only the validator rejection path fires.
	notif := &models.Notification{URL: "http://10.0.0.1/hook"} // RFC1918 → strict rejects
	if err := n.send(context.Background(), notif, map[string]interface{}{}); err == nil {
		t.Fatal("expected validator to reject RFC1918 under strict policy")
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

	// Notifier now uses the shared lowercase UA helper. Assert the prefix
	// rather than the exact string so the OS suffix doesn't pin the test.
	if !strings.HasPrefix(gotUA, "bindery/") {
		t.Errorf("User-Agent: want prefix 'bindery/', got %q", gotUA)
	}
	if strings.Contains(gotUA, "Bindery") {
		t.Errorf("User-Agent must be lowercase to clear nzbfinder.ws WAF; got %q", gotUA)
	}
}
