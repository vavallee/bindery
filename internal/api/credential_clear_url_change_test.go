package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// secretRecorder is a stand-in for a server the admin moved an integration to.
// It records everything a request carries so a test can prove a saved secret
// never arrived.
type secretRecorder struct {
	mu   sync.Mutex
	seen []string
	srv  *httptest.Server
}

func newSecretRecorder(t *testing.T) *secretRecorder {
	t.Helper()
	rec := &secretRecorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.seen = append(rec.seen, r.URL.String())
		for k, v := range r.Header {
			rec.seen = append(rec.seen, k+"="+strings.Join(v, ","))
		}
		if u, p, ok := r.BasicAuth(); ok {
			rec.seen = append(rec.seen, "basic="+u+":"+p)
		}
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1"}`))
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (s *secretRecorder) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func (s *secretRecorder) assertNoSecret(t *testing.T, secrets ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) == 0 {
		t.Fatal("the probe never reached the new server, so the test proves nothing")
	}
	all := strings.Join(s.seen, "\n")
	for _, secret := range secrets {
		if strings.Contains(all, secret) {
			t.Fatalf("saved secret %q reached the new server:\n%s", secret, all)
		}
	}
}

func (s *secretRecorder) hostPort(t *testing.T) (string, int) {
	t.Helper()
	u, err := url.Parse(s.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	return u.Hostname(), port
}

func urlIDParam(req *http.Request, id int64) *http.Request {
	return withURLParam(req, "id", strconv.FormatInt(id, 10))
}

func TestIndexerUpdate_URLChangeDropsKeyAndTestSendsNone(t *testing.T) {
	ctx := context.Background()
	h := indexerFixture(t)
	idx := &models.Indexer{Name: "NZB", URL: "http://10.20.30.41:9117", Type: "newznab", APIKey: "idx-secret", Categories: []int{7020}}
	if err := h.indexers.Create(ctx, idx); err != nil {
		t.Fatal(err)
	}
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Update(rec, urlIDParam(httptest.NewRequest(http.MethodPut, "/indexer/1", bytes.NewBufferString(body)), idx.ID))
		if rec.Code != http.StatusOK {
			t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("response echoed a secret: %s", rec.Body.String())
		}
	}
	stored := func() string {
		got, _ := h.indexers.GetByID(ctx, idx.ID)
		return got.APIKey
	}

	// Same URL spelled differently: key stays.
	put(`{"name":"NZB","url":"http://10.20.30.41:9117/","type":"newznab"}`)
	if stored() != "idx-secret" {
		t.Fatalf("key lost on a no-op URL edit: %q", stored())
	}

	// New URL, new key: both stored.
	other := newSecretRecorder(t)
	put(`{"name":"NZB","url":"` + other.srv.URL + `","type":"newznab","apiKey":"fresh-secret"}`)
	if stored() != "fresh-secret" {
		t.Fatalf("new key not stored: %q", stored())
	}

	// Back to the first server, then move without a key: cleared, and the
	// by-id test sends nothing secret.
	put(`{"name":"NZB","url":"http://10.20.30.41:9117","type":"newznab","apiKey":"idx-secret"}`)
	put(`{"name":"NZB","url":"` + other.srv.URL + `","type":"newznab"}`)
	if stored() != "" {
		t.Fatalf("key survived the move: %q", stored())
	}
	rec := httptest.NewRecorder()
	h.Test(rec, urlIDParam(httptest.NewRequest(http.MethodPost, "/indexer/1/test", nil), idx.ID))
	other.assertNoSecret(t, "idx-secret")
}

func TestProwlarrUpdate_URLChangeDropsKeyAndTestSendsNone(t *testing.T) {
	ctx := context.Background()
	h, instances, _, _ := prowlarrFixture(t)
	p := &models.ProwlarrInstance{Name: "P", URL: "http://10.20.30.41:9696", APIKey: "prow-secret", Enabled: true}
	if err := instances.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Update(rec, urlIDParam(httptest.NewRequest(http.MethodPut, "/prowlarr/1", bytes.NewBufferString(body)), p.ID))
		if rec.Code != http.StatusOK {
			t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("response echoed a secret: %s", rec.Body.String())
		}
	}
	stored := func() string {
		got, _ := instances.GetByID(ctx, p.ID)
		return got.APIKey
	}

	put(`{"name":"P","url":"http://10.20.30.41:9696/","enabled":true}`)
	if stored() != "prow-secret" {
		t.Fatalf("key lost on a no-op URL edit: %q", stored())
	}

	other := newSecretRecorder(t)
	put(`{"name":"P","url":"` + other.srv.URL + `","enabled":true,"apiKey":"fresh-secret"}`)
	if stored() != "fresh-secret" {
		t.Fatalf("new key not stored: %q", stored())
	}

	put(`{"name":"P","url":"http://10.20.30.41:9696","enabled":true,"apiKey":"prow-secret"}`)
	put(`{"name":"P","url":"` + other.srv.URL + `","enabled":true}`)
	if stored() != "" {
		t.Fatalf("key survived the move: %q", stored())
	}
	rec := httptest.NewRecorder()
	h.Test(rec, urlIDParam(httptest.NewRequest(http.MethodPost, "/prowlarr/1/test", nil), p.ID))
	other.assertNoSecret(t, "prow-secret")
}

func TestDownloadClientUpdate_HostChangeDropsCredentialsAndTestSendsNone(t *testing.T) {
	ctx := context.Background()
	h, clients := downloadClientFixture(t)
	seeded := seedDownloadClient(t, clients, models.DownloadClient{
		Name: "SAB", Type: "sabnzbd", Host: "10.10.10.10", Port: 8080, APIKey: "dc-secret",
		Category: "books", Enabled: true,
	})
	stored := func() *models.DownloadClient {
		got, _ := clients.GetByID(ctx, seeded.ID)
		return got
	}

	// Same endpoint, host in another case: credentials stay.
	rec := updateDownloadClient(t, h, seeded.ID, `{"name":"SAB","host":"10.10.10.10","port":8080,"type":"sabnzbd","category":"books"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-op update = %d: %s", rec.Code, rec.Body.String())
	}
	if s := stored(); s.APIKey != "dc-secret" {
		t.Fatalf("credentials lost on a no-op edit: %+v", s)
	}

	// Move with a new key only: the key is stored, the password and proxy
	// password are not carried over.
	other := newSecretRecorder(t)
	host, port := other.hostPort(t)
	moved := `{"name":"SAB","host":"` + host + `","port":` + strconv.Itoa(port) + `,"type":"sabnzbd","category":"books"`
	rec = updateDownloadClient(t, h, seeded.ID, moved+`,"apiKey":"fresh-secret"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move with key = %d: %s", rec.Code, rec.Body.String())
	}
	if s := stored(); s.APIKey != "fresh-secret" {
		t.Fatalf("after move with key: %+v", s)
	}

	// Move back, restore secrets, then move without any: everything cleared and
	// the by-id test sends no secret to the new host.
	_ = clients.Update(ctx, &models.DownloadClient{
		ID: seeded.ID, Name: "SAB", Type: "sabnzbd", Host: "10.10.10.10", Port: 8080, APIKey: "dc-secret",
		Category: "books", Enabled: true,
	})
	rec = updateDownloadClient(t, h, seeded.ID, moved+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move without key = %d: %s", rec.Code, rec.Body.String())
	}
	if s := stored(); s.APIKey != "" {
		t.Fatalf("credentials survived the move: %+v", s)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("response echoed a secret: %s", rec.Body.String())
	}
	trec := httptest.NewRecorder()
	h.Test(trec, urlIDParam(httptest.NewRequest(http.MethodPost, "/downloadclient/1/test", nil), seeded.ID))
	other.assertNoSecret(t, "dc-secret")
}
