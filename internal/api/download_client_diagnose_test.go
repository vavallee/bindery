package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/fsutil"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// runDiagnose saves client against a fresh handler configured with the
// given download folder and library roots, runs Diagnose and decodes the
// response along with the raw body.
type diagnoseSetup struct {
	downloadDir          string
	audiobookDownloadDir string
	roots                []string
	remap                string
	probe                func(a, b string) (bool, string)
	// goos defaults to "linux" so the Windows path rules are deterministic.
	goos      string
	fsTimeout time.Duration
}

func runDiagnose(t *testing.T, setup diagnoseSetup, client *models.DownloadClient) (diagnoseResponse, string) {
	t.Helper()
	h, clients := downloadClientFixture(t)
	h.WithStoragePaths(setup.downloadDir, setup.audiobookDownloadDir).WithDownloadPathRemap(setup.remap)
	h.goos = setup.goos
	if h.goos == "" {
		h.goos = "linux"
	}
	h.fsTimeout = setup.fsTimeout
	if len(setup.roots) > 0 {
		h.WithRoots(NewLibraryRoots(nil, setup.roots...))
	}
	h.hardlinkProbe = setup.probe
	if err := clients.Create(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	id := strconv.FormatInt(client.ID, 10)
	h.Diagnose(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/"+id+"/diagnose", nil), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	var out diagnoseResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out, raw
}

func diagCheckByCode(t *testing.T, resp diagnoseResponse, code string) diagCheckResult {
	t.Helper()
	for _, c := range resp.Checks {
		if c.Code == code {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", code, resp.Checks)
	return diagCheckResult{}
}

func wantDiagStatus(t *testing.T, resp diagnoseResponse, code, status string) diagCheckResult {
	t.Helper()
	c := diagCheckByCode(t, resp, code)
	if c.Status != status {
		t.Fatalf("%s status = %q, want %q (message: %s)", code, c.Status, status, c.Message)
	}
	return c
}

func serverAddr(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

// qbitDiagServer is a qBittorrent fake answering login, version and the
// category list with the given JSON.
func qbitDiagServer(t *testing.T, categoriesJSON string) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.1.4"))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(categoriesJSON))
		case "/api/v2/app/defaultSavePath":
			_, _ = w.Write([]byte(""))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return serverAddr(t, srv.URL)
}

func qbitCategory(name, savePath string) string {
	b, _ := json.Marshal(map[string]map[string]string{name: {"name": name, "savePath": savePath}})
	return string(b)
}

func qbitClient(host string, port int, category, remap string) *models.DownloadClient {
	return &models.DownloadClient{
		Name: "qBit", Type: "qbittorrent", Host: host, Port: port,
		Username: "u", Password: "p", Category: category, PathRemap: remap, Enabled: true,
	}
}

func TestDiagnose_QbittorrentAllPass(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	library := t.TempDir()
	host, port := qbitDiagServer(t, qbitCategory("books", "/remote/books"))

	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads, roots: []string{library}},
		qbitClient(host, port, "books", "/remote/books:"+downloads))

	for _, code := range []string{diagCodeConfig, diagCodeConnect, diagCodeCategory, diagCodeClientPath, diagCodeRemap, diagCodeLocalPath, diagCodeHardlinks} {
		wantDiagStatus(t, resp, code, diagPass)
	}
	if len(resp.Paths) != 1 || resp.Paths[0].MediaType != "" {
		t.Fatalf("paths = %+v, want one shared row when both media types land in one folder", resp.Paths)
	}
	if p := resp.Paths[0]; p.ClientPath != "/remote/books" || p.RemapRule != "client" || p.LocalPath != downloads || p.Source != "the category save path" {
		t.Errorf("paths = %+v", resp.Paths)
	}
	if len(resp.Hardlinks) != 1 || resp.Hardlinks[0].Root != library || !resp.Hardlinks[0].Linkable || resp.Hardlinks[0].DownloadPath != downloads {
		t.Errorf("hardlinks = %+v", resp.Hardlinks)
	}
	if resp.PrimaryFix != "" {
		t.Errorf("primaryFix = %q, want empty when nothing failed or warned", resp.PrimaryFix)
	}
}

// TestDiagnose_IndexerReachIsAlwaysUnknown: Bindery cannot test the client's
// own route to indexers (#639, #2505), so the row says so every time, even
// after an earlier failure.
func TestDiagnose_IndexerReachIsAlwaysUnknown(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	host, port := qbitDiagServer(t, `{}`)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, qbitClient(host, port, "books", ""))
	wantDiagStatus(t, resp, diagCodeCategory, diagFail)
	last := resp.Checks[len(resp.Checks)-1]
	if last.Code != diagCodeIndexerReach || last.Status != diagUnknown {
		t.Fatalf("last check = %+v, want indexer_reach unknown", last)
	}
}

// TestDiagnose_MissingCategorySkipsTheRest covers the skipped cascade: once a
// check fails, every later check is reported as skipped without running.
func TestDiagnose_MissingCategorySkipsTheRest(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	host, port := qbitDiagServer(t, qbitCategory("movies", "/remote/movies"))
	probes := 0
	resp, _ := runDiagnose(t, diagnoseSetup{
		downloadDir: t.TempDir(), roots: []string{t.TempDir()},
		probe: func(string, string) (bool, string) { probes++; return true, "" },
	}, qbitClient(host, port, "books", ""))

	cat := wantDiagStatus(t, resp, diagCodeCategory, diagFail)
	if !strings.Contains(cat.Message, `"books"`) || !strings.Contains(cat.Message, `"movies"`) {
		t.Errorf("category message should name the missing and existing categories: %q", cat.Message)
	}
	for _, code := range []string{diagCodeClientPath, diagCodeRemap, diagCodeLocalPath, diagCodeHardlinks} {
		wantDiagStatus(t, resp, code, diagSkipped)
	}
	if resp.PrimaryFix != cat.Fix || resp.PrimaryFix == "" {
		t.Errorf("primaryFix = %q, want the category fix %q", resp.PrimaryFix, cat.Fix)
	}
	if probes != 0 {
		t.Errorf("hardlink probe ran %d times after a failure", probes)
	}
}

func TestDiagnose_NestedCategoryPasses(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	host, port := qbitDiagServer(t, qbitCategory("books/ebooks", downloads))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "books/ebooks", ""))
	wantDiagStatus(t, resp, diagCodeCategory, diagPass)
	wantDiagStatus(t, resp, diagCodeRemap, diagPass)
	if resp.Paths[0].RemapRule != "none" {
		t.Errorf("remapRule = %q, want none", resp.Paths[0].RemapRule)
	}
	wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
}

// TestDiagnose_MissingRemap: the client reports its own mount path and nothing
// translates it, so Bindery would look outside its download folder.
func TestDiagnose_MissingRemap(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	host, port := qbitDiagServer(t, qbitCategory("books", "/torrents/complete/books"))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "books", ""))

	// No remap applies and the folder is not one Bindery uses, so the remap
	// row must not read as a pass while the fix says to add a remap.
	remap := wantDiagStatus(t, resp, diagCodeRemap, diagWarn)
	if !strings.Contains(remap.Message, "is not a folder Bindery uses") {
		t.Errorf("remap message = %q", remap.Message)
	}
	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if !strings.Contains(local.Message, "outside every folder") {
		t.Errorf("message = %q", local.Message)
	}
	if !strings.Contains(resp.PrimaryFix, "path remap") || resp.PrimaryFix != local.Fix {
		t.Errorf("primaryFix = %q, want the local_path fix naming the path remap (%q)", resp.PrimaryFix, local.Fix)
	}
	wantDiagStatus(t, resp, diagCodeHardlinks, diagSkipped)
}

func TestDiagnose_WindowsPathWithoutRemapFails(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	host, port := qbitDiagServer(t, qbitCategory("books", `D:\Torrents\books`))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: "/downloads"}, qbitClient(host, port, "books", ""))
	remap := wantDiagStatus(t, resp, diagCodeRemap, diagFail)
	// The qBittorrent client normalises separators, so the path arrives with
	// forward slashes.
	if !strings.Contains(remap.Fix, `D:/Torrents/books:/downloads`) {
		t.Errorf("fix should give a remap to paste, got %q", remap.Fix)
	}
	wantDiagStatus(t, resp, diagCodeLocalPath, diagSkipped)
}

func TestDiagnose_CaseDivergenceWarns(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Mkdir(filepath.Join(downloads, "Books"), 0o750); err != nil {
		t.Fatal(err)
	}
	host, port := qbitDiagServer(t, qbitCategory("books", "/remote/books"))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads},
		qbitClient(host, port, "books", "/remote:"+downloads))

	if !fsutil.IsCaseSensitiveFSForTests(t, downloads) {
		// The filesystem folds the case, so the remapped path really does
		// reach the folder and a grab would land in it. There is nothing to
		// warn about, and saying otherwise would send the user chasing a
		// rename that changes nothing.
		local := wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
		if !strings.Contains(local.Message, filepath.Join(downloads, "books")) {
			t.Errorf("message should name the folder it reached, got %q", local.Message)
		}
		return
	}
	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagWarn)
	if !strings.Contains(local.Fix, `"Books"`) {
		t.Errorf("fix should name the on-disk case, got %q", local.Fix)
	}
}

func TestDiagnose_UnreadableDirFailsNamingUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	locked := filepath.Join(downloads, "books")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	host, port := qbitDiagServer(t, qbitCategory("books", locked))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "books", ""))
	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if !strings.Contains(local.Message, "permission denied") {
		t.Errorf("message = %q", local.Message)
	}
	if want := fmt.Sprintf("uid %d", os.Getuid()); !strings.Contains(local.Fix, want) {
		t.Errorf("fix %q should name %s", local.Fix, want)
	}
}

// TestDiagnose_ClientPathOutsideConfiguredFoldersTouchesNothing is the S7
// rule. A client that reports a folder outside every configured download
// folder and library root (a stand-in for /etc) must get a diagnosis without
// Bindery statting, listing or writing a probe file into that folder. A write
// probe that creates and removes a file still bumps the folder's mtime, so an
// unchanged mtime proves nothing was created there.
func TestDiagnose_ClientPathOutsideConfiguredFoldersTouchesNothing(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	outside := t.TempDir()
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(outside, past, past); err != nil {
		t.Fatal(err)
	}
	probes := 0
	host, port := qbitDiagServer(t, qbitCategory("books", outside))
	resp, _ := runDiagnose(t, diagnoseSetup{
		downloadDir: t.TempDir(), roots: []string{t.TempDir()},
		probe: func(string, string) (bool, string) { probes++; return true, "" },
	}, qbitClient(host, port, "books", ""))

	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if !strings.Contains(local.Message, "outside every folder") || !strings.Contains(local.Message, "did not look inside") {
		t.Errorf("message = %q", local.Message)
	}
	wantDiagStatus(t, resp, diagCodeHardlinks, diagSkipped)
	if probes != 0 {
		t.Errorf("hardlink probe ran %d times against an outside folder", probes)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(past) {
		t.Errorf("outside folder mtime changed from %v to %v: something was created in it", past, info.ModTime())
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("outside folder gained entries: %v", entries)
	}
}

// TestDiagnose_SymlinkOutOfDownloadFolderTouchesNothing: a path that is under
// the download folder as written but leads outside it through a symlink gets
// the same treatment.
func TestDiagnose_SymlinkOutOfDownloadFolderTouchesNothing(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(downloads, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(outside, past, past); err != nil {
		t.Fatal(err)
	}
	host, port := qbitDiagServer(t, qbitCategory("books", link))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "books", ""))
	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if !strings.Contains(local.Message, "symbolic link") {
		t.Errorf("message = %q", local.Message)
	}
	// The link name shares a prefix with the download folder, but the
	// problem is where the link goes, not the letter case.
	if strings.Contains(local.Fix, "letter case") {
		t.Errorf("fix blames letter case for a symlink leading outside: %q", local.Fix)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(past) {
		t.Errorf("outside folder mtime changed: something was created in it")
	}
}

// TestDiagnose_HardlinkProbePerRoot: every library root gets its own real
// link probe, even when roots share a filesystem device. Two bind mounts of
// one filesystem report the same device ID and still refuse links across
// them, so sharing one probe result between them reported a false green.
func TestDiagnose_HardlinkProbePerRoot(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	parent := t.TempDir()
	rootA := filepath.Join(parent, "a")
	rootB := filepath.Join(parent, "b")
	for _, d := range []string{rootA, rootB} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	probed := map[string]int{}
	host, port := qbitDiagServer(t, qbitCategory("books", downloads))
	resp, _ := runDiagnose(t, diagnoseSetup{
		downloadDir: downloads, roots: []string{rootA, rootB},
		probe: func(_, root string) (bool, string) {
			mu.Lock()
			defer mu.Unlock()
			probed[root]++
			if root == rootB {
				return false, "hardlinks between them fail (EXDEV)"
			}
			return true, ""
		},
	}, qbitClient(host, port, "books", ""))

	mu.Lock()
	defer mu.Unlock()
	if probed[rootA] != 1 || probed[rootB] != 1 {
		t.Errorf("probes per root = %v, want one each for two roots on one device", probed)
	}
	if len(resp.Hardlinks) != 2 {
		t.Fatalf("hardlinks = %+v, want a row per root", resp.Hardlinks)
	}
	for _, row := range resp.Hardlinks {
		if want := row.Root == rootA; row.Linkable != want {
			t.Errorf("row %+v: linkable = %v, want %v", row, row.Linkable, want)
		}
	}
	links := wantDiagStatus(t, resp, diagCodeHardlinks, diagWarn)
	if !strings.Contains(links.Message, "1 of 2") {
		t.Errorf("message = %q", links.Message)
	}
}

// sabDiagServer is a SABnzbd fake. It records every get_config request's
// query so tests can assert only the needed section and keyword are asked
// for. Its unfiltered answers carry a news server password and an email
// password, the fields S8 is about.
type sabDiag struct {
	mu        sync.Mutex
	getConfig []url.Values
}

const (
	sabSecretKey      = "sab-apikey-SECRET-1234"
	sabSecretPassword = "news-pass-SECRET-5678"
)

func sabDiagServer(t *testing.T, completeDir, catDir string, refuseConfig bool) (*sabDiag, string, int) {
	t.Helper()
	fake := &sabDiag{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("mode") {
		case "get_cats":
			_, _ = w.Write([]byte(`{"categories":["*","books"]}`))
		case "get_config":
			fake.mu.Lock()
			fake.getConfig = append(fake.getConfig, q)
			fake.mu.Unlock()
			if refuseConfig {
				_, _ = w.Write([]byte(`{"status":false,"error":"API Key Incorrect"}`))
				return
			}
			switch q.Get("section") {
			case "misc":
				fmt.Fprintf(w, `{"config":{"misc":{"complete_dir":%q,"email_pwd":%q}}}`, completeDir, sabSecretPassword)
			case "categories":
				fmt.Fprintf(w, `{"config":{"categories":[{"name":"books","dir":%q}],"servers":[{"password":%q}]}}`, catDir, sabSecretPassword)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	host, port := serverAddr(t, srv.URL)
	return fake, host, port
}

func TestDiagnose_SabnzbdRelativeCategoryFolder(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Mkdir(filepath.Join(downloads, "ebooks"), 0o750); err != nil {
		t.Fatal(err)
	}
	fake, host, port := sabDiagServer(t, downloads, "ebooks", false)
	resp, raw := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
		Name: "SAB", Type: "sabnzbd", Host: host, Port: port, APIKey: sabSecretKey, Category: "books", Enabled: true,
	})
	wantDiagStatus(t, resp, diagCodeCategory, diagPass)
	wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
	if want := filepath.Join(downloads, "ebooks"); resp.Paths[0].LocalPath != want {
		t.Errorf("localPath = %q, want %q", resp.Paths[0].LocalPath, want)
	}

	// S8: only the needed section and keyword are requested.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.getConfig) != 2 {
		t.Fatalf("get_config calls = %d, want 2", len(fake.getConfig))
	}
	if q := fake.getConfig[0]; q.Get("section") != "misc" || q.Get("keyword") != "complete_dir" {
		t.Errorf("first get_config = %v, want section=misc keyword=complete_dir", q)
	}
	if q := fake.getConfig[1]; q.Get("section") != "categories" || q.Get("keyword") != "books" {
		t.Errorf("second get_config = %v, want section=categories keyword=books", q)
	}
	for _, secret := range []string{sabSecretKey, sabSecretPassword} {
		if strings.Contains(raw, secret) {
			t.Errorf("response contains secret %q: %s", secret, raw)
		}
	}
}

// TestDiagnose_SabnzbdNZBKeyIsUnknownNotFail: SABnzbd refuses get_config for an
// NZB only key. That must read as "can't tell", never as a failure.
func TestDiagnose_SabnzbdNZBKeyIsUnknownNotFail(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	_, host, port := sabDiagServer(t, "", "", true)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, &models.DownloadClient{
		Name: "SAB", Type: "sabnzbd", Host: host, Port: port, APIKey: "nzbkey-only", Category: "books", Enabled: true,
	})
	path := wantDiagStatus(t, resp, diagCodeClientPath, diagUnknown)
	if !strings.Contains(path.Message, "full API key") {
		t.Errorf("message = %q", path.Message)
	}
	for _, c := range resp.Checks {
		if c.Status == diagFail {
			t.Errorf("check %s failed: %s", c.Code, c.Message)
		}
	}
}

// TestDiagnose_ClientSecretsNeverReachTheResponse is S8: a client whose error
// replies echo its API key or password must not put either into the JSON.
func TestDiagnose_ClientSecretsNeverReachTheResponse(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	t.Run("sabnzbd api key", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "bad key %s", r.URL.Query().Get("apikey"))
		}))
		defer srv.Close()
		host, port := serverAddr(t, srv.URL)
		resp, raw := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, &models.DownloadClient{
			Name: "SAB", Type: "sabnzbd", Host: host, Port: port, APIKey: sabSecretKey, Category: "books", Enabled: true,
		})
		wantDiagStatus(t, resp, diagCodeConnect, diagFail)
		if strings.Contains(raw, sabSecretKey) {
			t.Errorf("response contains the API key: %s", raw)
		}
	})

	t.Run("qbittorrent password", func(t *testing.T) {
		const password = "qbit-pass-SECRET-3456"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "login refused for password %s", r.PostForm.Get("password"))
		}))
		defer srv.Close()
		host, port := serverAddr(t, srv.URL)
		resp, raw := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, &models.DownloadClient{
			Name: "qBit", Type: "qbittorrent", Host: host, Port: port, Username: "admin", Password: password, Category: "books", Enabled: true,
		})
		wantDiagStatus(t, resp, diagCodeConnect, diagFail)
		if strings.Contains(raw, password) {
			t.Errorf("response contains the password: %s", raw)
		}
	})

	t.Run("transmission password", func(t *testing.T) {
		const password = "trans-pass-SECRET-9012"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, pass, _ := r.BasicAuth()
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, "wrong password %s", pass)
		}))
		defer srv.Close()
		host, port := serverAddr(t, srv.URL)
		resp, raw := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, &models.DownloadClient{
			Name: "Tr", Type: "transmission", Host: host, Port: port, Username: "admin", Password: password, Enabled: true,
		})
		wantDiagStatus(t, resp, diagCodeConnect, diagFail)
		if strings.Contains(raw, password) {
			t.Errorf("response contains the password: %s", raw)
		}
	})
}

func TestDiagnose_TransmissionAsksOnlyForDownloadDir(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	var mu sync.Mutex
	var fields [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string `json:"method"`
			Arguments struct {
				Fields []string `json:"fields"`
			} `json:"arguments"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req.Method == "session-get" {
			mu.Lock()
			fields = append(fields, req.Arguments.Fields)
			mu.Unlock()
		}
		fmt.Fprintf(w, `{"result":"success","arguments":{"download-dir":%q,"rpc-version":17}}`, downloads)
	}))
	defer srv.Close()
	host, port := serverAddr(t, srv.URL)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
		Name: "Tr", Type: "transmission", Host: host, Port: port, Category: "books", Enabled: true,
	})
	wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)

	mu.Lock()
	defer mu.Unlock()
	sawDownloadDirOnly := false
	for _, f := range fields {
		if len(f) == 1 && f[0] == "download-dir" {
			sawDownloadDirOnly = true
		}
	}
	if !sawDownloadDirOnly {
		t.Errorf("session-get fields = %v, want one call asking only for download-dir", fields)
	}
}

func TestDiagnose_DelugeMoveCompletedPath(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result string
		switch req.Method {
		case "auth.login", "web.connected":
			result = "true"
		case "core.get_config_values":
			b, _ := json.Marshal(map[string]any{
				"download_location":   "/incomplete",
				"move_completed":      true,
				"move_completed_path": downloads,
			})
			result = string(b)
		default:
			result = "null"
		}
		fmt.Fprintf(w, `{"result":%s,"error":null,"id":%d}`, result, req.ID)
	}))
	defer srv.Close()
	host, port := serverAddr(t, srv.URL)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
		Name: "Deluge", Type: "deluge", Host: host, Port: port, Password: "deluge", Category: "books", Enabled: true,
	})
	wantDiagStatus(t, resp, diagCodeCategory, diagUnknown)
	wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	if resp.Paths[0].ClientPath != downloads {
		t.Errorf("clientPath = %q, want move_completed_path %q", resp.Paths[0].ClientPath, downloads)
	}
	wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
}

func TestDiagnose_NzbgetCategoryDestDir(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"version"`) {
			_, _ = w.Write([]byte(`{"version":"1.1","result":"21.0"}`))
			return
		}
		fmt.Fprintf(w, `{"version":"1.1","result":[{"Name":"MainDir","Value":%q},{"Name":"DestDir","Value":"${MainDir}/other"},{"Name":"Category1.Name","Value":"books"},{"Name":"Category1.DestDir","Value":"${MainDir}"}]}`, downloads)
	}))
	defer srv.Close()
	host, port := serverAddr(t, srv.URL)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
		Name: "NZBGet", Type: "nzbget", Host: host, Port: port, Category: "books", Enabled: true,
	})
	wantDiagStatus(t, resp, diagCodeCategory, diagPass)
	wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
}

// TestDiagnose_RtorrentUsesTheSavePathBinderySends: Bindery sends
// d.directory.set on every rTorrent add, so directory.default is not where a
// grab lands. A seedbox whose default points elsewhere and whose remap maps
// Bindery's download folder must come out green.
func TestDiagnose_RtorrentUsesTheSavePathBinderySends(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	xmlString := func(s string) string {
		return `<?xml version="1.0"?><methodResponse><params><param><value><string>` + s + `</string></value></param></params></methodResponse>`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		switch {
		case strings.Contains(string(body), "system.client_version"):
			_, _ = io.WriteString(w, xmlString("0.9.8"))
		case strings.Contains(string(body), "d.multicall2"):
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><array><data></data></array></value></param></params></methodResponse>`)
		case strings.Contains(string(body), "directory.default"):
			_, _ = io.WriteString(w, xmlString("/home/seedbox/rtorrent/default"))
		default:
			_, _ = io.WriteString(w, xmlString(""))
		}
	}))
	defer srv.Close()
	host, port := serverAddr(t, srv.URL)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
		Name: "rT", Type: "rtorrent", Host: host, Port: port, Category: "books", Enabled: true,
		PathRemap: "/home/seedbox/bindery:" + downloads,
	})
	wantDiagStatus(t, resp, diagCodeConnect, diagPass)
	wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	if p := resp.Paths[0]; p.ClientPath != "/home/seedbox/bindery" || p.Source != "the save path Bindery sends" {
		t.Errorf("paths = %+v, want the inverse remapped download folder Bindery sends", resp.Paths)
	}
	wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
}

func TestDiagnose_BadSavedHostFailsWithoutConnecting(t *testing.T) {
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, &models.DownloadClient{
		Name: "qBit", Type: "qbittorrent", Host: "10.0.0.5:8080/qbit", Port: 8080, Enabled: true,
	})
	wantDiagStatus(t, resp, diagCodeConfig, diagFail)
	wantDiagStatus(t, resp, diagCodeConnect, diagSkipped)
}

func TestDiagnose_NotFound(t *testing.T) {
	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	h.Diagnose(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/99/diagnose", nil), "id", "99"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestRunDiagChecks_SkippedCascade exercises the runner on its own.
func TestRunDiagChecks_SkippedCascade(t *testing.T) {
	ran := []string{}
	mk := func(code, status string, always bool) diagCheck {
		return diagCheck{code: code, always: always, run: func(context.Context, *diagState) []diagCheckResult {
			ran = append(ran, code)
			return one(diagCheckResult{Status: status, Message: code, Fix: "fix " + code})
		}}
	}
	st := &diagState{client: &models.DownloadClient{}}
	out := runDiagChecks(context.Background(), st, []diagCheck{
		mk("a", diagWarn, false), mk("b", diagFail, false), mk("c", diagPass, false), mk("d", diagUnknown, true),
	})
	if strings.Join(ran, ",") != "a,b,d" {
		t.Errorf("ran = %v, want a,b,d", ran)
	}
	want := []string{diagWarn, diagFail, diagSkipped, diagUnknown}
	for i, c := range out {
		if c.Status != want[i] {
			t.Errorf("check %s status = %q, want %q", c.Code, c.Status, want[i])
		}
	}
	if got := primaryFix(out); got != "fix b" {
		t.Errorf("primaryFix = %q, want the failure's fix ahead of the earlier warning", got)
	}
}
