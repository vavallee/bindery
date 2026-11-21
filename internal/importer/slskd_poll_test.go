package importer

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/downloader/slskd/slskdtest"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

type slskdPollFixture struct {
	*contentFixture
	fake   *slskdtest.Fake
	client *models.DownloadClient
	root   string // the downloads folder as Bindery sees it
	now    time.Time
	hand   []string
}

func newSlskdPollFixture(t *testing.T) *slskdPollFixture {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	cf := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	fake, srv := slskdtest.New()
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	root := t.TempDir()
	client := &models.DownloadClient{
		Name: "slskd", Type: "slskd", Host: u.Hostname(), Port: port, APIKey: "test-key",
		Enabled: true, EnabledForBooks: true, EnabledForAudiobooks: true, PathRemap: "/app/downloads:" + root,
	}
	if err := db.NewDownloadClientRepo(cf.sqlDB).Create(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	f := &slskdPollFixture{contentFixture: cf, fake: fake, client: client, root: root, now: time.Now()}
	cf.scanner.slskdNow = func() time.Time { return f.now }
	return f
}

type slskdGrabOpts struct {
	guid  string
	files []slskd.BatchFile
}

// start grabs the way the API does: a row first, then the batch, with the
// batch id stored as the row's remote id.
func (f *slskdPollFixture) start(t *testing.T, o slskdGrabOpts) *models.Download {
	t.Helper()
	ctx := context.Background()
	if o.files == nil {
		o.files = []slskd.BatchFile{{Filename: `@@p\Mary Mary\Mary Mary.epub`, Size: 6000}}
	}
	addr, err := slskd.EncodeGrab(slskd.Grab{User: "peer1", Files: o.files})
	if err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{
		BookID: &f.book.ID, GUID: o.guid, Title: "Mary Mary [EPUB]", NZBURL: addr,
		Status: models.StateGrabbed, Protocol: "soulseek", MediaType: models.MediaTypeEbook,
		DownloadClientID: &f.client.ID,
	}
	if err := f.downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	res, err := downloader.SendDownload(ctx, f.client, addr, dl.Title, downloader.SendOptions{
		MediaType: models.MediaTypeEbook, DownloadID: dl.ID, GlobalRemap: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.downloads.SetNzoID(ctx, dl.ID, res.RemoteID); err != nil {
		t.Fatal(err)
	}
	if err := f.downloads.UpdateStatus(ctx, dl.ID, models.StateDownloading); err != nil {
		t.Fatal(err)
	}
	dl, _ = f.downloads.GetByID(ctx, dl.ID)
	return dl
}

func (f *slskdPollFixture) folder(dl *models.Download) string {
	return filepath.Join(f.root, "bindery", strconv.FormatInt(dl.ID, 10))
}

func (f *slskdPollFixture) writeBook(t *testing.T, dl *models.Download) {
	t.Helper()
	if err := os.MkdirAll(f.folder(dl), 0o755); err != nil {
		t.Fatal(err)
	}
	f.dir = f.folder(dl)
	f.writeEpubTitled(t, "Mary Mary.epub", "Mary, Mary", "James Patterson")
}

func (f *slskdPollFixture) poll() { f.scanner.checkSlskdDownloads(context.Background(), f.client) }

func (f *slskdPollFixture) get(t *testing.T, id int64) *models.Download {
	t.Helper()
	dl, err := f.downloads.GetByID(context.Background(), id)
	if err != nil || dl == nil {
		t.Fatalf("reload: %v", err)
	}
	return dl
}

func (f *slskdPollFixture) batchID(dl *models.Download) string { return *dl.SABnzbdNzoID }

// The whole path: queued, then moving, then finished, then imported by the
// normal pipeline from the download's own folder, with the transfers tidied.
func TestSlskdPoll_QueuedToImported(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:poll1"})
	id := f.batchID(dl)

	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateDownloading {
		t.Fatalf("queued: status %q", got.Status)
	}
	f.fake.SetAll(id, slskdtest.InProgress, 3000)
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateDownloading {
		t.Fatalf("in progress: status %q", got.Status)
	}

	f.writeBook(t, dl)
	f.fake.SetAll(id, slskdtest.Succeeded, 6000)
	f.dl = dl
	f.poll()

	got := f.get(t, dl.ID)
	if got.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	if f.bookFileCount(t) != 1 {
		t.Errorf("book files = %d", f.bookFileCount(t))
	}
	if n := len(f.fake.Batch(id).Transfers); n != 0 {
		t.Errorf("%d finished transfers left in slskd's list", n)
	}
	for _, k := range f.fake.Keys {
		if k != "test-key" {
			t.Errorf("X-API-Key = %q", k)
		}
	}
}

// Completed means ALL requested files succeeded. One errored file fails the
// download, takes the others back, deletes what landed and imports nothing.
func TestSlskdPoll_PartialFailureFailsTheWholeDownload(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:partial", files: []slskd.BatchFile{
		{Filename: `@@p\Dune\01.mp3`, Size: 100}, {Filename: `@@p\Dune\02.mp3`, Size: 100}, {Filename: `@@p\Dune\03.mp3`, Size: 100},
	}})
	id := f.batchID(dl)
	if err := os.MkdirAll(f.folder(dl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.folder(dl), "01.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.fake.SetFile(id, 0, slskdtest.Succeeded, 100)
	f.fake.SetFile(id, 1, slskdtest.Errored, 10)
	f.fake.SetFile(id, 2, slskdtest.InProgress, 50)

	f.poll()

	got := f.get(t, dl.ID)
	if got.Status != models.StateFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.ErrorMessage, "1 of the 3 files failed") || !strings.Contains(got.ErrorMessage, "nothing was imported") {
		t.Errorf("message = %q", got.ErrorMessage)
	}
	if n := len(f.fake.Batch(id).Transfers); n != 0 {
		t.Errorf("%d transfers left running in slskd", n)
	}
	if _, err := os.Stat(f.folder(dl)); !os.IsNotExist(err) {
		t.Errorf("the half download was left on disk: %v", err)
	}
	if f.bookFileCount(t) != 0 {
		t.Error("a half download was imported")
	}
	if evs, _ := f.history.ListByType(context.Background(), models.HistoryEventDownloadFailed); len(evs) != 1 {
		t.Errorf("downloadFailed events = %d", len(evs))
	}
}

func TestSlskdPoll_RefusedAndRejectedTransfersFail(t *testing.T) {
	for state, word := range map[string]string{
		slskdtest.Rejected:  "rejected by the peer",
		slskdtest.TimedOut:  "timed out",
		slskdtest.Cancelled: "cancelled",
	} {
		f := newSlskdPollFixture(t)
		dl := f.start(t, slskdGrabOpts{guid: "slskd:" + strings.ReplaceAll(state, ", ", "")})
		f.fake.SetAll(f.batchID(dl), state, 0)
		f.poll()
		got := f.get(t, dl.ID)
		if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, word) {
			t.Errorf("%s: status %q message %q", state, got.Status, got.ErrorMessage)
		}
	}
}

// Stall timeout. Mutation check: make failSlskd skip RemoveDownload and the
// cancel assertions fail; make slskdStalled return false and the status ones do.
func TestSlskdPoll_QueuedTooLongIsCancelledAndFailed(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:stallq"})
	id := f.batchID(dl)
	f.fake.SetPlace(id, 7)

	f.now = f.now.Add(SlskdQueueStall - time.Minute)
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateDownloading {
		t.Fatalf("just under the limit: status %q", got.Status)
	}
	if f.fake.Count("DELETE ") != 0 {
		t.Fatal("cancelled early")
	}

	f.now = f.now.Add(2 * time.Minute)
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "did not start sending within 30 minutes") {
		t.Fatalf("status %q message %q", got.Status, got.ErrorMessage)
	}
	if f.fake.Count("DELETE ") != 1 || len(f.fake.Batch(id).Transfers) != 0 {
		t.Errorf("the stalled transfer was not cancelled in slskd (DELETE calls %d)", f.fake.Count("DELETE "))
	}
}

func TestSlskdPoll_NoBytesForTenMinutesIsCancelledAndFailed(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:stallb"})
	id := f.batchID(dl)
	f.fake.SetAll(id, slskdtest.InProgress, 1000)
	f.poll()

	// Bytes keep arriving, so any amount of waiting is fine.
	for i := 1; i <= 4; i++ {
		f.now = f.now.Add(8 * time.Minute)
		f.fake.SetAll(id, slskdtest.InProgress, int64(1000+i*500))
		f.poll()
		if got := f.get(t, dl.ID); got.Status != models.StateDownloading {
			t.Fatalf("a moving transfer was failed after %d cycles: %q", i, got.Status)
		}
	}
	// Then it stops.
	f.now = f.now.Add(SlskdBytesStall - time.Minute)
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateDownloading {
		t.Fatalf("just under the limit: %q", got.Status)
	}
	f.now = f.now.Add(2 * time.Minute)
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "stopped moving for 10 minutes") {
		t.Fatalf("status %q message %q", got.Status, got.ErrorMessage)
	}
	if f.fake.Count("DELETE ") != 1 {
		t.Errorf("the stalled transfer was not cancelled (DELETE calls %d)", f.fake.Count("DELETE "))
	}
}

func TestSlskdPoll_LimitsAreConfigurable(t *testing.T) {
	f := newSlskdPollFixture(t)
	f.scanner.slskdQueueStall = 2 * time.Minute
	dl := f.start(t, slskdGrabOpts{guid: "slskd:cfg"})
	f.poll()
	f.now = f.now.Add(3 * time.Minute)
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateFailed {
		t.Errorf("status %q, want failed under a 2 minute limit", got.Status)
	}
}

func TestSlskdPoll_BatchGoneFromSlskdFails(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:gone"})
	delete(f.fake.Batches, strings.ToLower(f.batchID(dl)))
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "no longer has this download") {
		t.Errorf("status %q message %q", got.Status, got.ErrorMessage)
	}
}

// slskd reporting fewer files than were asked for is not a complete download.
func TestSlskdPoll_MissingFilesAreNotComplete(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:missing", files: []slskd.BatchFile{
		{Filename: `@@p\a.mp3`, Size: 10}, {Filename: `@@p\b.mp3`, Size: 10},
	}})
	id := f.batchID(dl)
	b := f.fake.Batch(id)
	b.Transfers = b.Transfers[:1]
	f.fake.SetAll(id, slskdtest.Succeeded, 10)
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "1 of the 2 files") {
		t.Errorf("status %q message %q", got.Status, got.ErrorMessage)
	}
}

// Finished in slskd but the folder is not visible here: recorded as an import
// failure that says why, retried while the files may still appear, and blocked
// once the skip streak runs out. Nothing is imported meanwhile.
func TestSlskdPoll_InvisibleFolderIsRecordedThenBlocked(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:invisible"})
	f.fake.SetAll(f.batchID(dl), slskdtest.Succeeded, 6000)
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateImportFailed || !strings.Contains(got.ErrorMessage, "PathRemap") {
		t.Fatalf("first cycle: status %q message %q", got.Status, got.ErrorMessage)
	}
	for i := 0; i < importSkipLimit+1; i++ {
		f.poll()
	}
	got = f.get(t, dl.ID)
	if got.Status != models.StateImportBlocked {
		t.Fatalf("status %q (%s), want blocked", got.Status, got.ErrorMessage)
	}
	if f.bookFileCount(t) != 0 {
		t.Error("something was imported from a folder that was never there")
	}
}

// The files turn up late: the next cycle imports them.
func TestSlskdPoll_LateFilesImportOnRetry(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:late"})
	f.fake.SetAll(f.batchID(dl), slskdtest.Succeeded, 6000)
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateImportFailed {
		t.Fatalf("status %q", got.Status)
	}
	f.writeBook(t, dl)
	f.dl = dl
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateImported {
		t.Fatalf("status %q (%s), want imported after the files appeared", got.Status, got.ErrorMessage)
	}
}

// A download folder swapped for a symlink is never followed.
func TestSlskdPoll_SymlinkedFolderIsRefused(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:symlink"})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.epub"), []byte(strings.Repeat("x", 6000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.root, "bindery"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, f.folder(dl)); err != nil {
		t.Skip("symlinks unavailable")
	}
	f.fake.SetAll(f.batchID(dl), slskdtest.Succeeded, 6000)
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateImportFailed || !strings.Contains(got.ErrorMessage, "symbolic link") {
		t.Fatalf("status %q message %q", got.Status, got.ErrorMessage)
	}
	if f.bookFileCount(t) != 0 {
		t.Error("a file behind a symlink was imported")
	}
}

// The block mode check runs inside the poller's import: a corrupt EPUB that
// slskd says finished is refused, blocklisted by GUID, and left for Match to book.
func TestSlskdPoll_CorruptEpubIsRefusedByBlockMode(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:corrupt"})
	if err := os.MkdirAll(f.folder(dl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.folder(dl), "Mary Mary.epub"), []byte("not a zip"+strings.Repeat(" ", 6000)), 0o644); err != nil {
		t.Fatal(err)
	}
	f.fake.SetAll(f.batchID(dl), slskdtest.Succeeded, 6000)
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateImportBlocked || !strings.Contains(got.ErrorMessage, "could not be opened") {
		t.Fatalf("status %q message %q", got.Status, got.ErrorMessage)
	}
	if ok, _ := f.blocklist.IsBlocked(context.Background(), "slskd:corrupt"); !ok {
		t.Error("not blocklisted by GUID")
	}
	if f.bookFileCount(t) != 0 {
		t.Error("a corrupt file was imported")
	}
}

// The poller never touches a row of another client or protocol, and makes no
// API call when it has nothing to look after.
func TestSlskdPoll_IgnoresOtherRowsAndStaysQuietWhenIdle(t *testing.T) {
	f := newSlskdPollFixture(t)
	f.poll()
	if len(f.fake.Requests) != 0 {
		t.Errorf("an idle poll made requests: %v", f.fake.Requests)
	}
	ctx := context.Background()
	other := &models.Download{GUID: "guid-torrent", Title: "t", Status: models.StateDownloading, Protocol: "torrent", DownloadClientID: &f.client.ID}
	if err := f.downloads.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	f.poll()
	if len(f.fake.Requests) != 0 {
		t.Errorf("a non Soulseek row triggered requests: %v", f.fake.Requests)
	}
	if got := f.get(t, other.ID); got.Status != models.StateDownloading {
		t.Errorf("another row moved to %q", got.Status)
	}
}

// slskd is not reachable: nothing changes state, nothing is failed.
func TestSlskdPoll_OutageChangesNothing(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:outage"})
	f.fake.Down = true
	f.now = f.now.Add(5 * time.Hour)
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateDownloading {
		t.Errorf("an outage moved the row to %q", got.Status)
	}
}

// The real CheckDownloads dispatch reaches the slskd poller and nothing else.
func TestCheckDownloads_DispatchMatrix_Slskd(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:matrix"})
	f.fake.SetAll(f.batchID(dl), slskdtest.Succeeded, 6000)
	f.writeBook(t, dl)
	f.dl = dl
	f.scanner.CheckDownloads(context.Background())
	if got := f.get(t, dl.ID); got.Status != models.StateImported {
		t.Fatalf("status %q (%s)", got.Status, got.ErrorMessage)
	}
	for _, r := range f.fake.Requests {
		if !strings.Contains(r, " /api/v0/") {
			t.Errorf("request %q is not an slskd endpoint", r)
		}
	}
}
