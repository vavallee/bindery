package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// A Jackett download link needs jackett_apikey, which the search response no
// longer shows the client. The grab posts the redacted URL back and the
// download must still reach Jackett with the real key.
func TestQueueGrab_UnsealsCredentialsFromTheSearchResponse(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	var authorised atomic.Bool
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("jackett_apikey") != "JSECRET" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authorised.Store(true)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	defer jackett.Close()

	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-1"}})
	}))
	defer sab.Close()

	h, _, _, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}

	redacted := newznab.RedactDownloadURL(jackett.URL + "/dl/tracker/?jackett_apikey=JSECRET&path=abc&file=Dune")
	if strings.Contains(redacted, "JSECRET") {
		t.Fatalf("precondition: search response URL leaks the key: %q", redacted)
	}
	body, _ := json.Marshal(map[string]string{"guid": "guid-1", "title": "Dune", "nzbUrl": redacted})
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if !authorised.Load() {
		t.Fatal("the download did not reach Jackett with its key")
	}
	if strings.Contains(rec.Body.String(), "JSECRET") {
		t.Fatalf("grab response leaks the key: %s", rec.Body.String())
	}
}

func TestQueueGrab_RejectsASealThatDoesNotOpen(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	body := `{"guid":"g","title":"Dune","nzbUrl":"https://tracker.example/dl?passkey=bindery-sealed.AAAA"}`
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "search again") {
		t.Fatalf("error should tell the user what to do: %s", rec.Body.String())
	}
}
