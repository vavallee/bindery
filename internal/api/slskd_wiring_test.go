package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestDownloadClientCreate_SlskdKeepsEmptyCategory(t *testing.T) {
	h, clients := downloadClientFixture(t)
	body := `{"name":"slskd","host":"10.10.10.10","port":5030,"type":"slskd","apiKey":"k","category":"","enabled":true}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var created models.DownloadClient
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	stored, err := clients.GetByID(context.Background(), created.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Category != "" || created.Category != "" {
		t.Errorf("slskd category = %q (stored %q), want empty", created.Category, stored.Category)
	}

	// Other types still default to "books".
	rec = httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(`{"name":"SAB","host":"10.10.10.11","type":"sabnzbd"}`)))
	var sab models.DownloadClient
	_ = json.NewDecoder(rec.Body).Decode(&sab)
	if sab.Category != "books" {
		t.Errorf("sabnzbd category = %q, want books", sab.Category)
	}
}

func TestOtherProtocol(t *testing.T) {
	for in, want := range map[string]string{"usenet": "torrent", "torrent": "usenet", "soulseek": ""} {
		if got := otherProtocol(in); got != want {
			t.Errorf("otherProtocol(%q) = %q, want %q", in, got, want)
		}
	}
}

// A soulseek release with no slskd client must never be explained by, or
// routed to, torrent or usenet clients.
func TestNoProtocolClientError_SoulseekHasNoOpposite(t *testing.T) {
	h, _, _, clients, _, ctx := queueFixture(t)
	if err := clients.Create(ctx, &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: "h", Port: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	err := h.noProtocolClientError(ctx, "soulseek")
	var nce *noClientError
	if !errors.As(err, &nce) {
		t.Fatalf("err = %T", err)
	}
	if !strings.Contains(nce.msg, "soulseek") || !strings.Contains(nce.msg, "slskd") || strings.Contains(nce.msg, "torrent") {
		t.Errorf("message %q", nce.msg)
	}

	// Every eligible client opted out of the media type: the answer is the
	// plain media type error, not a hint about another protocol.
	h2, _, _, clients2, _, ctx2 := queueFixture(t)
	if err := clients2.Create(ctx2, &models.DownloadClient{Name: "slskd", Type: "slskd", Host: "h", Port: 2, Enabled: true, EnabledForBooks: false, EnabledForAudiobooks: false}); err != nil {
		t.Fatal(err)
	}
	if err := clients2.Create(ctx2, &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: "h", Port: 1, Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true}); err != nil {
		t.Fatal(err)
	}
	_, err = h2.selectClients(ctx2, "soulseek", models.MediaTypeEbook)
	if err == nil || strings.Contains(err.Error(), "torrent") {
		t.Errorf("selectClients err = %v", err)
	}
}
