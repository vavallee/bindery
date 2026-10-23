package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// #2036: rootless Podman with pasta reaches the host only at 169.254.1.2, so
// admin-typed service URLs may opt in to link-local. Cloud metadata never.

const linkLocalEnv = "BINDERY_ALLOW_LINK_LOCAL_SERVICES"

func TestIndexerCreate_LinkLocalNeedsOptIn(t *testing.T) {
	body := `{"name":"Prowlarr","url":"http://169.254.1.2:9696/1/api","apiKey":"k"}`
	create := func() *httptest.ResponseRecorder {
		h := indexerFixture(t)
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer", bytes.NewBufferString(body)))
		return rec
	}

	t.Setenv(linkLocalEnv, "")
	rec := create()
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), linkLocalEnv) {
		t.Fatalf("option off: got %d %s, want 400 naming %s", rec.Code, rec.Body.String(), linkLocalEnv)
	}

	t.Setenv(linkLocalEnv, "1")
	if rec := create(); rec.Code != http.StatusCreated {
		t.Fatalf("option on: got %d %s, want 201", rec.Code, rec.Body.String())
	}
}

func TestIndexerCreate_MetadataRefusedEvenWithOptIn(t *testing.T) {
	t.Setenv(linkLocalEnv, "1")
	for _, u := range []string{
		"http://169.254.169.254/latest",
		"http://169.254.170.2/v2/credentials",
		"http://[fd00:ec2::254]/latest",
		"http://[::ffff:169.254.169.254]/latest",
	} {
		h := indexerFixture(t)
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer", bytes.NewBufferString(`{"name":"x","url":"`+u+`","apiKey":"k"}`)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", u, rec.Code, rec.Body.String())
		}
	}
}

func TestProwlarrCreate_LinkLocalNeedsOptIn(t *testing.T) {
	body := `{"name":"Main","url":"http://169.254.1.2:9696","apiKey":"secret","enabled":true}`
	create := func() *httptest.ResponseRecorder {
		h, _, _, _ := prowlarrFixture(t)
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/prowlarr", bytes.NewBufferString(body)))
		return rec
	}

	t.Setenv(linkLocalEnv, "")
	rec := create()
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), linkLocalEnv) {
		t.Fatalf("option off: got %d %s, want 400 naming %s", rec.Code, rec.Body.String(), linkLocalEnv)
	}

	t.Setenv(linkLocalEnv, "1")
	if rec := create(); rec.Code != http.StatusCreated {
		t.Fatalf("option on: got %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// The download client probe validates the saved address first, then dials. With
// the option on the validation passes and the failure is a plain connect error
// (nothing listens on 169.254.1.2 here); with it off the policy refuses.
func TestDownloadClientTest_LinkLocalNeedsOptIn(t *testing.T) {
	for _, typ := range []string{"qbittorrent", "sabnzbd"} {
		run := func() *httptest.ResponseRecorder {
			h, clients := downloadClientFixture(t)
			c := &models.DownloadClient{Name: typ, Type: typ, Host: "169.254.1.2", Port: 8080, Username: "u", Password: "p", APIKey: "k", Enabled: true}
			if err := clients.Create(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/downloadclient/1/test", nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			h.Test(rec, withURLParam(req, "id", "1"))
			return rec
		}

		t.Setenv(linkLocalEnv, "")
		rec := run()
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), linkLocalEnv) {
			t.Errorf("%s option off: got %d %s, want 400 naming %s", typ, rec.Code, rec.Body.String(), linkLocalEnv)
		}

		t.Setenv(linkLocalEnv, "1")
		rec = run()
		if strings.Contains(rec.Body.String(), "not allowed") {
			t.Errorf("%s option on: policy still refused: %d %s", typ, rec.Code, rec.Body.String())
		}
	}
}

func TestDownloadClientTest_MetadataRefusedEvenWithOptIn(t *testing.T) {
	t.Setenv(linkLocalEnv, "1")
	h, clients := downloadClientFixture(t)
	c := &models.DownloadClient{Name: "q", Type: "qbittorrent", Host: "169.254.169.254", Port: 80, Username: "u", Password: "p", Enabled: true}
	if err := clients.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/1/test", nil), "id", "1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// Indexer-provided download links are not admin-typed: they stay refused for
// link-local with the option on, and never advertise the option.
func TestDownloadFetchPolicy_LinkLocalStaysRefused(t *testing.T) {
	t.Setenv(linkLocalEnv, "1")
	for _, loop := range []string{"", "1"} {
		t.Setenv(httpsec.DownloadFetchEnvVar, loop)
		err := httpsec.ValidateOutboundURL("http://169.254.1.2/x.torrent", httpsec.DownloadFetchPolicy())
		if err == nil || strings.Contains(err.Error(), linkLocalEnv) {
			t.Errorf("download link policy (loopback env %q): err = %v, want a refusal without the hint", loop, err)
		}
	}
}
