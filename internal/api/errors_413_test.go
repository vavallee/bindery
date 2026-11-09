package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Body size cap (audit E9). MaxRequestBody caps JSON bodies at 1 MiB with
// http.MaxBytesReader; the handlers below used to read that as a malformed
// payload and answer 400 "invalid request body". Each case sends a
// syntactically valid 2 MiB body through the production middleware and
// expects 413 with the fixed message.

type bodyCase struct {
	name   string
	build  func(d *sql.DB) http.HandlerFunc
	method string
	// prepare is set for handlers that look a row up first: it creates the
	// row and returns its id.
	prepare func(t *testing.T, d *sql.DB) string
}

func oversizeJSON() string {
	return `{"name":"` + strings.Repeat("a", 2<<20) + `"}`
}

func newOversizeIndexerHandler(d *sql.DB) *IndexerHandler {
	return NewIndexerHandler(db.NewIndexerRepo(d), db.NewBookRepo(d), db.NewAuthorRepo(d),
		db.NewMetadataProfileRepo(d), nil, db.NewSettingsRepo(d), db.NewBlocklistRepo(d))
}

func TestOversizeBody_Is413(t *testing.T) {
	cases := []bodyCase{
		{name: "delay profile create", method: http.MethodPost, build: func(d *sql.DB) http.HandlerFunc {
			return NewDelayProfileHandler(db.NewDelayProfileRepo(d)).Create
		}},
		{name: "metadata profile create", method: http.MethodPost, build: func(d *sql.DB) http.HandlerFunc {
			return NewMetadataProfileHandler(db.NewMetadataProfileRepo(d)).Create
		}},
		{name: "notification create", method: http.MethodPost, build: func(d *sql.DB) http.HandlerFunc {
			return NewNotificationHandler(db.NewNotificationRepo(d), nil).Create
		}},
		{name: "import list create", method: http.MethodPost, build: func(d *sql.DB) http.HandlerFunc {
			return NewImportListHandler(db.NewImportListRepo(d), db.NewSettingsRepo(d), nil, nil).Create
		}},
		{name: "download client create", method: http.MethodPost, build: func(d *sql.DB) http.HandlerFunc {
			return NewDownloadClientHandler(db.NewDownloadClientRepo(d)).Create
		}},
		{name: "download client test config", method: http.MethodPost, build: func(d *sql.DB) http.HandlerFunc {
			return NewDownloadClientHandler(db.NewDownloadClientRepo(d)).TestConfig
		}},
		{name: "download client update", method: http.MethodPut,
			prepare: func(t *testing.T, d *sql.DB) string {
				c := &models.DownloadClient{Name: "c", Type: "sabnzbd", Host: "h", Port: 1, Enabled: true}
				if err := db.NewDownloadClientRepo(d).Create(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				return strconv.FormatInt(c.ID, 10)
			},
			build: func(d *sql.DB) http.HandlerFunc {
				return NewDownloadClientHandler(db.NewDownloadClientRepo(d)).Update
			}},
		{name: "indexer create", method: http.MethodPost, build: func(d *sql.DB) http.HandlerFunc {
			return newOversizeIndexerHandler(d).Create
		}},
		{name: "indexer test config", method: http.MethodPost, build: func(d *sql.DB) http.HandlerFunc {
			return newOversizeIndexerHandler(d).TestConfig
		}},
		{name: "indexer update", method: http.MethodPut,
			prepare: func(t *testing.T, d *sql.DB) string {
				idx := &models.Indexer{Name: "i", URL: "http://x.test", APIKey: "k", Type: "newznab", Enabled: true}
				if err := db.NewIndexerRepo(d).Create(context.Background(), idx); err != nil {
					t.Fatal(err)
				}
				return strconv.FormatInt(idx.ID, 10)
			},
			build: func(d *sql.DB) http.HandlerFunc { return newOversizeIndexerHandler(d).Update }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			id := "1"
			if c.prepare != nil {
				id = c.prepare(t, d)
			}
			req := httptest.NewRequest(c.method, "/x/"+id, strings.NewReader(oversizeJSON()))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			MaxRequestBody(c.build(d)).ServeHTTP(rec, withURLParam(req, "id", id))
			wantFixed(t, rec, http.StatusRequestEntityTooLarge, "request body too large")
		})
	}
}
