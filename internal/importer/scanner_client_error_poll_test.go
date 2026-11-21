package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// A torrent the client reports as errored is recorded and never blocklisted
// (#4111). These run the pollers themselves, so they guard the call sites in
// scanner_poll.go: qBittorrent's error state is usually a full disk, and a
// full disk would blocklist every release in turn.

const clientErrHash = "cccccccccccccccccccccccccccccccccccccccc"

func clientErrorFixture(t *testing.T, clientType string, handler http.HandlerFunc) (*Scanner, *db.DownloadRepo, *db.BlocklistRepo, *db.HistoryRepo, *models.DownloadClient, *clientErrorRepoll) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	blocklist := db.NewBlocklistRepo(database)
	history := db.NewHistoryRepo(database)
	s := NewScanner(dlRepo, clientRepo, db.NewBookRepo(database), db.NewAuthorRepo(database), history, t.TempDir(), "", "", "", "")
	s.WithFormatEnforcement(db.NewQualityProfileRepo(database), blocklist)

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{Name: clientType, Type: clientType, Host: host, Port: port, Enabled: true}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}
	// A real indexer that is otherwise serving: exactly the conditions under
	// which a counted failure would escalate, so a call site that counts the
	// client's error blocklists on the third poll.
	idx := &models.Indexer{Name: "idx", Type: "torznab", URL: "http://idx.invalid", Enabled: true}
	if err := db.NewIndexerRepo(database).Create(ctx, idx); err != nil {
		t.Fatal(err)
	}
	h := clientErrHash
	dl := &models.Download{
		GUID: "guid-client-error", Title: "Errored Torrent", Status: models.StateDownloading,
		Protocol: "torrent", TorrentID: &h, DownloadClientID: &client.ID, IndexerID: &idx.ID,
		NZBURL: "magnet:?xt=urn:btih:" + clientErrHash,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if err := history.Create(ctx, &models.HistoryEvent{
		EventType: models.HistoryEventGrabbed, SourceTitle: "Served Elsewhere",
		Data: `{"guid":"g-served","indexerId":` + strconv.FormatInt(idx.ID, 10) + `}`,
	}); err != nil {
		t.Fatal(err)
	}
	return s, dlRepo, blocklist, history, client, &clientErrorRepoll{database: database, history: history, indexerID: idx.ID, dlID: dl.ID}
}

// clientErrorRepoll puts the failed row back to downloading and lets the time
// between two polls pass, with the indexer serving something else.
type clientErrorRepoll struct {
	database  *sql.DB
	history   *db.HistoryRepo
	indexerID int64
	dlID      int64
}

func (r *clientErrorRepoll) next(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.database.Exec(`UPDATE downloads SET status = ? WHERE id = ?`, string(models.StateDownloading), r.dlID); err != nil {
		t.Fatal(err)
	}
	if err := r.history.Create(ctx, &models.HistoryEvent{
		EventType: models.HistoryEventGrabbed, SourceTitle: "Served Elsewhere",
		Data: `{"guid":"g-served","indexerId":` + strconv.FormatInt(r.indexerID, 10) + `}`,
	}); err != nil {
		t.Fatal(err)
	}
	type row struct {
		id int64
		at time.Time
	}
	var all []row
	func() {
		rows, err := r.database.Query(`SELECT id, created_at FROM history`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.id, &x.at); err != nil {
				t.Fatal(err)
			}
			all = append(all, x)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}()
	for _, x := range all {
		if _, err := r.database.Exec(`UPDATE history SET created_at = ? WHERE id = ?`, x.at.Add(-7*time.Hour), x.id); err != nil {
			t.Fatal(err)
		}
	}
}

func assertClientErrorRecordedNotBlocklisted(t *testing.T, dlRepo *db.DownloadRepo, blocklist *db.BlocklistRepo, history *db.HistoryRepo, polls int) {
	t.Helper()
	ctx := context.Background()
	got, err := dlRepo.GetByGUID(ctx, "guid-client-error")
	if err != nil || got == nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.ErrorMessage, "so the release was not blocklisted") {
		t.Errorf("queue message %q does not say the release was left alone", got.ErrorMessage)
	}
	entries, err := blocklist.List(ctx)
	if err != nil || len(entries) != 0 {
		t.Fatalf("blocklist = %+v (%v), want empty", entries, err)
	}
	events, err := history.ListByType(ctx, models.HistoryEventDownloadFailed)
	if err != nil || len(events) != polls {
		t.Fatalf("history: %v, %d failure events, want %d", err, len(events), polls)
	}
	for _, ev := range events {
		if !strings.Contains(ev.Data, `"class":"clientError"`) {
			t.Errorf("history event %s does not carry the clientError class", ev.Data)
		}
	}
}

func TestCheckQbittorrentDownloads_ErroredTorrentIsNotBlocklisted(t *testing.T) {
	s, dlRepo, blocklist, history, client, again := clientErrorFixture(t, "qbittorrent", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash": clientErrHash, "name": "Errored Torrent", "state": "error",
				"progress": 0.4, "amount_left": 100, "size": 200, "category": "",
				"save_path": "/tmp/x", "content_path": "/tmp/x/Errored Torrent",
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	const polls = 3
	for i := 0; i < polls; i++ {
		if i > 0 {
			again.next(t)
		}
		s.checkQbittorrentDownloads(context.Background(), client)
	}
	assertClientErrorRecordedNotBlocklisted(t, dlRepo, blocklist, history, polls)
}

func TestCheckDelugeDownloads_ErroredTorrentIsNotBlocklisted(t *testing.T) {
	s, dlRepo, blocklist, history, client, again := clientErrorFixture(t, "deluge", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		write := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
		}
		switch req.Method {
		case "auth.login", "auth.check_session", "web.connected":
			write(true)
		case "core.get_torrents_status":
			write(map[string]any{clientErrHash: map[string]any{
				"name": "Errored Torrent", "hash": clientErrHash, "progress": 40.0, "state": "Error",
				"total_size": 200, "total_done": 80, "download_location": "/tmp/x",
			}})
		default:
			write(nil)
		}
	})
	const polls = 3
	for i := 0; i < polls; i++ {
		if i > 0 {
			again.next(t)
		}
		s.checkDelugeDownloads(context.Background(), client)
	}
	assertClientErrorRecordedNotBlocklisted(t, dlRepo, blocklist, history, polls)
}
