package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func qbitCategoryServer(t *testing.T, savePath string) *httptest.Server {
	t.Helper()
	quoted, err := json.Marshal(savePath)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(`{"books":{"name":"books","savePath":` + string(quoted) + `}}`))
		case "/api/v2/app/defaultSavePath":
			_, _ = w.Write([]byte(""))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// The raw client path is exposed so the UI can seed a remap (#2031).
func TestPathVisibility_RemotePathPopulated(t *testing.T) {
	visible := t.TempDir()
	srv := qbitCategoryServer(t, "/remote/downloads")
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)

	t.Run("not visible", func(t *testing.T) {
		client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Category: "books"}
		got := CheckCompletedPathVisibility(context.Background(), client, "/d", "", "")
		if got.Status != PathNotVisible {
			t.Fatalf("status = %q", got.Status)
		}
		if got.RemotePath != "/remote/downloads" {
			t.Fatalf("RemotePath = %q, want /remote/downloads", got.RemotePath)
		}
	})
	t.Run("visible keeps the client path, not the remapped one", func(t *testing.T) {
		client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Category: "books", PathRemap: "/remote/downloads:" + visible}
		got := CheckCompletedPathVisibility(context.Background(), client, "/d", "", "")
		if got.Status != PathVisible || got.RemotePath != "/remote/downloads" || got.Path != filepath.Clean(visible) {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestPathVisibility_RemotePathWindows(t *testing.T) {
	srv := qbitCategoryServer(t, `S:\Downloads\bindery`)
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Category: "books"}
	got := CheckCompletedPathVisibility(context.Background(), client, "/d", "", "")
	// The qBittorrent adapter hands the path over with forward slashes; both
	// separator styles are the same Windows path to pathmap.
	if got.RemotePath != "S:/Downloads/bindery" {
		t.Fatalf("RemotePath = %q", got.RemotePath)
	}
	cands, _ := RemapCandidates("qbittorrent", got.RemotePath, []RemapRoot{{Path: "/data/downloads", Source: RemapSourceEbookDir}})
	if len(cands) != 1 || cands[0].After != "/data/downloads/Example Book" {
		t.Fatalf("candidates from the reported path = %+v", cands)
	}
}

func TestSupportsRemapAssistant(t *testing.T) {
	for typ, want := range map[string]bool{
		"qbittorrent": true, "nzbget": true, "rtorrent": true,
		"sabnzbd": false, "transmission": false, "deluge": false, "": false,
	} {
		if got := SupportsRemapAssistant(typ); got != want {
			t.Errorf("SupportsRemapAssistant(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestRemapCandidates(t *testing.T) {
	roots := []RemapRoot{
		{Path: "/data/downloads", Source: RemapSourceEbookDir},
		{Path: "/data/downloads/", Source: RemapSourceLibrary}, // duplicate once cleaned
		{Path: "", Source: RemapSourceAudiobookDir},
		{Path: "/books", Source: RemapSourceLibrary},
	}

	t.Run("posix", func(t *testing.T) {
		got, note := RemapCandidates("qbittorrent", "/downloads/books", roots)
		if len(got) != 2 || note != "" {
			t.Fatalf("got %+v note %q, want 2 candidates", got, note)
		}
		if got[0].Path != "/data/downloads" || got[0].Source != RemapSourceEbookDir {
			t.Errorf("first = %+v", got[0])
		}
		if got[0].Before != "/downloads/books/Example Book" || got[0].After != "/data/downloads/Example Book" {
			t.Errorf("preview = %q -> %q", got[0].Before, got[0].After)
		}
	})
	t.Run("windows drive", func(t *testing.T) {
		got, _ := RemapCandidates("rtorrent", `S:\Downloads\books`, roots[:1])
		if len(got) != 1 || got[0].Before != `S:\Downloads\books\Example Book` || got[0].After != "/data/downloads/Example Book" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("windows share", func(t *testing.T) {
		got, _ := RemapCandidates("nzbget", `\\nas\books\dl`, roots[:1])
		if len(got) != 1 || got[0].Before != `\\nas\books\dl\Example Book` || got[0].After != "/data/downloads/Example Book" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("unsupported type offers nothing", func(t *testing.T) {
		for _, typ := range []string{"sabnzbd", "transmission", "deluge", ""} {
			if got, note := RemapCandidates(typ, "/downloads", roots); got != nil || note != "" {
				t.Errorf("%q: got %+v %q, want none", typ, got, note)
			}
		}
	})
	t.Run("no remote path offers nothing", func(t *testing.T) {
		if got, note := RemapCandidates("qbittorrent", "  ", roots); got != nil || note != "" {
			t.Errorf("got %+v %q", got, note)
		}
	})
}

// The commonest not visible case: the client reports /downloads and Bindery's
// own download folder is /downloads too, with nothing mounted there. A remap
// of /downloads onto itself changes nothing, so it is never offered (#2031).
func TestRemapCandidates_IdentityIsSkipped(t *testing.T) {
	same := []RemapRoot{{Path: "/downloads", Source: RemapSourceEbookDir}}

	for _, typ := range []string{"qbittorrent", "rtorrent", "nzbget"} {
		got, note := RemapCandidates(typ, "/downloads", same)
		if len(got) != 0 || note != RemapNoteSamePath {
			t.Errorf("%s: got %+v note %q, want no candidates and %q", typ, got, note, RemapNoteSamePath)
		}
	}
	t.Run("trailing slash and subfolder both count as the same path", func(t *testing.T) {
		got, note := RemapCandidates("qbittorrent", "/downloads/", same)
		if len(got) != 0 || note != RemapNoteSamePath {
			t.Errorf("got %+v note %q", got, note)
		}
	})
	t.Run("other roots survive and no note is raised", func(t *testing.T) {
		got, note := RemapCandidates("rtorrent", "/downloads", []RemapRoot{
			{Path: "/downloads", Source: RemapSourceEbookDir},
			{Path: "/data/dl", Source: RemapSourceAudiobookDir},
		})
		if len(got) != 1 || got[0].Path != "/data/dl" || note != "" {
			t.Errorf("got %+v note %q", got, note)
		}
	})
}

// A path holding a comma or a colon cannot be written in the from:to,from:to
// syntax, so no rule is generated for it and the panel says why.
func TestRemapCandidates_SyntaxCharacters(t *testing.T) {
	roots := []RemapRoot{{Path: "/data/downloads", Source: RemapSourceEbookDir}}

	for _, remote := range []string{"/data/Books, Audio", "/mnt/a:b"} {
		got, note := RemapCandidates("qbittorrent", remote, roots)
		if len(got) != 0 || note != RemapNoteUnsupportedChars {
			t.Errorf("remote %q: got %+v note %q", remote, got, note)
		}
	}
	t.Run("a local folder with one is skipped, the rest kept", func(t *testing.T) {
		got, note := RemapCandidates("qbittorrent", "/downloads", []RemapRoot{
			{Path: "/mnt/a:b", Source: RemapSourceEbookDir},
			{Path: "/data/x,y", Source: RemapSourceAudiobookDir},
			{Path: "/data/ok", Source: RemapSourceLibrary},
		})
		if len(got) != 1 || got[0].Path != "/data/ok" || note != "" {
			t.Errorf("got %+v note %q", got, note)
		}
	})
	t.Run("only unusable local folders raises the note", func(t *testing.T) {
		got, note := RemapCandidates("qbittorrent", "/downloads", []RemapRoot{{Path: "/mnt/a:b", Source: RemapSourceEbookDir}})
		if len(got) != 0 || note != RemapNoteUnsupportedChars {
			t.Errorf("got %+v note %q", got, note)
		}
	})
	t.Run("a Windows drive colon is not a syntax character", func(t *testing.T) {
		got, note := RemapCandidates("qbittorrent", `S:\Downloads`, roots)
		if len(got) != 1 || note != "" {
			t.Errorf("got %+v note %q", got, note)
		}
	})
}
