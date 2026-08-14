package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

// insertDBEntryFields is insertDBEntry plus structured attrs, which the export
// renders inline (the List JSON path carries them as a map instead).
func insertDBEntryFields(t *testing.T, repo *db.LogRepo, ts time.Time, level, component, msg string, fields map[string]string) {
	t.Helper()
	if err := repo.Insert(context.Background(), db.LogEntry{
		TS:        ts,
		Level:     level,
		Component: component,
		Message:   msg,
		Fields:    fields,
	}); err != nil {
		t.Fatalf("insert db log: %v", err)
	}
}

// exportBody runs Export against the given query string and returns the
// response recorder plus the body split into non-comment lines.
func exportBody(t *testing.T, h *LogHandler, query string) (*httptest.ResponseRecorder, []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Export(rec, httptest.NewRequest(http.MethodGet, "/system/logs/export"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var entries []string
	for _, line := range strings.Split(strings.TrimRight(rec.Body.String(), "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			entries = append(entries, line)
		}
	}
	return rec, entries
}

func TestLogExport_AttachmentHeaders(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	insertDBEntry(t, repo, time.Now().UTC(), "INFO", "importer", "hello")

	rec, _ := exportBody(t, h, "")

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q; want text/plain", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "bindery-logs-") || !strings.Contains(cd, ".txt") {
		t.Errorf("Content-Disposition = %q; want a timestamped bindery-logs-*.txt attachment", cd)
	}
	// The header block has to say what produced the file so a copy pasted
	// into an issue is self-describing.
	body := rec.Body.String()
	for _, want := range []string{"# Bindery log export", "# generated:", "# filters:", "# max entries:"} {
		if !strings.Contains(body, want) {
			t.Errorf("export header missing %q:\n%s", want, body)
		}
	}
}

func TestLogExport_LineFormat(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	ts := time.Date(2026, 8, 12, 14, 3, 22, 123000000, time.UTC)
	insertDBEntryFields(t, repo, ts, "ERROR", "importer", "import failed", map[string]string{
		"title": "two words",
		"book":  "42",
	})

	from := ts.Add(-time.Minute).Format(time.RFC3339)
	to := ts.Add(time.Minute).Format(time.RFC3339)
	_, entries := exportBody(t, h, "?from="+from+"&to="+to)

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %v", len(entries), entries)
	}
	want := `2026-08-12T14:03:22.123Z ERROR [importer] import failed book=42 title="two words"`
	if entries[0] != want {
		t.Errorf("line = %q\nwant   %q", entries[0], want)
	}
}

func TestLogExport_HonoursFilters(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	now := time.Now().UTC()
	insertDBEntry(t, repo, now.Add(-time.Minute), "INFO", "scheduler", "job started")
	insertDBEntry(t, repo, now, "ERROR", "downloader", "download failed")

	// Same params as the Logs tab sends: the export must reflect exactly the
	// filters that were applied on screen (#1903).
	_, entries := exportBody(t, h, "?component=downloader")
	if len(entries) != 1 {
		t.Fatalf("component filter: got %d entries, want 1: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], "download failed") {
		t.Errorf("component filter returned the wrong row: %q", entries[0])
	}

	_, entries = exportBody(t, h, "?level=error")
	if len(entries) != 1 || !strings.Contains(entries[0], "ERROR") {
		t.Fatalf("level filter: got %v, want a single ERROR row", entries)
	}

	_, entries = exportBody(t, h, "?q=started")
	if len(entries) != 1 || !strings.Contains(entries[0], "job started") {
		t.Fatalf("search filter: got %v, want the matching row", entries)
	}
}

// TestLogExport_OutsideDefaultWindow locks in that the export inherits List's
// last-hour default rather than dumping the whole retention window.
func TestLogExport_OutsideDefaultWindow(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	old := time.Now().UTC().Add(-25 * time.Hour)
	insertDBEntry(t, repo, old, "INFO", "scheduler", "yesterday")

	if _, entries := exportBody(t, h, ""); len(entries) != 0 {
		t.Fatalf("default window returned %v; want nothing older than an hour", entries)
	}
	from := old.Add(-time.Minute).Format(time.RFC3339)
	if _, entries := exportBody(t, h, "?from="+from); len(entries) != 1 {
		t.Fatalf("widened window returned %d entries, want 1", len(entries))
	}
}

// TestLogExport_RedactsSecrets — an export is meant to be attached to a public
// issue, so a logged URL carrying an indexer apikey must not ride along.
func TestLogExport_RedactsSecrets(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	now := time.Now().UTC()
	insertDBEntryFields(t, repo, now, "ERROR", "indexer",
		"fetch failed: https://idx.example/api?t=search&apikey=s3cr3tvalue",
		map[string]string{"url": "https://idx.example/dl?token=anothersecret"})

	rec, entries := exportBody(t, h, "")
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	body := rec.Body.String()
	if strings.Contains(body, "s3cr3tvalue") || strings.Contains(body, "anothersecret") {
		t.Fatalf("export leaked a secret:\n%s", body)
	}
	if strings.Count(entries[0], "REDACTED") != 2 {
		t.Errorf("expected both the message and the attr to be redacted: %q", entries[0])
	}
}

// TestLogExport_RedactsTheFilterHeader — the echoed filters are operator
// input, so they get the same treatment as log values: an apikey pasted into
// the search box must not end up in the header of an issue attachment, and a
// newline must not be able to forge log lines inside the comment block.
func TestLogExport_RedactsTheFilterHeader(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	insertDBEntry(t, repo, time.Now().UTC(), "INFO", "indexer", "nothing to see")

	rec, _ := exportBody(t, h, "?q="+url.QueryEscape("https://idx.example/api?apikey=s3cr3tvalue\nINFO forged line"))

	body := rec.Body.String()
	if strings.Contains(body, "s3cr3tvalue") {
		t.Errorf("search term leaked a secret into the header:\n%s", body)
	}
	if strings.Contains(body, "\nINFO forged line") {
		t.Errorf("newline in the search term broke the header block:\n%s", body)
	}
}

// TestLogExport_EscapesNewlines keeps the one-entry-per-line contract: a
// multi-line stack trace must not be able to forge extra records.
func TestLogExport_EscapesNewlines(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	insertDBEntry(t, repo, time.Now().UTC(), "ERROR", "importer", "panic:\ngoroutine 1\r\nstack")

	_, entries := exportBody(t, h, "")
	if len(entries) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], `panic:\ngoroutine 1\nstack`) {
		t.Errorf("newlines not escaped: %q", entries[0])
	}
}

// TestLogExport_CapIsAnnounced — truncation is allowed, silent truncation is
// not: the footer has to say the ceiling was hit and how to get the rest. A
// ceiling the CALLER asked for is reported as their own limit; only the
// server's logExportMaxRows is called a cap, because "reached the 2-entry
// export cap" reads like a system limit when the 2 came from the request.
func TestLogExport_CapIsAnnounced(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	now := time.Now().UTC()
	for i := range 5 {
		insertDBEntry(t, repo, now.Add(-time.Duration(i)*time.Second), "INFO", "scheduler", "entry")
	}

	rec, entries := exportBody(t, h, "?limit=2")
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want the 2 the cap allows", len(entries))
	}
	body := rec.Body.String()
	if !strings.Contains(body, "# 2 entries") {
		t.Errorf("missing entry-count footer:\n%s", body)
	}
	if !strings.Contains(body, "stopped at the requested limit of 2 entries") {
		t.Errorf("requested limit not announced in the footer:\n%s", body)
	}

	// Under the cap: count reported, no truncation notice.
	rec, entries = exportBody(t, h, "?limit=50")
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5", len(entries))
	}
	if strings.Contains(rec.Body.String(), "export cap") {
		t.Errorf("cap announced for a complete export:\n%s", rec.Body.String())
	}
}

// TestLogExport_PagesAcrossBatches drives the streaming loop past one page so a
// regression in the offset/limit arithmetic (dropped or duplicated rows) fails
// here rather than in a user's 50k-line attachment.
func TestLogExport_PagesAcrossBatches(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	now := time.Now().UTC()
	// One row more than a page and a half so the loop runs twice and the
	// second page is short.
	total := logExportPageSize + 500
	for i := range total {
		insertDBEntry(t, repo, now.Add(-time.Duration(i)*time.Millisecond), "INFO", "scheduler", "entry-"+strconv.Itoa(i))
	}

	_, entries := exportBody(t, h, "")
	if len(entries) != total {
		t.Fatalf("got %d entries, want %d", len(entries), total)
	}
	seen := make(map[string]bool, total)
	for _, line := range entries {
		msg := line[strings.LastIndex(line, " ")+1:]
		if seen[msg] {
			t.Fatalf("duplicate row across pages: %q", msg)
		}
		seen[msg] = true
	}

	// The cap clamps the final page rather than over-reading it.
	if _, capped := exportBody(t, h, "?limit=1100"); len(capped) != 1100 {
		t.Fatalf("capped export returned %d entries, want 1100", len(capped))
	}
}

// TestLogExport_RingFallback covers deployments without a persistent store —
// the endpoint still returns the buffer instead of an empty file.
func TestLogExport_RingFallback(t *testing.T) {
	h, ring := logHandlerFixture(t)
	seedRecord(ring, slog.LevelError, "ring failure", slog.String("book", "42"))

	rec, entries := exportBody(t, h, "")
	if len(entries) != 1 || !strings.Contains(entries[0], "ring failure") {
		t.Fatalf("got %v, want the ring entry", entries)
	}
	if !strings.Contains(rec.Body.String(), "in-memory ring buffer") {
		t.Errorf("ring exports should say where they came from:\n%s", rec.Body.String())
	}
	if !strings.Contains(entries[0], "book=42") {
		t.Errorf("ring attrs missing: %q", entries[0])
	}
}

// TestLogExport_ToOnlyRangeMatchesTheTable is the divergence the export exists
// to avoid. List defaults to the last hour only when NEITHER bound is given, so
// a to-only request shows an unbounded-below table; Export used to derive
// from=to-1h whenever from was empty and hand the user a file covering one hour
// of what they were looking at. The header disclosed it, but the point of the
// endpoint is that the file IS the screen.
func TestLogExport_ToOnlyRangeMatchesTheTable(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	now := time.Now().UTC()
	insertDBEntry(t, repo, now.Add(-3*time.Hour), "INFO", "scheduler", "old-entry")
	insertDBEntry(t, repo, now.Add(-10*time.Minute), "INFO", "scheduler", "recent-entry")

	rec, entries := exportBody(t, h, "?to="+url.QueryEscape(now.Format(time.RFC3339)))

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want both rows the to-only table showed:\n%s", len(entries), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "old-entry") {
		t.Errorf("the row before to-1h was dropped:\n%s", rec.Body.String())
	}
	// No lower bound was requested, so none may be invented in the header.
	if strings.Contains(rec.Body.String(), "from=") {
		t.Errorf("header advertises a from bound the caller never set:\n%s", rec.Body.String())
	}
}

// TestLogExport_RedactsEveryPartOfTheLine — the endpoint promises that what
// reaches the file is escaped and redacted, not just the message and the attr
// values. Level, component and attr KEYS are code-supplied today, so this is
// the contract rather than an attacker path: a component carrying a newline
// would forge log lines in a file headed for a public issue, and a key holding
// a space or '=' breaks the key=value parse for whoever greps it.
func TestLogExport_RedactsEveryPartOfTheLine(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	ts := time.Now().UTC()
	insertDBEntryFields(t, repo, ts, "ERROR", "importer\n2026-01-01T00:00:00.000Z INFO [fake] forged", "boom",
		map[string]string{"weird key": "value", "url": "https://x/api?apikey=SUPERSECRETVALUE1"})

	_, entries := exportBody(t, h, "")

	if len(entries) != 1 {
		t.Fatalf("got %d lines, want 1 — a component newline forged a record: %v", len(entries), entries)
	}
	if strings.Contains(entries[0], "SUPERSECRETVALUE1") {
		t.Errorf("attr value not redacted: %q", entries[0])
	}
	if !strings.Contains(entries[0], `"weird key"=`) {
		t.Errorf("attr key with a space was not quoted: %q", entries[0])
	}
}

// TestLogExport_KeysetSurvivesIdenticalTimestamps walks the export past a page
// boundary with every row sharing one timestamp — the shape that breaks an
// ORDER BY ts DESC with no tiebreaker. Rows must appear exactly once each.
func TestLogExport_KeysetSurvivesIdenticalTimestamps(t *testing.T) {
	h, _, repo := logHandlerWithDB(t)
	ts := time.Now().UTC().Truncate(time.Second)
	total := logExportPageSize + 200
	for i := range total {
		insertDBEntry(t, repo, ts, "INFO", "scheduler", "entry-"+strconv.Itoa(i))
	}

	_, entries := exportBody(t, h, "")

	if len(entries) != total {
		t.Fatalf("got %d entries, want %d", len(entries), total)
	}
	seen := make(map[string]bool, total)
	for _, line := range entries {
		msg := line[strings.LastIndex(line, " ")+1:]
		if seen[msg] {
			t.Fatalf("duplicate row across pages: %q", msg)
		}
		seen[msg] = true
	}
}

// TestLogExport_RingHeaderDeclaresWhatItIgnored — Ring.Snapshot filters on
// level and limit only. Echoing "component=downloader" over a dump of the whole
// buffer sends whoever reads the pasted file chasing the wrong process.
func TestLogExport_RingHeaderDeclaresWhatItIgnored(t *testing.T) {
	h, ring := logHandlerFixture(t)
	seedRecord(ring, slog.LevelError, "ring failure")

	rec, _ := exportBody(t, h, "?component=downloader&q=hardlink&from=2026-01-01T00:00:00Z")
	body := rec.Body.String()

	if strings.Contains(body, "component=downloader") || strings.Contains(body, "q=hardlink") {
		t.Errorf("ring export claims filters it never applied:\n%s", body)
	}
	if !strings.Contains(body, "NOT applied") || !strings.Contains(body, "component") {
		t.Errorf("ring export doesn't declare the filters it dropped:\n%s", body)
	}
	if !strings.Contains(body, "# order: oldest first") {
		t.Errorf("ring export doesn't declare its ordering:\n%s", body)
	}
}
