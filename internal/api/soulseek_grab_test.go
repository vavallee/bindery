package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/downloader/slskd/slskdtest"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// slskdGrabFixture is a queue handler with an enabled slskd client backed by the
// fake, and a SABnzbd that fails the test if anything reaches it.
func slskdGrabFixture(t *testing.T) (*QueueHandler, *slskdtest.Fake, *models.DownloadClient, context.Context, func(string) *models.Download, *atomic.Int32) {
	h, _, fake, client, ctx, get, calls := slskdGrabFixtureDB(t)
	return h, fake, client, ctx, get, calls
}

func slskdGrabFixtureDB(t *testing.T) (*QueueHandler, *sql.DB, *slskdtest.Fake, *models.DownloadClient, context.Context, func(string) *models.Download, *atomic.Int32) {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	h, database, downloads, clients, _, ctx := queueFixture(t)
	fake, srv := slskdtest.New()
	t.Cleanup(srv.Close)
	host, port := testServerHostPort(t, srv.URL)
	client := &models.DownloadClient{Name: "slskd", Type: "slskd", Host: host, Port: port, APIKey: "test-key",
		Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	var sabCalls atomic.Int32
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sabCalls.Add(1)
		_, _ = w.Write([]byte(`{"status":true,"nzo_ids":["x"]}`))
	}))
	t.Cleanup(sab.Close)
	sh, sp := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: sh, Port: sp, Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true}); err != nil {
		t.Fatal(err)
	}
	get := func(guid string) *models.Download {
		d, err := downloads.GetByGUID(ctx, guid)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	return h, database, fake, client, ctx, get, &sabCalls
}

func mustAddr(t *testing.T, g slskd.Grab) string {
	t.Helper()
	a, err := slskd.EncodeGrab(g)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func slskdGrabBody(guid, title, addr string, extra string) *bytes.Buffer {
	return bytes.NewBufferString(fmt.Sprintf(`{"guid":%q,"title":%q,"nzbUrl":%q,"protocol":"soulseek"%s}`, guid, title, addr, extra))
}

func TestGrab_SoulseekCreatesRowAndStartsBatch(t *testing.T) {
	h, fake, client, _, get, sabCalls := slskdGrabFixture(t)
	addr := mustAddr(t, slskd.Grab{User: "peer1", Files: []slskd.BatchFile{{Filename: `@@p\Dune\Dune.epub`, Size: 2048}}})

	// An indexer id posted with a Soulseek grab is ignored: no indexer made it.
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/queue/grab", slskdGrabBody("slskd:abc", "Dune [EPUB]", addr, `,"indexerId":5,"mediaType":"audiobook"`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	dl := get("slskd:abc")
	if dl == nil {
		t.Fatal("no download row")
	}
	if dl.Protocol != "soulseek" || dl.IndexerID != nil || dl.DownloadClientID == nil || *dl.DownloadClientID != client.ID {
		t.Errorf("row = protocol %q indexer %v client %v", dl.Protocol, dl.IndexerID, dl.DownloadClientID)
	}
	if dl.Status != models.StateDownloading || dl.SABnzbdNzoID == nil || *dl.SABnzbdNzoID == "" || dl.TorrentID != nil {
		t.Errorf("status %q nzo %v torrent %v", dl.Status, dl.SABnzbdNzoID, dl.TorrentID)
	}
	// The files decide the slot, not what the caller said.
	if dl.MediaType != models.MediaTypeEbook {
		t.Errorf("media type = %q, want ebook from the .epub", dl.MediaType)
	}
	b := fake.Batch(*dl.SABnzbdNzoID)
	if b == nil || b.Destination != fmt.Sprintf("bindery/%d", dl.ID) || b.Username != "peer1" || len(b.Transfers) != 1 {
		t.Fatalf("slskd got %+v", b)
	}
	if strings.Contains(b.Destination, "Dune") || strings.Contains(b.Destination, "peer1") {
		t.Errorf("destination %q names the title or the peer", b.Destination)
	}
	if sabCalls.Load() != 0 {
		t.Errorf("a Soulseek grab reached SABnzbd %d times", sabCalls.Load())
	}
	// The response never carries the address, which lists the peer's paths.
	if strings.Contains(rec.Body.String(), addr) || !strings.Contains(rec.Body.String(), slskd.RedactedGrabURL) {
		t.Errorf("response leaks or drops the address: %s", rec.Body.String())
	}
	// History records the grab, with no indexer.
	events, err := h.history.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var grabbed int
	for _, e := range events {
		if e.EventType == models.HistoryEventGrabbed && e.SourceTitle == "Dune [EPUB]" {
			grabbed++
			if strings.Contains(e.Data, `"indexerId":5`) {
				t.Errorf("history attributes an indexer: %s", e.Data)
			}
		}
	}
	if grabbed != 1 {
		t.Errorf("grabbed events = %d", grabbed)
	}
}

// Every shape of hostile or oversized request is refused before a row exists
// and before slskd hears of it.
func TestGrab_SoulseekRefusesBadAddresses(t *testing.T) {
	h, fake, _, _, get, sabCalls := slskdGrabFixture(t)

	// The encoder refuses to build a hostile address, so build them by hand.
	handcrafted := func(g slskd.Grab) string {
		raw, _ := json.Marshal(g)
		return slskd.GrabScheme + base64RawURL(raw)
	}
	many := make([]slskd.BatchFile, slskd.MaxGrabFiles+1)
	for i := range many {
		many[i] = slskd.BatchFile{Filename: "f" + strconv.Itoa(i), Size: 1}
	}
	cases := map[string]string{
		"traversal":    handcrafted(slskd.Grab{User: "u", Files: []slskd.BatchFile{{Filename: `@@u\..\..\etc\passwd.epub`, Size: 10}}}),
		"absolute":     handcrafted(slskd.Grab{User: "u", Files: []slskd.BatchFile{{Filename: `/etc/passwd.epub`, Size: 10}}}),
		"too many":     handcrafted(slskd.Grab{User: "u", Files: many}),
		"too big":      handcrafted(slskd.Grab{User: "u", Files: []slskd.BatchFile{{Filename: "a.epub", Size: slskd.MaxGrabBytes + 1}}}),
		"no files":     handcrafted(slskd.Grab{User: "u"}),
		"not an addr":  "http://x/y.nzb",
		"redacted one": slskd.RedactedGrabURL,
	}
	for name, addr := range cases {
		rec := httptest.NewRecorder()
		h.Grab(rec, httptest.NewRequest(http.MethodPost, "/queue/grab", slskdGrabBody("slskd:"+name, "t", addr, "")))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
		if get("slskd:"+name) != nil {
			t.Errorf("%s: a row was created", name)
		}
	}
	if fake.Count("POST ") != 0 {
		t.Errorf("a refused grab reached slskd %d times", fake.Count("POST "))
	}
	if sabCalls.Load() != 0 {
		t.Error("a refused Soulseek grab reached SABnzbd")
	}
	// The inner path refuses too (pending and retry callers reach grab directly).
	if _, err := h.grab(context.Background(), grabRequest{GUID: "slskd:inner", Title: "t", Protocol: "soulseek", NZBURL: "http://x/y"}); err == nil {
		t.Error("grab() accepted a non-Soulseek address for a Soulseek result")
	}
}

func TestGrab_SoulseekOfflinePeerFailsTheRowCleanly(t *testing.T) {
	h, fake, _, _, get, _ := slskdGrabFixture(t)
	fake.Offline = true
	addr := mustAddr(t, slskd.Grab{User: "peer1", Files: []slskd.BatchFile{{Filename: "a.epub", Size: 100}}})
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/queue/grab", slskdGrabBody("slskd:off", "t", addr, "")))
	if rec.Code < 400 {
		t.Fatalf("status %d", rec.Code)
	}
	dl := get("slskd:off")
	if dl == nil || dl.Status != models.StateFailed || dl.ErrorMessage == "" {
		t.Fatalf("row = %+v", dl)
	}
}

// A retry of a failed Soulseek row sends the files again as a fresh batch.
func TestRetry_SoulseekResendsAsANewBatch(t *testing.T) {
	h, fake, _, ctx, get, _ := slskdGrabFixture(t)
	addr := mustAddr(t, slskd.Grab{User: "peer1", Files: []slskd.BatchFile{{Filename: "a.epub", Size: 100}}})
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/queue/grab", slskdGrabBody("slskd:retry", "t", addr, "")))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	dl := get("slskd:retry")
	first := *dl.SABnzbdNzoID
	if err := h.downloads.SetError(ctx, dl.ID, "stalled"); err != nil {
		t.Fatal(err)
	}
	dl = get("slskd:retry")
	if _, err := h.resendRelease(ctx, *dl); err != nil {
		t.Fatalf("resend: %v", err)
	}
	dl = get("slskd:retry")
	if dl.Status != models.StateDownloading || *dl.SABnzbdNzoID == first {
		t.Errorf("row after retry: status %q id %q (was %q)", dl.Status, *dl.SABnzbdNzoID, first)
	}
	if fake.Count("POST ") != 2 {
		t.Errorf("POST count %d", fake.Count("POST "))
	}
}

func TestQueue_SoulseekRowShowsPeerAndLiveState(t *testing.T) {
	h, fake, _, _, get, _ := slskdGrabFixture(t)
	addr := mustAddr(t, slskd.Grab{User: "peer1", Files: []slskd.BatchFile{{Filename: "a.epub", Size: 100}, {Filename: "b.epub", Size: 100}}})
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/queue/grab", slskdGrabBody("slskd:q", "Dune [EPUB]", addr, "")))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("grab %d: %s", rec.Code, rec.Body.String())
	}
	dl := get("slskd:q")
	fake.SetPlace(*dl.SABnzbdNzoID, 4)

	list := httptest.NewRecorder()
	h.List(list, httptest.NewRequest(http.MethodGet, "/queue", nil))
	var resp struct {
		Items []struct {
			Status     string `json:"status"`
			Protocol   string `json:"protocol"`
			NZBURL     string `json:"nzbUrl"`
			Percentage string `json:"percentage"`
			Soulseek   *struct {
				User          string `json:"user"`
				State         string `json:"state"`
				Files         int    `json:"files"`
				QueuePosition *int   `json:"queuePosition"`
			} `json:"soulseek"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %s", list.Body.String())
	}
	it := resp.Items[0]
	if it.Soulseek == nil || it.Soulseek.User != "peer1" || it.Soulseek.State != "waiting" || it.Soulseek.Files != 2 ||
		it.Soulseek.QueuePosition == nil || *it.Soulseek.QueuePosition < 1 {
		t.Errorf("soulseek = %+v", it.Soulseek)
	}
	if it.Status != "downloading" || it.Protocol != "soulseek" || it.NZBURL != slskd.RedactedGrabURL {
		t.Errorf("row = %+v", it)
	}
	if strings.Contains(list.Body.String(), "a.epub") {
		t.Errorf("the queue response lists peer file paths: %s", list.Body.String())
	}

	// With slskd unreachable the row still shows the peer, and no live state.
	fake.Down = true
	list = httptest.NewRecorder()
	h.List(list, httptest.NewRequest(http.MethodGet, "/queue", nil))
	var again struct {
		Items []struct {
			Soulseek *struct {
				User  string `json:"user"`
				State string `json:"state"`
			} `json:"soulseek"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &again); err != nil || len(again.Items) != 1 {
		t.Fatalf("list after outage: %v %s", err, list.Body.String())
	}
	if s := again.Items[0].Soulseek; s == nil || s.User != "peer1" || s.State != "" {
		t.Errorf("after outage soulseek = %+v", s)
	}
}

// Removing a Soulseek download from the queue cancels its transfers in slskd,
// and only the owner (or an admin) may.
func TestQueueDelete_SoulseekCancelsTransfersAndHonoursOwnership(t *testing.T) {
	h, database, fake, _, ctx, get, _ := slskdGrabFixtureDB(t)
	auth.SetEnforceTenancyForTests(t, true)
	users := db.NewUserRepo(database)
	uAlice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	uMallory, err := users.Create(ctx, "mallory", "h2")
	if err != nil {
		t.Fatal(err)
	}
	alice, mallory := uAlice.ID, uMallory.ID
	addr := mustAddr(t, slskd.Grab{User: "peer1", Files: []slskd.BatchFile{{Filename: "a.epub", Size: 100}, {Filename: "b.epub", Size: 100}}})
	user := func(id int64, role string) context.Context {
		return auth.WithUserRole(auth.WithUserID(ctx, id), role)
	}
	// A signed in user grabs only what a search returned, so the registry holds it.
	reg := NewSearchResultRegistry()
	reg.remember([]newznab.SearchResult{{GUID: "slskd:del", Title: "t", NZBURL: addr, Size: 200, Protocol: "soulseek"}})
	h.WithSearchResults(reg)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/queue/grab", slskdGrabBody("slskd:del", "t", slskd.RedactedGrabURL, "")).WithContext(user(alice, "user")))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("grab %d: %s", rec.Code, rec.Body.String())
	}
	dl := get("slskd:del")
	if dl.OwnerUserID != alice {
		t.Fatalf("owner = %d, want %d", dl.OwnerUserID, alice)
	}
	del := func(c context.Context) int {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", strconv.FormatInt(dl.ID, 10))
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/queue/%d", dl.ID), nil).WithContext(c)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h.Delete(w, req)
		return w.Code
	}
	if code := del(user(mallory, "user")); code != http.StatusNotFound {
		t.Errorf("another user's delete = %d, want 404", code)
	}
	if n := len(fake.Batch(*dl.SABnzbdNzoID).Transfers); n != 2 || get("slskd:del") == nil {
		t.Fatalf("a refused delete changed something: %d transfers", n)
	}
	if code := del(user(alice, "user")); code != http.StatusNoContent {
		t.Fatalf("owner delete = %d", code)
	}
	if n := len(fake.Batch(*dl.SABnzbdNzoID).Transfers); n != 0 {
		t.Errorf("%d transfers still in slskd after removal", n)
	}
	if get("slskd:del") != nil {
		t.Error("the row survived")
	}
}

func base64RawURL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
