package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// These tests pin one rule: text that came from the database, the filesystem
// or another service never reaches an API client. Each case feeds a handler
// an error that would reveal infrastructure if echoed (a *url.Error carrying
// an API key and a resolver address, a SQL driver message, an os.PathError)
// and asserts the response body is the fixed message and nothing else. Modelled
// on TestCalibreDeliveries_ServerErrorsDoNotLeak.

const leakSecret = "SECRETKEY123"

// leakyNeedles are fragments no response body may contain, whichever handler
// produced it.
var leakyNeedles = []string{
	leakSecret, "sql", "SQL", "database", "dial tcp", "no such host",
	"10.43.0.10", "no such file", "permission denied", "/etc/", "apikey",
}

// transportLeak is what a metadata client returns when DNS fails: the
// request URL (with the API key) and the cluster resolver address.
func transportLeak() error {
	return &url.Error{
		Op:  "Get",
		URL: "https://provider.example/v1/books?key=" + leakSecret,
		Err: errors.New("dial tcp: lookup provider.example on 10.43.0.10:53: no such host"),
	}
}

// sqlLeak is what a storage fault looks like once it has been wrapped.
func sqlLeak() error { return errors.New("sql: database is locked: apikey=" + leakSecret) }

func errorField(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%q)", err, rec.Body.String())
	}
	msg, _ := body["error"].(string)
	return msg
}

// wantFixed asserts status, the exact error text, and the absence of every
// leaky fragment from the whole body.
func wantFixed(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d; body = %s", rec.Code, status, rec.Body.String())
	}
	if got := errorField(t, rec); got != msg {
		t.Errorf("error = %q, want %q", got, msg)
	}
	for _, needle := range leakyNeedles {
		if strings.Contains(rec.Body.String(), needle) {
			t.Errorf("body leaks %q: %s", needle, rec.Body.String())
		}
	}
}

const (
	msgServerError = "internal server error"
	msgUpstream    = "metadata provider unavailable"
	msgSendFailed  = "failed to send the release to the download client"
)

// ---- E1: queue grab and retry ----

func grabBody(releaseURL string) string {
	return `{"guid":"leak-guid","nzbUrl":"` + releaseURL + `","title":"Leak Test","protocol":"usenet"}`
}

// leakyQueue wires a queue handler whose only client is a SABnzbd stand-in
// that refuses every release with a secret looking message, plus an indexer
// stand-in that serves the NZB.
func leakyQueue(t *testing.T) (*QueueHandler, *db.DownloadRepo, string) {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": "apikey=" + leakSecret + " refused"})
	}))
	t.Cleanup(sab.Close)
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	t.Cleanup(indexerSrv.Close)

	h, _, downloads, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, sab.URL)
	if err := clients.Create(ctx, &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true}); err != nil {
		t.Fatal(err)
	}
	return h, downloads, indexerSrv.URL + "/x.nzb"
}

func postGrab(h *QueueHandler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewBufferString(body)))
	return rec
}

func TestErrorLeak_GrabSendFailureIsGeneric502(t *testing.T) {
	h, _, release := leakyQueue(t)
	wantFixed(t, postGrab(h, grabBody(release)), http.StatusBadGateway, msgSendFailed)
}

func TestErrorLeak_GrabDatabaseErrorIsGeneric500(t *testing.T) {
	h, database, _, _, _, _ := queueFixture(t)
	_ = database.Close()
	wantFixed(t, postGrab(h, grabBody("http://indexer.example/x.nzb")), http.StatusInternalServerError, msgServerError)
}

// A missing client is a setup problem, not an outage: 400 with the actionable
// text and a code, so the web app no longer reads the message to tell.
func TestErrorLeak_GrabNoClientIs400WithCode(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	rec := postGrab(h, grabBody("http://indexer.example/x.nzb"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "no_download_client" || !strings.Contains(body["error"], "no enabled") {
		t.Errorf("body = %v, want code no_download_client and the actionable text", body)
	}
}

func TestErrorLeak_RetrySendFailureIsGeneric502(t *testing.T) {
	h, downloads, release := leakyQueue(t)
	dl := &models.Download{GUID: "leak-retry", Title: "T", Status: models.StateFailed, Protocol: "usenet", NZBURL: release}
	if err := downloads.Create(context.Background(), dl); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.RetryDownload(rec, retryDownloadRequest(t, dl.ID))
	wantFixed(t, rec, http.StatusBadGateway, msgSendFailed)
}

func TestErrorLeak_RetryNoClientIs400WithCode(t *testing.T) {
	h, _, downloads, _, _, ctx := queueFixture(t)
	dl := &models.Download{GUID: "leak-noclient", Title: "T", Status: models.StateFailed, Protocol: "usenet", NZBURL: "http://example.invalid/x.nzb"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.RetryDownload(rec, retryDownloadRequest(t, dl.ID))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"no_download_client"`) {
		t.Fatalf("status = %d, body = %s; want 400 with the no_download_client code", rec.Code, rec.Body.String())
	}
}

// A bulk retry reports per row, and a row's message is shown to the user, so
// it follows the same rule as the single route.
func TestErrorLeak_BulkRetryPerRowMessageIsGeneric(t *testing.T) {
	h, downloads, release := leakyQueue(t)
	dl := &models.Download{GUID: "leak-bulk", Title: "T", Status: models.StateFailed, Protocol: "usenet", NZBURL: release}
	if err := downloads.Create(context.Background(), dl); err != nil {
		t.Fatal(err)
	}
	rec, parsed := postBulkRetry(t, h, `{"ids":[`+strconv.FormatInt(dl.ID, 10)+`]`+`}`)
	got := parsed.Results[strconv.FormatInt(dl.ID, 10)]
	if got.OK || got.Error != msgSendFailed {
		t.Errorf("row result = %+v, want the generic send failure", got)
	}
	for _, needle := range leakyNeedles {
		if strings.Contains(rec.Body.String(), needle) {
			t.Errorf("bulk response leaks %q: %s", needle, rec.Body.String())
		}
	}
}

func TestErrorLeak_BulkDeletePerRowDatabaseErrorIsGeneric(t *testing.T) {
	h, database, downloads, _, _, ctx := queueFixture(t)
	dl := &models.Download{GUID: "leak-bulkdel", Title: "T", Status: models.StateFailed, Protocol: "usenet", NZBURL: "http://example.invalid/x.nzb"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TRIGGER leak_block_delete BEFORE DELETE ON downloads BEGIN SELECT RAISE(ABORT, 'sql: apikey=` + leakSecret + `'); END`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.BulkDelete(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/bulk-delete", bytes.NewBufferString(`{"ids":[`+strconv.FormatInt(dl.ID, 10)+`]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, needle := range leakyNeedles {
		if strings.Contains(rec.Body.String(), needle) {
			t.Errorf("bulk delete response leaks %q: %s", needle, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), msgServerError) {
		t.Errorf("row error should be the generic message: %s", rec.Body.String())
	}
}

// ---- E2: metadata provider errors ----

type leakyAudnex struct{ err error }

func (c leakyAudnex) GetBook(context.Context, string) (*audnex.Book, error) { return nil, c.err }

func TestErrorLeak_EnrichAudiobookProviderError(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	h.meta = metadata.NewAggregator(&stubMetaProvider{name: "openlibrary"}).WithAudnexClient(leakyAudnex{err: transportLeak()})
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t", ASIN: "B000000001",
		Status: "wanted", MediaType: models.MediaTypeAudiobook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(book.ID, 10)
	rec := httptest.NewRecorder()
	h.EnrichAudiobook(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/"+id+"/enrich", nil), "id", id))
	wantFixed(t, rec, http.StatusBadGateway, msgUpstream)
}

func TestErrorLeak_RebindProviderError(t *testing.T) {
	h, _, _, _, _, book, _ := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{err: transportLeak()})
	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	wantFixed(t, rec, http.StatusBadGateway, msgUpstream)
}

func TestErrorLeak_RelinkAuthorProviderErrors(t *testing.T) {
	provider := func() *metadata.Aggregator {
		return metadata.NewAggregator(&covAuSearchProvider{
			stubMetaProvider: stubMetaProvider{name: "openlibrary"},
			searchErr:        transportLeak(), getErr: transportLeak(),
		})
	}
	t.Run("relink search", func(t *testing.T) {
		env := covAuSetup(t)
		target := &models.Author{ForeignID: "abs:author:jay", Name: "Jay Dee", SortName: "Dee, Jay", MetadataProvider: "audiobookshelf", Monitored: true}
		if err := env.authors.Create(env.ctx, target); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		covAuHandler(env, provider()).RelinkUpstream(rec, covAuRequest(http.MethodPost, "/", "", map[string]string{"id": covAuID(target.ID)}, 0, ""))
		wantFixed(t, rec, http.StatusBadGateway, msgUpstream)
	})
	t.Run("relink explicit id", func(t *testing.T) {
		env := covAuSetup(t)
		target := &models.Author{ForeignID: "abs:author:jay", Name: "Jay Dee", SortName: "Dee, Jay", MetadataProvider: "audiobookshelf", Monitored: true}
		if err := env.authors.Create(env.ctx, target); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		covAuHandler(env, provider()).RelinkUpstream(rec, covAuRequest(http.MethodPost, "/", `{"foreignAuthorId":"OL-X"}`, map[string]string{"id": covAuID(target.ID)}, 0, ""))
		wantFixed(t, rec, http.StatusBadGateway, msgUpstream)
	})
	t.Run("candidates", func(t *testing.T) {
		env := covAuSetup(t)
		rec := httptest.NewRecorder()
		covAuHandler(env, provider()).RelinkCandidates(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.alice, "admin"))
		wantFixed(t, rec, http.StatusBadGateway, msgUpstream)
	})
}

// The add-book and adoption writers share addBookLookupError; both must
// answer with the generic provider message.
func TestErrorLeak_AddBookAndAdoptionLookupErrors(t *testing.T) {
	lookup := &addBookLookupError{Err: transportLeak()}
	t.Run("add book", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&AuthorHandler{}).writeAddBookError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author/1/book", nil), lookup)
		wantFixed(t, rec, http.StatusBadGateway, msgUpstream)
	})
	t.Run("adoption", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&AdoptionHandler{}).writeAdoptionError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/adopt", nil), lookup)
		wantFixed(t, rec, http.StatusBadGateway, msgUpstream)
	})
}

// ---- E3: filesystem errors ----

func TestErrorLeak_RootFolderCreateMissingPath(t *testing.T) {
	h := rootFolderFixture(t)
	missing := filepath.Join(t.TempDir(), "etc-shadow-x", "inner")
	body, _ := json.Marshal(map[string]string{"path": missing})
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/rootfolder", bytes.NewReader(body)))
	wantFixed(t, rec, http.StatusBadRequest, rootFolderStatMessage(fs.ErrNotExist))
	if strings.Contains(rec.Body.String(), "etc-shadow-x") {
		t.Errorf("body echoes the submitted path: %s", rec.Body.String())
	}
}

// The manual-import handlers stat a path they were given and used to answer
// with the os error, which names the full path and the errno. With no library
// roots configured, containment allows any path, so a missing one reaches the
// stat.
func TestErrorLeak_ManualImportPathNotAccessible(t *testing.T) {
	missing := "/does/not/exist/leaky-dir/book.epub"
	h := NewManualImportHandler(&stubManualImportScanner{}, nil, nil)
	const msg = "path not accessible"

	t.Run("lookup", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Lookup(rec, lookupRequest(missing))
		wantFixed(t, rec, http.StatusBadRequest, msg)
	})
	t.Run("scan", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Scan(rec, httptest.NewRequest(http.MethodGet, "/scan?path="+url.QueryEscape(missing), nil))
		wantFixed(t, rec, http.StatusBadRequest, msg)
	})
	t.Run("reassign preview", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ReassignPreview(rec, previewRequest(missing, 1, ""))
		wantFixed(t, rec, http.StatusBadRequest, msg)
	})
	t.Run("import", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Import(rec, httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(`{"path":"`+missing+`","bookId":1}`)))
		wantFixed(t, rec, http.StatusBadRequest, msg)
	})
}

// ---- E5: storage errors the preview and role handlers used to echo ----

func TestErrorLeak_ReassignPreviewErrors(t *testing.T) {
	root := t.TempDir()
	src := makeBookPath(t, root, "book.epub", false)
	cases := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"unknown book", fmt.Errorf("%w: 99", importer.ErrPreviewBookNotFound), http.StatusBadRequest, "book not found"},
		{"storage fault", sqlLeak(), http.StatusInternalServerError, msgServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := NewManualImportHandler(&stubManualImportScanner{previewErr: c.err}, nil, nil).WithRoots(NewLibraryRoots(nil, root))
			rec := httptest.NewRecorder()
			h.ReassignPreview(rec, previewRequest(src, 99, ""))
			wantFixed(t, rec, c.status, c.msg)
		})
	}
}

func TestErrorLeak_SetRole(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	u, err := users.Create(ctx, "someone", "h")
	if err != nil {
		t.Fatal(err)
	}
	put := func(id int64, role string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"`+role+`"}`, id, ctx))
		return rec
	}
	if rec := put(u.ID, "superadmin"); rec.Code != http.StatusBadRequest || !strings.Contains(errorField(t, rec), "invalid role") {
		t.Errorf("invalid role: %d %s", rec.Code, rec.Body.String())
	}
	// An id that exists nowhere used to surface "get user role: sql: no rows
	// in result set" as a 400.
	rec := put(424242, "user")
	wantFixed(t, rec, http.StatusNotFound, "No user with that id.")
}

func TestErrorLeak_SetRoleDatabaseError(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	users := db.NewUserRepo(database)
	h := NewUserManagementHandler(users)
	u, err := users.Create(context.Background(), "someone", "h")
	if err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"user"}`, u.ID, context.Background()))
	wantFixed(t, rec, http.StatusInternalServerError, msgServerError)
}

func TestErrorLeak_MigrateErrors(t *testing.T) {
	t.Run("provider hold keeps its actionable text", func(t *testing.T) {
		rec := httptest.NewRecorder()
		hold := &metadata.DailyQuotaError{}
		writeMigrateError(rec, httptest.NewRequest(http.MethodPost, "/migrate/csv", nil), hold)
		if rec.Code != http.StatusBadRequest || !strings.Contains(errorField(t, rec), "daily quota") {
			t.Errorf("hold: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("csv syntax error keeps its line and column", func(t *testing.T) {
		rec := httptest.NewRecorder()
		_, perr := csv.NewReader(strings.NewReader("a,\"b\nc,d")).ReadAll()
		writeMigrateError(rec, httptest.NewRequest(http.MethodPost, "/migrate/csv", nil), fmt.Errorf("wrapped: %w", perr))
		if rec.Code != http.StatusBadRequest || !strings.Contains(errorField(t, rec), "line") {
			t.Errorf("csv: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("storage fault", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeMigrateError(rec, httptest.NewRequest(http.MethodPost, "/migrate/csv", nil), sqlLeak())
		wantFixed(t, rec, http.StatusInternalServerError, msgServerError)
	})
}

func uploadRequest(t *testing.T, field, contentType, content string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="`+field+`"; filename="a.csv"`)
	hdr.Set("Content-Type", contentType)
	part, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, content)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/migrate/csv", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestErrorLeak_MigrateUploadErrors(t *testing.T) {
	h := &MigrateHandler{}
	t.Run("not multipart", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ImportCSV(rec, httptest.NewRequest(http.MethodPost, "/api/v1/migrate/csv", strings.NewReader("a,b")))
		wantFixed(t, rec, http.StatusBadRequest, "invalid upload: send a multipart form with a file field")
	})
	t.Run("missing file field", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ImportCSV(rec, uploadRequest(t, "other", "text/csv", "a"))
		wantFixed(t, rec, http.StatusBadRequest, "invalid upload: send a multipart form with a file field")
	})
	t.Run("too large", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ImportCSV(rec, uploadRequest(t, "file", "text/csv", strings.Repeat("x", 6<<20)))
		wantFixed(t, rec, http.StatusRequestEntityTooLarge, "upload too large")
	})
}

func TestErrorLeak_ABSImportErrors(t *testing.T) {
	cfg := func(context.Context) ABSStoredConfig {
		return ABSStoredConfig{BaseURL: "https://abs.example.com", APIKey: "k", LibraryID: "lib", Enabled: true}
	}
	t.Run("start storage fault", func(t *testing.T) {
		h := NewABSImportHandler(&stubABSImporter{startErr: sqlLeak()}, cfg)
		rec := httptest.NewRecorder()
		h.Start(rec, httptest.NewRequest(http.MethodPost, "/api/v1/abs/import", strings.NewReader(`{"dryRun":true}`)))
		wantFixed(t, rec, http.StatusInternalServerError, msgServerError)
	})
	rollbackCases := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"run not found", fmt.Errorf("%w: 9", abs.ErrRunNotFound), http.StatusNotFound, "import run not found"},
		{"unavailable", abs.ErrRollbackUnavailable, http.StatusServiceUnavailable, "rollback is unavailable"},
		{"storage fault", sqlLeak(), http.StatusInternalServerError, msgServerError},
	}
	for _, c := range rollbackCases {
		t.Run("rollback "+c.name, func(t *testing.T) {
			h := NewABSImportHandler(&stubABSImporter{rollbackErr: c.err}, cfg)
			for name, call := range map[string]func(http.ResponseWriter, *http.Request){"preview": h.RollbackPreview, "rollback": h.Rollback} {
				rec := httptest.NewRecorder()
				call(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/abs/import/runs/9/rollback", nil), "runID", "9"))
				t.Run(name, func(t *testing.T) { wantFixed(t, rec, c.status, c.msg) })
			}
		})
	}
}

// ---- E4: upstream text from connection tests ----

// secretBody is what a misbehaving service might return: the first line is
// the useful part, the rest is a page or trace that must never be relayed.
const secretBody = "request /api?mode=version&apikey=" + leakSecret + " was unauthorised\n<html><pre>stack trace /srv/app/internal/secret.go:42</pre>"

func TestErrorLeak_DownloadClientTestConfigUpstreamBody(t *testing.T) {
	t.Cleanup(httpsec.AllowLoopbackForTests())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, secretBody)
	}))
	t.Cleanup(srv.Close)
	host, port := testServerHostPort(t, srv.URL)

	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test",
		strings.NewReader(`{"type":"sabnzbd","host":"`+host+`","port":`+strconv.Itoa(port)+`}`)))
	assertUpstreamDetail(t, rec)
}

func TestErrorLeak_DownloadClientTestSavedUpstreamBody(t *testing.T) {
	t.Cleanup(httpsec.AllowLoopbackForTests())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, secretBody)
	}))
	t.Cleanup(srv.Close)
	host, port := testServerHostPort(t, srv.URL)

	h, clients := downloadClientFixture(t)
	c := &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true}
	if err := clients.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(c.ID, 10)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/"+id+"/test", nil), "id", id))
	assertUpstreamDetail(t, rec)
}

// A refused connection is the upstream being down, not a bad request.
func TestDownloadClientTestConfig_ConnectionFailureIs502(t *testing.T) {
	t.Cleanup(httpsec.AllowLoopbackForTests())
	srv := httptest.NewServer(http.NotFoundHandler())
	host, port := testServerHostPort(t, srv.URL)
	srv.Close() // nothing listens on the port any more

	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test",
		strings.NewReader(`{"type":"sabnzbd","host":"`+host+`","port":`+strconv.Itoa(port)+`}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	// The operator still gets Bindery's own hint for the failure, but the
	// error text, which embeds the request URL, stays in the log.
	msg := errorField(t, rec)
	want := "could not reach SABnzbd at " + net.JoinHostPort(host, strconv.Itoa(port)) + " (connection refused"
	if !strings.HasPrefix(msg, want) {
		t.Errorf("error = %q, want it to start with %q (client, host:port, refused hint)", msg, want)
	}
	if strings.Contains(msg, "http://") || strings.Contains(msg, "apikey") {
		t.Errorf("error = %q echoes the request URL", msg)
	}
}

// assertUpstreamDetail: 502, the credential redacted, one line, capped.
func assertUpstreamDetail(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	msg := errorField(t, rec)
	if strings.Contains(rec.Body.String(), leakSecret) {
		t.Errorf("credential in body: %s", rec.Body.String())
	}
	for _, frag := range []string{"stack trace", "secret.go", "<html>", "\\n"} {
		if strings.Contains(rec.Body.String(), frag) {
			t.Errorf("body relays more than the first line (%q): %s", frag, rec.Body.String())
		}
	}
	if len(msg) == 0 || len(msg) > upstreamDetailMax {
		t.Errorf("error length = %d, want 1..%d: %q", len(msg), upstreamDetailMax, msg)
	}
}

func TestErrorLeak_ProwlarrTestAndSyncUpstreamBody(t *testing.T) {
	t.Cleanup(httpsec.AllowLoopbackForTests())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, secretBody)
	}))
	t.Cleanup(srv.Close)
	h, instances, _, _ := prowlarrFixture(t)
	inst := &models.ProwlarrInstance{Name: "P", URL: srv.URL, APIKey: "k", Enabled: true}
	if err := instances.Create(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(inst.ID, 10)

	// Test keeps its documented contract: 200 with ok false (a boolean) and the reason.
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/"+id+"/test", nil), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("test status = %d, want the documented 200 envelope: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	detail, _ := out["error"].(string)
	if out["ok"] != false || detail == "" || len(detail) > upstreamDetailMax {
		t.Errorf("test body = %v", out)
	}
	for _, frag := range []string{leakSecret, "stack trace", "secret.go", "<html>"} {
		if strings.Contains(rec.Body.String(), frag) {
			t.Errorf("test body leaks %q: %s", frag, rec.Body.String())
		}
	}

	rec = httptest.NewRecorder()
	h.Sync(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/"+id+"/sync", nil), "id", id))
	assertUpstreamDetail(t, rec)
}

func TestProwlarrTestAndSync_StorageErrorIs500NotNotFound(t *testing.T) {
	// A closed store used to read as "not found".
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	h2 := NewProwlarrHandler(db.NewProwlarrRepo(database), db.NewIndexerRepo(database))
	_ = database.Close()
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){"test": h2.Test, "sync": h2.Sync} {
		rec := httptest.NewRecorder()
		call(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/1/"+name, nil), "id", "1"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500: %s", name, rec.Code, rec.Body.String())
		}
	}
}

// ---- helpers ----

func TestUpstreamDetail(t *testing.T) {
	long := strings.Repeat("e", 500)
	multibyte := strings.Repeat("é", 300) // 2 bytes per rune
	cases := []struct {
		name, in string
		check    func(string) bool
	}{
		{"first line only", "first\nsecond", func(s string) bool { return s == "first" }},
		{"crlf", "first\r\nsecond", func(s string) bool { return s == "first" }},
		{"redacted", "GET https://h/x?apikey=" + leakSecret + " failed", func(s string) bool { return !strings.Contains(s, leakSecret) }},
		{"capped", long, func(s string) bool { return len(s) == upstreamDetailMax }},
		{"rune boundary", multibyte, func(s string) bool { return len(s) <= upstreamDetailMax && strings.ToValidUTF8(s, "") == s }},
		{"empty", "", func(s string) bool { return s == upstreamDetailEmpty }},
		{"leading blank line", "\r\n\r\nreal reason\nmore", func(s string) bool { return s == "real reason" }},
	}
	for _, c := range cases {
		if got := upstreamDetail(c.in); !c.check(got) {
			t.Errorf("%s: upstreamDetail = %q", c.name, got)
		}
	}
}

func TestDecodeJSON(t *testing.T) {
	var v struct{ A int }
	t.Run("ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if !decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"A":1}`)), &v) || v.A != 1 {
			t.Fatalf("decode failed: %+v", v)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"A":`)), &v) {
			t.Fatal("decode should have failed")
		}
		wantFixed(t, rec, http.StatusBadRequest, "invalid request body")
	})
	t.Run("too large", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"A":1,"pad":"`+strings.Repeat("x", 4096)+`"}`))
		req.Body = http.MaxBytesReader(rec, req.Body, 64)
		if decodeJSON(rec, req, &v) {
			t.Fatal("decode should have failed")
		}
		wantFixed(t, rec, http.StatusRequestEntityTooLarge, "request body too large")
	})
}

func TestWriteNotFoundOrServerError(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	writeNotFoundOrServerError(rec, r, sqlLeak(), false)
	wantFixed(t, rec, http.StatusInternalServerError, msgServerError)

	rec = httptest.NewRecorder()
	writeNotFoundOrServerError(rec, r, nil, false)
	wantFixed(t, rec, http.StatusNotFound, "not found")
}
