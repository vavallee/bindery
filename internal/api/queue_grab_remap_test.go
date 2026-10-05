package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// TestQueueGrab_SavePathUsesGlobalRemap: a manual grab through qBittorrent
// with no category sends a save path, and with only BINDERY_DOWNLOAD_PATH_REMAP
// set that path has to be the client's, not Bindery's /downloads (#2665).
func TestQueueGrab_SavePathUsesGlobalRemap(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	var mu sync.Mutex
	var savePaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			_ = r.ParseForm()
			mu.Lock()
			savePaths = append(savePaths, r.FormValue("savepath"))
			mu.Unlock()
			_, _ = w.Write([]byte("Ok."))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	t.Cleanup(srv.Close)

	h, database, _, clients, books, ctx := queueFixture(t)
	h.WithStoragePaths("/downloads", "").WithDownloadPathRemap("/data:/downloads")
	host, port := testServerHostPort(t, srv.URL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "qbit", Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Enabled: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	author := &models.Author{ForeignID: "remap-author", Name: "Remap Author", SortName: "Author, Remap", Monitored: true}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	book := &models.Book{ForeignID: "remap-book", AuthorID: author.ID, Title: "Remap Book", SortTitle: "remap book", MediaType: models.MediaTypeEbook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatalf("create book: %v", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"guid":      "remap-guid",
		"title":     "Remap Author - Remap Book (epub)",
		"nzbUrl":    "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd&dn=Remap+Book",
		"size":      100,
		"bookId":    book.ID,
		"protocol":  "torrent",
		"mediaType": models.MediaTypeEbook,
	})
	w := httptest.NewRecorder()
	h.Grab(w, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewReader(payload)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("grab: status %d: %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(savePaths) != 1 || savePaths[0] != "/data" {
		t.Errorf("save paths sent = %q, want one add with /data", savePaths)
	}
}
