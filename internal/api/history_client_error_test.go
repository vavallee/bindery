package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// clientErrorSamples are download client failures as Go's network stack and
// the clients word them. Each names where the client lives.
var clientErrorSamples = []string{
	"could not reach SABnzbd at http://10.9.8.7:8085: connection refused",
	"load.raw_start: dial tcp 10.0.0.5:5000: connect: connection refused",
	"d.multicall2: dial tcp [fd00::5]:5000: i/o timeout",
	"dial tcp: lookup qbittorrent on 127.0.0.11:53: no such host",
	"deluge daemon at deluged:58846 is not connected",
}

// ownMessages are Bindery's own failure texts, which must reach every user.
var ownMessages = []string{
	"stalled: no peers / no download progress",
	"no book files found in \"/downloads/Bob Book\"",
	"Download failed: out of retention",
	// SABnzbd's own content failure carries a documentation link, which is
	// not the client's address.
	"Aborted, cannot be completed - https://sabnzbd.org/not-complete",
	"stalled since 2026-10-29T10:30:00Z",
	"metadata lookup failed for this release",
}

func historyMessagesAs(t *testing.T, h *HistoryHandler, role string) (map[string]string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history", nil).WithContext(withAuthCtx(context.Background(), 7, role))
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s history: %d %s", role, rec.Code, rec.Body.String())
	}
	var resp historyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, it := range resp.Items {
		var data map[string]any
		_ = json.Unmarshal([]byte(it.Data), &data)
		msg, _ := data["message"].(string)
		out[it.SourceTitle] = msg
	}
	return out, rec.Body.String()
}

// TestHistoryListMasksClientErrorsForNonAdmins: a downloadFailed event
// written before send failures got fixed text carries the client's own error.
// A caller who is not an admin gets the generic send failure text instead;
// Bindery's own failure messages, and every message for an admin, are shown
// as stored.
func TestHistoryListMasksClientErrorsForNonAdmins(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repo := db.NewHistoryRepo(database)
	ctx := context.Background()
	all := map[string]string{}
	for i, msg := range append(append([]string{}, clientErrorSamples...), ownMessages...) {
		title := "row-" + strconv.Itoa(i)
		all[title] = msg
		data, _ := json.Marshal(map[string]string{"guid": title, "message": msg})
		if err := repo.Create(ctx, &models.HistoryEvent{EventType: models.HistoryEventDownloadFailed, SourceTitle: title, Data: string(data)}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHistoryHandler(repo, db.NewBlocklistRepo(database), db.NewBookRepo(database))

	user, _ := historyMessagesAs(t, h, "user")
	admin, _ := historyMessagesAs(t, h, "admin")
	for title, msg := range all {
		isClient := false
		for _, s := range clientErrorSamples {
			isClient = isClient || s == msg
		}
		wantUser := msg
		if isClient {
			wantUser = sendFailedMessage
		}
		if user[title] != wantUser {
			t.Errorf("user message for %q = %q, want %q", msg, user[title], wantUser)
		}
		if admin[title] != msg {
			t.Errorf("admin message = %q, want %q", admin[title], msg)
		}
	}
}

// TestQueueGrabSendFailureHistoryAndWebhookUseFixedText: a grab the client
// cannot be reached for writes the generic send failure text, plus Bindery's
// own network hint, to History and to the failure webhook. History is read by
// every account and a webhook leaves the house, so neither names the client's
// address, for an admin either; the client's own error is in the log line and
// on the download row.
func TestQueueGrabSendFailureHistoryAndWebhookUseFixedText(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	defer indexerSrv.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	host, port := testServerHostPort(t, dead.URL)
	dead.Close() // connection refused from here on
	address := host + ":" + strconv.Itoa(port)

	var mu sync.Mutex
	var hooks []string
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hooks = append(hooks, string(b))
		mu.Unlock()
	}))
	defer hookSrv.Close()

	h, database, _, clients, _, ctx := queueFixture(t)
	notifs := db.NewNotificationRepo(database)
	if err := notifs.Create(ctx, &models.Notification{Name: "hook", Type: "webhook", URL: hookSrv.URL, Enabled: true, OnFailure: true}); err != nil {
		t.Fatal(err)
	}
	n := notifier.New(notifs)
	n.SetValidator(nil)
	h.WithNotifier(n)
	if err := clients.Create(ctx, &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true}); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"guid":"hist-fail-guid","nzbUrl":"` + indexerSrv.URL + `/x.nzb","title":"Some Title"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("grab: %d %s", rec.Code, rec.Body.String())
	}

	hh := NewHistoryHandler(db.NewHistoryRepo(database), db.NewBlocklistRepo(database), db.NewBookRepo(database))
	for _, role := range []string{"user", "admin"} {
		msgs, raw := historyMessagesAs(t, hh, role)
		if strings.Contains(raw, address) {
			t.Errorf("%s history names the client address %s: %s", role, address, raw)
		}
		if got := msgs["Some Title"]; !strings.HasPrefix(got, sendFailedMessage) {
			t.Errorf("%s history message = %q, want the generic send failure text", role, got)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hooks) != 1 {
		t.Fatalf("webhook calls = %d, want 1", len(hooks))
	}
	if strings.Contains(hooks[0], address) || !strings.Contains(hooks[0], sendFailedMessage) {
		t.Errorf("webhook payload names the client address or lacks the generic text: %s", hooks[0])
	}
}
