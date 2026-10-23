package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/models"
)

type blockedFixture struct {
	h        *SetupStateHandler
	indexers *db.IndexerRepo
	clients  *db.DownloadClientRepo
	history  *db.HistoryRepo
	roots    *db.RootFolderRepo
	health   *downloader.HealthStore
	cfg      *config.Config
	lib, dl  string
	clock    time.Time
	clientID int64
}

// newBlockedFixture builds a set up instance that has imported before, with
// healthy folders and one healthy client. It deliberately writes NO telemetry
// or funnel settings: the gate must work from the real rows alone.
func newBlockedFixture(t *testing.T) *blockedFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	f := &blockedFixture{
		indexers: db.NewIndexerRepo(database),
		clients:  db.NewDownloadClientRepo(database),
		history:  db.NewHistoryRepo(database),
		roots:    db.NewRootFolderRepo(database),
		health:   downloader.NewHealthStore(),
		clock:    time.Now(),
	}
	f.lib, f.dl = t.TempDir(), t.TempDir()
	f.cfg = &config.Config{LibraryDir: f.lib, DownloadDir: f.dl}
	f.h = NewSetupStateHandler(f.indexers, f.clients, db.NewAuthorRepo(database), db.NewSettingsRepo(database)).
		WithBlockedChecks(f.cfg, f.health, f.history, f.roots)
	f.h.blocked.now = func() time.Time { return f.clock }

	ctx := context.Background()
	if err := f.indexers.Create(ctx, &models.Indexer{Name: "ix", URL: "http://x/api", Type: "newznab", Enabled: true}); err != nil {
		t.Fatalf("indexer: %v", err)
	}
	f.clientID = f.addClient(t, "sab", "sabnzbd", true, downloader.HealthOK)
	f.addHistory(t, models.HistoryEventBookImported)
	return f
}

func (f *blockedFixture) addClient(t *testing.T, name, typ string, enabled bool, status string) int64 {
	t.Helper()
	c := &models.DownloadClient{Name: name, Type: typ, Host: name, Port: 8080, Enabled: enabled}
	if err := f.clients.Create(context.Background(), c); err != nil {
		t.Fatalf("client: %v", err)
	}
	f.health.Set(c.ID, models.DownloadClientHealth{Status: status, Message: "details at /secret/path"})
	return c.ID
}

func (f *blockedFixture) addHistory(t *testing.T, eventType string) {
	t.Helper()
	if err := f.history.Create(context.Background(), &models.HistoryEvent{EventType: eventType, SourceTitle: "x"}); err != nil {
		t.Fatalf("history: %v", err)
	}
}

// get issues one request and returns the decoded state and raw body.
func (f *blockedFixture) get(t *testing.T, role string) (SetupState, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/setup-state", nil)
	if role != "" {
		req = req.WithContext(auth.WithUserRole(req.Context(), role))
	}
	w := httptest.NewRecorder()
	f.h.Get(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var s SetupState
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return s, w.Body.String()
}

// settled observes the condition, lets blockedPersistFor pass, and observes
// again, which is what a user returning to the page does.
func (f *blockedFixture) settled(t *testing.T) SetupState {
	t.Helper()
	f.get(t, "admin")
	f.clock = f.clock.Add(blockedPersistFor + time.Second)
	s, _ := f.get(t, "admin")
	return s
}

func (f *blockedFixture) wantBlocked(t *testing.T, area string) {
	t.Helper()
	s := f.settled(t)
	if !s.Blocked || s.BlockedArea != area {
		t.Fatalf("want blocked %s, got %+v", area, s)
	}
}

func (f *blockedFixture) wantClear(t *testing.T) {
	t.Helper()
	if s := f.settled(t); s.Blocked || s.BlockedArea != "" || s.BlockedReason != "" {
		t.Fatalf("want not blocked, got %+v", s)
	}
}

func TestSetupStateBlocked_HealthyInstanceIsNotBlocked(t *testing.T) {
	newBlockedFixture(t).wantClear(t)
}

// The gate comes from real rows, with no telemetry settings at all.
func TestSetupStateBlocked_GateIsHistoryNotTelemetry(t *testing.T) {
	f := newBlockedFixture(t)
	f.cfg.LibraryDir = filepath.Join(f.lib, "gone")
	f.wantBlocked(t, "storage")
}

func TestSetupStateBlocked_NeverImportedIsNotBlocked(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	f := &blockedFixture{clock: time.Now(), history: db.NewHistoryRepo(database), health: downloader.NewHealthStore()}
	f.cfg = &config.Config{LibraryDir: filepath.Join(t.TempDir(), "gone")}
	f.h = NewSetupStateHandler(db.NewIndexerRepo(database), db.NewDownloadClientRepo(database), db.NewAuthorRepo(database), db.NewSettingsRepo(database)).
		WithBlockedChecks(f.cfg, f.health, f.history, db.NewRootFolderRepo(database))
	f.h.blocked.now = func() time.Time { return f.clock }
	// A grab, a failed import and a rename are not an import.
	for _, e := range []string{models.HistoryEventGrabbed, models.HistoryEventImportFailed, models.HistoryEventBookRenamed} {
		f.addHistory(t, e)
	}
	f.wantClear(t)
}

func TestSetupStateBlocked_ManualFolderImportCounts(t *testing.T) {
	f := newBlockedFixture(t)
	// Replace the bookImported event with only a folder import.
	events, _ := f.history.List(context.Background())
	for _, e := range events {
		_ = f.history.Delete(context.Background(), e.ID)
	}
	f.addHistory(t, models.HistoryEventDownloadFolderImport)
	f.cfg.LibraryDir = filepath.Join(f.lib, "gone")
	f.wantBlocked(t, "storage")
}

// Without a history repo the gate cannot be evaluated, so nothing is flagged.
func TestSetupStateBlocked_NoHistoryRepoNeverBlocks(t *testing.T) {
	f := newBlockedFixture(t)
	f.h.blocked.history = nil
	f.cfg.LibraryDir = filepath.Join(f.lib, "gone")
	f.wantClear(t)
}

func TestSetupStateBlocked_MissingLibraryDir(t *testing.T) {
	f := newBlockedFixture(t)
	f.cfg.LibraryDir = filepath.Join(f.lib, "gone")
	f.get(t, "admin")
	_, body := f.get(t, "admin")
	if strings.Contains(body, f.lib) || strings.Contains(body, f.dl) {
		t.Errorf("response leaks a folder path: %s", body)
	}
	f.wantBlocked(t, "storage")
}

// Root folders are the importer's real destinations. LibraryDir is only the
// last fallback, so a missing LibraryDir with healthy root folders is fine,
// and a broken root folder is not.
func TestSetupStateBlocked_RootFoldersAreTheDestinations(t *testing.T) {
	f := newBlockedFixture(t)
	good := t.TempDir()
	if _, err := f.roots.Create(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	f.cfg.LibraryDir = filepath.Join(f.lib, "never-created")
	f.wantClear(t)

	if _, err := f.roots.Create(context.Background(), filepath.Join(good, "gone")); err != nil {
		t.Fatal(err)
	}
	f.h.blocked.storageAt = time.Time{}
	f.wantBlocked(t, "storage")
}

// Windows installs default DownloadDir to "" (#2902).
func TestSetupStateBlocked_EmptyDownloadDirIsNotAnError(t *testing.T) {
	f := newBlockedFixture(t)
	f.cfg.DownloadDir = ""
	f.wantClear(t)
}

func TestSetupStateBlocked_MissingDownloadDir(t *testing.T) {
	f := newBlockedFixture(t)
	f.cfg.DownloadDir = filepath.Join(f.dl, "gone")
	f.wantBlocked(t, "storage")
}

// The importer only warns when it cannot remove a source after a move, so a
// download dir needs to be readable, not writable.
func TestSetupStateBlocked_ReadOnlyDownloadDirIsFine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newBlockedFixture(t)
	if err := os.Chmod(f.dl, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.dl, 0o700) })
	f.wantClear(t)
}

func TestSetupStateBlocked_UnreadableDownloadDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newBlockedFixture(t)
	if err := os.Chmod(f.dl, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.dl, 0o700) })
	f.wantBlocked(t, "storage")
}

func TestSetupStateBlocked_UnwritableLibraryDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newBlockedFixture(t)
	if err := os.Chmod(f.lib, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.lib, 0o700) })
	f.wantBlocked(t, "storage")
}

// A path remap means the client's folder maps elsewhere, so the configured
// download dir may be unused and missing without breaking anything.
func TestSetupStateBlocked_PathRemapSkipsDownloadDir(t *testing.T) {
	f := newBlockedFixture(t)
	f.cfg.DownloadDir = filepath.Join(f.dl, "gone")
	f.cfg.DownloadPathRemap = "/remote:/local"
	f.wantClear(t)

	f.cfg.DownloadPathRemap = ""
	f.h.blocked.storageAt = time.Time{}
	f.wantBlocked(t, "storage")

	list, _ := f.clients.List(context.Background())
	list[0].PathRemap = "/remote:/local"
	if err := f.clients.Update(context.Background(), &list[0]); err != nil {
		t.Fatal(err)
	}
	f.h.blocked.storageAt = time.Time{}
	f.wantClear(t)
}

func TestSetupStateBlocked_AudiobookDirsAreProbed(t *testing.T) {
	f := newBlockedFixture(t)
	f.cfg.AudiobookDir = filepath.Join(f.lib, "gone-audio")
	f.wantBlocked(t, "storage")

	f.cfg.AudiobookDir = ""
	f.cfg.AudiobookDownloadDir = filepath.Join(f.dl, "gone-audio-dl")
	f.h.blocked.storageAt = time.Time{}
	f.wantBlocked(t, "storage")

	f.cfg.AudiobookDownloadDir = t.TempDir()
	f.h.blocked.storageAt = time.Time{}
	f.cfg.AudiobookDir = t.TempDir()
	f.wantClear(t)
}

// The request path must not write into the library (config.CheckDir does).
func TestSetupStateBlocked_ProbeLeavesNoFiles(t *testing.T) {
	f := newBlockedFixture(t)
	f.settled(t)
	for _, dir := range []string{f.lib, f.dl} {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("%s has %d entries after a GET", dir, len(entries))
		}
	}
}

func TestSetupStateBlocked_StorageProbeIsCached(t *testing.T) {
	f := newBlockedFixture(t)
	calls := 0
	f.h.blocked.probe = func(path string, write bool) bool { calls++; return dirAccessible(path, write) }
	f.get(t, "admin")
	first := calls
	if first == 0 {
		t.Fatal("probe never ran")
	}
	f.clock = f.clock.Add(storageProbeTTL / 2)
	f.get(t, "admin")
	if calls != first {
		t.Errorf("probe re-ran inside the TTL: %d -> %d", first, calls)
	}
	f.clock = f.clock.Add(storageProbeTTL)
	f.get(t, "admin")
	if calls == first {
		t.Error("probe did not re-run after the TTL")
	}
}

// A hung mount must neither hang the request nor start a second probe.
func TestSetupStateBlocked_HungProbeAnswersFromLastKnownState(t *testing.T) {
	f := newBlockedFixture(t)
	f.get(t, "admin") // known good
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	f.h.blocked.probe = func(string, bool) bool { started <- struct{}{}; <-release; return false }
	f.h.blocked.wait = 50 * time.Millisecond
	f.clock = f.clock.Add(storageProbeTTL + time.Second)

	done := make(chan SetupState)
	go func() { s, _ := f.get(t, "admin"); done <- s }()
	select {
	case s := <-done:
		if s.Blocked {
			t.Error("last known good state should have been returned")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request hung on a stuck probe")
	}
	f.get(t, "admin")
	if n := len(started); n > 1 {
		t.Errorf("%d probes started while one was in flight", n)
	}
	close(release)
}

func TestSetupStateBlocked_NoEnabledIndexer(t *testing.T) {
	f := newBlockedFixture(t)
	list, _ := f.indexers.List(context.Background())
	list[0].Enabled = false
	if err := f.indexers.Update(context.Background(), &list[0]); err != nil {
		t.Fatal(err)
	}
	f.wantBlocked(t, "indexer")
}

func TestSetupStateBlocked_NoEnabledClient(t *testing.T) {
	f := newBlockedFixture(t)
	list, _ := f.clients.List(context.Background())
	list[0].Enabled = false
	if err := f.clients.Update(context.Background(), &list[0]); err != nil {
		t.Fatal(err)
	}
	f.wantBlocked(t, "download-client")
}

func TestSetupStateBlocked_AllClientsFailing(t *testing.T) {
	f := newBlockedFixture(t)
	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthError, Message: "cannot read /downloads/secret"})
	f.wantBlocked(t, "download-client")
	if _, body := f.get(t, "admin"); strings.Contains(body, "/downloads/secret") || strings.Contains(body, "/secret/path") {
		t.Errorf("client health message leaked: %s", body)
	}

	// One healthy client keeps the pipeline moving.
	f.addClient(t, "nzbget", "nzbget", true, downloader.HealthOK)
	f.wantClear(t)
}

// Disabled clients never receive grabs, so their health is irrelevant either
// way.
func TestSetupStateBlocked_DisabledClientsAreNotCounted(t *testing.T) {
	f := newBlockedFixture(t)
	f.addClient(t, "off", "nzbget", false, downloader.HealthError)
	f.wantClear(t) // healthy enabled client, failing disabled one

	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthError})
	f.addClient(t, "off-ok", "deluge", false, downloader.HealthOK)
	// The only healthy client is disabled, so the enabled one failing is
	// still every enabled client failing.
	f.wantBlocked(t, "download-client")
}

func TestSetupStateBlocked_UnknownAndCheckingAreNotFailures(t *testing.T) {
	f := newBlockedFixture(t)
	for _, st := range []string{downloader.HealthUnknown, downloader.HealthChecking} {
		f.health.Set(f.clientID, models.DownloadClientHealth{Status: st})
		f.wantClear(t)
	}
}

func TestSetupStateBlocked_AdvisoryAloneIsNotBlocked(t *testing.T) {
	f := newBlockedFixture(t)
	f.health.SetAdvisory(f.clientID, downloader.AdvisoryUnpackers, models.DownloadClientHealth{Status: downloader.HealthError, Message: "no unrar"})
	f.wantClear(t)
}

// One bad observation, such as a client restarting, must not turn the notice
// red; it has to persist, and a recovery in between starts the clock over.
func TestSetupStateBlocked_DebouncesBlips(t *testing.T) {
	f := newBlockedFixture(t)
	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthError})
	if s, _ := f.get(t, "admin"); s.Blocked {
		t.Fatal("first bad observation reported blocked")
	}
	f.clock = f.clock.Add(blockedPersistFor / 2)
	if s, _ := f.get(t, "admin"); s.Blocked {
		t.Fatal("reported blocked before the condition persisted")
	}

	// Recovers, then fails again: the clock restarts.
	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthOK})
	f.get(t, "admin")
	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthError})
	f.clock = f.clock.Add(blockedPersistFor)
	if s, _ := f.get(t, "admin"); s.Blocked {
		t.Fatal("a recovery in between did not restart the clock")
	}
	f.clock = f.clock.Add(blockedPersistFor + time.Second)
	if s, _ := f.get(t, "admin"); !s.Blocked {
		t.Fatal("a persistent failure was never reported")
	}
}

// A blip, a long gap with nobody looking, then a fresh blip must not read as a
// condition that has lasted the whole time.
func TestSetupStateBlocked_IdleGapRestartsTheClock(t *testing.T) {
	f := newBlockedFixture(t)
	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthError})
	f.get(t, "admin") // blip seen at t0
	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthOK})

	f.clock = f.clock.Add(6 * time.Hour)
	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthError})
	if s, _ := f.get(t, "admin"); s.Blocked {
		t.Fatal("debounce bypassed by a stale sighting")
	}
	f.clock = f.clock.Add(blockedPersistFor + time.Second)
	if s, _ := f.get(t, "admin"); !s.Blocked {
		t.Fatal("a persistent failure after the gap was never reported")
	}
}

// Sightings spaced inside the stale window do accumulate.
func TestSetupStateBlocked_RegularSightingsAccumulate(t *testing.T) {
	f := newBlockedFixture(t)
	f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthError})
	for i := 0; i < 3; i++ {
		f.get(t, "admin")
		f.clock = f.clock.Add(blockedStaleAfter / 2)
	}
	if s, _ := f.get(t, "admin"); !s.Blocked {
		t.Fatal("sightings inside the stale window did not accumulate")
	}
}

// A different cause is a different condition and gets its own full clock.
func TestSetupStateBlocked_AreaChangeRestartsTheClock(t *testing.T) {
	f := newBlockedFixture(t)
	good := f.cfg.LibraryDir
	f.cfg.LibraryDir = filepath.Join(f.lib, "gone")
	f.wantBlocked(t, "storage")

	// Storage recovers and, in the same moment, the indexer goes away.
	f.cfg.LibraryDir = good
	f.h.blocked.storageAt = time.Time{}
	list, _ := f.indexers.List(context.Background())
	list[0].Enabled = false
	if err := f.indexers.Update(context.Background(), &list[0]); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.get(t, "admin"); s.Blocked {
		t.Fatalf("new cause inherited the old clock: %+v", s)
	}
	f.wantBlockedAfterWindow(t, "indexer")
}

func (f *blockedFixture) wantBlockedAfterWindow(t *testing.T, area string) {
	t.Helper()
	f.clock = f.clock.Add(blockedPersistFor + time.Second)
	if s, _ := f.get(t, "admin"); !s.Blocked || s.BlockedArea != area {
		t.Fatalf("want blocked %s after its own window, got %+v", area, s)
	}
}

// Nothing identifying about the install may reach a response: not a folder
// path, not a root folder name, not a client host or its health message.
func TestSetupStateBlocked_ResponseNamesNoPathHostOrMessage(t *testing.T) {
	const (
		libMarker  = "identifiable-library-marker"
		rootMarker = "identifiable-root-marker"
		dlMarker   = "identifiable-download-marker"
		hostMarker = "identifiable-host.invalid"
		msgMarker  = "identifiable-client-message"
	)
	markers := []string{libMarker, rootMarker, dlMarker, hostMarker, msgMarker}
	assertClean := func(t *testing.T, f *blockedFixture, area string) {
		t.Helper()
		s := f.settled(t)
		if !s.Blocked || s.BlockedArea != area {
			t.Fatalf("want blocked %s, got %+v", area, s)
		}
		_, body := f.get(t, "admin")
		for _, m := range markers {
			if strings.Contains(body, m) {
				t.Errorf("%s response leaks %q: %s", area, m, body)
			}
		}
	}
	newF := func(t *testing.T) *blockedFixture {
		f := newBlockedFixture(t)
		f.cfg.LibraryDir = filepath.Join(f.lib, libMarker)
		f.cfg.DownloadDir = filepath.Join(f.dl, dlMarker)
		list, _ := f.clients.List(context.Background())
		list[0].Host = hostMarker
		if err := f.clients.Update(context.Background(), &list[0]); err != nil {
			t.Fatal(err)
		}
		f.health.Set(f.clientID, models.DownloadClientHealth{Status: downloader.HealthError, Message: msgMarker})
		return f
	}

	t.Run("storage", func(t *testing.T) {
		f := newF(t)
		if _, err := f.roots.Create(context.Background(), filepath.Join(f.lib, rootMarker)); err != nil {
			t.Fatal(err)
		}
		assertClean(t, f, "storage")
	})
	t.Run("indexer", func(t *testing.T) {
		f := newF(t)
		for _, d := range []string{f.cfg.LibraryDir, f.cfg.DownloadDir} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		list, _ := f.indexers.List(context.Background())
		list[0].Enabled = false
		if err := f.indexers.Update(context.Background(), &list[0]); err != nil {
			t.Fatal(err)
		}
		assertClean(t, f, "indexer")
	})
	t.Run("download-client", func(t *testing.T) {
		f := newF(t)
		for _, d := range []string{f.cfg.LibraryDir, f.cfg.DownloadDir} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		assertClean(t, f, "download-client")
	})
}

func TestSetupStateBlocked_NonAdminNeverSeesIt(t *testing.T) {
	f := newBlockedFixture(t)
	f.cfg.LibraryDir = filepath.Join(f.lib, "gone")
	for _, role := range []string{"user", ""} {
		f.get(t, role)
		f.clock = f.clock.Add(blockedPersistFor + time.Second)
		s, body := f.get(t, role)
		if s.Blocked || s.BlockedArea != "" || s.BlockedReason != "" {
			t.Errorf("role %q saw blocked state: %s", role, body)
		}
	}
}
