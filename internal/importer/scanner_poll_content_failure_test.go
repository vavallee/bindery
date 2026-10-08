package importer

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/models"
)

// contentFailureFixture is a scanner with a blocklist wired the way main.go
// wires it, plus the repos to give its downloads books and clients.
type contentFailureFixture struct {
	scanner   *Scanner
	downloads *db.DownloadRepo
	blocklist *db.BlocklistRepo
	clients   *db.DownloadClientRepo
	books     *db.BookRepo
	authors   *db.AuthorRepo
}

func newContentFailureFixture(t *testing.T) contentFailureFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	blocklist := db.NewBlocklistRepo(database)
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	s := NewScanner(dlRepo, clientRepo, books, authors,
		db.NewHistoryRepo(database), t.TempDir(), "", "", "", "")
	s.WithFormatEnforcement(db.NewQualityProfileRepo(database), blocklist)
	return contentFailureFixture{scanner: s, downloads: dlRepo, blocklist: blocklist, clients: clientRepo, books: books, authors: authors}
}

// addBook creates a wanted book and returns its id.
func (f contentFailureFixture) addBook(t *testing.T, ctx context.Context, foreignID string) int64 {
	t.Helper()
	author := &models.Author{Name: "Author " + foreignID, ForeignID: "a-" + foreignID, SortName: "Author"}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Book " + foreignID, ForeignID: "b-" + foreignID, Status: "wanted", MediaType: models.MediaTypeEbook}
	if err := f.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return book.ID
}

func (f contentFailureFixture) addDownload(t *testing.T, ctx context.Context, client *models.DownloadClient, guid, sourceID string, bookID *int64) {
	t.Helper()
	id := sourceID
	dl := &models.Download{
		GUID:             guid,
		Title:            "Broken Book",
		Status:           models.StateDownloading,
		Protocol:         "usenet",
		SABnzbdNzoID:     &id,
		BookID:           bookID,
		DownloadClientID: &client.ID,
	}
	if err := f.downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
}

func (f contentFailureFixture) entries(t *testing.T, ctx context.Context) []models.BlocklistEntry {
	t.Helper()
	entries, err := f.blocklist.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// assertBlocklisted checks the row is failed, whether it is on the blocklist
// with the expected reason, and that the blocklist the wanted sweep builds
// (decision.NewBlocklistedSpec, keyed on GUID) skips the release next time.
func (f contentFailureFixture) assertBlocklisted(t *testing.T, ctx context.Context, guid string, want bool, wantReason string) {
	t.Helper()
	got, err := f.downloads.GetByGUID(ctx, guid)
	if err != nil || got == nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateFailed {
		t.Errorf("status = %q, want %q", got.Status, models.StateFailed)
	}
	entries := f.entries(t, ctx)
	if !want {
		if len(entries) != 0 {
			t.Fatalf("blocklist = %+v, want empty: a client side failure must keep the #2710 cooldown", entries)
		}
		return
	}
	if len(entries) != 1 {
		t.Fatalf("blocklist has %d entries, want 1", len(entries))
	}
	if entries[0].GUID != guid || entries[0].Reason != wantReason {
		t.Errorf("blocklist entry = {guid %q, reason %q}, want {%q, %q}",
			entries[0].GUID, entries[0].Reason, guid, wantReason)
	}
	ok, _ := decision.NewBlocklistedSpec(entries).IsSatisfiedBy(decision.Release{GUID: guid}, models.Book{})
	if ok {
		t.Error("the next sweep's blocklist spec would still accept the release")
	}
}

// TestCheckNZBGetDownloads_ContentFailureBlocklists covers #3024 for NZBGet: a
// status about the NZB itself blocklists the release, one about the client
// does not.
func TestCheckNZBGetDownloads_ContentFailureBlocklists(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"FAILURE/PAR", true},
		{"FAILURE/UNPACK", true},
		{"FAILURE/HEALTH", true},
		{"FAILURE/MOVE", false},
		{"FAILURE/INTERNAL_ERROR", false},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			ctx := context.Background()
			srv := httptest.NewServer(nzbgetHandler(t, []nzbget.HistoryItem{{
				NZBID: 77, NZBName: "Broken Book", Status: tc.status,
			}}, nil))
			defer srv.Close()

			f := newContentFailureFixture(t)
			client := nzbgetClient(t, ctx, f.clients, srv.URL)
			f.addDownload(t, ctx, client, "guid-ng-3024", "77", nil)

			f.scanner.checkNZBGetDownloads(ctx, client)
			f.assertBlocklisted(t, ctx, "guid-ng-3024", tc.want, "downloadFailed: "+tc.status)
		})
	}
}

// TestCheckSABnzbdDownloads_ContentFailureBlocklists is the SABnzbd analogue,
// using fail_message strings copied from SABnzbd's source.
func TestCheckSABnzbdDownloads_ContentFailureBlocklists(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{"Aborted, cannot be completed - https://sabnzbd.org/not-complete", true},
		{"Repair failed, not enough repair blocks (412 short)", true},
		{"Unpacking failed, CRC error", true},
		{"Unpacking failed, disk full", false},
		{"Failed to move files", false},
		{"some message SABnzbd has never written", false},
	}
	for _, tc := range cases {
		t.Run(tc.message, func(t *testing.T) {
			ctx := context.Background()
			srv := httptest.NewServer(sabHistoryHandler(t, []sabnzbd.HistorySlot{{
				NzoID: "SABnzbd_nzo_3024", Name: "Broken Book", Status: "Failed", FailMessage: tc.message,
			}}, nil))
			defer srv.Close()

			f := newContentFailureFixture(t)
			client := sabClient(t, ctx, f.clients, srv.URL)
			f.addDownload(t, ctx, client, "guid-sab-3024", "SABnzbd_nzo_3024", nil)

			f.scanner.checkSABnzbdDownloads(ctx, client)
			f.assertBlocklisted(t, ctx, "guid-sab-3024", tc.want, "downloadFailed: "+tc.message)
		})
	}
}

// TestCheckNZBGetDownloads_ContentFailureDedupe drives the dedupe through the
// poll path: a release that is already blocklisted for the same book (a
// manual grab re-sent it) is not added again.
func TestCheckNZBGetDownloads_ContentFailureDedupe(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(nzbgetHandler(t, []nzbget.HistoryItem{{
		NZBID: 78, NZBName: "Broken Book", Status: "FAILURE/PAR",
	}}, nil))
	defer srv.Close()

	f := newContentFailureFixture(t)
	bookID := f.addBook(t, ctx, "dedupe")
	if err := f.blocklist.Create(ctx, &models.BlocklistEntry{BookID: &bookID, GUID: "guid-dupe", Title: "Broken Book", Reason: "downloadFailed: FAILURE/PAR"}); err != nil {
		t.Fatal(err)
	}
	client := nzbgetClient(t, ctx, f.clients, srv.URL)
	f.addDownload(t, ctx, client, "guid-dupe", "78", &bookID)

	f.scanner.checkNZBGetDownloads(ctx, client)

	if got := f.entries(t, ctx); len(got) != 1 {
		t.Fatalf("blocklist has %d entries, want 1: a repeat failure stacked a duplicate", len(got))
	}
}

// TestCheckNZBGetDownloads_ContentFailureOtherBookRow pins that the dedupe is
// per book. A row for the same GUID under another book does not stand in for
// this one, because deleting that book deletes its rows and would silently
// unblock the release for this book too.
func TestCheckNZBGetDownloads_ContentFailureOtherBookRow(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(nzbgetHandler(t, []nzbget.HistoryItem{{
		NZBID: 79, NZBName: "Broken Book", Status: "FAILURE/PAR",
	}}, nil))
	defer srv.Close()

	f := newContentFailureFixture(t)
	bookA := f.addBook(t, ctx, "a")
	bookB := f.addBook(t, ctx, "b")
	if err := f.blocklist.Create(ctx, &models.BlocklistEntry{BookID: &bookA, GUID: "guid-shared", Title: "Broken Book", Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	client := nzbgetClient(t, ctx, f.clients, srv.URL)
	f.addDownload(t, ctx, client, "guid-shared", "79", &bookB)

	f.scanner.checkNZBGetDownloads(ctx, client)
	if err := f.blocklist.DeleteByBookID(ctx, bookA); err != nil {
		t.Fatal(err)
	}

	got := f.entries(t, ctx)
	if len(got) != 1 || got[0].BookID == nil || *got[0].BookID != bookB {
		t.Fatalf("blocklist after deleting book A's rows = %+v, want one row for book B", got)
	}
}

// TestCheckNZBGetDownloads_ContentFailureStorm covers a sweep's worth of
// failures arriving at once, the reporter's shape in #3024 (about 36 a sweep,
// mostly FAILURE/HEALTH on an old backlog). Statuses NZBGet never uses for its
// own faults blocklist every one. FAILURE/UNPACK with no log to go on is the
// broken unrar case, and the breaker stops it at contentBreakerDistinct-1.
func TestCheckNZBGetDownloads_ContentFailureStorm(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{"FAILURE/HEALTH", 6},
		{"FAILURE/BAD", 6},
		{"FAILURE/UNPACK", 2},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			ctx := context.Background()
			var items []nzbget.HistoryItem
			for i := 1; i <= 6; i++ {
				items = append(items, nzbget.HistoryItem{NZBID: 100 + i, NZBName: fmt.Sprintf("Book %d", i), Status: tc.status})
			}
			srv := httptest.NewServer(nzbgetHandler(t, items, nil))
			defer srv.Close()

			f := newContentFailureFixture(t)
			client := nzbgetClient(t, ctx, f.clients, srv.URL)
			for i := 1; i <= 6; i++ {
				f.addDownload(t, ctx, client, fmt.Sprintf("guid-storm-%d", i), fmt.Sprint(100+i), nil)
			}

			f.scanner.checkNZBGetDownloads(ctx, client)

			if got := f.entries(t, ctx); len(got) != tc.want {
				t.Fatalf("blocklist has %d entries after 6 releases failed with %s, want %d", len(got), tc.status, tc.want)
			}
			for i := 1; i <= 6; i++ {
				dl, err := f.downloads.GetByGUID(ctx, fmt.Sprintf("guid-storm-%d", i))
				if err != nil || dl == nil || dl.Status != models.StateFailed {
					t.Errorf("download %d not failed: %+v %v", i, dl, err)
				}
			}
		})
	}
}
