package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// TestDownloadClientTest_KeepsPausedBlocklistingAdvisory pins that the Test
// button answers with what the health store shows, not the raw path probe.
// The UI replaces the client's health with this answer, so returning the raw
// probe made the importer's "automatic blocklisting is paused" advisory
// vanish from the page the moment the user pressed Test (#3024).
func TestDownloadClientTest_KeepsPausedBlocklistingAdvisory(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	qbit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.1.4"))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer qbit.Close()
	u, _ := url.Parse(qbit.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	h, clients := downloadClientFixture(t)
	store := downloader.NewHealthStore()
	h.WithHealth(store)
	client := &models.DownloadClient{Name: "qBit", Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Enabled: true}
	if err := clients.Create(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	store.SetAdvisory(client.ID, downloader.AdvisoryBlocklist, models.DownloadClientHealth{Status: downloader.HealthError, Message: "Automatic blocklisting is paused"})

	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/1/test", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Health *models.DownloadClientHealth `json:"health"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Health == nil || !strings.Contains(out.Health.Message, "Automatic blocklisting is paused") {
		t.Fatalf("Test answered health %+v, want it to keep the advisory", out.Health)
	}
}

// TestDownloadClientUpdate_ResetsPausedBlocklisting pins that editing a
// client, and disabling it, resets the importer's breaker and clears its
// advisory (#3024).
func TestDownloadClientUpdate_ResetsPausedBlocklisting(t *testing.T) {
	h, clients := downloadClientFixture(t)
	store := downloader.NewHealthStore()
	var resets []int64
	h.WithHealth(store).WithContentBreakerReset(func(id int64) { resets = append(resets, id) })
	ctx := context.Background()
	client := &models.DownloadClient{Name: "nzb", Type: "nzbget", Host: "10.1.2.3", Port: 6789, Enabled: true}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	store.SetAdvisory(client.ID, downloader.AdvisoryBlocklist, models.DownloadClientHealth{Status: downloader.HealthError, Message: "paused"})

	body := `{"enabled":false}`
	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/downloadclient/1", bytes.NewBufferString(body)), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(resets) == 0 || resets[0] != client.ID {
		t.Fatalf("breaker resets = %v, want client %d", resets, client.ID)
	}
	if h := store.Get(client.ID); h != nil {
		t.Fatalf("health after disabling = %+v, want the advisory cleared", h)
	}
}
