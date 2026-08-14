package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/models"
)

// TestQbitCompletion covers the completion classifier that decides whether an
// import may run against a qBittorrent torrent (#1884).
//
// The cases are grouped by which rule decides them, and every negative case
// carries at least one POSITIVE signal too — that is the whole point: the
// previous implementation was a bare OR of positive signals, so any one of
// them alone flipped a healthy in-flight torrent to "complete".
func TestQbitCompletion(t *testing.T) {
	tests := []struct {
		name         string
		torrent      qbittorrent.Torrent
		wantComplete bool
		wantFailed   bool
	}{
		// --- #1884: negative signals must beat progress ---
		{
			// The exact fixture from nathang21's report: 0.15 s after the grab,
			// qBittorrent reports progress 1.0 for a torrent it has not started
			// (libtorrent's is_seed() is trivially true before the piece picker
			// exists) while every byte is still outstanding.
			name: "just-added torrent reporting progress 1.0 with all bytes outstanding",
			torrent: qbittorrent.Torrent{
				State: "downloading", Progress: 1.0, Size: 734003200, AmountLeft: 734003200,
			},
		},
		{
			name:    "downloading state outranks a progress of 1.0",
			torrent: qbittorrent.Torrent{State: "downloading", Progress: 1.0},
		},
		{
			name:    "metaDL magnet with no metadata yet",
			torrent: qbittorrent.Torrent{State: "metaDL", Progress: 1.0},
		},
		{
			name:    "checkingResumeData before initialisation",
			torrent: qbittorrent.Torrent{State: "checkingResumeData", Progress: 1.0},
		},
		{
			name:    "allocating",
			torrent: qbittorrent.Torrent{State: "allocating", Progress: 1.0},
		},
		{
			// The tail of the temp/incomplete-directory flow: the payload is
			// complete but the final save path does not exist until the move
			// lands, so importing here walks a directory that is not there yet.
			name:    "moving out of the temp directory is not in place yet",
			torrent: qbittorrent.Torrent{State: "moving", Progress: 1.0, Size: 100, AmountLeft: 0},
		},
		{
			name:    "stalledDL with a stale progress of 1.0",
			torrent: qbittorrent.Torrent{State: "stalledDL", Progress: 1.0},
		},
		{
			name:    "queuedDL",
			torrent: qbittorrent.Torrent{State: "queuedDL", Progress: 1.0},
		},
		{
			// amount_left is the client's own byte counter and outranks
			// progress, which is a display value that can lead the counters.
			name:    "outstanding bytes outrank an upload-family state",
			torrent: qbittorrent.Torrent{State: "stalledUP", Progress: 1.0, Size: 100, AmountLeft: 40},
		},

		// --- #969: the positive signals that must keep working ---
		{
			name:         "stalledUP with everything present",
			torrent:      qbittorrent.Torrent{State: "stalledUP", Progress: 1.0, Size: 100, AmountLeft: 0},
			wantComplete: true,
		},
		{
			name:         "uploading",
			torrent:      qbittorrent.Torrent{State: "uploading", Progress: 1.0, Size: 100, AmountLeft: 0},
			wantComplete: true,
		},
		{
			// The substring checks miss this state; amount_left==0 with a known
			// size is what catches it.
			name:         "queuedUP recognised via amount_left",
			torrent:      qbittorrent.Torrent{State: "queuedUP", Size: 100, AmountLeft: 0},
			wantComplete: true,
		},
		{
			name:         "pausedUP",
			torrent:      qbittorrent.Torrent{State: "pausedUP", Progress: 1.0, Size: 100, AmountLeft: 0},
			wantComplete: true,
		},
		{
			// A re-check of a torrent whose data is already in place. Long-
			// standing behaviour, deliberately preserved.
			name:         "checkingUP",
			torrent:      qbittorrent.Torrent{State: "checkingUP", Progress: 1.0, Size: 100, AmountLeft: 0},
			wantComplete: true,
		},
		{
			// #769: a torrent whose payload was moved into the library by a
			// prior Bindery import reports missingFiles at 100%. It must stay
			// "complete" so the caller's already-in-library check can close the
			// download out instead of leaving it stuck.
			name:         "missingFiles stays complete so the already-in-library check runs",
			torrent:      qbittorrent.Torrent{State: "missingFiles", Progress: 1.0},
			wantComplete: true,
		},

		// --- failure ---
		{
			name:       "errored torrent",
			torrent:    qbittorrent.Torrent{State: "error", Progress: 1.0, Size: 100, AmountLeft: 0},
			wantFailed: true,
		},
		{
			name:       "errored torrent while downloading",
			torrent:    qbittorrent.Torrent{State: "errored", Progress: 0.3, Size: 100, AmountLeft: 70},
			wantFailed: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			complete, failed := qbitCompletion(tc.torrent)
			if complete != tc.wantComplete {
				t.Errorf("qbitCompletion(%+v) complete = %v, want %v", tc.torrent, complete, tc.wantComplete)
			}
			if failed != tc.wantFailed {
				t.Errorf("qbitCompletion(%+v) failed = %v, want %v", tc.torrent, failed, tc.wantFailed)
			}
		})
	}
}

// TestImportSourcePresent covers the guard that decides whether spending a
// retry attempt is honest (#1884).
func TestImportSourcePresent(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(dir, "gone.epub")
	missingDir := filepath.Join(dir, "no-such-release")
	liveSymlink := filepath.Join(dir, "linked.epub")
	if err := os.Symlink(present, liveSymlink); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		downloadPath  string
		explicitFiles []string
		want          bool
	}{
		{"explicit file present", missingDir, []string{present}, true},
		// #1955: qBittorrent enumerated a file the filesystem no longer has.
		{"explicit file absent", dir, []string{absent}, false},
		{"one of several explicit files present", dir, []string{absent, present}, true},
		// The explicit list is authoritative when non-empty: an existing
		// download path does not rescue a list of phantom files.
		{"absent explicit files are not rescued by an existing path", dir, []string{absent}, false},
		{"no list, path present", dir, nil, true},
		// #1884: the final save path qBittorrent has not created yet.
		{"no list, path absent", missingDir, nil, false},
		{"no list, empty path", "", nil, false},
		{"no list, whitespace path", "   ", nil, false},
		// The guard and filterImportableFiles must apply the same predicate. A
		// symlink with a live target passes os.Stat but is dropped by the
		// filter, so calling it "present" spends a retry attempt on a file list
		// the importer has already emptied — the full budget, one poll at a
		// time, on a download nothing can import.
		{"explicit symlink with a live target is not importable", dir, []string{liveSymlink}, false},
		{"a real file alongside a symlink still counts", dir, []string{liveSymlink, present}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := importSourcePresent(tc.downloadPath, tc.explicitFiles); got != tc.want {
				t.Errorf("importSourcePresent(%q, %v) = %v, want %v",
					tc.downloadPath, tc.explicitFiles, got, tc.want)
			}
		})
	}
}

// TestDiscoverBookFiles_DropsFilesNotOnDisk covers the #1955 phantom-file case:
// the download client's file list describes what the TORRENT contains, not what
// is on disk. A file that has been moved into the library or deleted with the
// book must not be reported as discovered, or the importer skips its
// already-in-library and path-missing checks and fails deep inside the mover.
func TestDiscoverBookFiles_DropsFilesNotOnDisk(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.m4b")
	if err := os.WriteFile(real, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	phantom := filepath.Join(dir, "Bob the Drag Queen - Harriet Tubman - Live in Concert.m4b")

	got := discoverBookFiles(dir, []string{phantom})
	if len(got) != 0 {
		t.Errorf("#1955 regression: a file the client lists but the filesystem does not have "+
			"must not be discovered; got %v", got)
	}

	got = discoverBookFiles(dir, []string{phantom, real})
	if len(got) != 1 || got[0] != real {
		t.Errorf("expected only the file that exists, got %v", got)
	}

	// Symlink rejection (the guard this filter grew out of) must survive.
	link := filepath.Join(dir, "link.epub")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if got := discoverBookFiles(dir, []string{link}); len(got) != 0 {
		t.Errorf("expected symlinked file to still be rejected, got %v", got)
	}
}

// qbitFixture serves a mutable single-torrent qBittorrent API. The caller
// swaps the torrent JSON and the /torrents/files response between polls to
// replay a timeline.
type qbitFixture struct {
	mu      sync.Mutex
	torrent map[string]any
	files   []map[string]any
}

func (f *qbitFixture) set(torrent map[string]any, files []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.torrent, f.files = torrent, files
}

func (f *qbitFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		torrent, files := f.torrent, f.files
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{torrent})
		case "/api/v2/torrents/files":
			if files == nil {
				files = []map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(files)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readinessFixture wires an in-memory scanner against a qbitFixture with one
// monitored book and one grabbed download.
type readinessFixture struct {
	scanner *Scanner
	qbit    *qbitFixture
	client  *models.DownloadClient
	dl      *models.Download
	repo    *db.DownloadRepo
	ctx     context.Context
}

func newReadinessFixture(t *testing.T, libraryDir, audiobookDir, mediaType, hash string) *readinessFixture {
	t.Helper()
	qf := &qbitFixture{}
	srv := qf.server(t)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, audiobookDir, "", "", "")

	author := &models.Author{ForeignID: "a-" + hash[:6], Name: "Ada Test", SortName: "Test, Ada"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "b-" + hash[:6], AuthorID: author.ID, Title: "A Readiness Book",
		Status: models.BookStatusWanted, MediaType: mediaType,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name: "qbit-readiness", Type: "qbittorrent", Host: host, Port: port, Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	h := hash
	dl := &models.Download{
		GUID:             "guid-" + hash[:6],
		Title:            "A Readiness Book",
		Status:           models.StateGrabbed,
		Protocol:         "torrent",
		TorrentID:        &h,
		BookID:           &book.ID,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return &readinessFixture{scanner: s, qbit: qf, client: client, dl: dl, repo: dlRepo, ctx: ctx}
}

func (f *readinessFixture) poll(t *testing.T, times int) *models.Download {
	t.Helper()
	for i := 0; i < times; i++ {
		f.scanner.checkQbittorrentDownloads(f.ctx, f.client)
	}
	got, err := f.repo.GetByGUID(f.ctx, f.dl.GUID)
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	return got
}

// TestCheckQbittorrentDownloads_TempPathDownloadIsNotImportedEarly replays
// nathang21's timeline (#1884).
//
// qBittorrent 5.2.3 with Session\TempPathEnabled=true. 0.15 s after the grab is
// sent, the torrent reports progress 1.0 — it has not started, so every byte is
// still outstanding — and content_path names the FINAL save path, which does
// not exist until qBittorrent finishes and moves the payload out of
// /data/torrents/incomplete. The old poller called that complete, imported
// against the empty final path, and spent the entire retry budget in under a
// minute before terminally blocking a perfectly healthy download.
//
// Bindery must not touch the download until the client says the payload is
// really there, and must then import it normally.
func TestCheckQbittorrentDownloads_TempPathDownloadIsNotImportedEarly(t *testing.T) {
	saveRoot := t.TempDir()   // /data/torrents/books
	libraryDir := t.TempDir() // the library
	const hash = "90a07d0abcdef1234567890abcdef1234567890a"

	f := newReadinessFixture(t, libraryDir, "", models.MediaTypeEbook, hash)

	// T+0.15s .. T+40s: still downloading into the temp directory.
	releasePath := filepath.Join(saveRoot, "A.Readiness.Book.EPUB-SEEDPOOL")
	f.qbit.set(map[string]any{
		"hash":         hash,
		"name":         "A.Readiness.Book.EPUB-SEEDPOOL",
		"state":        "downloading",
		"progress":     1.0,
		"size":         734003200,
		"amount_left":  734003200,
		"save_path":    saveRoot,
		"content_path": releasePath,
	}, nil)

	got := f.poll(t, 4)
	if got.Status != models.StateGrabbed {
		t.Errorf("#1884 regression: a torrent the client is still downloading must not be imported; "+
			"status moved from %q to %q", models.StateGrabbed, got.Status)
	}
	if got.ImportRetryCount != 0 {
		t.Errorf("#1884 regression: retry budget spent (%d attempts) while the download was still running",
			got.ImportRetryCount)
	}
	if got.ErrorMessage != "" {
		t.Errorf("#1884 regression: healthy in-flight download carries an error message: %q", got.ErrorMessage)
	}

	// The download finishes: qBittorrent moves the payload to the final save
	// path and starts seeding.
	if err := os.MkdirAll(releasePath, 0o755); err != nil {
		t.Fatal(err)
	}
	epub := filepath.Join(releasePath, "book.epub")
	if err := os.WriteFile(epub, []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.qbit.set(map[string]any{
		"hash":         hash,
		"name":         "A.Readiness.Book.EPUB-SEEDPOOL",
		"state":        "stalledUP",
		"progress":     1.0,
		"size":         734003200,
		"amount_left":  0,
		"save_path":    saveRoot,
		"content_path": releasePath,
	}, []map[string]any{{"name": "A.Readiness.Book.EPUB-SEEDPOOL/book.epub", "size": 4}})

	got = f.poll(t, 1)
	if got.Status != models.StateImported {
		t.Fatalf("expected the import to run once the payload landed, got status %q (error %q)",
			got.Status, got.ErrorMessage)
	}
}

// TestCheckQbittorrentDownloads_MissingPayloadDoesNotExhaustRetries replays
// flaevers' timeline (#1955) and pins the retry-accounting half of #1884.
//
// The user deleted the audiobook from the UI, so the .m4b is gone from disk;
// qBittorrent still holds the torrent at 100% and still enumerates its one
// file. The first import must fail with a message that describes what is
// actually wrong, and every subsequent poll must decline to spend a retry
// attempt on a condition retrying cannot influence — so the retry budget is
// still whole, and the download imports the moment the file comes back.
//
// Declining is not the same as ignoring: a streak that never ends is bounded by
// importSkipLimit, which is what
// TestCheckQbittorrentDownloads_MissingPayloadIsEventuallyBlocked pins. This
// test stays well inside that bound.
func TestCheckQbittorrentDownloads_MissingPayloadDoesNotExhaustRetries(t *testing.T) {
	saveRoot := t.TempDir()
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	const hash = "34ecf3a58bfd2fbb85debf2ec6e2f069613cf656"
	const fileName = "Bob the Drag Queen - Harriet Tubman - Live in Concert.m4b"

	f := newReadinessFixture(t, libraryDir, audiobookDir, models.MediaTypeAudiobook, hash)

	m4b := filepath.Join(saveRoot, fileName)
	torrent := map[string]any{
		"hash":         hash,
		"name":         fileName,
		"state":        "stalledUP",
		"progress":     1.0,
		"size":         120000000,
		"amount_left":  0,
		"save_path":    saveRoot,
		"content_path": m4b,
	}
	files := []map[string]any{{"name": fileName, "size": 120000000}}
	f.qbit.set(torrent, files)

	got := f.poll(t, 1)
	if got.Status != models.StateImportFailed {
		t.Fatalf("expected the first attempt to fail visibly, got status %q", got.Status)
	}
	if !strings.Contains(got.ErrorMessage, m4b) {
		t.Errorf("expected the failure to name the path it looked at, got %q", got.ErrorMessage)
	}
	for _, want := range []string{"still be finishing", "moved or deleted", "PathRemap"} {
		if !strings.Contains(got.ErrorMessage, want) {
			t.Errorf("#1884: the failure must offer every real cause, not assert one; %q missing from %q",
				want, got.ErrorMessage)
		}
	}
	if strings.Contains(got.ErrorMessage, "configure PathRemap on the download client so Bindery can resolve the path") {
		t.Errorf("#1884 regression: the message still asserts PathRemap is the cause: %q", got.ErrorMessage)
	}

	// Every later poll: the client still says complete and still lists the
	// file, but there is nothing on disk. Spending the budget here is what
	// terminally blocked both reporters' downloads.
	got = f.poll(t, importRetryLimit+3)
	if got.ImportRetryCount != 0 {
		t.Errorf("#1884 regression: %d retry attempts spent on a download with nothing on disk to import",
			got.ImportRetryCount)
	}
	if got.Status == models.StateImportBlocked {
		t.Fatalf("#1955 regression: download terminally blocked (%q) after only a handful of polls, "+
			"with the message %q — the files were simply not there", got.Status, got.ErrorMessage)
	}
	if got.Status != models.StateImportFailed {
		t.Fatalf("expected the download to stay retryable at %q, got %q", models.StateImportFailed, got.Status)
	}

	// The file comes back (re-downloaded, or the user restored it). The
	// skipped polls must not have cost anything: the next one imports.
	if err := os.WriteFile(m4b, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = f.poll(t, 1)
	if got.Status != models.StateImported {
		t.Fatalf("expected the import to run once the file was back, got %q (error %q)",
			got.Status, got.ErrorMessage)
	}
	if got.ImportRetryCount != 1 {
		t.Errorf("expected exactly the one real retry to be counted, got %d", got.ImportRetryCount)
	}
}

// newPhantomPayloadFixture builds flaevers' shape (#1955): qBittorrent holds a
// complete torrent and enumerates its single file, but nothing is on disk. It
// returns the fixture and the path the client claims the file is at.
func newPhantomPayloadFixture(t *testing.T) (*readinessFixture, string) {
	t.Helper()
	saveRoot := t.TempDir()
	const hash = "9f2c0f1b7a1c4de0a2c5f0b9d3e7a41c8b6d5e20"
	const fileName = "A.Readiness.Book.M4B-SEEDPOOL.m4b"

	f := newReadinessFixture(t, t.TempDir(), t.TempDir(), models.MediaTypeAudiobook, hash)
	m4b := filepath.Join(saveRoot, fileName)
	f.qbit.set(map[string]any{
		"hash":         hash,
		"name":         fileName,
		"state":        "stalledUP",
		"progress":     1.0,
		"size":         120000000,
		"amount_left":  0,
		"save_path":    saveRoot,
		"content_path": m4b,
	}, []map[string]any{{"name": fileName, "size": 120000000}})
	return f, m4b
}

// TestCheckQbittorrentDownloads_MissingPayloadIsEventuallyBlocked closes the
// trap the retry guard would otherwise create.
//
// A download whose files never appear satisfies neither arm of
// blockStaleImportFailures: no attempt is ever counted, so the retry budget is
// never exhausted, and the torrent is still in the client, so the source has
// not vanished. Before importSkipLimit, such a row stayed StateImportFailed
// forever — untouched by every automatic path, and refused by the Grab 409 with
// "wait for it to settle", which nothing would ever do. The only escape was
// deleting the queue row.
//
// It must instead reach a terminal, re-grabbable, legible state in bounded
// time, without ever having spent a retry attempt on a poll that could not have
// worked.
func TestCheckQbittorrentDownloads_MissingPayloadIsEventuallyBlocked(t *testing.T) {
	f, m4b := newPhantomPayloadFixture(t)

	got := f.poll(t, 1)
	if got.Status != models.StateImportFailed {
		t.Fatalf("expected the first attempt to fail visibly, got %q", got.Status)
	}

	// One poll short of the limit the download is still retryable: the guard's
	// real job (import it the moment the files turn up) is not cut short.
	got = f.poll(t, importSkipLimit-1)
	if got.Status != models.StateImportFailed {
		t.Fatalf("expected the download to still be retryable after %d skipped polls, got %q (%q)",
			importSkipLimit-1, got.Status, got.ErrorMessage)
	}

	got = f.poll(t, 1)
	if got.Status != models.StateImportBlocked {
		t.Fatalf("a download whose files never appear must reach a terminal state; after %d skipped "+
			"polls it is still %q — nothing else will ever move this row, and Grab refuses to re-grab it",
			importSkipLimit, got.Status)
	}
	if got.ImportRetryCount != 0 {
		t.Errorf("#1884 regression: %d retry attempts spent on polls with nothing on disk to import",
			got.ImportRetryCount)
	}
	for _, want := range []string{m4b, "Retry import", "grab the release again", "PathRemap"} {
		if !strings.Contains(got.ErrorMessage, want) {
			t.Errorf("the blocking message must be actionable; %q missing from %q", want, got.ErrorMessage)
		}
	}
	if strings.Contains(strings.ToLower(got.ErrorMessage), "wait") {
		t.Errorf("the blocking message must not tell the user to wait for something that will not "+
			"happen: %q", got.ErrorMessage)
	}
}

// TestCheckQbittorrentDownloads_ManualRetryResetsTheSkipStreak pins that Queue
// → Retry import really is a way out and not a no-op.
//
// The streak is scanner-side bookkeeping and the retry is a database write, so
// nothing connects them except the row snapshot recordImportSkip keeps. Without
// that check, a row re-armed one poll before the limit would be blocked again
// on the very next poll, and the button would appear to do nothing.
func TestCheckQbittorrentDownloads_ManualRetryResetsTheSkipStreak(t *testing.T) {
	f, _ := newPhantomPayloadFixture(t)

	f.poll(t, 1)
	got := f.poll(t, importSkipLimit-1)
	if got.Status != models.StateImportFailed {
		t.Fatalf("setup: expected %q one poll short of the limit, got %q", models.StateImportFailed, got.Status)
	}

	accepted, found, err := f.repo.ResetImportRetry(f.ctx, f.dl.ID)
	if err != nil || !accepted || !found {
		t.Fatalf("Retry import: accepted=%v found=%v err=%v", accepted, found, err)
	}

	if got = f.poll(t, importSkipLimit-1); got.Status == models.StateImportBlocked {
		t.Fatalf("Retry import must restart the scanner's patience: the download was blocked again "+
			"after %d polls (%q)", importSkipLimit-1, got.ErrorMessage)
	}
}
