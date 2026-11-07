package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// categoryRecorder is a fake indexer plus SABnzbd in one server: it serves a
// real NZB for any *.nzb path and records the category every addfile call
// carried, which is what lands the download in the client's ebook or
// audiobook category.
type categoryRecorder struct {
	mu   sync.Mutex
	cats []string
}

func (c *categoryRecorder) last(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cats) != 1 {
		t.Fatalf("expected exactly one dispatch to the download client, got %d (%v)", len(c.cats), c.cats)
	}
	return c.cats[0]
}

func newCategoryRecorder(t *testing.T) (*categoryRecorder, *httptest.Server) {
	t.Helper()
	rec := &categoryRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".nzb") {
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`))
			return
		}
		if r.URL.Query().Get("mode") == "addfile" {
			rec.mu.Lock()
			rec.cats = append(rec.cats, r.URL.Query().Get("cat"))
			rec.mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-1"}})
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

const (
	ebookCategory     = "readarr-ebook"
	audiobookCategory = "readarr-audio"
)

// categoryFixture builds a queue handler with one SABnzbd client split into
// an ebook and an audiobook category, and a dual-format (media_type=both)
// book, which is the shape #2933 was reported against.
func categoryFixture(t *testing.T, srvURL string) (*QueueHandler, *sql.DB, *models.Book, context.Context) {
	t.Helper()
	h, database, _, clients, books, ctx := queueFixture(t)
	host, port := testServerHostPort(t, srvURL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
		EnabledForBooks: true, EnabledForAudiobooks: true,
		Category: ebookCategory, CategoryAudiobook: audiobookCategory,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	authors := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "cat-author", Name: "Zogarth", SortName: "Zogarth", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	book := &models.Book{
		ForeignID: "cat-book", AuthorID: author.ID, Title: "The Primal Hunter 9",
		SortTitle: "primal hunter 9", MediaType: models.MediaTypeBoth,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatalf("create book: %v", err)
	}
	return h, database, book, ctx
}

// TestQueueGrab_CategoryFollowsTheRelease is #2933. The book page's combined
// search ("Search ebook + audiobook indexers") lists results under Ebooks and
// Audiobooks, but used to post the BOOK's media type, "both", with every grab.
// "both" matches no client category, so an audiobook landed in the ebook
// category. The category must follow the release being grabbed.
func TestQueueGrab_CategoryFollowsTheRelease(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	cases := []struct {
		name      string
		title     string
		mediaType string
		want      string
	}{
		// What the fixed book page sends: the result's own classification
		// from the indexer leg that found it, even with no format in the title
		// (the reporter's release names none).
		{"result classified audiobook", "Zogarth - The Primal Hunter - 09 - The Primal Hunter 9", models.MediaTypeAudiobook, audiobookCategory},
		{"result classified ebook", "Zogarth - The Primal Hunter - 09 - The Primal Hunter 9", models.MediaTypeEbook, ebookCategory},
		// A caller that still sends the book's "both" (an older UI, an API
		// client) gets the release title's format.
		{"both with an m4b release", "Zogarth - The Primal Hunter 9 [M4B]", models.MediaTypeBoth, audiobookCategory},
		{"both with an epub release", "Zogarth - The Primal Hunter 9 - epub", models.MediaTypeBoth, ebookCategory},
		{"both with no format in the title", "Zogarth - The Primal Hunter 9", models.MediaTypeBoth, ebookCategory},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, srv := newCategoryRecorder(t)
			h, _, book, _ := categoryFixture(t, srv.URL)

			payload, _ := json.Marshal(map[string]any{
				"guid":      "guid-" + strconv.Itoa(i),
				"title":     tc.title,
				"nzbUrl":    srv.URL + "/release.nzb",
				"size":      100,
				"bookId":    book.ID,
				"protocol":  "usenet",
				"mediaType": tc.mediaType,
			})
			w := httptest.NewRecorder()
			h.Grab(w, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewReader(payload)))
			if w.Code != http.StatusAccepted {
				t.Fatalf("grab: status %d: %s", w.Code, w.Body.String())
			}
			if got := rec.last(t); got != tc.want {
				t.Errorf("category = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPendingGrab_AudiobookOfDualFormatBookUsesAudiobookCategory covers the
// force grab of a held release. The pending row records the format leg that
// found it, and that, not the book's "both", must pick the category (#2933).
func TestPendingGrab_AudiobookOfDualFormatBookUsesAudiobookCategory(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()

	rec, srv := newCategoryRecorder(t)
	queue, database, book, ctx := categoryFixture(t, srv.URL)
	pending := db.NewPendingReleaseRepo(database)

	const title = "Zogarth - The Primal Hunter - 09 - The Primal Hunter 9"
	releaseJSON, _ := json.Marshal(map[string]any{
		"guid": "pending-audio", "title": title, "nzbUrl": srv.URL + "/release.nzb",
		"size": 100, "protocol": "usenet",
	})
	if err := pending.Upsert(ctx, &models.PendingRelease{
		BookID: book.ID, MediaType: models.MediaTypeAudiobook, Title: title,
		GUID: "pending-audio", Protocol: "usenet", Reason: "delay", ReleaseJSON: string(releaseJSON),
	}); err != nil {
		t.Fatalf("upsert pending: %v", err)
	}
	all, err := pending.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("pending list: %v (%d rows)", err, len(all))
	}
	id := strconv.FormatInt(all[0].ID, 10)

	h := NewPendingHandler(pending, queue, queue.downloads, queue.books)
	w := httptest.NewRecorder()
	h.Grab(w, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/pending/"+id+"/grab", nil), "id", id))
	if w.Code != http.StatusCreated {
		t.Fatalf("pending grab: status %d: %s", w.Code, w.Body.String())
	}
	if got := rec.last(t); got != audiobookCategory {
		t.Errorf("category = %q, want %q", got, audiobookCategory)
	}
}
