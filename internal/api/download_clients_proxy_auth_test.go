package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func proxiedQbitServer(t *testing.T) (host string, port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "proxyuser" || pass != "proxypass" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v4.6.0"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	p, _ := strconv.Atoi(u.Port())
	return u.Hostname(), p
}

func TestDownloadClientProxyAuth_NeverReturnedAndKeptOnUpdate(t *testing.T) {
	h, clients := downloadClientFixture(t)
	seeded := seedDownloadClient(t, clients, models.DownloadClient{
		Name: "qBit", Type: "qbittorrent", Host: "10.10.10.11", Port: 8080, Username: "admin", Password: "qbit-pass",
		ProxyUsername: "proxyuser", ProxyPassword: "proxy-secret", Enabled: true,
	})

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/downloadclient", nil))
	if bytes.Contains(rec.Body.Bytes(), []byte("proxy-secret")) {
		t.Fatalf("list leaked the proxy password: %s", rec.Body.String())
	}
	got := []models.DownloadClient{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].ProxyUsername != "proxyuser" || !got[0].ProxyPasswordConfigured || got[0].ProxyPassword != "" {
		t.Fatalf("unexpected list shape: %+v", got)
	}

	// Edit form saves without the secret: omitted and empty both keep it.
	for _, body := range []string{
		`{"name":"qBit2","type":"qbittorrent","host":"10.10.10.11","port":8080,"enabled":true}`,
		`{"name":"qBit3","type":"qbittorrent","host":"10.10.10.11","port":8080,"enabled":true,"proxyUsername":"proxyuser","proxyPassword":""}`,
	} {
		if rec := updateDownloadClient(t, h, seeded.ID, body); rec.Code != http.StatusOK {
			t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
		} else if bytes.Contains(rec.Body.Bytes(), []byte("proxy-secret")) {
			t.Fatalf("update leaked the proxy password: %s", rec.Body.String())
		}
		stored, _ := clients.GetByID(context.Background(), seeded.ID)
		if stored.ProxyPassword != "proxy-secret" {
			t.Fatalf("proxy password not kept, got %q", stored.ProxyPassword)
		}
	}

	// An explicit flag clears it; a value together with the flag is rejected.
	if rec := updateDownloadClient(t, h, seeded.ID,
		`{"name":"x","type":"qbittorrent","host":"10.10.10.11","port":8080,"proxyPassword":"a","clearProxyPassword":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("contradiction: want 400, got %d", rec.Code)
	}
	if rec := updateDownloadClient(t, h, seeded.ID,
		`{"name":"x","type":"qbittorrent","host":"10.10.10.11","port":8080,"clearProxyPassword":true}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	stored, _ := clients.GetByID(context.Background(), seeded.ID)
	if stored.ProxyPassword != "" {
		t.Fatalf("proxy password not cleared, got %q", stored.ProxyPassword)
	}
}

func TestDownloadClientProxyAuth_OnlyKeptForQbittorrent(t *testing.T) {
	h, clients := downloadClientFixture(t)
	body := `{"name":"SAB","host":"10.10.10.10","type":"sabnzbd","apiKey":"k","proxyUsername":"u","proxyPassword":"p","enabled":true}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	list, _ := clients.List(context.Background())
	if len(list) != 1 || list[0].ProxyUsername != "" || list[0].ProxyPassword != "" {
		t.Fatalf("proxy auth stored on a non qBittorrent client: %+v", list)
	}
}

func TestDownloadClientProxyAuth_TestConfigUsesIt(t *testing.T) {
	h, _ := downloadClientFixture(t)
	host, port := proxiedQbitServer(t)
	body := `{"name":"qBit","type":"qbittorrent","host":"` + host + `","port":` + strconv.Itoa(port) +
		`,"username":"admin","password":"x","proxyUsername":"proxyuser","proxyPassword":"proxypass"}`
	rec := httptest.NewRecorder()
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("test config behind a basic-auth proxy: %d %s", rec.Code, rec.Body.String())
	}
}

// The edit form never holds the proxy password, so Test must fill it in from
// the saved row.
func TestDownloadClientProxyAuth_TestConfigHydratesStoredSecret(t *testing.T) {
	h, clients := downloadClientFixture(t)
	host, port := proxiedQbitServer(t)
	seeded := seedDownloadClient(t, clients, models.DownloadClient{
		Name: "qBit", Type: "qbittorrent", Host: host, Port: port, Username: "admin", Password: "x",
		ProxyUsername: "proxyuser", ProxyPassword: "proxypass", Enabled: true,
	})
	body := `{"id":` + strconv.FormatInt(seeded.ID, 10) + `,"name":"qBit","type":"qbittorrent","host":"` + host +
		`","port":` + strconv.Itoa(port) + `,"username":"admin","password":"x","proxyUsername":"proxyuser"}`
	rec := httptest.NewRecorder()
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("test config with stored proxy secret: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDownloadClientProxyAuth_StoredTestUsesIt(t *testing.T) {
	h, clients := downloadClientFixture(t)
	host, port := proxiedQbitServer(t)
	seeded := seedDownloadClient(t, clients, models.DownloadClient{
		Name: "qBit", Type: "qbittorrent", Host: host, Port: port, Username: "admin", Password: "x",
		ProxyUsername: "proxyuser", ProxyPassword: "proxypass", Enabled: true,
	})
	idStr := strconv.FormatInt(seeded.ID, 10)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/"+idStr+"/test", nil), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("stored test: %d %s", rec.Code, rec.Body.String())
	}
}
