package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

type remapAssistResponse struct {
	PathVisibility *struct {
		Status     string `json:"status"`
		RemotePath string `json:"remotePath"`
	} `json:"pathVisibility"`
	RemapCandidates []struct {
		Path   string `json:"path"`
		Source string `json:"source"`
		Before string `json:"before"`
		After  string `json:"after"`
	} `json:"remapCandidates"`
	RemapNote string `json:"remapNote"`
}

func qbitStubForRemap(t *testing.T, savePath string) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.1.4"))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(`{"books":{"name":"books","savePath":"` + savePath + `"}}`))
		case "/api/v2/app/defaultSavePath":
			_, _ = w.Write([]byte(""))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func runRemapAssistTest(t *testing.T, h *DownloadClientHandler, c *models.DownloadClient) (remapAssistResponse, string) {
	t.Helper()
	id := strconv.FormatInt(c.ID, 10)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/"+id+"/test", nil), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out remapAssistResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out, rec.Body.String()
}

func TestDownloadClientTest_RemapAssist(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	visible := t.TempDir()

	t.Run("warning carries the remote path and candidates, and changes nothing", func(t *testing.T) {
		host, port := qbitStubForRemap(t, "/remote/downloads")
		h, clients := downloadClientFixture(t)
		h.WithStoragePaths("/data/downloads", "/data/audio").WithRoots(NewLibraryRoots(nil, "/books"))
		c := &models.DownloadClient{Name: "q", Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Category: "books", Enabled: true}
		if err := clients.Create(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		out, raw := runRemapAssistTest(t, h, c)
		if out.PathVisibility == nil || out.PathVisibility.Status != "warning" || out.PathVisibility.RemotePath != "/remote/downloads" {
			t.Fatalf("pathVisibility = %+v", out.PathVisibility)
		}
		var paths, sources []string
		for _, cand := range out.RemapCandidates {
			paths = append(paths, cand.Path)
			sources = append(sources, cand.Source)
		}
		if got := strings.Join(paths, ","); got != "/data/downloads,/data/audio,/books" {
			t.Fatalf("candidates = %q", got)
		}
		// Ebook folder first, then the audiobook folder, then library roots,
		// each labelled for what it is.
		if got := strings.Join(sources, ","); got != "ebookDownloadDir,audiobookDownloadDir,library" {
			t.Fatalf("sources = %q", got)
		}
		if strings.Contains(raw, "pathRemap") || strings.Contains(raw, "path_remap") {
			t.Fatalf("response hands back a remap to replay: %s", raw)
		}
		// The diagnostic is read only: the stored remap is untouched.
		stored, err := clients.GetByID(context.Background(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.PathRemap != "" {
			t.Fatalf("Test wrote path_remap %q", stored.PathRemap)
		}
	})

	t.Run("the same path on both sides says to mount it instead of offering a no-op remap", func(t *testing.T) {
		host, port := qbitStubForRemap(t, "/downloads")
		h, clients := downloadClientFixture(t)
		h.WithStoragePaths("/downloads", "")
		c := &models.DownloadClient{Name: "q", Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Category: "books", Enabled: true}
		if err := clients.Create(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		out, _ := runRemapAssistTest(t, h, c)
		if out.PathVisibility == nil || out.PathVisibility.Status != "warning" {
			t.Fatalf("pathVisibility = %+v", out.PathVisibility)
		}
		if len(out.RemapCandidates) != 0 || out.RemapNote != "samePath" {
			t.Fatalf("candidates = %+v note = %q", out.RemapCandidates, out.RemapNote)
		}
	})

	t.Run("visible path offers no candidates", func(t *testing.T) {
		host, port := qbitStubForRemap(t, "/remote/downloads")
		h, clients := downloadClientFixture(t)
		h.WithStoragePaths("/data/downloads", "")
		c := &models.DownloadClient{Name: "q", Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Category: "books", PathRemap: "/remote/downloads:" + visible, Enabled: true}
		if err := clients.Create(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		out, _ := runRemapAssistTest(t, h, c)
		if out.PathVisibility == nil || out.PathVisibility.Status != "ok" {
			t.Fatalf("pathVisibility = %+v", out.PathVisibility)
		}
		if len(out.RemapCandidates) != 0 {
			t.Fatalf("candidates = %+v", out.RemapCandidates)
		}
	})
}

// The unsaved-config Test has no saved row to apply a remap to, so it never
// carries candidates.
func TestDownloadClientTestConfig_NoRemapCandidates(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	host, port := qbitStubForRemap(t, "/remote/downloads")
	h, _ := downloadClientFixture(t)
	h.WithStoragePaths("/data/downloads", "")
	body := `{"type":"qbittorrent","host":"` + host + `","port":` + strconv.Itoa(port) + `,"username":"u","password":"p","category":"books"}`
	rec := httptest.NewRecorder()
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "remapCandidates") {
		t.Fatalf("unexpected candidates: %s", rec.Body.String())
	}
}
