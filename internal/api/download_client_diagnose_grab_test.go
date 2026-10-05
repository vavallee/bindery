package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// qbitGrabServer is a qBittorrent fake with a configurable category map and
// default save path.
func qbitGrabServer(t *testing.T, categories map[string]string, defaultSavePath string) (string, int) {
	t.Helper()
	cats := map[string]map[string]string{}
	for name, savePath := range categories {
		cats[name] = map[string]string{"name": name, "savePath": savePath}
	}
	catsJSON, _ := json.Marshal(cats)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.1.4"))
		case "/api/v2/torrents/categories":
			_, _ = w.Write(catsJSON)
		case "/api/v2/app/defaultSavePath":
			_, _ = w.Write([]byte(defaultSavePath))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return serverAddr(t, srv.URL)
}

// TestDiagnose_QbittorrentEmptyCategorySavePathUsesDefaultPlusCategory: with
// automatic torrent management on, a category with no save path saves to the
// default save path plus the category name, not to the default save path.
func TestDiagnose_QbittorrentEmptyCategorySavePathUsesDefaultPlusCategory(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Mkdir(filepath.Join(downloads, "books"), 0o750); err != nil {
		t.Fatal(err)
	}
	host, port := qbitGrabServer(t, map[string]string{"books": ""}, "/remote")
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "books", "/remote:"+downloads))
	path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	if p := resp.Paths[0]; p.ClientPath != "/remote/books" || p.LocalPath != filepath.Join(downloads, "books") {
		t.Errorf("paths = %+v, want the default save path plus the category", resp.Paths)
	}
	if !strings.Contains(path.Message, "the client default save path plus the category name") {
		t.Errorf("message should name where the path came from: %q", path.Message)
	}
}

// TestDiagnose_QbittorrentNoCategoryUsesTheSavePathBinderySends: without a
// category Bindery sends an explicit save path, so that is the folder to check.
func TestDiagnose_QbittorrentNoCategoryUsesTheSavePathBinderySends(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	host, port := qbitGrabServer(t, map[string]string{}, "/somewhere/else")
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "", "/qbit/downloads:"+downloads))
	path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	if !strings.Contains(path.Message, "the save path Bindery sends") {
		t.Errorf("message = %q", path.Message)
	}
	if p := resp.Paths[0]; p.ClientPath != "/qbit/downloads" || p.LocalPath != downloads {
		t.Errorf("paths = %+v", resp.Paths)
	}
	wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
}

// TestDiagnose_ChecksEbookAndAudiobookFolders: grabs resolve their category
// per media type, so a broken audiobook folder must show up even while the
// ebook folder works.
func TestDiagnose_ChecksEbookAndAudiobookFolders(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Mkdir(filepath.Join(downloads, "books"), 0o750); err != nil {
		t.Fatal(err)
	}
	host, port := qbitGrabServer(t, map[string]string{"books": "/remote/books", "audio": "/remote/audio"}, "/remote")
	client := qbitClient(host, port, "books", "/remote:"+downloads)
	client.CategoryAudiobook = "audio"
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, client)

	if len(resp.Paths) != 2 || resp.Paths[0].MediaType != models.MediaTypeEbook || resp.Paths[1].MediaType != models.MediaTypeAudiobook {
		t.Fatalf("paths = %+v, want an ebook row and an audiobook row", resp.Paths)
	}
	var ebookLocal, audioLocal *diagCheckResult
	for i := range resp.Checks {
		c := &resp.Checks[i]
		if c.Code != diagCodeLocalPath {
			continue
		}
		switch c.MediaType {
		case models.MediaTypeEbook:
			ebookLocal = c
		case models.MediaTypeAudiobook:
			audioLocal = c
		}
	}
	if ebookLocal == nil || ebookLocal.Status != diagPass {
		t.Errorf("ebook local_path = %+v, want pass", ebookLocal)
	}
	if audioLocal == nil || audioLocal.Status != diagFail || !strings.Contains(audioLocal.Message, "audio") {
		t.Errorf("audiobook local_path = %+v, want a failure naming the audio folder", audioLocal)
	}
}

// TestDiagnose_NzbgetAppendCategoryDir: a category with no DestDir of its own
// lands in DestDir/<category> while AppendCategoryDir is on (the default).
func TestDiagnose_NzbgetAppendCategoryDir(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Mkdir(filepath.Join(downloads, "Books"), 0o750); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"version"`) {
			_, _ = w.Write([]byte(`{"version":"1.1","result":"21.0"}`))
			return
		}
		fmt.Fprintf(w, `{"version":"1.1","result":[{"Name":"MainDir","Value":%q},{"Name":"DestDir","Value":"${MainDir}"},{"Name":"Server1.Password","Value":"news-SECRET-pass"},{"Name":"Category1.Name","Value":"Books"},{"Name":"Category1.DestDir","Value":""}]}`, downloads)
	}))
	defer srv.Close()
	host, port := serverAddr(t, srv.URL)
	resp, raw := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
		Name: "NZBGet", Type: "nzbget", Host: host, Port: port, Category: "Books", Enabled: true,
	})
	path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	if want := filepath.Join(downloads, "Books"); resp.Paths[0].ClientPath != want {
		t.Errorf("clientPath = %q, want %q", resp.Paths[0].ClientPath, want)
	}
	if !strings.Contains(path.Message, "AppendCategoryDir") {
		t.Errorf("message = %q", path.Message)
	}
	if strings.Contains(raw, "news-SECRET-pass") {
		t.Errorf("response carries the NZBGet server password")
	}
}

// TestDiagnose_WindowsPathOnWindowsIsCheckedDirectly: a drive path needs no
// remap when Bindery itself runs on Windows.
func TestDiagnose_WindowsPathOnWindowsIsCheckedDirectly(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	host, port := qbitDiagServer(t, qbitCategory("books", `D:\Torrents\books`))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: "D:/Torrents", goos: "windows"}, qbitClient(host, port, "books", ""))
	wantDiagStatus(t, resp, diagCodeRemap, diagPass)
	local := diagCheckByCode(t, resp, diagCodeLocalPath)
	if local.Status == diagSkipped || strings.Contains(local.Message, "outside every folder") {
		t.Errorf("local_path = %+v, want the drive path checked as a local folder", local)
	}
}

// TestDiagnose_DanglingSymlinkIsNotFollowed: downloads/dangling points at a
// folder outside every configured root that does not exist yet. Bindery must
// not follow it, and must not create anything outside, before or after the
// target appears.
func TestDiagnose_DanglingSymlinkIsNotFollowed(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "notyet")
	link := filepath.Join(downloads, "dangling")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(outside, past, past); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	probes := 0
	setup := diagnoseSetup{
		downloadDir: downloads, roots: []string{t.TempDir()},
		probe: func(string, string) (bool, string) { mu.Lock(); probes++; mu.Unlock(); return true, "" },
	}
	host, port := qbitDiagServer(t, qbitCategory("books", link))

	resp, _ := runDiagnose(t, setup, qbitClient(host, port, "books", ""))
	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if !strings.Contains(local.Message, "symbolic link that does not resolve") {
		t.Errorf("message = %q", local.Message)
	}
	if info, err := os.Stat(outside); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("outside folder changed while the link dangled")
	}

	// The target appears. The link now leads outside and is still refused.
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, past, past); err != nil {
		t.Fatal(err)
	}
	resp, _ = runDiagnose(t, setup, qbitClient(host, port, "books", ""))
	wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if info, err := os.Stat(target); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("something was created in the link target once it appeared")
	}
	mu.Lock()
	defer mu.Unlock()
	if probes != 0 {
		t.Errorf("hardlink probe ran %d times through the link", probes)
	}
}

// TestDiagnose_PrefixConfusionIsOutside: "<download>-evil" shares a string
// prefix with the download folder but is not under it.
func TestDiagnose_PrefixConfusionIsOutside(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	evil := downloads + "-evil"
	if err := os.Mkdir(evil, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(evil) })
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(evil, past, past); err != nil {
		t.Fatal(err)
	}
	host, port := qbitDiagServer(t, qbitCategory("books", evil))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "books", ""))
	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if !strings.Contains(local.Message, "outside every folder") {
		t.Errorf("message = %q", local.Message)
	}
	if info, err := os.Stat(evil); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("something was created in the prefix confused folder")
	}
}

// TestDiagnose_SlowFilesystemAnswersUnknown: a probe that blocks past the
// filesystem deadline, as on a dead network mount, returns unknown instead of
// holding the request.
func TestDiagnose_SlowFilesystemAnswersUnknown(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	host, port := qbitDiagServer(t, qbitCategory("books", downloads))
	// The probe blocks until the test ends (or 20 seconds pass), standing in
	// for a stat on a mount that never answers. Timing is measured from
	// inside the probe so database setup under -race does not count.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var mu sync.Mutex
	var entered time.Time
	resp, _ := runDiagnose(t, diagnoseSetup{
		downloadDir: downloads, roots: []string{t.TempDir()}, fsTimeout: 50 * time.Millisecond,
		probe: func(string, string) (bool, string) {
			mu.Lock()
			entered = time.Now()
			mu.Unlock()
			select {
			case <-release:
			case <-time.After(20 * time.Second):
			}
			return true, ""
		},
	}, qbitClient(host, port, "books", ""))
	links := wantDiagStatus(t, resp, diagCodeHardlinks, diagUnknown)
	if !strings.Contains(links.Message, "did not respond") {
		t.Errorf("message = %q", links.Message)
	}
	mu.Lock()
	defer mu.Unlock()
	if entered.IsZero() {
		t.Fatal("the probe never ran")
	}
	if elapsed := time.Since(entered); elapsed > 5*time.Second {
		t.Errorf("Diagnose returned %v after the probe blocked, want it to return at the filesystem deadline", elapsed)
	}
}

func delugeLabelServer(t *testing.T, downloads string, labelOptions string) (string, int) {
	t.Helper()
	return delugeLabelServerWithLabels(t, downloads, labelOptions, "")
}

// delugeLabelServerWithLabels is delugeLabelServer whose label.get_labels
// answers labels (a JSON list); "" leaves the method unanswered, as with the
// plugin off. Like the real plugin, label.get_options knows labels only by
// their lowercase id.
func delugeLabelServerWithLabels(t *testing.T, _ string, labelOptions, labels string) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int64             `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login", "web.connected":
			fmt.Fprintf(w, `{"result":true,"error":null,"id":%d}`, req.ID)
		case "core.get_config_values":
			fmt.Fprintf(w, `{"result":{"download_location":"/incomplete","move_completed":false,"move_completed_path":""},"error":null,"id":%d}`, req.ID)
		case "label.get_labels":
			if labels == "" {
				fmt.Fprintf(w, `{"result":null,"error":{"code":2,"message":"Unknown method"},"id":%d}`, req.ID)
				return
			}
			fmt.Fprintf(w, `{"result":%s,"error":null,"id":%d}`, labels, req.ID)
		case "label.get_options":
			if labelOptions == "" {
				fmt.Fprintf(w, `{"result":null,"error":{"code":2,"message":"Unknown method"},"id":%d}`, req.ID)
				return
			}
			var label string
			if len(req.Params) > 0 {
				_ = json.Unmarshal(req.Params[0], &label)
			}
			if label != strings.ToLower(label) {
				fmt.Fprintf(w, `{"result":null,"error":{"code":2,"message":"KeyError: '%s'"},"id":%d}`, label, req.ID)
				return
			}
			fmt.Fprintf(w, `{"result":%s,"error":null,"id":%d}`, labelOptions, req.ID)
		default:
			fmt.Fprintf(w, `{"result":null,"error":null,"id":%d}`, req.ID)
		}
	}))
	t.Cleanup(srv.Close)
	return serverAddr(t, srv.URL)
}

func TestDiagnose_DelugeLabelMovePath(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	applied, _ := json.Marshal(map[string]any{"apply_move_completed": true, "move_completed": true, "move_completed_path": downloads})

	t.Run("label move path wins", func(t *testing.T) {
		host, port := delugeLabelServer(t, downloads, string(applied))
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
			Name: "Deluge", Type: "deluge", Host: host, Port: port, Password: "deluge", Category: "books", Enabled: true,
		})
		path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
		if resp.Paths[0].ClientPath != downloads || !strings.Contains(path.Message, `label "books"`) {
			t.Errorf("paths = %+v, message = %q", resp.Paths, path.Message)
		}
	})

	// Grabs send a category with capitals as the lowercase label Deluge
	// stores (#2665), so the label's move path applies to it as well.
	t.Run("capital letters reach the lowercase label", func(t *testing.T) {
		host, port := delugeLabelServer(t, downloads, string(applied))
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
			Name: "Deluge", Type: "deluge", Host: host, Port: port, Password: "deluge", Category: "Books", Enabled: true,
		})
		path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
		if resp.Paths[0].ClientPath != downloads || !strings.Contains(path.Message, `label "books"`) {
			t.Errorf("paths = %+v, message = %q", resp.Paths, path.Message)
		}
	})

	t.Run("unreadable label warns", func(t *testing.T) {
		host, port := delugeLabelServer(t, downloads, "")
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
			Name: "Deluge", Type: "deluge", Host: host, Port: port, Password: "deluge", Category: "books", Enabled: true,
		})
		path := wantDiagStatus(t, resp, diagCodeClientPath, diagWarn)
		if !strings.Contains(path.Message, "not checked") {
			t.Errorf("message = %q", path.Message)
		}
	})

	// Grabs send the category untrimmed, so "books " is not the "books" label.
	t.Run("category with a trailing space warns", func(t *testing.T) {
		host, port := delugeLabelServer(t, downloads, "")
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
			Name: "Deluge", Type: "deluge", Host: host, Port: port, Password: "deluge", Category: "books ", Enabled: true,
		})
		path := wantDiagStatus(t, resp, diagCodeClientPath, diagWarn)
		if !strings.Contains(path.Message, "starts or ends with a space") {
			t.Errorf("message = %q", path.Message)
		}
	})
}

// TestDiagnose_DelugeLabelCategory: with the Label plugin on, the category
// check compares the configured category with Deluge's labels the way grabs
// send it, lowercased (#2665), and says when it lowercases.
func TestDiagnose_DelugeLabelCategory(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	deluge := func(host string, port int, category string) *models.DownloadClient {
		return &models.DownloadClient{Name: "Deluge", Type: "deluge", Host: host, Port: port, Password: "deluge", Category: category, Enabled: true}
	}

	t.Run("a capitalised category matches its lowercase label", func(t *testing.T) {
		host, port := delugeLabelServerWithLabels(t, downloads, "", `["books","tv"]`)
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, deluge(host, port, "Books"))
		cat := wantDiagStatus(t, resp, diagCodeCategory, diagPass)
		if !strings.Contains(cat.Message, `has the category "books"`) || !strings.Contains(cat.Message, `sends the category "Books" as "books"`) {
			t.Errorf("message = %q", cat.Message)
		}
	})

	t.Run("a lowercase category says nothing about case", func(t *testing.T) {
		host, port := delugeLabelServerWithLabels(t, downloads, "", `["books"]`)
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, deluge(host, port, "books"))
		cat := wantDiagStatus(t, resp, diagCodeCategory, diagPass)
		if strings.Contains(cat.Message, "lowercase") {
			t.Errorf("message = %q", cat.Message)
		}
	})

	t.Run("a missing label fails", func(t *testing.T) {
		host, port := delugeLabelServerWithLabels(t, downloads, "", `["tv"]`)
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, deluge(host, port, "Books"))
		cat := wantDiagStatus(t, resp, diagCodeCategory, diagFail)
		if !strings.Contains(cat.Message, `no category "books"`) || !strings.Contains(cat.Message, `"tv"`) {
			t.Errorf("message = %q", cat.Message)
		}
	})

	t.Run("plugin off stays unknown", func(t *testing.T) {
		host, port := delugeLabelServerWithLabels(t, downloads, "", "")
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, deluge(host, port, "Books"))
		cat := wantDiagStatus(t, resp, diagCodeCategory, diagUnknown)
		if !strings.Contains(cat.Message, "Label plugin") {
			t.Errorf("message = %q", cat.Message)
		}
	})
}

// TestDiagnose_GlobalRemapAppliesToSentSavePath: the save path a grab sends
// falls back to BINDERY_DOWNLOAD_PATH_REMAP when the client has no remap of
// its own (#2665), so the doctor reports the client's folder and the global
// remap reading it back, not Bindery's own folder.
func TestDiagnose_GlobalRemapAppliesToSentSavePath(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()

	t.Run("qbittorrent without a category", func(t *testing.T) {
		// qBittorrent's default sits above the folder it is sent, so the
		// global remap is plausibly meant for it and nothing is flagged.
		host, port := qbitGrabServer(t, map[string]string{}, "/qbit")
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads, remap: "/qbit/downloads:" + downloads}, qbitClient(host, port, "", ""))
		wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
		remap := wantDiagStatus(t, resp, diagCodeRemap, diagPass)
		if !strings.Contains(remap.Message, "BINDERY_DOWNLOAD_PATH_REMAP") {
			t.Errorf("message = %q", remap.Message)
		}
		if resp.Paths[0].ClientPath != "/qbit/downloads" {
			t.Errorf("clientPath = %q, want the client's folder from the global remap, as the grab sends it", resp.Paths[0].ClientPath)
		}
	})

	t.Run("a client remap wins over the global one", func(t *testing.T) {
		host, port := qbitGrabServer(t, map[string]string{}, "/default")
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads, remap: "/global/downloads:" + downloads}, qbitClient(host, port, "", "/qbit/downloads:"+downloads))
		remap := wantDiagStatus(t, resp, diagCodeRemap, diagPass)
		if !strings.Contains(remap.Message, "This client's path remap") {
			t.Errorf("message = %q", remap.Message)
		}
		if resp.Paths[0].ClientPath != "/qbit/downloads" {
			t.Errorf("clientPath = %q, want the client remap's folder", resp.Paths[0].ClientPath)
		}
	})

	t.Run("a category save path is not sent by Bindery", func(t *testing.T) {
		host, port := qbitGrabServer(t, map[string]string{"books": "/qbit/downloads"}, "/default")
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads, remap: "/qbit/downloads:" + downloads}, qbitClient(host, port, "books", ""))
		wantDiagStatus(t, resp, diagCodeRemap, diagPass)
	})

	// The mixed setup: the global remap is written for another client (say
	// SABnzbd sees Bindery's folder as /data), but this qBittorrent mounts
	// Bindery's folder at the same path. The global remap still rewrites the
	// folder it is sent, and the doctor says so and names the opt out.
	t.Run("a global remap meant for another client is flagged", func(t *testing.T) {
		host, port := qbitGrabServer(t, map[string]string{}, downloads)
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads, remap: "/data:" + downloads}, qbitClient(host, port, "", ""))
		path := wantDiagStatus(t, resp, diagCodeClientPath, diagWarn)
		if resp.Paths[0].ClientPath != "/data" {
			t.Errorf("clientPath = %q, want /data, the folder the grab is sent", resp.Paths[0].ClientPath)
		}
		if !strings.Contains(path.Message, "BINDERY_DOWNLOAD_PATH_REMAP") || !strings.Contains(path.Fix, downloads+":"+downloads) {
			t.Errorf("check = %+v", path)
		}
	})

	t.Run("an identity client remap opts out of the global one", func(t *testing.T) {
		host, port := qbitGrabServer(t, map[string]string{}, downloads)
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads, remap: "/data:" + downloads}, qbitClient(host, port, "", downloads+":"+downloads))
		wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
		if resp.Paths[0].ClientPath != downloads {
			t.Errorf("clientPath = %q, want Bindery's own folder %q", resp.Paths[0].ClientPath, downloads)
		}
		wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
	})
}

// TestDiagnose_FilesystemSlotsAreCapped: repeated Diagnose calls against a
// filesystem call that never returns leave at most cap(diagFSSlots) calls in
// flight, and later calls say a previous check is still waiting.
func TestDiagnose_FilesystemSlotsAreCapped(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	waitForFreeSlots(t)
	downloads := t.TempDir()
	library := t.TempDir()
	host, port := qbitDiagServer(t, qbitCategory("books", downloads))

	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		waitForFreeSlots(t)
	})
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	stuck := func(string, string) (bool, string) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return true, ""
	}

	sawBusy := false
	for i := 0; i < 8; i++ {
		resp, _ := runDiagnose(t, diagnoseSetup{
			downloadDir: downloads, roots: []string{library}, fsTimeout: 30 * time.Millisecond, probe: stuck,
		}, qbitClient(host, port, "books", ""))
		for _, c := range resp.Checks {
			if strings.Contains(c.Message, "still waiting for the filesystem") {
				sawBusy = true
			}
		}
	}
	if got := len(diagFSSlots); got > cap(diagFSSlots) {
		t.Fatalf("slots in use = %d, over the cap %d", got, cap(diagFSSlots))
	}
	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > cap(diagFSSlots) {
		t.Errorf("stuck filesystem calls in flight = %d, want at most %d", maxInFlight, cap(diagFSSlots))
	}
	if maxInFlight == 0 {
		t.Error("the stuck probe never ran")
	}
	if !sawBusy {
		t.Error("no response said a previous check is still waiting")
	}
}

func waitForFreeSlots(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(diagFSSlots) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d filesystem slots still held", len(diagFSSlots))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDiagnose_ReadOnlyDownloadFolderIsNotGreen: the shared hardlink check
// answers linkable when it cannot write its probe file; the doctor says it
// could not test instead.
func TestDiagnose_ReadOnlyDownloadFolderIsNotGreen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into read only directories")
	}
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Chmod(downloads, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(downloads, 0o700) })
	host, port := qbitDiagServer(t, qbitCategory("books", downloads))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads, roots: []string{t.TempDir()}}, qbitClient(host, port, "books", ""))
	wantDiagStatus(t, resp, diagCodeLocalPath, diagWarn)
	links := wantDiagStatus(t, resp, diagCodeHardlinks, diagUnknown)
	if !strings.Contains(links.Message, "could not write a test file") {
		t.Errorf("message = %q", links.Message)
	}
	if len(resp.Hardlinks) != 1 || resp.Hardlinks[0].Result != "unknown" || resp.Hardlinks[0].Linkable {
		t.Errorf("hardlinks = %+v", resp.Hardlinks)
	}
}

// TestDiagnose_MissingLibraryRootIsNotProbed: a library folder that does not
// exist is reported missing, and no test link is written in an ancestor that
// may lie outside every configured root.
func TestDiagnose_MissingLibraryRootIsNotProbed(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	parent := t.TempDir()
	missing := filepath.Join(parent, "not", "there")
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(parent, past, past); err != nil {
		t.Fatal(err)
	}
	probed := 0
	host, port := qbitDiagServer(t, qbitCategory("books", downloads))
	resp, _ := runDiagnose(t, diagnoseSetup{
		downloadDir: downloads, roots: []string{missing},
		probe: func(string, string) (bool, string) { probed++; return true, "" },
	}, qbitClient(host, port, "books", ""))
	links := wantDiagStatus(t, resp, diagCodeHardlinks, diagWarn)
	if !strings.Contains(links.Message, "do not exist") {
		t.Errorf("message = %q", links.Message)
	}
	if len(resp.Hardlinks) != 1 || resp.Hardlinks[0].Result != "missing" {
		t.Errorf("hardlinks = %+v", resp.Hardlinks)
	}
	if probed != 0 {
		t.Errorf("probe ran %d times for a missing root", probed)
	}
	if info, err := os.Stat(parent); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("something was written in the missing root's ancestor")
	}
}
