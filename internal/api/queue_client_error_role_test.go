package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

func listQueueAs(t *testing.T, h *QueueHandler, userID int64, role string) (queueListResponse, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil).WithContext(withAuthCtx(context.Background(), userID, role))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s queue list: %d %s", role, rr.Code, rr.Body.String())
	}
	var resp queueListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp, rr.Body.String()
}

// TestQueueStaleClientMessageByRole: a download client that cannot be polled
// is named to everyone, but its own error, which carries the client's address,
// only reaches an admin. Download client configuration is admin only, and the
// rule is the caller's role, so it holds with tenancy on and off.
func TestQueueStaleClientMessageByRole(t *testing.T) {
	for _, tenancy := range []bool{false, true} {
		t.Run("tenancy="+strconv.FormatBool(tenancy), func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tenancy)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			host, port := testServerHostPort(t, srv.URL)
			srv.Close() // connection refused from here on
			address := host + ":" + strconv.Itoa(port)

			h, database, _, _, _, ctx := queueFixture(t)
			bob, err := db.NewUserRepo(database).Create(ctx, "bob", "h")
			if err != nil {
				t.Fatal(err)
			}
			client := createTestDownloadClient(t, h, &models.DownloadClient{
				Name: "internal-sab", Type: "sabnzbd", Host: host, Port: port,
				APIKey: "SECRETSABKEY", Category: "books", Enabled: true,
			})
			dl := &models.Download{
				GUID: "guid-bob", DownloadClientID: &client.ID, Title: "Bob Book",
				NZBURL: "https://example.com/b.nzb", Status: models.DownloadStatusDownloading,
				Protocol: "usenet", SABnzbdNzoID: strPtr("nzobob"), OwnerUserID: bob.ID,
			}
			createTestDownload(t, h, dl)

			resp, body := listQueueAs(t, h, bob.ID, "user")
			if strings.Contains(body, address) || strings.Contains(body, "SECRETSABKEY") {
				t.Errorf("user queue response names the download client address or key: %s", body)
			}
			if len(resp.StaleClients) != 1 {
				t.Fatalf("user staleClients = %+v, want one", resp.StaleClients)
			}
			if got := resp.StaleClients[0]; got.Name != "internal-sab" || got.Message != "download client unreachable" {
				t.Errorf("user stale client = %+v, want name internal-sab and %q", got, "download client unreachable")
			}

			resp, _ = listQueueAs(t, h, 1, "admin")
			if len(resp.StaleClients) != 1 || !strings.Contains(resp.StaleClients[0].Message, address) {
				t.Errorf("admin stale client = %+v, want the client's own error naming %s", resp.StaleClients, address)
			}
		})
	}
}

// TestQueueErrorMessageClientTextByRole: a download row's error message that
// came from the download client is generic for a caller who is not an admin,
// whether it was stored with the marker or before it existed, while Bindery's
// own messages pass through for everyone. An admin sees the stored text
// without the marker.
func TestQueueErrorMessageClientTextByRole(t *testing.T) {
	h := newQueueTestHandler(t)
	clientText := "could not reach SABnzbd at http://10.9.8.7:8085 — dial tcp 10.9.8.7:8085: connect: connection refused"
	rows := map[string]string{
		"marked": downloader.SendFailureMessage(errors.New(clientText)),
		"legacy": clientText,
		"own":    "no book files found in \"/downloads/Bob Book\"",
	}
	var legacy []string
	for i, sample := range clientErrorSamples {
		guid := "legacy-" + strconv.Itoa(i)
		rows[guid] = sample
		legacy = append(legacy, guid)
	}
	var own []string
	for i, m := range ownMessages {
		guid := "own-" + strconv.Itoa(i)
		rows[guid] = m
		own = append(own, guid)
	}
	for guid, msg := range rows {
		createTestDownload(t, h, &models.Download{
			GUID: guid, Title: guid, NZBURL: "https://example.com/" + guid + ".nzb",
			Status: models.StateFailed, Protocol: "usenet", ErrorMessage: msg,
		})
	}
	byGUID := func(resp queueListResponse) map[string]string {
		out := map[string]string{}
		for _, it := range resp.Items {
			out[it.GUID] = it.ErrorMessage
		}
		return out
	}

	user, _ := listQueueAs(t, h, 2, "user")
	got := byGUID(user)
	for _, guid := range append([]string{"marked", "legacy"}, legacy...) {
		if got[guid] != sendFailedMessage {
			t.Errorf("user %s row errorMessage = %q, want %q", guid, got[guid], sendFailedMessage)
		}
	}
	for _, guid := range append([]string{"own"}, own...) {
		if got[guid] != rows[guid] {
			t.Errorf("user %s row errorMessage = %q, want it unchanged", guid, got[guid])
		}
	}

	admin, _ := listQueueAs(t, h, 1, "admin")
	got = byGUID(admin)
	for guid := range rows {
		want := rows[guid]
		if guid == "marked" {
			want = clientText
		}
		if got[guid] != want {
			t.Errorf("admin %s row errorMessage = %q, want %q", guid, got[guid], want)
		}
	}
}

// TestQueueGrabSendFailureStoredForRoleRendering: a grab the client refuses
// stores the client's text marked as such, so the queue gives a user the
// generic message even when that text carries no URL, and an admin the
// client's own words.
func TestQueueGrabSendFailureStoredForRoleRendering(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	defer indexerSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": false})
	}))
	defer srv.Close()

	h, _, _, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, srv.URL)
	if err := clients.Create(ctx, &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true}); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"guid":"send-fail-guid","nzbUrl":"` + indexerSrv.URL + `/x.nzb","title":"Some Title"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("grab: %d %s", rec.Code, rec.Body.String())
	}

	find := func(resp queueListResponse) string {
		for _, it := range resp.Items {
			if it.GUID == "send-fail-guid" {
				return it.ErrorMessage
			}
		}
		t.Fatal("grabbed row not in the queue")
		return ""
	}
	user, _ := listQueueAs(t, h, 2, "user")
	if got := find(user); got != sendFailedMessage {
		t.Errorf("user errorMessage = %q, want %q", got, sendFailedMessage)
	}
	admin, _ := listQueueAs(t, h, 1, "admin")
	if got := find(admin); !strings.Contains(got, "SABnzbd rejected download") || strings.HasPrefix(got, "download client error") {
		t.Errorf("admin errorMessage = %q, want the client's own text unmarked", got)
	}
}

// TestQueueErrorMessageImportFailuresPassThrough: an import failure's message
// is Bindery's own and can hold a path that looks like a host:port or a DNS
// error. Only a row in the failed state, where a send failure lands, is read
// for client text, so these reach a user unchanged.
func TestQueueErrorMessageImportFailuresPassThrough(t *testing.T) {
	h := newQueueTestHandler(t)
	rows := map[string]struct {
		status models.DownloadState
		msg    string
	}{
		"vol":    {models.StateImportFailed, "no book files found in \"/downloads/Vol.2:10\""},
		"host":   {models.StateImportBlocked, "audiobook destination invalid: open /library/qbittorrent:8080/x: permission denied"},
		"lookup": {models.StateImportFailed, "dial tcp: lookup of the edition on the shelf failed"},
	}
	for guid, r := range rows {
		createTestDownload(t, h, &models.Download{
			GUID: guid, Title: guid, NZBURL: "https://example.com/" + guid + ".nzb",
			Status: r.status, Protocol: "usenet", ErrorMessage: r.msg,
		})
	}
	resp, _ := listQueueAs(t, h, 2, "user")
	got := map[string]string{}
	for _, it := range resp.Items {
		got[it.GUID] = it.ErrorMessage
	}
	for guid, r := range rows {
		if got[guid] != r.msg {
			t.Errorf("user %s row (%s) errorMessage = %q, want it unchanged", guid, r.status, got[guid])
		}
	}
}
