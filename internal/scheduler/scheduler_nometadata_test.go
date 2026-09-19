package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// transmissionNoMetadataFake is a Transmission RPC endpoint that reports
// torrents in the shape reported in #2709: accepted, still present, zero
// files, zero total size, no connected peers, empty errorString. It records
// the torrent-remove calls so a test can assert what was cleaned up.
//
// stuck is how many torrents have no metadata; healthy is how many have
// resolved and are part-downloaded. Torrent ids run 1..stuck+healthy, stuck
// ones first, which is also the denominator LooksLikeClientOutage divides by.
type transmissionNoMetadataFake struct {
	*httptest.Server
	mu      sync.Mutex
	removed []int64
}

func newTransmissionNoMetadataFake(t *testing.T, stuck, healthy int) *transmissionNoMetadataFake {
	t.Helper()
	f := &transmissionNoMetadataFake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string `json:"method"`
			Arguments struct {
				IDs []int64 `json:"ids"`
			} `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "torrent-remove":
			f.mu.Lock()
			f.removed = append(f.removed, req.Arguments.IDs...)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success"})
		case "torrent-get":
			torrents := make([]map[string]any, 0, stuck+healthy)
			id := 1
			for i := 0; i < stuck; i++ {
				torrents = append(torrents, map[string]any{
					"id": id, "status": 0, "errorString": "",
					"totalSize": 0, "percentDone": 0,
					"metadataPercentComplete": 0, "peersConnected": 0,
				})
				id++
			}
			for i := 0; i < healthy; i++ {
				torrents = append(torrents, map[string]any{
					"id": id, "status": 4, "errorString": "",
					"totalSize": 8192, "percentDone": 0.5,
					"metadataPercentComplete": 1, "peersConnected": 9,
				})
				id++
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"arguments": map[string]any{"torrents": torrents},
				"result":    "success",
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success"})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *transmissionNoMetadataFake) removals() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.removed...)
}

// noMetadataFixture builds an install with one Transmission client and one
// grabbed torrent download per torrent id in guids.
type noMetadataFixture struct {
	scheduler *Scheduler
	downloads *db.DownloadRepo
	blocklist *db.BlocklistRepo
	history   *db.HistoryRepo
	guids     []string
}

// guid is the single-download shorthand the simple tests use.
func (f *noMetadataFixture) guid() string { return f.guids[0] }

func newNoMetadataFixture(t *testing.T, srvURL string, grabbedAgo time.Duration, torrentIDs ...int64) *noMetadataFixture {
	t.Helper()
	if len(torrentIDs) == 0 {
		torrentIDs = []int64{1}
	}
	host, port := stallServerHostPort(t, srvURL)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	authorsRepo := db.NewAuthorRepo(database)
	booksRepo := db.NewBookRepo(database)
	author := &models.Author{ForeignID: "OLMETA", Name: "Meta", SortName: "Meta", MetadataProvider: "ol", Monitored: true}
	if err := authorsRepo.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}

	clientsRepo := db.NewDownloadClientRepo(database)
	client := &models.DownloadClient{
		Name: "transmission", Type: "transmission", Host: host, Port: port, Enabled: true,
	}
	if err := clientsRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	indexersRepo := db.NewIndexerRepo(database)
	idx := &models.Indexer{Name: "mock", Type: "newznab", URL: "http://x", APIKey: "k", Enabled: true, Priority: 25}
	if err := indexersRepo.Create(ctx, idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	downloadsRepo := db.NewDownloadRepo(database)
	var guids []string
	for _, tid := range torrentIDs {
		book := &models.Book{
			ForeignID: fmt.Sprintf("OLBMETA%d", tid), AuthorID: author.ID,
			Title:     fmt.Sprintf("Unresolved Book %d", tid),
			SortTitle: fmt.Sprintf("Unresolved Book %d", tid), Status: models.BookStatusWanted,
			Genres: []string{}, MetadataProvider: "ol", Monitored: true,
		}
		if err := booksRepo.Create(ctx, book); err != nil {
			t.Fatalf("create book: %v", err)
		}
		remote := strconv.FormatInt(tid, 10)
		guid := "g-nometa-" + remote
		dl := &models.Download{
			GUID: guid, Title: "Unresolved Release " + remote, BookID: &book.ID,
			IndexerID: &idx.ID, DownloadClientID: &client.ID,
			Status: models.StateGrabbed, Protocol: "torrent",
			TorrentID: &remote,
		}
		if err := downloadsRepo.Create(ctx, dl); err != nil {
			t.Fatalf("create download: %v", err)
		}
		if _, err := database.ExecContext(ctx,
			"UPDATE downloads SET status=?, grabbed_at=? WHERE id=?",
			models.DownloadStatusDownloading, time.Now().UTC().Add(-grabbedAgo), dl.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		guids = append(guids, guid)
	}

	settingsRepo := db.NewSettingsRepo(database)
	// Keep the re-search goroutine out of the test; the grab path has its own.
	_ = settingsRepo.Set(ctx, "autoGrab.enabled", "false")

	blocklistRepo := db.NewBlocklistRepo(database)
	historyRepo := db.NewHistoryRepo(database)

	return &noMetadataFixture{
		scheduler: &Scheduler{
			downloads: downloadsRepo,
			clients:   clientsRepo,
			indexers:  indexersRepo,
			books:     booksRepo,
			authors:   authorsRepo,
			settings:  settingsRepo,
			blocklist: blocklistRepo,
			history:   historyRepo,
			searcher:  indexer.NewSearcher(),
		},
		downloads: downloadsRepo,
		blocklist: blocklistRepo,
		history:   historyRepo,
		guids:     guids,
	}
}

// TestCheckStalledDownloads_TransmissionNoMetadata is #2709 end to end: a
// magnet Transmission accepted and never resolved, older than the stall
// timeout, is failed, removed from the client and given a history event, with
// an error message that says what happened.
//
// It is explicitly NOT blocklisted. The stall says nothing about the release,
// so banning it for ever (the blocklist has no expiry) would be punishing the
// release for the network's behaviour.
func TestCheckStalledDownloads_TransmissionNoMetadata(t *testing.T) {
	fake := newTransmissionNoMetadataFake(t, 1, 3)
	fx := newNoMetadataFixture(t, fake.URL, 34*24*time.Hour)
	ctx := context.Background()

	fx.scheduler.checkStalledDownloads(ctx)

	got, err := fx.downloads.GetByGUID(ctx, fx.guid())
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.DownloadStatusFailed {
		t.Errorf("download status: want %q, got %q", models.DownloadStatusFailed, got.Status)
	}
	if !strings.Contains(got.ErrorMessage, "metadata") {
		t.Errorf("error message should say the metadata never resolved, got %q", got.ErrorMessage)
	}

	if removed := fake.removals(); len(removed) != 1 || removed[0] != 1 {
		t.Errorf("expected the empty torrent to be removed from Transmission, got %v", removed)
	}

	blocked, err := fx.blocklist.IsBlocked(ctx, fx.guid())
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if blocked {
		t.Error("a no-metadata stall must not blocklist the release: the blocklist is permanent and the stall says nothing about the release")
	}

	events, err := fx.history.ListByType(ctx, models.HistoryEventDownloadStalled)
	if err != nil {
		t.Fatalf("ListByType: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 downloadStalled history event, got %d", len(events))
	}
}

// TestCheckStalledDownloads_NoMetadataClientOutage is the batch guard. A
// client that has lost DHT, UDP or its port forward reports every magnet it is
// holding as metadata-less at once. That is the client's fault, not a run of
// bad releases, so the whole batch is left alone rather than failed.
func TestCheckStalledDownloads_NoMetadataClientOutage(t *testing.T) {
	// Four stuck, one healthy: 4 of 5 unfinished torrents, over the threshold.
	fake := newTransmissionNoMetadataFake(t, 4, 1)
	fx := newNoMetadataFixture(t, fake.URL, 34*24*time.Hour, 1, 2, 3, 4)
	ctx := context.Background()

	fx.scheduler.checkStalledDownloads(ctx)

	for _, guid := range fx.guids {
		got, err := fx.downloads.GetByGUID(ctx, guid)
		if err != nil {
			t.Fatalf("GetByGUID %s: %v", guid, err)
		}
		if got.Status != models.DownloadStatusDownloading {
			t.Errorf("%s: a client-wide fault must not fail the downloads, got status %q", guid, got.Status)
		}
		blocked, err := fx.blocklist.IsBlocked(ctx, guid)
		if err != nil {
			t.Fatalf("IsBlocked %s: %v", guid, err)
		}
		if blocked {
			t.Errorf("%s: a client-wide fault must never blocklist anything", guid)
		}
	}
	if len(fake.removals()) != 0 {
		t.Errorf("nothing should have been removed from the client, got %v", fake.removals())
	}
	events, err := fx.history.ListByType(ctx, models.HistoryEventDownloadStalled)
	if err != nil {
		t.Fatalf("ListByType: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no stall history events during a client outage, got %d", len(events))
	}
}

// TestCheckStalledDownloads_NoMetadataBelowOutageFloor pins the other side of
// the batch guard: two dead magnets are not an outage, they are two dead
// magnets, and the user still wants them failed.
func TestCheckStalledDownloads_NoMetadataBelowOutageFloor(t *testing.T) {
	fake := newTransmissionNoMetadataFake(t, 2, 0)
	fx := newNoMetadataFixture(t, fake.URL, 34*24*time.Hour, 1, 2)
	ctx := context.Background()

	fx.scheduler.checkStalledDownloads(ctx)

	for _, guid := range fx.guids {
		got, err := fx.downloads.GetByGUID(ctx, guid)
		if err != nil {
			t.Fatalf("GetByGUID %s: %v", guid, err)
		}
		if got.Status != models.DownloadStatusFailed {
			t.Errorf("%s: want failed, got %q", guid, got.Status)
		}
	}
}

// TestCheckStalledDownloads_NoMetadataYoungerThanTimeout is the false positive
// guard. A magnet that has only just been grabbed looks exactly like a dead
// one: zero files, zero size, no peers. Nothing may happen to it until it has
// had the whole stall timeout to resolve.
func TestCheckStalledDownloads_NoMetadataYoungerThanTimeout(t *testing.T) {
	fake := newTransmissionNoMetadataFake(t, 1, 3)
	fx := newNoMetadataFixture(t, fake.URL, 30*time.Minute) // default timeout is 120m
	ctx := context.Background()

	fx.scheduler.checkStalledDownloads(ctx)

	got, err := fx.downloads.GetByGUID(ctx, fx.guid())
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.DownloadStatusDownloading {
		t.Errorf("a magnet still inside the stall timeout must be left alone, got status %q", got.Status)
	}
	if len(fake.removals()) != 0 {
		t.Errorf("nothing should have been removed from the client, got %v", fake.removals())
	}
	blocked, _ := fx.blocklist.IsBlocked(ctx, fx.guid())
	if blocked {
		t.Error("a magnet still inside the stall timeout must not be blocklisted")
	}
}

// TestCheckStalledDownloads_HealthyTransmissionDownloadUntouched pins the other
// side of the rule: a torrent whose metadata resolved is not stalled, however
// long it has been going.
func TestCheckStalledDownloads_HealthyTransmissionDownloadUntouched(t *testing.T) {
	fake := newTransmissionNoMetadataFake(t, 0, 1)
	fx := newNoMetadataFixture(t, fake.URL, 34*24*time.Hour)
	ctx := context.Background()

	fx.scheduler.checkStalledDownloads(ctx)

	got, err := fx.downloads.GetByGUID(ctx, fx.guid())
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.DownloadStatusDownloading {
		t.Errorf("a torrent with metadata must be left alone, got status %q", got.Status)
	}
	if len(fake.removals()) != 0 {
		t.Errorf("nothing should have been removed from the client, got %v", fake.removals())
	}
}

// TestHandleStalledDownload_RejectsStallNone pins the zero value: a caller that
// forgets to set the kind must not fail a download with a plausible reason.
func TestHandleStalledDownload_RejectsStallNone(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()
	ctx := context.Background()

	downloadsRepo := db.NewDownloadRepo(database)
	blocklistRepo := db.NewBlocklistRepo(database)
	dl := &models.Download{
		GUID: "g-none", Title: "not stalled", Status: models.DownloadStatusDownloading,
		Protocol: "torrent",
	}
	if err := downloadsRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	s := &Scheduler{
		downloads: downloadsRepo,
		blocklist: blocklistRepo,
		settings:  db.NewSettingsRepo(database),
	}
	s.handleStalledDownload(ctx, dl, nil, 0)

	got, err := downloadsRepo.GetByGUID(ctx, "g-none")
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.DownloadStatusDownloading {
		t.Errorf("the zero stall kind must not fail the download, got status %q", got.Status)
	}
	if blocked, _ := blocklistRepo.IsBlocked(ctx, "g-none"); blocked {
		t.Error("the zero stall kind must not blocklist anything")
	}
}
