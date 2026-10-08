package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/models"
)

// fakeNZBGet answers history, loadlog and editqueue, with history and logs
// that a test can change between polls.
type fakeNZBGet struct {
	mu      sync.Mutex
	items   []nzbget.HistoryItem
	logs    map[int][]nzbget.LogEntry
	tools   []nzbget.Tool // nil answers sysinfo like NZBGet before 24
	sysinfo int
}

func (f *fakeNZBGet) setTools(tools []nzbget.Tool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools = tools
}

func (f *fakeNZBGet) sysinfoCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sysinfo
}

func (f *fakeNZBGet) set(items []nzbget.HistoryItem, logs map[int][]nzbget.LogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items, f.logs = items, logs
}

func (f *fakeNZBGet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Method {
	case "history":
		_ = json.NewEncoder(w).Encode(map[string]any{"result": f.items})
	case "loadlog":
		var id int
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params[0], &id)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": f.logs[id]})
	case "editqueue":
		_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
	case "sysinfo":
		f.sysinfo++
		if f.tools == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": 1, "message": "Invalid procedure"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"Tools": f.tools}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func logLines(lines ...string) []nzbget.LogEntry {
	out := make([]nzbget.LogEntry, len(lines))
	for i, l := range lines {
		out[i] = nzbget.LogEntry{ID: i + 1, Kind: "INFO", Text: l}
	}
	return out
}

// Log lines as NZBGet writes them (UnpackController.cpp, ScriptController.cpp).
var (
	unrarMissingLog = logLines(
		"Unpacking Broken Book",
		"Unrar: Could not start /usr/bin/unrar: No such file or directory",
		"Unrar failed",
		// A broken post processing script on the same job must not hide
		// or change the verdict.
		"Notify: Could not start /scripts/Notify.py: No such file or directory",
	)
	unrarCRCLog = logLines(
		"Unpacking Broken Book",
		"Unrar: Extracting  book.epub",
		"Unrar: book.epub - CRC failed",
		"Cancelling unrar due to errors",
		"Unrar error code: 3",
		"Unrar failed",
		"Could not start /scripts/Notify.py: Permission denied",
	)
)

func TestContentFailureBreaker(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	b := &contentFailureBreaker{now: func() time.Time { return now }}

	// One release failing again and again stays allowed.
	for i := 0; i < 3; i++ {
		if allow, _, _ := b.recordFailure(1, "guid-a", "FAILURE/UNPACK"); !allow {
			t.Fatalf("repeat failure %d of one release was refused", i)
		}
	}
	// The second distinct release is allowed, the third trips.
	if allow, _, _ := b.recordFailure(1, "guid-b", "FAILURE/UNPACK"); !allow {
		t.Fatal("second distinct release refused")
	}
	allow, tripped, distinct := b.recordFailure(1, "guid-c", "FAILURE/UNPACK")
	if allow || !tripped || distinct != 3 {
		t.Fatalf("third distinct release: allow=%v tripped=%v distinct=%d, want false true 3", allow, tripped, distinct)
	}
	// Per kind: an UNPACK trip leaves PAR alone, and per client.
	if allow, _, _ := b.recordFailure(1, "guid-d", "FAILURE/PAR"); !allow {
		t.Fatal("an UNPACK trip refused a PAR failure")
	}
	if allow, _, _ := b.recordFailure(2, "guid-e", "FAILURE/UNPACK"); !allow {
		t.Fatal("another client's failure was refused")
	}
	// Stays open past the window.
	now = now.Add(5 * time.Hour)
	if allow, tripped, _ := b.recordFailure(1, "guid-f", "FAILURE/UNPACK"); allow || tripped {
		t.Fatalf("open breaker: allow=%v tripped=%v, want false false", allow, tripped)
	}
	// Only the same stage closes it.
	if b.stageSucceeded(1, "FAILURE/PAR") {
		t.Fatal("a PAR success reported closing an open breaker")
	}
	if allow, _, _ := b.recordFailure(1, "guid-g", "FAILURE/UNPACK"); allow {
		t.Fatal("a PAR success closed the UNPACK breaker")
	}
	if !b.stageSucceeded(1, "FAILURE/UNPACK") {
		t.Fatal("an UNPACK success did not report the open breaker")
	}
	if allow, _, _ := b.recordFailure(1, "guid-h", "FAILURE/UNPACK"); !allow {
		t.Fatal("failure after the stage succeeded was refused")
	}
	// reset forgets a client.
	b.open(1, "FAILURE/SCAN", "test")
	b.reset(1)
	if len(b.openReasons(1)) != 0 {
		t.Fatal("reset left a breaker open")
	}

	// Failures further apart than the window do not add up.
	b2 := &contentFailureBreaker{now: func() time.Time { return now }}
	for i := 0; i < 5; i++ {
		if allow, _, _ := b2.recordFailure(1, fmt.Sprintf("guid-%d", i), "FAILURE/UNPACK"); !allow {
			t.Fatalf("failure %d, %s after the previous one, was refused", i, contentBreakerWindow)
		}
		now = now.Add(contentBreakerWindow + time.Minute)
	}
}

// pollFixture is a scanner, a fake NZBGet and a health store wired together.
type pollFixture struct {
	contentFailureFixture
	nzb    *fakeNZBGet
	health *downloader.HealthStore
	client *models.DownloadClient
	ctx    context.Context
}

func newPollFixture(t *testing.T) *pollFixture {
	t.Helper()
	fake := &fakeNZBGet{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	f := newContentFailureFixture(t)
	health := downloader.NewHealthStore()
	f.scanner.WithClientHealth(health)
	ctx := context.Background()
	return &pollFixture{contentFailureFixture: f, nzb: fake, health: health, client: nzbgetClient(t, ctx, f.clients, srv.URL), ctx: ctx}
}

// fail sends one failed job for a new release through a poll.
func (p *pollFixture) fail(t *testing.T, nzbID int, status string, log []nzbget.LogEntry) {
	t.Helper()
	p.addDownload(t, p.ctx, p.client, fmt.Sprintf("guid-%d", nzbID), fmt.Sprint(nzbID), nil)
	p.nzb.set([]nzbget.HistoryItem{{NZBID: nzbID, Status: status}}, map[int][]nzbget.LogEntry{nzbID: log})
	p.scanner.checkNZBGetDownloads(p.ctx, p.client)
}

// complete sends one completed job through a poll.
func (p *pollFixture) complete(t *testing.T, nzbID int, item nzbget.HistoryItem) {
	t.Helper()
	p.addDownload(t, p.ctx, p.client, fmt.Sprintf("guid-%d", nzbID), fmt.Sprint(nzbID), nil)
	item.NZBID = nzbID
	item.DestDir = t.TempDir()
	p.nzb.set([]nzbget.HistoryItem{item}, nil)
	p.scanner.checkNZBGetDownloads(p.ctx, p.client)
}

func (p *pollFixture) blocked(t *testing.T, nzbID int) bool {
	t.Helper()
	ok, err := p.blocklist.IsBlocked(p.ctx, fmt.Sprintf("guid-%d", nzbID))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func (p *pollFixture) advisory() string {
	if h := p.health.Get(p.client.ID); h != nil {
		return h.Message
	}
	return ""
}

// TestNZBGetUnpackFailure_HostFaultLogNotBlocklisted: the job log shows unrar
// could not be started, so the release is not blocklisted, the client's
// health says why, and later evidence free unpack failures are held back too.
func TestNZBGetUnpackFailure_HostFaultLogNotBlocklisted(t *testing.T) {
	p := newPollFixture(t)
	p.fail(t, 301, "FAILURE/UNPACK", unrarMissingLog)
	if p.blocked(t, 301) {
		t.Fatal("an unpack failure whose log shows unrar could not start was blocklisted")
	}
	if !strings.Contains(p.advisory(), "Could not start /usr/bin/unrar") {
		t.Fatalf("client health = %q, want the paused blocklisting advisory naming the log line", p.advisory())
	}
	p.fail(t, 302, "FAILURE/UNPACK", nil)
	if p.blocked(t, 302) {
		t.Fatal("an evidence free unpack failure was blocklisted after a host fault")
	}
	// A different step is not held back.
	p.fail(t, 303, "FAILURE/PAR", nil)
	p.fail(t, 304, "FAILURE/HEALTH", nil)
	if !p.blocked(t, 303) || !p.blocked(t, 304) {
		t.Fatal("an unpack host fault stopped a PAR or HEALTH failure from blocklisting")
	}
}

// TestNZBGetUnpackFailure_CRCLogBlocklisted: a CRC error in the log is the
// release, and blocklists even while the unpack breaker is open.
func TestNZBGetUnpackFailure_CRCLogBlocklisted(t *testing.T) {
	p := newPollFixture(t)
	p.fail(t, 311, "FAILURE/UNPACK", unrarCRCLog)
	if !p.blocked(t, 311) {
		t.Fatal("an unpack failure whose log shows a CRC error was not blocklisted")
	}
	p.fail(t, 312, "FAILURE/UNPACK", unrarMissingLog)
	p.fail(t, 313, "FAILURE/UNPACK", unrarCRCLog)
	if !p.blocked(t, 313) {
		t.Fatal("a CRC error was not blocklisted while the breaker was open")
	}
}

// TestNZBGetUnpackFailure_OnlyUnpackSuccessCloses: a plain epub completing
// (SUCCESS/HEALTH, UnpackStatus NONE) never ran unrar and does not close an
// unpack trip; a job that unpacked does.
func TestNZBGetUnpackFailure_OnlyUnpackSuccessCloses(t *testing.T) {
	p := newPollFixture(t)
	p.fail(t, 321, "FAILURE/UNPACK", unrarMissingLog)

	p.complete(t, 322, nzbget.HistoryItem{Status: "SUCCESS/HEALTH", UnpackStatus: "NONE", ParStatus: "NONE"})
	if p.advisory() == "" {
		t.Fatal("a completed epub that never ran unrar cleared the advisory")
	}
	p.fail(t, 323, "FAILURE/UNPACK", nil)
	if p.blocked(t, 323) {
		t.Fatal("a completed epub that never ran unrar reopened unpack blocklisting")
	}

	p.complete(t, 324, nzbget.HistoryItem{Status: "SUCCESS/UNPACK", UnpackStatus: "SUCCESS", ParStatus: "NONE"})
	if a := p.advisory(); a != "" {
		t.Fatalf("client health after a successful unpack = %q, want the advisory cleared", a)
	}
	p.fail(t, 325, "FAILURE/UNPACK", nil)
	if !p.blocked(t, 325) {
		t.Fatal("unpack blocklisting did not resume after a successful unpack")
	}
}

// TestNZBGetUnpackFailure_MissingUnRARHoldsBack: when NZBGet's sysinfo says
// UnRAR is missing, an evidence free unpack failure is not blocklisted, and a
// missing 7-Zip alone holds nothing back. sysinfo is asked once, lazily, and
// only for an evidence free unpack failure.
func TestNZBGetUnpackFailure_MissingUnRARHoldsBack(t *testing.T) {
	p := newPollFixture(t)
	p.nzb.setTools([]nzbget.Tool{{Name: "7-Zip", Path: "/usr/bin/7z"}, {Name: "UnRAR", Path: ""}})
	p.fail(t, 331, "FAILURE/UNPACK", nil)
	if p.blocked(t, 331) {
		t.Fatal("an unpack failure was blocklisted while NZBGet cannot find UnRAR")
	}
	if !strings.Contains(p.advisory(), "cannot find UnRAR") {
		t.Fatalf("client health = %q, want the missing UnRAR error", p.advisory())
	}
	p.fail(t, 332, "FAILURE/PAR", nil)
	p.fail(t, 333, "FAILURE/HEALTH", nil)
	p.fail(t, 334, "FAILURE/UNPACK", unrarCRCLog)
	if !p.blocked(t, 332) || !p.blocked(t, 333) || !p.blocked(t, 334) {
		t.Fatal("a missing UnRAR held back a PAR, HEALTH or CRC evidenced failure")
	}
	if n := p.nzb.sysinfoCalls(); n != 1 {
		t.Fatalf("sysinfo calls = %d, want 1 (lazy, cached, only for evidence free unpack failures)", n)
	}
}

func TestNZBGetUnpackFailure_Missing7ZipAloneStillBlocklists(t *testing.T) {
	p := newPollFixture(t)
	p.nzb.setTools([]nzbget.Tool{{Name: "7-Zip", Path: ""}, {Name: "UnRAR", Path: "/usr/bin/unrar"}})
	p.fail(t, 341, "FAILURE/UNPACK", nil)
	if !p.blocked(t, 341) {
		t.Fatal("a missing 7-Zip alone stopped an unpack failure from blocklisting")
	}
	if a := p.advisory(); a != "" {
		t.Fatalf("client health = %q, want nothing for a missing 7-Zip alone", a)
	}
}

// TestNZBGetUnpackFailure_CRCStormAllBlocklisted is the reporter's real case
// in #3024: a sweep's worth of genuinely broken RAR releases, each with CRC
// errors in its log. Evidence beats the count, so all six are blocklisted.
func TestNZBGetUnpackFailure_CRCStormAllBlocklisted(t *testing.T) {
	p := newPollFixture(t)
	var items []nzbget.HistoryItem
	logs := map[int][]nzbget.LogEntry{}
	for i := 351; i <= 356; i++ {
		p.addDownload(t, p.ctx, p.client, fmt.Sprintf("guid-%d", i), fmt.Sprint(i), nil)
		items = append(items, nzbget.HistoryItem{NZBID: i, Status: "FAILURE/UNPACK"})
		logs[i] = unrarCRCLog
	}
	p.nzb.set(items, logs)
	p.scanner.checkNZBGetDownloads(p.ctx, p.client)
	for i := 351; i <= 356; i++ {
		if !p.blocked(t, i) {
			t.Errorf("release %d with a CRC error in its log was not blocklisted", i)
		}
	}
	if a := p.advisory(); a != "" {
		t.Fatalf("client health = %q, want no paused blocklisting for broken releases", a)
	}
}
