package scheduler

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// TestSearchAndGrabFormat_SavePathUsesGlobalRemap: an automatic grab through
// qBittorrent with no category sends a save path, and with only
// BINDERY_DOWNLOAD_PATH_REMAP set that path has to be the client's, not
// Bindery's /downloads (#2665).
func TestSearchAndGrabFormat_SavePathUsesGlobalRemap(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	ctx := context.Background()

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

	s, _, _, book := freeleechFixture(t, false, []newznab.SearchResult{{
		GUID: "g-remap", Title: "Ratio Book.epub",
		NZBURL:   "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd&dn=Ratio+Book",
		Protocol: "torrent", DownloadVolumeFactor: ratio(0),
	}})
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)
	clients, err := s.clients.List(ctx)
	if err != nil || len(clients) != 1 {
		t.Fatalf("clients = %v, err = %v", clients, err)
	}
	c := clients[0]
	c.Host, c.Port, c.Username, c.Password = host, port, "u", "p"
	if err := s.clients.Update(ctx, &c); err != nil {
		t.Fatalf("update client: %v", err)
	}
	s.WithStoragePaths("/downloads", "")
	s.WithDownloadClientHealth(nil, "/data:/downloads")

	s.searchAndGrabFormat(ctx, book, models.MediaTypeEbook, nil)

	mu.Lock()
	defer mu.Unlock()
	if len(savePaths) != 1 || savePaths[0] != "/data" {
		t.Errorf("save paths sent = %q, want one add with /data", savePaths)
	}
}
