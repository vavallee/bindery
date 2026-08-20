package sabnzbd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAddURL_RejectionCarriesSABsReason covers the reporting half of #2105.
// AddURLResponse decoded only status and nzo_ids, so SAB's own explanation of
// a refused upload was thrown away and the grab failed as a bare "SABnzbd
// rejected download": the one component in the chain that behaved correctly,
// named without saying what it objected to.
//
// SAB purges the uploaded file before its own backup step, so the bytes that
// would explain it are gone by the time anyone looks. Its reply is the only
// place the reason survives.
func TestAddURL_RejectionCarriesSABsReason(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	const reason = "Error: Invalid NZB file Legendary.Rule.nzb, skipping"
	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(AddURLResponse{Status: false, Error: reason})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	_, err := c.AddURL(context.Background(), indexerSrv.URL+"/file.nzb", "Legendary Rule", "books", 0)
	if err == nil {
		t.Fatal("AddURL returned nil error for a refused upload")
	}
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("error = %q, want it to carry SAB's reason %q", err.Error(), reason)
	}
}

// TestAddURL_RejectionWithoutAReasonSaysSo pins the other branch. An older or
// differently configured SAB can answer status:false with no error string, and
// the message must not imply Bindery simply failed to read one. Saying SAB gave
// no reason points the next person at SAB's log rather than at Bindery's.
func TestAddURL_RejectionWithoutAReasonSaysSo(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(AddURLResponse{Status: false})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	_, err := c.AddURL(context.Background(), indexerSrv.URL+"/file.nzb", "Legendary Rule", "books", 0)
	if err == nil {
		t.Fatal("AddURL returned nil error for a refused upload")
	}
	if !strings.Contains(err.Error(), "gave no reason") {
		t.Errorf("error = %q, want it to say SABnzbd gave no reason", err.Error())
	}
}

// TestAddURL_WhitespaceOnlyReasonIsTreatedAsNone guards the trim: a reason of
// spaces would otherwise produce "rejected download: " with nothing after it,
// which reads as a truncated message rather than an absent one.
func TestAddURL_WhitespaceOnlyReasonIsTreatedAsNone(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(AddURLResponse{Status: false, Error: "   "})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	_, err := c.AddURL(context.Background(), indexerSrv.URL+"/file.nzb", "Legendary Rule", "books", 0)
	if err == nil {
		t.Fatal("AddURL returned nil error for a refused upload")
	}
	if strings.HasSuffix(err.Error(), ": ") || strings.HasSuffix(err.Error(), ":") {
		t.Errorf("error = %q, want no dangling colon for a blank reason", err.Error())
	}
	if !strings.Contains(err.Error(), "gave no reason") {
		t.Errorf("error = %q, want it to say SABnzbd gave no reason", err.Error())
	}
}
