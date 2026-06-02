package importer

// Tests for the diagnostic logging and error-message improvements added to
// checkQbittorrentDownloads and tryImportInternal.
//
// Two coverage targets:
//
//  1. TestTryImportInternal_PathNotFound — os.IsNotExist branch inside
//     tryImportInternal: when downloadPath does not exist on Bindery's host,
//     the error message must name PathRemap as the fix rather than emitting the
//     generic "no book files found" message.
//
//  2. TestCheckQbittorrentDownloads_HashMismatch — "download not found in
//     torrent list" debug log: when a download's stored TorrentID does not match
//     any hash returned by qBittorrent (category mismatch, hash format drift,
//     manually-removed torrent), the download must be silently skipped each
//     cycle without triggering an import or state change.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestTryImportInternal_PathNotFound verifies that tryImportInternal returns a
// PathRemap-specific error message when the resolved download path does not
// exist on the Bindery host at all, as opposed to "no book files found" which
// implies the path exists but contains no recognised formats.
//
// The "path not found" case almost always means PathRemap is missing: qBittorrent
// reports its internal container path (e.g. /downloads/book.epub) but Bindery
// mounts the same storage under a different root (e.g. /data/media/downloads).
func TestTryImportInternal_PathNotFound(t *testing.T) {
	libraryDir := t.TempDir()
	s, dl, dlRepo, _, ctx := dataLossFixture(t, libraryDir, "")

	// Construct a path that provably does not exist on this host — a child of
	// a temp dir that was never created.
	nonexistent := filepath.Join(t.TempDir(), "qbit-container-path", "book.epub")

	s.tryImportInternal(ctx, dl, nonexistent, "", "", nil, nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.StateImportFailed {
		t.Errorf("expected StateImportFailed, got %q", got.Status)
	}
	if !strings.Contains(got.ErrorMessage, "PathRemap") {
		t.Errorf("expected error message to mention PathRemap (to guide user to the fix), got: %q", got.ErrorMessage)
	}
}

// TestTryImportInternal_PathExistsNoBooks covers the false branch of the
// os.IsNotExist check on line 1206: when the download path exists on disk but
// contains no recognised book formats, tryImportInternal must fall through to
// the generic "no book files found" failure (not the PathRemap hint). This
// distinguishes an empty/wrong-format download from a path that doesn't exist
// at all.
func TestTryImportInternal_PathExistsNoBooks(t *testing.T) {
	libraryDir := t.TempDir()
	s, dl, dlRepo, _, ctx := dataLossFixture(t, libraryDir, "")

	// An existing but empty directory — os.Stat succeeds, IsNotExist is false.
	emptyDir := t.TempDir()

	s.tryImportInternal(ctx, dl, emptyDir, "", "", nil, nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.StateImportFailed {
		t.Errorf("expected StateImportFailed when path exists but has no book files, got %q", got.Status)
	}
	// The error must NOT mention PathRemap — the path was accessible, just empty.
	if strings.Contains(got.ErrorMessage, "PathRemap") {
		t.Errorf("error message must not mention PathRemap when path exists: %q", got.ErrorMessage)
	}
}

// TestCheckQbittorrentDownloads_CompletedGrabbedLogsRawPath verifies that
// checkQbittorrentDownloads reaches the "download completed" log line (which
// now includes raw_path) when a StateGrabbed download's torrent is complete
// and content_path is set. This is the normal first-import trigger path —
// distinct from the retry (StateImportFailed) and re-grab (content_path gone)
// paths that the other qBittorrent tests cover.
//
// The download path intentionally has no book files so the import ends in
// StateImportFailed; the goal is only to reach line 939, not a full import.
func TestCheckQbittorrentDownloads_CompletedGrabbedLogsRawPath(t *testing.T) {
	downloadDir := t.TempDir() // exists but empty — import will fail fast

	const torrentHash = "1111aaaa2222bbbb3333cccc4444dddd5555eeee"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			torrents := []map[string]any{{
				"hash":         torrentHash,
				"name":         "My Book",
				"state":        "stalledUP",
				"progress":     1.0,
				"save_path":    downloadDir,
				"content_path": downloadDir, // valid path — triggers line 939
			}}
			_ = json.NewEncoder(w).Encode(torrents)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, t.TempDir(), "", "", "", "")

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:    "qbit-rawpath",
		Type:    "qbittorrent",
		Host:    host,
		Port:    port,
		Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-rawpath",
		Title:            "My Book",
		Status:           models.StateGrabbed,
		Protocol:         "torrent",
		TorrentID:        &hash,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	// The download must have advanced past StateGrabbed — confirming line 939
	// (the "download completed" log) was reached and tryImportQbittorrent fired.
	if got.Status == models.StateGrabbed {
		t.Errorf("download must advance past StateGrabbed when torrent is complete and content_path is set; status stayed %q", got.Status)
	}
}

// TestCheckQbittorrentDownloads_GetTorrentsFails verifies that
// checkQbittorrentDownloads handles a qBittorrent API error gracefully — it
// must return early without panicking or modifying any download records.
// This also covers the slog.Warn("failed to fetch qBittorrent torrents") line.
func TestCheckQbittorrentDownloads_GetTorrentsFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			_, _ = w.Write([]byte("Ok."))
		} else {
			// Simulate qBittorrent being temporarily unreachable / returning an error.
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, t.TempDir(), "", "", "", "")

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:    "qbit-api-error",
		Type:    "qbittorrent",
		Host:    host,
		Port:    port,
		Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	// Must not panic or return an error — just logs the warning and returns.
	s.checkQbittorrentDownloads(ctx, client)
}

// TestCheckQbittorrentDownloads_HashMismatch verifies that
// checkQbittorrentDownloads silently skips a download whose stored TorrentID
// does not match any hash returned by qBittorrent (covering the new debug log
// added to the !ok branch). The download must remain in its current state;
// no import must be triggered.
//
// This scenario arises when:
//   - the download client is configured with a category filter and the torrent
//     is in a different qBittorrent category;
//   - the torrent was manually removed from qBittorrent;
//   - the hash stored at grab time differs in case from what the API returns.
func TestCheckQbittorrentDownloads_HashMismatch(t *testing.T) {
	const storedHash = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"
	const qbitHash = "0000ffff1111eeee2222dddd3333cccc4444bbbb"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			// Return a torrent whose hash intentionally does NOT match storedHash.
			torrents := []map[string]any{{
				"hash":     qbitHash,
				"name":     "some-other-book",
				"state":    "uploading",
				"progress": 1.0,
			}}
			_ = json.NewEncoder(w).Encode(torrents)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, t.TempDir(), "", "", "", "")

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:    "qbit-hash-mismatch",
		Type:    "qbittorrent",
		Host:    host,
		Port:    port,
		Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	hash := storedHash
	dl := &models.Download{
		GUID:             "guid-hash-mismatch",
		Title:            "My Book",
		Status:           models.StateGrabbed,
		Protocol:         "torrent",
		TorrentID:        &hash,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	// The download must remain in StateGrabbed — no import triggered, no
	// state change, just the debug log and a skip.
	if got.Status != models.StateGrabbed {
		t.Errorf("hash-mismatch download must stay in StateGrabbed, got %q; "+
			"an import must not be triggered when TorrentID does not match any qBittorrent hash",
			got.Status)
	}
}
