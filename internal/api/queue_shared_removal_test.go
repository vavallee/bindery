package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// removalStub is a download client of one type that answers every call with
// success and counts two things: every request it saw, and the ones that ask
// it to drop a job. The shared case asserts it was never contacted at all, so
// neither the removal nor its deleteFiles reached the client.
type removalStub struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	removals int
}

func newRemovalStub(t *testing.T, clientType string) *removalStub {
	t.Helper()
	s := &removalStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests++
		s.mu.Unlock()
		removal := false
		switch clientType {
		case "qbittorrent":
			removal = r.URL.Path == "/api/v2/torrents/delete"
			if r.URL.Path == "/api/v2/auth/login" {
				_, _ = w.Write([]byte("Ok."))
			}
		case "transmission":
			removal = strings.Contains(string(body), `"torrent-remove"`)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{}})
		case "deluge", "nzbget":
			var req struct {
				Method string `json:"method"`
				ID     int64  `json:"id"`
			}
			_ = json.Unmarshal(body, &req)
			removal = req.Method == "core.remove_torrent" || req.Method == "editqueue"
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.1", "result": true, "error": nil, "id": req.ID})
		case "rtorrent":
			removal = strings.Contains(string(body), "<methodName>d.erase</methodName>")
			w.Header().Set("Content-Type", "text/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`)
		case "sabnzbd":
			removal = r.URL.Query().Get("mode") == "queue" && r.URL.Query().Get("name") == "delete"
			_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
		default:
			t.Errorf("removalStub: unknown client type %q", clientType)
		}
		if removal {
			s.mu.Lock()
			s.removals++
			s.mu.Unlock()
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *removalStub) counts() (requests, removals int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.removals
}

// sharedRemovalCase is one client type and the remote id two rows share.
type sharedRemovalCase struct {
	clientType string
	remoteID   string
}

func (c sharedRemovalCase) apply(dl *models.Download) {
	id := c.remoteID
	switch c.clientType {
	case "nzbget", "sabnzbd":
		dl.Protocol = "usenet"
		dl.SABnzbdNzoID = &id
	default:
		dl.Protocol = "torrent"
		dl.TorrentID = &id
	}
}

var sharedRemovalCases = []sharedRemovalCase{
	{"qbittorrent", "0123456789abcdef0123456789abcdef01234567"},
	{"transmission", "0123456789abcdef0123456789abcdef01234567"},
	{"deluge", "0123456789abcdef0123456789abcdef01234567"},
	{"rtorrent", "0123456789abcdef0123456789abcdef01234567"},
	{"nzbget", "42"},
	{"sabnzbd", "SABnzbd_nzo_shared"},
}

// seedSharedDownloads creates a client of c's type pointed at a fresh stub and
// two downloads naming the same job in it, one owned by alice and one by bob:
// the shape a torrent client produces when two users grab the same release
// and the second add adopts the torrent the first one put there.
func seedSharedDownloads(t *testing.T, c sharedRemovalCase) (h *QueueHandler, database *sql.DB, downloads *db.DownloadRepo, stub *removalStub, alice, bob int64, dlA, dlB *models.Download) {
	t.Helper()
	h, database, downloads, clients, _, ctx := queueFixture(t)
	users := db.NewUserRepo(database)
	uA, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	uB, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}

	stub = newRemovalStub(t, c.clientType)
	host, port := testServerHostPort(t, stub.URL)
	client := &models.DownloadClient{
		Name: c.clientType, Type: c.clientType, Host: host, Port: port,
		Username: "u", Password: "p", APIKey: "k", Enabled: true,
	}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	mk := func(guid string, owner int64) *models.Download {
		dl := &models.Download{
			GUID: guid, DownloadClientID: &client.ID, Title: "Shared Book",
			NZBURL: "http://indexer.example/" + guid, Status: models.StateDownloading,
			OwnerUserID: owner,
		}
		c.apply(dl)
		if err := downloads.Create(ctx, dl); err != nil {
			t.Fatalf("create download: %v", err)
		}
		return dl
	}
	dlA = mk("guid-alice-indexer-1", uA.ID)
	dlB = mk("guid-bob-indexer-2", uB.ID)
	return h, database, downloads, stub, uA.ID, uB.ID, dlA, dlB
}

func deleteQueueItemAs(t *testing.T, h *QueueHandler, uid, id int64, suffix string) {
	t.Helper()
	idStr := strconv.FormatInt(id, 10)
	rec := httptest.NewRecorder()
	h.Delete(rec, withURLParam(reqAsUser(http.MethodDelete, "/api/v1/queue/"+idStr+suffix, uid), "id", idStr))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete %d: expected 204, got %d: %s", id, rec.Code, rec.Body.String())
	}
}

// TestQueueDelete_KeepsDownloadAnotherQueueItemUses: under tenancy two users'
// rows can name one torrent (or job). Removing one user's queue item, even
// with deleteFiles, must not touch the client while the other row still uses
// it; removing the last row must remove it as before.
func TestQueueDelete_KeepsDownloadAnotherQueueItemUses(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	for _, c := range sharedRemovalCases {
		t.Run(c.clientType, func(t *testing.T) {
			h, _, downloads, stub, alice, bob, dlA, dlB := seedSharedDownloads(t, c)
			ctx := context.Background()

			deleteQueueItemAs(t, h, alice, dlA.ID, "?deleteFiles=true")
			if req, _ := stub.counts(); req != 0 {
				t.Fatalf("the client was contacted %d times while bob's row still uses the download", req)
			}
			if got, _ := downloads.GetByID(ctx, dlA.ID); got != nil {
				t.Fatalf("alice's row should be gone, got %+v", got)
			}
			got, err := downloads.GetByID(ctx, dlB.ID)
			if err != nil || got == nil {
				t.Fatalf("bob's row must be intact, got %+v err %v", got, err)
			}
			if got.Status != models.StateDownloading {
				t.Fatalf("bob's row changed state to %s", got.Status)
			}

			// rTorrent's data delete is Bindery's own RemoveAll on a resolved
			// path, which this stub cannot serve; the removal is what counts.
			deleteQueueItemAs(t, h, bob, dlB.ID, "")
			if _, rem := stub.counts(); rem != 1 {
				t.Fatalf("removing the last row should remove the download from the client once, got %d removals", rem)
			}
			if got, _ := downloads.GetByID(ctx, dlB.ID); got != nil {
				t.Fatalf("bob's row should be gone, got %+v", got)
			}
		})
	}
}

// TestQueueDelete_SharedDownloadIgnoresCase: torrent ids are compared without
// regard to case, so a row written in upper case still counts as a user.
func TestQueueDelete_SharedDownloadIgnoresCase(t *testing.T) {
	c := sharedRemovalCase{"qbittorrent", "0123456789abcdef0123456789abcdef01234567"}
	h, database, _, stub, alice, _, dlA, dlB := seedSharedDownloads(t, c)
	if _, err := database.Exec("UPDATE downloads SET torrent_id=UPPER(torrent_id) WHERE id=?", dlB.ID); err != nil {
		t.Fatal(err)
	}
	deleteQueueItemAs(t, h, alice, dlA.ID, "?deleteFiles=true")
	if req, _ := stub.counts(); req != 0 {
		t.Fatalf("the client was contacted %d times while another row still uses the torrent", req)
	}
}

// TestQueueDelete_FailedSiblingDoesNotHoldTheDownload: a failed row is a
// finished attempt nothing will act on again, so it does not keep the
// download in the client.
func TestQueueDelete_FailedSiblingDoesNotHoldTheDownload(t *testing.T) {
	c := sharedRemovalCase{"qbittorrent", "0123456789abcdef0123456789abcdef01234567"}
	h, database, _, stub, alice, _, dlA, dlB := seedSharedDownloads(t, c)
	if _, err := database.Exec("UPDATE downloads SET status=? WHERE id=?", models.StateFailed, dlB.ID); err != nil {
		t.Fatal(err)
	}
	deleteQueueItemAs(t, h, alice, dlA.ID, "")
	if _, rem := stub.counts(); rem != 1 {
		t.Fatalf("expected the download to be removed, got %d removals", rem)
	}
}

// TestQueueBulkDelete_SharedDownloadRemovedOnce: a bulk remove of both rows
// runs them concurrently. Each row is deleted before its check, so whichever
// check runs last sees no other user and removes the download; it must never
// be left in the client with nothing tracking it.
func TestQueueBulkDelete_SharedDownloadRemovedOnce(t *testing.T) {
	for i := 0; i < 20; i++ {
		c := sharedRemovalCase{"qbittorrent", "0123456789abcdef0123456789abcdef01234567"}
		h, _, downloads, stub, _, _, dlA, dlB := seedSharedDownloads(t, c)
		body := `{"ids":[` + strconv.FormatInt(dlA.ID, 10) + `,` + strconv.FormatInt(dlB.ID, 10) + `]}`
		rec := httptest.NewRecorder()
		h.BulkDelete(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/bulk/delete", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("bulk delete: %d %s", rec.Code, rec.Body.String())
		}
		if _, rem := stub.counts(); rem < 1 {
			t.Fatalf("iteration %d: the shared download was left in the client after both rows were removed", i)
		}
		for _, id := range []int64{dlA.ID, dlB.ID} {
			if got, _ := downloads.GetByID(context.Background(), id); got != nil {
				t.Fatalf("row %d should be gone", id)
			}
		}
	}
}

// TestQueueDelete_SharedAcrossClientEntriesOnOneDaemon: two client entries
// configured against one daemon (an ebook and an audiobook entry on the same
// qBittorrent) adopt torrents across each other, so a row on the second entry
// still uses the torrent a row on the first one is being removed from.
func TestQueueDelete_SharedAcrossClientEntriesOnOneDaemon(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	c := sharedRemovalCase{"qbittorrent", "0123456789abcdef0123456789abcdef01234567"}
	h, database, downloads, stub, alice, bob, dlA, dlB := seedSharedDownloads(t, c)
	ctx := context.Background()
	clients := db.NewDownloadClientRepo(database)

	first, err := clients.GetByID(ctx, *dlA.DownloadClientID)
	if err != nil || first == nil {
		t.Fatalf("load client: %v", err)
	}
	second := *first
	second.ID = 0
	second.Name = "qbittorrent audiobooks"
	second.Host = strings.ToUpper(first.Host)
	second.Category = "audiobooks"
	if err := clients.Create(ctx, &second); err != nil {
		t.Fatalf("create second entry: %v", err)
	}
	if _, err := database.Exec("UPDATE downloads SET download_client_id=? WHERE id=?", second.ID, dlB.ID); err != nil {
		t.Fatal(err)
	}

	deleteQueueItemAs(t, h, alice, dlA.ID, "?deleteFiles=true")
	if req, _ := stub.counts(); req != 0 {
		t.Fatalf("the daemon was contacted %d times while a row on the other entry still uses the torrent", req)
	}
	if got, _ := downloads.GetByID(ctx, dlB.ID); got == nil {
		t.Fatal("bob's row must be intact")
	}

	deleteQueueItemAs(t, h, bob, dlB.ID, "")
	if _, rem := stub.counts(); rem != 1 {
		t.Fatalf("removing the last row should remove the torrent once, got %d removals", rem)
	}
}
