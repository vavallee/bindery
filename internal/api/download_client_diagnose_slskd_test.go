package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

type slskdDiag struct {
	app        string
	options    string
	roleStatus int
	sawQuery   bool
}

func (d *slskdDiag) server(t *testing.T) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			d.sawQuery = true
		}
		if r.Header.Get("X-API-Key") != "diagdiagdiag" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v0/application":
			_, _ = w.Write([]byte(d.app))
		case r.URL.Path == "/api/v0/options":
			_, _ = w.Write([]byte(d.options))
		case strings.HasPrefix(r.URL.Path, "/api/v0/searches/") && r.Method == http.MethodPut:
			w.WriteHeader(d.roleStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return serverAddr(t, srv.URL)
}

const slskdGoodApp = `{"version":{"current":"0.26.1"},"server":{"isConnected":true,"isLoggedIn":true},"vpn":{"forwardedPort":50300}}`

func slskdClient(host string, port int, remap string) *models.DownloadClient {
	return &models.DownloadClient{Name: "slskd", Type: "slskd", Host: host, Port: port, APIKey: "diagdiagdiag", PathRemap: remap, Enabled: true}
}

func TestDiagnose_SlskdAllPass(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	d := &slskdDiag{app: slskdGoodApp, options: `{"directories":{"downloads":"/app/downloads"}}`, roleStatus: http.StatusNotFound}
	host, port := d.server(t)

	resp, raw := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, slskdClient(host, port, "/app/downloads:"+downloads))

	for _, code := range []string{diagCodeConfig, diagCodeConnect, diagCodeCategory, diagCodeClientPath, diagCodeRemap, diagCodeLocalPath,
		diagCodeSlskdVersion, diagCodeSlskdLogin, diagCodeSlskdRole, diagCodeSlskdPort} {
		wantDiagStatus(t, resp, code, diagPass)
	}
	if len(resp.Paths) != 1 || resp.Paths[0].ClientPath != "/app/downloads" || resp.Paths[0].LocalPath != downloads {
		t.Errorf("paths = %+v", resp.Paths)
	}
	if d.sawQuery {
		t.Error("a request carried a query string")
	}
	if strings.Contains(raw, "diagdiagdiag") {
		t.Error("the API key appears in the response")
	}
}

func TestDiagnose_SlskdOldVersion(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: `{"version":{"current":"0.25.1"},"server":{"isConnected":true,"isLoggedIn":true}}`, roleStatus: http.StatusNotFound}
	host, port := d.server(t)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, slskdClient(host, port, ""))
	c := wantDiagStatus(t, resp, diagCodeSlskdVersion, diagFail)
	if !strings.Contains(c.Message, "0.26.0") || !strings.Contains(c.Fix, "Update slskd") {
		t.Errorf("message %q fix %q", c.Message, c.Fix)
	}
	if resp.PrimaryFix == "" {
		t.Error("expected a primary fix")
	}
}

func TestDiagnose_SlskdNotLoggedIn(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: `{"version":{"current":"0.26.0"},"server":{"isConnected":true,"isLoggedIn":false}}`, roleStatus: http.StatusNotFound}
	host, port := d.server(t)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, slskdClient(host, port, ""))
	c := wantDiagStatus(t, resp, diagCodeSlskdLogin, diagFail)
	if !strings.Contains(c.Message, "not logged in") {
		t.Errorf("message %q", c.Message)
	}
}

func TestDiagnose_SlskdNotConnected(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: `{"version":{"current":"0.26.0"},"server":{"isConnected":false,"isLoggedIn":false}}`, roleStatus: http.StatusNotFound}
	host, port := d.server(t)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, slskdClient(host, port, ""))
	wantDiagStatus(t, resp, diagCodeSlskdLogin, diagFail)
}

func TestDiagnose_SlskdReadOnlyKey(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: slskdGoodApp, roleStatus: http.StatusForbidden}
	host, port := d.server(t)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, slskdClient(host, port, ""))
	c := wantDiagStatus(t, resp, diagCodeSlskdRole, diagFail)
	if !strings.Contains(c.Message, "read only") || !strings.Contains(c.Fix, "readwrite") {
		t.Errorf("message %q fix %q", c.Message, c.Fix)
	}
}

func TestDiagnose_SlskdRoleCouldNotBeVerified(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: slskdGoodApp, roleStatus: http.StatusInternalServerError}
	host, port := d.server(t)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, slskdClient(host, port, ""))
	c := wantDiagStatus(t, resp, diagCodeSlskdRole, diagUnknown)
	if !strings.Contains(c.Message, "could not verify") {
		t.Errorf("message %q", c.Message)
	}
}

func TestDiagnose_SlskdNoForwardedPortIsInformational(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: `{"version":{"current":"0.26.0"},"server":{"isConnected":true,"isLoggedIn":true},"vpn":{"forwardedPort":null}}`, roleStatus: http.StatusNotFound}
	host, port := d.server(t)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, slskdClient(host, port, ""))
	c := wantDiagStatus(t, resp, diagCodeSlskdPort, diagUnknown)
	if !strings.Contains(c.Message, "cannot detect") || !strings.Contains(c.Fix, "listen port") {
		t.Errorf("message %q", c.Message)
	}
	for _, other := range resp.Checks {
		if other.Code != diagCodeSlskdPort && other.Status == diagFail {
			t.Errorf("%s failed because of an advisory: %+v", other.Code, other)
		}
	}
}

func TestDiagnose_SlskdWrongKey(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: slskdGoodApp, roleStatus: http.StatusNotFound}
	host, port := d.server(t)
	c := slskdClient(host, port, "")
	c.APIKey = "wrongwrongwrong"
	resp, raw := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, c)
	wantDiagStatus(t, resp, diagCodeConnect, diagFail)
	for _, code := range []string{diagCodeSlskdVersion, diagCodeSlskdLogin, diagCodeSlskdRole, diagCodeSlskdPort} {
		wantDiagStatus(t, resp, code, diagSkipped)
	}
	if strings.Contains(raw, "wrongwrongwrong") {
		t.Error("the API key appears in the response")
	}
}

func TestDiagnose_SlskdOversizedBody(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: `{"version":{"current":"` + strings.Repeat("9", 2<<20) + `"}}`, roleStatus: http.StatusNotFound}
	host, port := d.server(t)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, slskdClient(host, port, ""))
	c := wantDiagStatus(t, resp, diagCodeConnect, diagFail)
	if !strings.Contains(c.Message, "larger than") {
		t.Errorf("message %q", c.Message)
	}
}

func TestDiagnose_SlskdNoDownloadsFolderReported(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	d := &slskdDiag{app: slskdGoodApp, options: `{}`, roleStatus: http.StatusNotFound}
	host, port := d.server(t)
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: t.TempDir()}, slskdClient(host, port, ""))
	wantDiagStatus(t, resp, diagCodeClientPath, diagUnknown)
}
