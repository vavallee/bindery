package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

const testBridgeKey = "plugin-key-0123456789abcdef"

// testGlobalAPIKey matches the .gitleaks.toml fixture allowlist.
const testGlobalAPIKey = "SCREAMING_TEST_KEY_GLOBAL_API"

type bridgeFixture struct {
	t        *testing.T
	ctx      context.Context
	settings *db.SettingsRepo
	repo     *db.CalibreDeliveryRepo
	books    *db.BookRepo
	d        *calibre.Deliverer
	h        *CalibreBridgeHandler
	limiter  *auth.LoginLimiter
	router   chi.Router
	root     string
	outside  string
	coverDir string
	store    *covers.Store
	author   *models.Author
}

// mountBridge is the same shape registerCalibreBridgeRoutes mounts in
// cmd/bindery; cmd/bindery's own test checks the real registration.
func mountBridge(r chi.Router, h *CalibreBridgeHandler) {
	r.Route("/bridge/v1", func(r chi.Router) {
		r.Use(h.Authenticate)
		r.Get("/hello", h.Hello)
		r.Group(func(r chi.Router) {
			r.Use(h.RequirePull)
			r.Get("/deliveries", h.List)
			r.Get("/deliveries/{id}/file", h.File)
			r.Get("/deliveries/{id}/cover", h.Cover)
			r.Post("/deliveries/{id}/ack", h.Ack)
			r.Post("/deliveries/{id}/nack", h.Nack)
		})
	})
}

func newBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	f := &bridgeFixture{
		t:        t,
		ctx:      context.Background(),
		settings: db.NewSettingsRepo(database),
		repo:     db.NewCalibreDeliveryRepo(database),
		books:    db.NewBookRepo(database),
		root:     t.TempDir(),
		outside:  t.TempDir(),
		coverDir: t.TempDir(),
	}
	f.store = covers.NewStore(f.coverDir)
	f.set(SettingCalibreMode, "plugin")
	f.set(SettingCalibrePluginTransport, "pull")
	f.set(SettingCalibrePluginAPIKey, testBridgeKey)
	f.set(SettingAuthAPIKey, testGlobalAPIKey)

	authors := db.NewAuthorRepo(database)
	f.author = &models.Author{ForeignID: "OLA1", Name: "Jane Austen", SortName: "Austen, Jane", Monitored: true}
	if err := authors.Create(f.ctx, f.author); err != nil {
		t.Fatal(err)
	}
	f.d = calibre.NewDeliverer(f.repo, f.books,
		func() calibre.Mode { return LoadCalibreMode(f.ctx, f.settings) },
		func() calibre.Config { return LoadCalibreConfig(f.ctx, f.settings) },
		func(calibre.Mode) calibre.Adder { return nil }).
		WithMetadata(authors, db.NewEditionRepo(database), db.NewSeriesRepo(database)).
		WithCovers(calibre.CoverSource{Store: f.store}).
		WithTransport(func() calibre.Transport { return LoadCalibreTransport(f.ctx, f.settings) })
	f.limiter = auth.NewLoginLimiter(3, time.Minute)
	f.h = NewCalibreBridgeHandler(f.d, NewFileHandler(f.books, f.root), f.settings, f.limiter, 15*time.Minute, "v9.9.9")
	f.router = chi.NewRouter()
	mountBridge(f.router, f.h)
	return f
}

func (f *bridgeFixture) set(key, value string) {
	f.t.Helper()
	if err := f.settings.Set(f.ctx, key, value); err != nil {
		f.t.Fatal(err)
	}
}

// book makes a book and queues one delivery per path. A path that does not
// exist yet is written with its own name as the content.
func (f *bridgeFixture) book(title string, paths ...string) (*models.Book, []int64) {
	f.t.Helper()
	b := &models.Book{ForeignID: "OL" + title, AuthorID: f.author.ID, Title: title, SortTitle: title,
		Status: models.BookStatusImported, Monitored: true, AnyEditionOK: true, MetadataProvider: "openlibrary"}
	if err := f.books.Create(f.ctx, b); err != nil {
		f.t.Fatal(err)
	}
	var ids []int64
	for _, p := range paths {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, []byte(filepath.Base(p)), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}
		if err := f.books.AddBookFile(f.ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			f.t.Fatal(err)
		}
		files, err := f.books.ListFiles(f.ctx, b.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		for _, bf := range files {
			if bf.Path == p {
				if _, err := f.d.Enqueue(f.ctx, b.ID, bf.ID, nil, p); err != nil {
					f.t.Fatal(err)
				}
				row, err := f.repo.GetByBookFile(f.ctx, bf.ID)
				if err != nil || row == nil {
					f.t.Fatalf("row: %v %v", row, err)
				}
				ids = append(ids, row.ID)
			}
		}
	}
	return b, ids
}

func (f *bridgeFixture) row(id int64) models.CalibreDelivery {
	f.t.Helper()
	r, err := f.repo.Get(f.ctx, id)
	if err != nil || r == nil {
		f.t.Fatalf("row %d: %v %v", id, r, err)
	}
	return *r
}

type bridgeReq struct {
	method  string
	path    string
	body    string
	auth    string // full Authorization header; "" sends none
	headers map[string]string
	remote  string
}

func (f *bridgeFixture) do(q bridgeReq) *httptest.ResponseRecorder {
	f.t.Helper()
	if q.method == "" {
		q.method = http.MethodGet
	}
	var body *bytes.Reader
	if q.body != "" {
		body = bytes.NewReader([]byte(q.body))
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(q.method, q.path, body)
	if q.remote != "" {
		req.RemoteAddr = q.remote
	}
	if q.auth != "" {
		req.Header.Set("Authorization", q.auth)
	}
	for k, v := range q.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// get is an authenticated GET.
func (f *bridgeFixture) get(path string, headers map[string]string) *httptest.ResponseRecorder {
	return f.do(bridgeReq{path: path, auth: "Bearer " + testBridgeKey, headers: headers})
}

func (f *bridgeFixture) post(path, body string) *httptest.ResponseRecorder {
	return f.do(bridgeReq{method: http.MethodPost, path: path, body: body, auth: "Bearer " + testBridgeKey})
}

func bridgeCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body.Error == "" {
		t.Fatalf("error body %q has no error text", rec.Body.String())
	}
	return body.Code
}

func TestBridgeAuth(t *testing.T) {
	f := newBridgeFixture(t)
	session, err := auth.SignSession([]byte("0123456789abcdef0123456789abcdef"), 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		req  bridgeReq
	}{
		{"missing header", bridgeReq{}},
		{"wrong key", bridgeReq{auth: "Bearer not-the-plugin-key-at-all"}},
		{"key as Basic", bridgeReq{auth: "Basic " + testBridgeKey}},
		{"empty bearer", bridgeReq{auth: "Bearer "}},
		{"global API key as bearer", bridgeReq{auth: "Bearer " + testGlobalAPIKey}},
		{"global API key header", bridgeReq{headers: map[string]string{"X-Api-Key": testGlobalAPIKey}}},
		{"global API key query", bridgeReq{path: "/bridge/v1/hello?apikey=" + testGlobalAPIKey}},
		{"session cookie", bridgeReq{headers: map[string]string{"Cookie": auth.SessionCookieName + "=" + session}}},
		{"plugin key as X-Api-Key", bridgeReq{headers: map[string]string{"X-Api-Key": testBridgeKey}}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.req
			if q.path == "" {
				q.path = "/bridge/v1/hello"
			}
			// A fresh address per case keeps the limiter out of this test.
			q.remote = "10.0.0." + strconv.Itoa(i+1) + ":1234"
			rec := f.do(q)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body)
			}
			if code := bridgeCode(t, rec); code != "unauthorized" {
				t.Fatalf("code %q, want unauthorized", code)
			}
			if !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
				t.Fatalf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}

	t.Run("correct key", func(t *testing.T) {
		rec := f.get("/bridge/v1/hello", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	})

	for _, stored := range []string{"", "fifteen-chars!!"} {
		t.Run("stored key "+strconv.Quote(stored)+" refuses even when matched", func(t *testing.T) {
			f.set(SettingCalibrePluginAPIKey, stored)
			defer f.set(SettingCalibrePluginAPIKey, testBridgeKey)
			for _, presented := range []string{"Bearer " + stored, "Bearer x"} {
				rec := f.do(bridgeReq{path: "/bridge/v1/hello", auth: presented, remote: "10.1.0.1:1"})
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("stored %q presented %q: status %d, want 401", stored, presented, rec.Code)
				}
			}
		})
	}

	t.Run("sixteen characters is enough", func(t *testing.T) {
		f.set(SettingCalibrePluginAPIKey, "sixteen-chars!!!")
		defer f.set(SettingCalibrePluginAPIKey, testBridgeKey)
		rec := f.do(bridgeReq{path: "/bridge/v1/hello", auth: "Bearer sixteen-chars!!!"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200", rec.Code)
		}
	})
}

func TestBridgeKeyMatches(t *testing.T) {
	cases := []struct {
		presented, stored string
		want              bool
	}{
		{testBridgeKey, testBridgeKey, true},
		{"", testBridgeKey, false},
		{"", "", true}, // equal; the length floor is what refuses an empty stored key
		{testBridgeKey[:len(testBridgeKey)-1], testBridgeKey, false},
		{testBridgeKey + "x", testBridgeKey, false},
		{strings.ToUpper(testBridgeKey), testBridgeKey, false},
	}
	for _, tc := range cases {
		if got := bridgeKeyMatches(tc.presented, tc.stored); got != tc.want {
			t.Errorf("bridgeKeyMatches(%q, %q) = %v, want %v", tc.presented, tc.stored, got, tc.want)
		}
	}
}

// Failures trip the bridge's own limiter; the login limiter is a separate
// instance and never sees them.
func TestBridgeAuth_SeparateLimiter(t *testing.T) {
	f := newBridgeFixture(t)
	login := auth.NewLoginLimiter(3, time.Minute)
	const remote = "192.0.2.7:5555"
	for i := 0; i < 3; i++ {
		if rec := f.do(bridgeReq{path: "/bridge/v1/hello", auth: "Bearer wrong-key-wrong-key", remote: remote}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i, rec.Code)
		}
	}
	rec := f.do(bridgeReq{path: "/bridge/v1/hello", auth: "Bearer " + testBridgeKey, remote: remote})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after 3 failures: status %d, want 429", rec.Code)
	}
	if code := bridgeCode(t, rec); code != "rate_limited" {
		t.Fatalf("code %q", code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "900" {
		t.Fatalf("Retry-After = %q, want 900", ra)
	}
	if !login.Allow("192.0.2.7") {
		t.Fatal("the login limiter must not be tripped by bridge failures")
	}
	// Another address is unaffected.
	if rec := f.do(bridgeReq{path: "/bridge/v1/hello", auth: "Bearer " + testBridgeKey, remote: "192.0.2.8:1"}); rec.Code != http.StatusOK {
		t.Fatalf("other address: status %d", rec.Code)
	}
}

func TestBridgeHelloAndTransportGate(t *testing.T) {
	f := newBridgeFixture(t)
	var hello struct {
		BinderyVersion string `json:"binderyVersion"`
		Protocol       int    `json:"protocol"`
		MaxBatch       int    `json:"maxBatch"`
		Transport      string `json:"transport"`
	}
	rec := f.get("/bridge/v1/hello", map[string]string{"X-Bridge-Version": "0.8.0", "X-Bridge-Capabilities": "book_metadata,cover,add_format"})
	if err := json.Unmarshal(rec.Body.Bytes(), &hello); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("hello: %d %s", rec.Code, rec.Body)
	}
	if hello.BinderyVersion != "v9.9.9" || hello.Protocol != 1 || hello.MaxBatch != 20 || hello.Transport != "pull" {
		t.Fatalf("hello = %+v", hello)
	}
	c := f.d.PullContact()
	if c.LastSeen == nil || c.PluginVersion != "0.8.0" || strings.Join(c.Capabilities, ",") != "book_metadata,cover,add_format" || c.RemoteAddr == "" {
		t.Fatalf("contact = %+v", c)
	}

	_, ids := f.book("Emma", filepath.Join(f.root, "emma.epub"))
	id := strconv.FormatInt(ids[0], 10)
	gated := []bridgeReq{
		{path: "/bridge/v1/deliveries"},
		{path: "/bridge/v1/deliveries/" + id + "/file"},
		{path: "/bridge/v1/deliveries/" + id + "/cover"},
		{method: http.MethodPost, path: "/bridge/v1/deliveries/" + id + "/ack", body: `{"calibreId":1,"outcome":"added"}`},
		{method: http.MethodPost, path: "/bridge/v1/deliveries/" + id + "/nack", body: `{"code":"x","retryable":true}`},
	}
	for _, setting := range []struct{ mode, transport string }{{"plugin", "push"}, {"calibredb", "pull"}, {"off", "pull"}} {
		f.set(SettingCalibreMode, setting.mode)
		f.set(SettingCalibrePluginTransport, setting.transport)
		for _, q := range gated {
			q.auth = "Bearer " + testBridgeKey
			rec := f.do(q)
			if rec.Code != http.StatusConflict {
				t.Fatalf("%s/%s %s %s: status %d, want 409", setting.mode, setting.transport, q.method, q.path, rec.Code)
			}
			if code := bridgeCode(t, rec); code != "not_in_pull_mode" {
				t.Fatalf("code %q", code)
			}
		}
		rec := f.get("/bridge/v1/hello", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"transport":"push"`) {
			t.Fatalf("%s/%s hello: %d %s, want 200 reporting push", setting.mode, setting.transport, rec.Code, rec.Body)
		}
	}
	if got := f.row(ids[0]); got.State != models.CalibreDeliveryPending {
		t.Fatalf("a gated request changed the row: %+v", got)
	}
}

func TestBridgeList(t *testing.T) {
	f := newBridgeFixture(t)
	emma, ids := f.book("Emma", filepath.Join(f.root, "emma.mobi"), filepath.Join(f.root, "emma.epub"))
	_, persuasion := f.book("Persuasion", filepath.Join(f.root, "persuasion.epub"))

	rec := f.get("/bridge/v1/deliveries?limit=1", map[string]string{"X-Bridge-Capabilities": "book_metadata,cover,add_format"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var page struct {
		Deliveries []map[string]any `json:"deliveries"`
		NextCursor *string          `json:"nextCursor"`
		Pending    *int             `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == nil || *page.NextCursor == "" || page.Pending == nil || *page.Pending != 3 {
		t.Fatalf("page = %s", rec.Body)
	}
	if len(page.Deliveries) != 1 {
		t.Fatalf("deliveries = %v, want the epub alone", page.Deliveries)
	}
	item := page.Deliveries[0]
	for _, k := range []string{"id", "bookId", "format", "sizeBytes", "action", "metadata", "hasCover"} {
		if _, ok := item[k]; !ok {
			t.Fatalf("item has no %q: %v", k, item)
		}
	}
	if int64(item["id"].(float64)) != ids[1] || item["action"] != "add" || item["format"] != "epub" ||
		int64(item["bookId"].(float64)) != emma.ID || item["hasCover"] != false ||
		int64(item["sizeBytes"].(float64)) != int64(len("emma.epub")) {
		t.Fatalf("item = %v", item)
	}
	meta := item["metadata"].(map[string]any)
	if meta["title"] != "Emma" || meta["identifiers"].(map[string]any)["bindery"] != strconv.FormatInt(emma.ID, 10) {
		t.Fatalf("metadata = %v", meta)
	}
	if _, ok := meta["coverPath"]; ok {
		t.Fatal("metadata carries a server cover path")
	}

	rec = f.get("/bridge/v1/deliveries?limit=1&cursor="+*page.NextCursor, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":`+strconv.FormatInt(persuasion[0], 10)+`,`) ||
		!strings.Contains(rec.Body.String(), `"nextCursor":""`) {
		t.Fatalf("second page: %d %s", rec.Code, rec.Body)
	}

	for _, bad := range []string{"limit=0", "limit=x", "cursor=abc"} {
		rec := f.get("/bridge/v1/deliveries?"+bad, nil)
		if rec.Code != http.StatusBadRequest || bridgeCode(t, rec) != "invalid_request" {
			t.Fatalf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	// limit is capped rather than refused.
	if rec := f.get("/bridge/v1/deliveries?limit=500", nil); rec.Code != http.StatusOK {
		t.Fatalf("limit=500: %d", rec.Code)
	}
}

// The action comes from the capability header: a second format is listed as
// add_format to a plugin that advertised it, and held back otherwise.
func TestBridgeList_ActionFollowsCapabilities(t *testing.T) {
	f := newBridgeFixture(t)
	_, ids := f.book("Emma", filepath.Join(f.root, "emma.epub"), filepath.Join(f.root, "emma.mobi"))
	if rec := f.post("/bridge/v1/deliveries/"+strconv.FormatInt(ids[0], 10)+"/ack", `{"calibreId":5,"outcome":"added","library":"L"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body)
	}
	rec := f.get("/bridge/v1/deliveries", map[string]string{"X-Bridge-Capabilities": "book_metadata"})
	if strings.Contains(rec.Body.String(), `"action"`) {
		t.Fatalf("without add_format the mobi was listed: %s", rec.Body)
	}
	rec = f.get("/bridge/v1/deliveries", map[string]string{"X-Bridge-Capabilities": "book_metadata,add_format"})
	if !strings.Contains(rec.Body.String(), `"action":"add_format"`) {
		t.Fatalf("with add_format: %s", rec.Body)
	}
}

func TestBridgeFile(t *testing.T) {
	f := newBridgeFixture(t)
	content := []byte("PK\x03\x04 an epub, honest")
	good := filepath.Join(f.root, "Jane Austen", "Emma (1815).epub")
	if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, content, 0o600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(f.outside, "secret.epub")
	dirInRoot := filepath.Join(f.root, "a-directory.epub")
	if err := os.Mkdir(dirInRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(f.root, "gone.epub")
	_, ids := f.book("Emma", good)
	_, outsideIDs := f.book("Outside", outsideFile)
	_, dirIDs := f.book("Dir", dirInRoot)
	_, goneIDs := f.book("Gone", gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	url := func(id int64) string { return "/bridge/v1/deliveries/" + strconv.FormatInt(id, 10) + "/file" }

	rec := f.get(url(ids[0]), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body = %q", rec.Body.Bytes())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(content)) {
		t.Fatalf("Content-Length = %q, want %d", cl, len(content))
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="book.epub"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	rec = f.get(url(outsideIDs[0]), nil)
	if rec.Code != http.StatusForbidden || bridgeCode(t, rec) != "path_forbidden" || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("outside the library: %d %s", rec.Code, rec.Body)
	}
	rec = f.get(url(dirIDs[0]), nil)
	if rec.Code != http.StatusForbidden || bridgeCode(t, rec) != "path_forbidden" {
		t.Fatalf("directory: %d %s", rec.Code, rec.Body)
	}

	rec = f.get(url(goneIDs[0]), nil)
	if rec.Code != http.StatusNotFound || bridgeCode(t, rec) != "not_found" {
		t.Fatalf("missing file: %d %s", rec.Code, rec.Body)
	}
	if got := f.row(goneIDs[0]); got.State != models.CalibreDeliverySkipped || got.Outcome == "" {
		t.Fatalf("missing file row = %+v, want skipped with a reason", got)
	}

	// Not pending any more: 404.
	if rec := f.post("/bridge/v1/deliveries/"+strconv.FormatInt(ids[0], 10)+"/ack", `{"calibreId":3,"outcome":"added"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("ack: %d", rec.Code)
	}
	if rec := f.get(url(ids[0]), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delivered row file: %d, want 404", rec.Code)
	}
	for _, id := range []string{"999999", "abc", "-1"} {
		rec := f.get("/bridge/v1/deliveries/"+id+"/file", nil)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Fatalf("id %s: %d", id, rec.Code)
		}
	}
}

// TestBridgeFile_SymlinkRefused: a delivery whose recorded path is a link
// inside the library is refused, so the bridge never hands the plugin the
// bytes of whatever the link names.
func TestBridgeFile_SymlinkRefused(t *testing.T) {
	f := newBridgeFixture(t)
	secret := filepath.Join(f.outside, "bindery.db")
	if err := os.WriteFile(secret, []byte(symlinkSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.root, "Jane Austen", "Persuasion.epub")
	symlinkOrSkipAPI(t, secret, link)
	_, ids := f.book("Persuasion", link)

	rec := f.get("/bridge/v1/deliveries/"+strconv.FormatInt(ids[0], 10)+"/file", nil)
	if rec.Code != http.StatusForbidden || bridgeCode(t, rec) != "path_forbidden" {
		t.Fatalf("symlinked delivery: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), symlinkSecret) {
		t.Fatal("bridge served the symlink target")
	}
}

// TestBridgeFile_SymlinkedLibraryRoot: a library root reached through a link
// still delivers.
func TestBridgeFile_SymlinkedLibraryRoot(t *testing.T) {
	f := newBridgeFixture(t)
	linked := filepath.Join(t.TempDir(), "books")
	symlinkOrSkipAPI(t, f.root, linked)
	f.h = NewCalibreBridgeHandler(f.d, NewFileHandler(f.books, linked), f.settings, f.limiter, 15*time.Minute, "v9.9.9")
	f.router = chi.NewRouter()
	mountBridge(f.router, f.h)

	path := filepath.Join(linked, "Emma.epub")
	_, ids := f.book("Emma", path)
	rec := f.get("/bridge/v1/deliveries/"+strconv.FormatInt(ids[0], 10)+"/file", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "Emma.epub" {
		t.Fatalf("linked root delivery: %d %s", rec.Code, rec.Body)
	}
}

// A png that the covers store holds, 1x1.
var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

func TestBridgeCover(t *testing.T) {
	f := newBridgeFixture(t)
	src := filepath.Join(t.TempDir(), "cover.png")
	if err := os.WriteFile(src, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := f.store.Put(src)
	if err != nil {
		t.Fatal(err)
	}
	withCover, ids := f.book("Emma", filepath.Join(f.root, "emma.epub"))
	withCover.ImageURL = ref
	if err := f.books.Update(f.ctx, withCover); err != nil {
		t.Fatal(err)
	}
	_, bare := f.book("Bare", filepath.Join(f.root, "bare.epub"))

	rec := f.get("/bridge/v1/deliveries", nil)
	if !strings.Contains(rec.Body.String(), `"hasCover":true`) || !strings.Contains(rec.Body.String(), `"hasCover":false`) {
		t.Fatalf("listing hasCover: %s", rec.Body)
	}

	rec = f.get("/bridge/v1/deliveries/"+strconv.FormatInt(ids[0], 10)+"/cover", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), tinyPNG) {
		t.Fatalf("cover: %d %q %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(tinyPNG)) {
		t.Fatalf("Content-Length = %q", rec.Header().Get("Content-Length"))
	}
	rec = f.get("/bridge/v1/deliveries/"+strconv.FormatInt(bare[0], 10)+"/cover", nil)
	if rec.Code != http.StatusNotFound || bridgeCode(t, rec) != "not_found" {
		t.Fatalf("no cover: %d %s", rec.Code, rec.Body)
	}
}

func TestBridgeAckAndNack(t *testing.T) {
	f := newBridgeFixture(t)
	book, ids := f.book("Emma", filepath.Join(f.root, "emma.epub"))
	_, other := f.book("Persuasion", filepath.Join(f.root, "persuasion.epub"))
	ack := "/bridge/v1/deliveries/" + strconv.FormatInt(ids[0], 10) + "/ack"

	body := `{"calibreId":42,"outcome":"added","coverApplied":true,"library":"C:\\Users\\me\\Calibre Library"}`
	for i := 0; i < 2; i++ {
		if rec := f.post(ack, body); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("ack %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	got := f.row(ids[0])
	if got.State != models.CalibreDeliveryDelivered || *got.CalibreID != 42 || got.TargetLibrary != `C:\Users\me\Calibre Library` {
		t.Fatalf("row = %+v", got)
	}
	if b, _ := f.books.GetByID(f.ctx, book.ID); b.CalibreID == nil || *b.CalibreID != 42 {
		t.Fatalf("books.calibre_id = %v, want 42", b.CalibreID)
	}
	if rec := f.post(ack, `{"calibreId":43,"outcome":"added"}`); rec.Code != http.StatusConflict || bridgeCode(t, rec) != "not_pending" {
		t.Fatalf("ack with another id: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`not json`, `{"calibreId":1,"outcome":"moved"}`, `{"calibreId":0,"outcome":"added"}`} {
		if rec := f.post(ack, bad); rec.Code != http.StatusBadRequest || bridgeCode(t, rec) != "invalid_request" {
			t.Fatalf("ack %s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if rec := f.post("/bridge/v1/deliveries/999999/ack", `{"calibreId":1,"outcome":"added"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("ack of a missing row: %d", rec.Code)
	}

	nack := "/bridge/v1/deliveries/" + strconv.FormatInt(other[0], 10) + "/nack"
	if rec := f.post(nack, `{"code":"calibre_busy","error":"database is locked","retryable":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("nack: %d %s", rec.Code, rec.Body)
	}
	got = f.row(other[0])
	if got.State != models.CalibreDeliveryPending || got.Attempts != 1 || got.LastError != "database is locked" || !got.NextAttemptAt.After(time.Now()) {
		t.Fatalf("retryable nack row = %+v", got)
	}
	// Backing off, so no longer due, but still pending: a terminal nack
	// still applies to it.
	if rec := f.post(nack, `{"code":"bad_format","error":"not an ebook","retryable":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("terminal nack: %d %s", rec.Code, rec.Body)
	}
	if got := f.row(other[0]); got.State != models.CalibreDeliveryFailed || got.LastErrorCode != "bad_format" {
		t.Fatalf("bad_format row = %+v, want failed", got)
	}
	if rec := f.post(nack, `{"code":"x","retryable":false}`); rec.Code != http.StatusConflict {
		t.Fatalf("nack of a failed row: %d", rec.Code)
	}
	if rec := f.post(nack, `{"code":"","retryable":false}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("nack without a code: %d", rec.Code)
	}
}

// The settings summary reports the plugin's last contact in pull.
func TestCalibreDeliverySummary_ReportsPullContact(t *testing.T) {
	f := newBridgeFixture(t)
	if rec := f.get("/bridge/v1/hello", map[string]string{"X-Bridge-Version": "0.8.0"}); rec.Code != http.StatusOK {
		t.Fatalf("hello: %d", rec.Code)
	}
	h := NewCalibreDeliveryHandler(f.repo, f.d, f.books, func() calibre.Mode { return calibre.ModePlugin }).
		WithTransport(func() calibre.Transport { return LoadCalibreTransport(f.ctx, f.settings) })
	rec := httptest.NewRecorder()
	h.Summary(rec, httptest.NewRequest(http.MethodGet, "/api/v1/calibre/deliveries/summary", nil))
	var body struct {
		Transport string `json:"transport"`
		Pull      struct {
			LastSeen      *time.Time `json:"lastSeen"`
			PluginVersion string     `json:"pluginVersion"`
		} `json:"pull"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Transport != "pull" || body.Pull.LastSeen == nil || body.Pull.PluginVersion != "0.8.0" {
		t.Fatalf("summary = %s", rec.Body)
	}
}
