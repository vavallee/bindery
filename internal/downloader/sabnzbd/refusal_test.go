package sabnzbd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMutationRefusalIsAnError covers #2192. SABnzbd answers a refused queue or
// history action with HTTP 200 and {"status": false, "error": "..."}, not a
// 4xx. Pause, Resume, Delete and DeleteHistory each decoded that body into a
// SimpleResponse and returned apiCall's nil, so every refusal reached Bindery
// as a success. The reported symptom was a post-import history delete that
// silently did nothing.
//
// Each case asserts the error names SAB and carries SAB's own reason, so the
// "cleanup failed" warning the importer already logs has something useful in it.
func TestMutationRefusalIsAnError(t *testing.T) {
	const reason = "nzo_id not found"

	cases := []struct {
		name string
		call func(*Client) error
	}{
		{"queue delete", func(c *Client) error { return c.Delete(context.Background(), "nzo_1", false) }},
		{"history delete", func(c *Client) error { return c.DeleteHistory(context.Background(), "nzo_1", false) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// The shape SAB actually sends for a refusal: 200 OK.
				_ = json.NewEncoder(w).Encode(SimpleResponse{Status: false, Error: reason})
			}))
			defer srv.Close()

			c := New("127.0.0.1", 0, "testkey", "", false)
			c.baseURL = srv.URL

			err := tc.call(c)
			if err == nil {
				t.Fatal("expected an error when SAB refuses the action, got nil")
			}
			if !strings.Contains(err.Error(), "SABnzbd") {
				t.Errorf("error should name the client, got %q", err)
			}
			if !strings.Contains(err.Error(), reason) {
				t.Errorf("error should carry SAB's reason %q, got %q", reason, err)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("error should name the refused action %q, got %q", tc.name, err)
			}
		})
	}
}

// TestMutationRefusalWithoutReason verifies the fallback wording when SAB
// refuses without filling in "error". This is the same fallback AddURL uses, so the
// message never degrades to a bare "SABnzbd rejected pause: ".
func TestMutationRefusalWithoutReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": false}`))
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = srv.URL

	err := c.DeleteHistory(context.Background(), "nzo_1", false)
	if err == nil {
		t.Fatal("expected an error when SAB refuses without a reason, got nil")
	}
	if !strings.Contains(err.Error(), "SABnzbd gave no reason") {
		t.Errorf("expected the no-reason fallback, got %q", err)
	}
}

// TestMutationSuccessIsNil pins the other half: an accepted action still
// returns nil, so the new check can't turn a working cleanup into a warning.
func TestMutationSuccessIsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SimpleResponse{Status: true})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = srv.URL

	if err := c.Delete(context.Background(), "nzo_1", true); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if err := c.DeleteHistory(context.Background(), "nzo_1", false); err != nil {
		t.Errorf("DeleteHistory: %v", err)
	}
}
