package importer

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/models"
)

// TestTryImportSABnzbd_RefusedHistoryDeleteWarnsButImportSucceeds is #2192 at
// the level the reporter actually hit it.
//
// SABnzbd answers a refused history delete with HTTP 200 and
// {"status": false, "error": "..."}. DeleteHistory decoded that body into a
// SimpleResponse and returned nil, so the cleanup closure tryImportSABnzbd
// passes to tryImportInternal always looked like it had worked: the job stayed
// in SAB's history and the "cleanup failed" warning, which has been wired up
// at all three call sites the whole time, never fired.
//
// Two assertions, and the second matters as much as the first:
//
//  1. The refusal reaches the log, carrying SAB's own reason.
//  2. The import still completes. Cleanup runs after the terminal status is
//     written, so a client that refuses to forget a job must not unwind or fail
//     the import.
func TestTryImportSABnzbd_RefusedHistoryDeleteWarnsButImportSucceeds(t *testing.T) {
	const reason = "Job not found"

	var gotMode, gotName, gotValue string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotMode, gotName, gotValue = q.Get("mode"), q.Get("name"), q.Get("value")
		w.Header().Set("Content-Type", "application/json")
		// SAB's refusal shape: 200 OK with the flag false.
		_, _ = w.Write([]byte(`{"status": false, "error": "` + reason + `"}`))
	}))
	defer srv.Close()

	libraryDir := t.TempDir()
	s, dl, dlRepo, bookRepo, ctx := dataLossFixture(t, libraryDir, "")

	downloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadDir, "book.epub"), bookSized("epub-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	host, port := serverHostPort(t, srv.URL)
	sab := sabnzbd.New(host, port, "apikey", "", false)

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s.tryImportSABnzbd(ctx, sab, dl, "nzo-abc123", downloadDir)

	// 1. The refusal surfaces.
	var cleanupLines []string
	for _, line := range strings.Split(logBuf.String(), "\n") {
		if strings.Contains(line, "cleanup failed") {
			cleanupLines = append(cleanupLines, line)
		}
	}
	joined := strings.Join(cleanupLines, "\n")
	if joined == "" {
		t.Fatalf("expected a \"cleanup failed\" warning when SAB refuses the history delete; full log:\n%s", logBuf.String())
	}
	if !strings.Contains(joined, reason) {
		t.Errorf("cleanup warning should carry SAB's reason %q, got:\n%s", reason, joined)
	}
	if !strings.Contains(joined, "sabnzbd") {
		t.Errorf("cleanup warning should name the client type, got:\n%s", joined)
	}

	// The cleanup did address the right history slot. A wrong request would
	// make a refusal unsurprising and the test vacuous.
	if gotMode != "history" || gotName != "delete" || gotValue != "nzo-abc123" {
		t.Errorf("unexpected cleanup call: mode=%s name=%s value=%s", gotMode, gotName, gotValue)
	}

	// 2. The import completed anyway.
	stored, err := dlRepo.GetByID(ctx, dl.ID)
	if err != nil {
		t.Fatalf("reload download: %v", err)
	}
	if stored.Status != models.StateImported {
		t.Errorf("a refused history delete must not unwind the import: download status = %q, want %q", stored.Status, models.StateImported)
	}
	book, err := bookRepo.GetByID(ctx, *dl.BookID)
	if err != nil {
		t.Fatalf("reload book: %v", err)
	}
	if book.Status != models.BookStatusImported {
		t.Errorf("book status = %q, want %q", book.Status, models.BookStatusImported)
	}
	entries, err := os.ReadDir(libraryDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected the imported file under libraryDir")
	}
}

// serverHostPort splits a httptest server URL into the host and port
// sabnzbd.New wants, since the client builds its own base URL.
func serverHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	trimmed := strings.TrimPrefix(raw, "http://")
	host, portStr, found := strings.Cut(trimmed, ":")
	if !found {
		t.Fatalf("no port in server url %q", raw)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse server port from %q: %v", raw, err)
	}
	return host, port
}
