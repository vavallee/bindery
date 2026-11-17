package api

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/bookorbit"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

const bookOrbitTestKey = "bok_secret_value_that_must_never_be_returned_000000"

type stubBookOrbitClient struct {
	me        *bookorbit.Identity
	libraries []bookorbit.Library
	err       error
}

func (c *stubBookOrbitClient) Me(context.Context) (*bookorbit.Identity, error) {
	return c.me, c.err
}

func (c *stubBookOrbitClient) ListLibraries(context.Context) ([]bookorbit.Library, error) {
	return c.libraries, c.err
}

type stubBookOrbitImporter struct {
	started  []bookorbit.Config
	startErr error
	runs     []models.ABSImportRun
	limit    int
	offset   int
}

func (i *stubBookOrbitImporter) Start(_ context.Context, cfg bookorbit.Config) error {
	if i.startErr != nil {
		return i.startErr
	}
	i.started = append(i.started, cfg)
	return nil
}

func (i *stubBookOrbitImporter) Progress() abs.ImportProgress {
	return abs.ImportProgress{Running: len(i.started) > 0, Message: "reading BookOrbit libraries"}
}

func (i *stubBookOrbitImporter) RunsPage(_ context.Context, limit, offset int) ([]models.ABSImportRun, error) {
	i.limit, i.offset = limit, offset
	return i.runs, nil
}

type bookOrbitFixture struct {
	h        *BookOrbitHandler
	settings *db.SettingsRepo
	importer *stubBookOrbitImporter
	client   *stubBookOrbitClient
	// built records what the handler asked a client to be built with.
	builtURL string
	builtKey string
}

func newBookOrbitFixture(t *testing.T) *bookOrbitFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	f := &bookOrbitFixture{
		settings: db.NewSettingsRepo(database),
		importer: &stubBookOrbitImporter{},
		client:   &stubBookOrbitClient{me: &bookorbit.Identity{Username: "reader", AuthenticationMethod: "api_key"}},
	}
	f.h = NewBookOrbitHandler(f.settings, f.importer)
	f.h.newClient = func(baseURL, apiKey string) (bookOrbitClient, error) {
		f.builtURL, f.builtKey = baseURL, apiKey
		return f.client, nil
	}
	return f
}

func (f *bookOrbitFixture) save(t *testing.T) {
	t.Helper()
	rec := f.put(`{"baseUrl":"http://192.168.1.20:3000","apiKey":"` + bookOrbitTestKey + `","libraryIds":["2","17"],"pathRemap":"/books:/media/books"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save config: %d %s", rec.Code, rec.Body)
	}
}

func (f *bookOrbitFixture) put(body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/bookorbit/config", bytes.NewBufferString(body)))
	return rec
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not an error object", rec.Body)
	}
	return body["error"]
}

// The key is stored, used, and never sent back: not by the save that stores
// it and not by the read after.
func TestBookOrbitConfigNeverReturnsTheKey(t *testing.T) {
	f := newBookOrbitFixture(t)
	rec := f.put(`{"baseUrl":"http://192.168.1.20:3000/api/v1/","apiKey":"` + bookOrbitTestKey + `","libraryIds":["2","17","2"],"pathRemap":"/books:/media/books"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), bookOrbitTestKey) {
		t.Fatal("the save response carries the API key")
	}

	get := httptest.NewRecorder()
	f.h.GetConfig(get, httptest.NewRequest(http.MethodGet, "/api/v1/bookorbit/config", nil))
	if strings.Contains(get.Body.String(), bookOrbitTestKey) || strings.Contains(get.Body.String(), "bok_") {
		t.Fatalf("GET config carries the API key: %s", get.Body)
	}
	var cfg bookOrbitConfigResponse
	if err := json.Unmarshal(get.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.APIKeyConfigured || cfg.BaseURL != "http://192.168.1.20:3000" || cfg.PathRemap != "/books:/media/books" {
		t.Errorf("config = %+v", cfg)
	}
	if len(cfg.LibraryIDs) != 2 || cfg.LibraryIDs[0] != "2" || cfg.LibraryIDs[1] != "17" {
		t.Errorf("library ids = %v, want [2 17] with the duplicate dropped", cfg.LibraryIDs)
	}
	assertSettingValue(t, f.settings, context.Background(), SettingBookOrbitAPIKey, bookOrbitTestKey)
}

// The generic settings endpoints must not hand out what the BookOrbit screen
// withholds: the key to nobody, the rest to admins only.
func TestBookOrbitSettingsAreNotLeakedByTheSettingsAPI(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.save(t)
	if !isSecretSetting(SettingBookOrbitAPIKey) {
		t.Fatal("bookorbit.api_key is not classified as a secret")
	}
	for _, key := range []string{SettingBookOrbitBaseURL, SettingBookOrbitAPIKey, SettingBookOrbitLibraryIDs, SettingBookOrbitPathRemap} {
		if !isAdminOnlySetting(key) {
			t.Errorf("%s is readable by non admins", key)
		}
	}

	settings := NewSettingsHandler(f.settings)
	asUser := httptest.NewRecorder()
	settings.List(asUser, userReq(httptest.NewRequest(http.MethodGet, "/api/v1/setting", nil)))
	for _, leak := range []string{bookOrbitTestKey, "192.168.1.20", "/media/books", "bookorbit."} {
		if strings.Contains(asUser.Body.String(), leak) {
			t.Errorf("non admin settings list contains %q", leak)
		}
	}
	asAdmin := httptest.NewRecorder()
	settings.List(asAdmin, adminReq(httptest.NewRequest(http.MethodGet, "/api/v1/setting", nil)))
	if strings.Contains(asAdmin.Body.String(), bookOrbitTestKey) {
		t.Error("admin settings list contains the API key")
	}
	if !strings.Contains(asAdmin.Body.String(), "192.168.1.20") {
		t.Error("admin settings list lost the base URL, so the fixture proves nothing")
	}

	one := httptest.NewRecorder()
	settings.Get(one, adminReq(withKey(httptest.NewRequest(http.MethodGet, "/api/v1/setting/bookorbit.api_key", nil), SettingBookOrbitAPIKey)))
	if one.Code != http.StatusNotFound || strings.Contains(one.Body.String(), bookOrbitTestKey) {
		t.Errorf("GET setting bookorbit.api_key = %d %s", one.Code, one.Body)
	}
}

// Saving the form again without retyping the key keeps the key.
func TestBookOrbitConfigEmptyKeyKeepsSavedKey(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.save(t)
	for _, body := range []string{`{"pathRemap":"/books:/srv/books"}`, `{"apiKey":"","pathRemap":"/books:/srv/books"}`, `{"apiKey":"   "}`} {
		if rec := f.put(body); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		assertSettingValue(t, f.settings, context.Background(), SettingBookOrbitAPIKey, bookOrbitTestKey)
	}
	assertSettingValue(t, f.settings, context.Background(), SettingBookOrbitPathRemap, "/books:/srv/books")
}

func TestBookOrbitConfigRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"no scheme":        `{"baseUrl":"bookorbit:3000"}`,
		"other scheme":     `{"baseUrl":"file:///etc/passwd"}`,
		"metadata address": `{"baseUrl":"http://169.254.169.254/"}`,
		"credentials":      `{"baseUrl":"http://u:p@bookorbit:3000"}`,
		"key with newline": `{"apiKey":"bok_a\nb"}`,
		"library id text":  `{"libraryIds":["two"]}`,
		"library id zero":  `{"libraryIds":["0"]}`,
		"remap one sided":  `{"pathRemap":"/books"}`,
		"not json":         `{`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBookOrbitFixture(t)
			f.save(t)
			rec := f.put(body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			// A refused save changes nothing.
			ctx := context.Background()
			assertSettingValue(t, f.settings, ctx, SettingBookOrbitBaseURL, "http://192.168.1.20:3000")
			assertSettingValue(t, f.settings, ctx, SettingBookOrbitAPIKey, bookOrbitTestKey)
			assertSettingValue(t, f.settings, ctx, SettingBookOrbitLibraryIDs, `["2","17"]`)
			assertSettingValue(t, f.settings, ctx, SettingBookOrbitPathRemap, "/books:/media/books")
		})
	}
}

// A LAN address is the normal place for a BookOrbit server and must save, as
// it does for Audiobookshelf.
func TestBookOrbitConfigAllowsLANAndLoopback(t *testing.T) {
	for _, url := range []string{"http://127.0.0.1:3000", "http://10.0.0.8:3000", "http://bookorbit.media.svc:3000"} {
		f := newBookOrbitFixture(t)
		if rec := f.put(`{"baseUrl":"` + url + `"}`); rec.Code != http.StatusOK {
			t.Errorf("%s refused: %d %s", url, rec.Code, rec.Body)
		}
	}
}

// The probe routes take the address from saved settings only. A base URL in
// the request body is ignored, so a probe cannot be aimed somewhere the save
// validation never saw.
func TestBookOrbitProbeUsesSavedURLOnly(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.save(t)
	f.client.libraries = []bookorbit.Library{{ID: 17, Name: "Romantasy", Folders: []string{"/books/Romantasy"}}, {ID: 2, Name: "Fantasy"}}

	rec := httptest.NewRecorder()
	f.h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/bookorbit/test", bytes.NewBufferString(`{"baseUrl":"http://attacker.example","apiKey":"bok_typed_not_saved"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	if f.builtURL != "http://192.168.1.20:3000" {
		t.Errorf("probe went to %q, want the saved URL", f.builtURL)
	}
	if f.builtKey != "bok_typed_not_saved" {
		t.Errorf("probe did not use the key typed into the form")
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["username"] != "reader" || out["authenticationMethod"] != "api_key" || out["libraryCount"] != float64(2) {
		t.Errorf("test response = %v", out)
	}
	if strings.Contains(rec.Body.String(), "bok_") {
		t.Error("test response echoes a key")
	}

	// No key in the body means the saved one.
	rec = httptest.NewRecorder()
	f.h.Libraries(rec, httptest.NewRequest(http.MethodPost, "/api/v1/bookorbit/libraries", nil))
	if rec.Code != http.StatusOK || f.builtKey != bookOrbitTestKey {
		t.Fatalf("libraries: %d, key used = saved? %v", rec.Code, f.builtKey == bookOrbitTestKey)
	}
	var libs []bookOrbitLibraryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &libs); err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 || libs[0].Name != "Fantasy" || libs[0].ID != "2" || libs[1].Folders[0] != "/books/Romantasy" || libs[0].Folders == nil {
		t.Errorf("libraries = %+v", libs)
	}
}

func TestBookOrbitProbeErrors(t *testing.T) {
	f := newBookOrbitFixture(t)
	rec := httptest.NewRecorder()
	f.h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/bookorbit/test", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("probe with nothing saved = %d", rec.Code)
	}

	f.save(t)
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"rejected key": {&bookorbit.APIError{StatusCode: 401, Message: "Invalid API key"}, "rejected the API key"},
		"no api there": {&bookorbit.APIError{StatusCode: 404}, "no API at this address"},
		"shape":        {&bookorbit.ShapeError{Endpoint: "GET /api/v1/libraries", Detail: "not a JSON array"}, "its API may have changed"},
	} {
		f.client.err = tc.err
		rec := httptest.NewRecorder()
		f.h.Libraries(rec, httptest.NewRequest(http.MethodPost, "/api/v1/bookorbit/libraries", nil))
		if rec.Code != http.StatusBadGateway || !strings.Contains(errorOf(t, rec), tc.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), bookOrbitTestKey) {
			t.Errorf("%s: error carries the key", name)
		}
	}
}

func startBookOrbitImport(f *bookOrbitFixture, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var reader *bytes.Buffer
	if body != "" {
		reader = bytes.NewBufferString(body)
	} else {
		reader = &bytes.Buffer{}
	}
	f.h.StartImport(rec, httptest.NewRequest(http.MethodPost, "/api/v1/bookorbit/import", reader))
	return rec
}

// A POST with no body, or one that does not say, is a dry run. Writing needs
// an explicit dryRun false.
func TestBookOrbitImportIsADryRunUnlessToldOtherwise(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.save(t)
	for _, body := range []string{"", `{}`, `{"dryRun":true}`} {
		if rec := startBookOrbitImport(f, body); rec.Code != http.StatusAccepted {
			t.Fatalf("%q: %d %s", body, rec.Code, rec.Body)
		}
	}
	for idx, cfg := range f.importer.started {
		if !cfg.DryRun {
			t.Errorf("start %d was not a dry run", idx)
		}
	}
	if rec := startBookOrbitImport(f, `{"dryRun":false}`); rec.Code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	last := f.importer.started[len(f.importer.started)-1]
	if last.DryRun {
		t.Error("dryRun false still ran dry")
	}
	if last.BaseURL != "http://192.168.1.20:3000" || last.APIKey != bookOrbitTestKey || last.PathRemap != "/books:/media/books" || len(last.LibraryIDs) != 2 {
		t.Errorf("run config = %+v", last)
	}
	if rec := startBookOrbitImport(f, `{"dryRun":false}`); strings.Contains(rec.Body.String(), bookOrbitTestKey) {
		t.Error("the start response carries the key")
	}
}

// A run can be narrowed to some of the selected libraries, never widened to
// one the admin did not select.
func TestBookOrbitImportLibrarySubset(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.save(t)
	if rec := startBookOrbitImport(f, `{"libraryIds":["17"]}`); rec.Code != http.StatusAccepted {
		t.Fatalf("subset: %d %s", rec.Code, rec.Body)
	}
	if got := f.importer.started[0].LibraryIDs; len(got) != 1 || got[0] != 17 {
		t.Errorf("library ids = %v", got)
	}
	rec := startBookOrbitImport(f, `{"libraryIds":["17","5"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(errorOf(t, rec), "library 5 is not one of the selected") {
		t.Fatalf("unselected library: %d %s", rec.Code, rec.Body)
	}
	if len(f.importer.started) != 1 {
		t.Error("a refused request still started an import")
	}
}

func TestBookOrbitImportNeedsConfiguration(t *testing.T) {
	f := newBookOrbitFixture(t)
	if rec := startBookOrbitImport(f, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured: %d", rec.Code)
	}
	if rec := f.put(`{"baseUrl":"http://192.168.1.20:3000","apiKey":"` + bookOrbitTestKey + `"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	rec := startBookOrbitImport(f, `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(errorOf(t, rec), "select at least one") {
		t.Fatalf("no library selected: %d %s", rec.Code, rec.Body)
	}
	if len(f.importer.started) != 0 {
		t.Error("an import started without configuration")
	}
}

func TestBookOrbitImportBusyAndShutdown(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.save(t)
	for name, tc := range map[string]struct {
		err  error
		code int
		want string
	}{
		"own run":      {bookorbit.ErrAlreadyRunning, http.StatusConflict, "BookOrbit import is already running"},
		"engine busy":  {abs.ErrAlreadyRunning, http.StatusConflict, "BookOrbit import is already running"},
		"other import": {abs.ErrOtherImportRunning, http.StatusConflict, "another library import is running"},
		"shutdown":     {abs.ErrShuttingDown, http.StatusServiceUnavailable, "shutting down"},
	} {
		f.importer.startErr = tc.err
		rec := startBookOrbitImport(f, `{}`)
		if rec.Code != tc.code || !strings.Contains(errorOf(t, rec), tc.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// Run history pages, and shows BookOrbit's own library ids rather than the
// prefixed ones the import engine stores.
func TestBookOrbitImportRuns(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.importer.runs = []models.ABSImportRun{{
		ID: 9, SourceID: bookorbit.SourceID, SourceLabel: bookorbit.Label, LibraryID: "bookorbit-17", Status: "completed", DryRun: true,
		SourceConfigJSON: `{"sourceId":"bookorbit","libraryId":"bookorbit-17","libraryIds":["bookorbit-2","bookorbit-17"],"pathRemap":"/books:/media/books"}`,
		SummaryJSON:      `{"dryRun":true,"stats":{"booksCreated":4,"ownedMarked":5,"pendingManual":1}}`,
	}}
	rec := httptest.NewRecorder()
	f.h.ImportRuns(rec, httptest.NewRequest(http.MethodGet, "/api/v1/bookorbit/import/runs?limit=25&offset=50", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.importer.limit != 25 || f.importer.offset != 50 {
		t.Errorf("limit, offset = %d, %d; want 25, 50", f.importer.limit, f.importer.offset)
	}
	var runs []abs.PersistedImportRun
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].LibraryID != "17" || runs[0].Source.LibraryID != "17" {
		t.Fatalf("runs = %+v", runs)
	}
	if got := runs[0].Source.LibraryIDs; len(got) != 2 || got[0] != "2" || got[1] != "17" {
		t.Errorf("source library ids = %v", got)
	}
	if runs[0].Summary.Stats.BooksCreated != 4 || runs[0].Summary.Stats.OwnedMarked != 5 || runs[0].Summary.Stats.PendingManual != 1 {
		t.Errorf("summary = %+v", runs[0].Summary.Stats)
	}
	if strings.Contains(rec.Body.String(), "bookorbit-") {
		t.Errorf("response still carries an engine library id: %s", rec.Body)
	}

	// The ceiling holds.
	f.h.ImportRuns(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/bookorbit/import/runs?limit=100000", nil))
	if f.importer.limit != absImportRunsMaxLimit {
		t.Errorf("limit = %d, want the ceiling %d", f.importer.limit, absImportRunsMaxLimit)
	}
}

func TestBookOrbitImportStatus(t *testing.T) {
	f := newBookOrbitFixture(t)
	rec := httptest.NewRecorder()
	f.h.ImportStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/bookorbit/import/status", nil))
	var progress abs.ImportProgress
	if err := json.Unmarshal(rec.Body.Bytes(), &progress); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d, %v", rec.Code, err)
	}
	if progress.Running {
		t.Error("idle importer reports running")
	}
}

// A transport failure reaches the admin as a plain sentence naming only the
// host they typed. The Go error, with its request URL and resolver address,
// stays in the log.
func TestBookOrbitTransportErrorsArePlain(t *testing.T) {
	f := newBookOrbitFixture(t)
	f.save(t)
	reqURL := "http://192.168.1.20:3000/api/v1/auth/me?token=" + bookOrbitTestKey
	wrap := func(err error) error { return &url.Error{Op: "Get", URL: reqURL, Err: err} }
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"dns":               {wrap(&net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "test-unreachable.invalid", Server: "10.43.0.10:53", IsNotFound: true}}), "Host not found"},
		"refused":           {wrap(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), "Connection refused"},
		"refused windows":   {wrap(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", syscall.Errno(10061))}), "Connection refused"},
		"dns timeout":       {wrap(&net.OpError{Op: "dial", Err: &net.DNSError{Err: "i/o timeout", Name: "bookorbit.lan", Server: "10.43.0.10:53", IsTimeout: true}}), "did not answer in time"},
		"scheme mismatch":   {wrap(http.ErrSchemeMismatch), "Use an http:// URL"},
		"deadline":          {wrap(context.DeadlineExceeded), "did not answer in time"},
		"unknown authority": {wrap(x509.UnknownAuthorityError{}), "TLS problem"},
		"hostname mismatch": {wrap(x509.HostnameError{Certificate: &x509.Certificate{}, Host: "10.9.9.9"}), "TLS problem"},
		"non 2xx":           {&bookorbit.APIError{StatusCode: 500, Message: "boom at /srv/internal/path"}, "unexpected HTTP status (500)"},
		"other":             {errors.New("something odd with " + bookOrbitTestKey), "Could not reach BookOrbit"},
	} {
		f.client.err = tc.err
		for route, call := range map[string]func(http.ResponseWriter, *http.Request){"test": f.h.Test, "libraries": f.h.Libraries} {
			rec := httptest.NewRecorder()
			call(rec, httptest.NewRequest(http.MethodPost, "/api/v1/bookorbit/"+route, nil))
			got := errorOf(t, rec)
			if rec.Code != http.StatusBadGateway || !strings.Contains(got, tc.want) {
				t.Errorf("%s/%s: %d %q, want %q", name, route, rec.Code, got, tc.want)
			}
			for _, bad := range []string{bookOrbitTestKey, "/api/v1", "Get \"", "10.43.0.10", "10.9.9.9", "dial", "/srv/internal", "token="} {
				if strings.Contains(got, bad) {
					t.Errorf("%s/%s: message %q leaks %q", name, route, got, bad)
				}
			}
		}
	}
}

// A scheme mixup against a real server: https:// at a plain http listener
// and http:// at a TLS one each name the scheme to use.
func TestBookOrbitSchemeMixups(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer secure.Close()

	for name, tc := range map[string]struct {
		baseURL string
		want    string
	}{
		"https to plain": {"https://" + strings.TrimPrefix(plain.URL, "http://"), "Use an http:// URL"},
		"http to tls":    {"http://" + strings.TrimPrefix(secure.URL, "https://"), "Try https://"},
	} {
		database, err := db.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		h := NewBookOrbitHandler(db.NewSettingsRepo(database), &stubBookOrbitImporter{})
		rec := httptest.NewRecorder()
		h.SetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/bookorbit/config", bytes.NewBufferString(`{"baseUrl":"`+tc.baseURL+`","apiKey":"`+bookOrbitTestKey+`"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: save %d %s", name, rec.Code, rec.Body)
		}
		rec = httptest.NewRecorder()
		h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/bookorbit/test", nil))
		got := errorOf(t, rec)
		if rec.Code != http.StatusBadGateway || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %d %q, want %q", name, rec.Code, got, tc.want)
		}
		if strings.Contains(got, bookOrbitTestKey) || strings.Contains(got, "/api/v1") {
			t.Errorf("%s: message %q leaks", name, got)
		}
		_ = database.Close()
	}
}
