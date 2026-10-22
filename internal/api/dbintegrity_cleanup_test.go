package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

var cleanupNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// driftFixture is the scan result most cleanup tests start from: one relationship
// a cleanup removes, one it clears, one it leaves alone.
func driftFixture() []db.ForeignKeyDrift {
	return []db.ForeignKeyDrift{
		{ChildTable: "authors", ParentTable: "users", OnDelete: "NO ACTION", Violations: 1},
		{ChildTable: "book_files", ParentTable: "books", OnDelete: "CASCADE", Violations: 4},
		{ChildTable: "downloads", ParentTable: "books", OnDelete: "SET NULL", Violations: 2},
	}
}

// stubbed is a handler whose scan, backup and repair are counted stubs, with the
// call order recorded so "backup before repair" can be asserted.
type stubbed struct {
	h       *DBIntegrityHandler
	scans   atomic.Int32
	backups atomic.Int32
	repairs atomic.Int32
	order   []string
	drift   []db.ForeignKeyDrift
	after   []db.ForeignKeyDrift
}

func newStubbed() *stubbed {
	s := &stubbed{drift: driftFixture()}
	h := NewDBIntegrityHandler(nil, "", nil)
	h.now = func() time.Time { return cleanupNow }
	h.scan = func(context.Context) ([]db.ForeignKeyDrift, error) {
		// The first scan is the preview or the confirming one; once a repair
		// has run, scans report what is left.
		s.scans.Add(1)
		if s.repairs.Load() > 0 {
			return s.after, nil
		}
		return s.drift, nil
	}
	h.backup = func(context.Context) (BackupSnapshot, error) {
		s.backups.Add(1)
		s.order = append(s.order, "backup")
		return BackupSnapshot{Name: "bindery_20261019_120000_pre-fk-cleanup.db", Path: "/data/backups/bindery_20261019_120000_pre-fk-cleanup.db"}, nil
	}
	h.repair = func(context.Context) (db.RepairReport, error) {
		s.repairs.Add(1)
		s.order = append(s.order, "repair")
		return db.RepairReport{Deleted: 4, Nulled: 2, Skipped: 1, SkipNotes: []string{"authors -> users: ON DELETE NO ACTION, no safe automatic repair"}}, nil
	}
	s.after = []db.ForeignKeyDrift{driftFixture()[0]}
	s.h = h
	return s
}

func postJSON(h http.HandlerFunc, ctx context.Context, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)).WithContext(ctx))
	return rec
}

func (s *stubbed) preview(t *testing.T) dbCleanupPreviewResponse {
	t.Helper()
	rec := postJSON(s.h.Preview, context.Background(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body.String())
	}
	var p dbCleanupPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func confirmBody(token string) string {
	b, _ := json.Marshal(map[string]any{"confirm": true, "previewToken": token})
	return string(b)
}

func TestDBCleanupPreview(t *testing.T) {
	s := newStubbed()
	p := s.preview(t)
	if p.Total != 7 || p.Fixable != 6 || p.Unfixable != 1 {
		t.Errorf("totals = %d/%d/%d, want 7 total, 6 fixable, 1 unfixable", p.Total, p.Fixable, p.Unfixable)
	}
	want := map[string]string{"authors": db.DriftActionKeep, "book_files": db.DriftActionDelete, "downloads": db.DriftActionClear}
	for _, r := range p.Relationships {
		if r.Action != want[r.ChildTable] {
			t.Errorf("%s action = %q, want %q", r.ChildTable, r.Action, want[r.ChildTable])
		}
	}
	if p.PreviewToken == "" || p.ExpiresAt == nil || !p.ExpiresAt.After(cleanupNow) {
		t.Errorf("preview carries no usable token: %+v", p)
	}
	if s.repairs.Load() != 0 || s.backups.Load() != 0 {
		t.Error("a preview changed something")
	}
}

// With nothing the cleanup could do, the preview offers no token, so there is
// nothing to confirm.
func TestDBCleanupPreviewOffersNoTokenWhenNothingIsFixable(t *testing.T) {
	s := newStubbed()
	s.drift = s.drift[:1] // only the NO ACTION row
	if p := s.preview(t); p.PreviewToken != "" || p.Fixable != 0 {
		t.Errorf("preview = %+v, want no token", p)
	}
	s.drift = nil
	if p := s.preview(t); p.PreviewToken != "" || p.Total != 0 {
		t.Errorf("clean preview = %+v, want no token", p)
	}
}

// A stray POST does nothing: no body, confirm false, a token with no confirm,
// or confirm with no token or a made up token all stop before any work.
func TestDBCleanupRefusesWithoutConfirmation(t *testing.T) {
	s := newStubbed()
	p := s.preview(t)
	for name, body := range map[string]string{
		"empty body":         "",
		"not json":           "garbage",
		"empty object":       "{}",
		"confirm false":      `{"confirm": false, "previewToken": "` + p.PreviewToken + `"}`,
		"token, no confirm":  `{"previewToken": "` + p.PreviewToken + `"}`,
		"confirm, no token":  `{"confirm": true}`,
		"confirm as string":  `{"confirm": "true", "previewToken": "` + p.PreviewToken + `"}`,
		"forged token":       confirmBody("1.2.3"),
		"made up token":      confirmBody("not-a-token"),
		"tampered signature": confirmBody(p.PreviewToken[:len(p.PreviewToken)-4] + "0000"),
	} {
		rec := postJSON(s.h.Cleanup, context.Background(), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if s.repairs.Load() != 0 || s.backups.Load() != 0 {
		t.Errorf("a refused cleanup still ran: %d backups, %d repairs", s.backups.Load(), s.repairs.Load())
	}
}

// A token signed for a different digest cannot be passed off: changing the
// digest inside the token breaks the signature.
func TestDBCleanupTokenCannotBeRetargeted(t *testing.T) {
	s := newStubbed()
	p := s.preview(t)
	parts := strings.Split(p.PreviewToken, ".")
	parts[1] = strings.Repeat("0", len(parts[1]))
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(strings.Join(parts, ".")))
	if rec.Code != http.StatusBadRequest || s.repairs.Load() != 0 {
		t.Errorf("status = %d, repairs = %d; want 400 and no repair", rec.Code, s.repairs.Load())
	}
}

func TestDBCleanupRunsBackupThenRepairAndVerifies(t *testing.T) {
	s := newStubbed()
	p := s.preview(t)
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(s.order, ","); got != "backup,repair" {
		t.Fatalf("order = %q, want the backup first", got)
	}
	var resp dbCleanupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Deleted != 4 || resp.Cleared != 2 || resp.Skipped != 1 {
		t.Errorf("counts = %+v", resp)
	}
	if !strings.HasSuffix(resp.BackupPath, "pre-fk-cleanup.db") || resp.BackupName == "" {
		t.Errorf("backup not reported: %+v", resp)
	}
	// The post repair scan is what Remaining reports, not the repair's own tally.
	if !resp.Verified || resp.RemainingTotal != 1 || len(resp.Remaining) != 1 || resp.Remaining[0].ChildTable != "authors" || resp.Remaining[0].Action != db.DriftActionKeep {
		t.Errorf("remaining = %+v", resp)
	}
	if len(resp.SkipNotes) != 1 {
		t.Errorf("skip notes = %v", resp.SkipNotes)
	}
}

// A token is for one preview of one database state: once the numbers move, the
// confirmation is refused and nothing runs.
func TestDBCleanupStalePreviewIsRefused(t *testing.T) {
	s := newStubbed()
	p := s.preview(t)
	s.drift = append([]db.ForeignKeyDrift(nil), s.drift...)
	s.drift[1].Violations = 5 // one more orphan appeared since the preview
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if s.backups.Load() != 0 || s.repairs.Load() != 0 {
		t.Error("a stale preview still ran a backup or repair")
	}
}

func TestDBCleanupExpiredPreviewIsRefused(t *testing.T) {
	s := newStubbed()
	p := s.preview(t)
	s.h.now = func() time.Time { return cleanupNow.Add(dbPreviewTTL + time.Second) }
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	if rec.Code != http.StatusConflict || s.repairs.Load() != 0 {
		t.Errorf("status = %d, repairs = %d; want 409 and no repair", rec.Code, s.repairs.Load())
	}
}

// Fail closed: when the backup cannot be taken, nothing is repaired, and the
// body does not carry the underlying error.
func TestDBCleanupBackupFailureMeansNoRepair(t *testing.T) {
	s := newStubbed()
	s.h.backup = func(context.Context) (BackupSnapshot, error) {
		s.backups.Add(1)
		return BackupSnapshot{}, errors.New("write /secret/path/backups: disk full")
	}
	p := s.preview(t)
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if s.repairs.Load() != 0 {
		t.Fatal("the repair ran without a backup")
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "disk full") {
		t.Errorf("500 body leaks the error: %s", rec.Body.String())
	}
}

// With no backup mechanism wired at all, the cleanup still refuses.
func TestDBCleanupWithoutBackupHandlerRefuses(t *testing.T) {
	h := NewDBIntegrityHandler(nil, "", nil)
	var repairs atomic.Int32
	h.scan = func(context.Context) ([]db.ForeignKeyDrift, error) { return driftFixture(), nil }
	h.repair = func(context.Context) (db.RepairReport, error) { repairs.Add(1); return db.RepairReport{}, nil }
	rec := postJSON(h.Preview, context.Background(), "")
	var p dbCleanupPreviewResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	rec = postJSON(h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	if rec.Code != http.StatusInternalServerError || repairs.Load() != 0 {
		t.Errorf("status = %d, repairs = %d; want 500 and no repair", rec.Code, repairs.Load())
	}
}

func TestDBCleanupRepairFailureIsGenericButNamesTheBackup(t *testing.T) {
	s := newStubbed()
	s.h.repair = func(context.Context) (db.RepairReport, error) {
		return db.RepairReport{}, errors.New("constraint failed on table book_files rowid 12")
	}
	p := s.preview(t)
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "constraint") || strings.Contains(body, "rowid") {
		t.Errorf("500 body leaks the error: %s", body)
	}
	if !strings.Contains(body, "pre-fk-cleanup.db") {
		t.Errorf("the operator is not told where the backup is: %s", body)
	}
}

// Cancelling the request stops the work and gives the lock back.
func TestDBCleanupCancellationReleasesTheLock(t *testing.T) {
	s := newStubbed()
	inRepair := make(chan struct{})
	s.h.repair = func(ctx context.Context) (db.RepairReport, error) {
		s.repairs.Add(1)
		close(inRepair)
		<-ctx.Done()
		return db.RepairReport{}, ctx.Err()
	}
	p := s.preview(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postJSON(s.h.Cleanup, ctx, confirmBody(p.PreviewToken)) }()
	select {
	case <-inRepair:
	case <-time.After(5 * time.Second):
		t.Fatal("repair never started")
	}
	// While it runs, nothing else may start.
	if rec := postJSON(s.h.Check, context.Background(), ""); rec.Code != http.StatusConflict {
		t.Errorf("check during a cleanup = %d, want 409", rec.Code)
	}
	if rec := postJSON(s.h.Preview, context.Background(), ""); rec.Code != http.StatusConflict {
		t.Errorf("preview during a cleanup = %d, want 409", rec.Code)
	}
	if rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken)); rec.Code != http.StatusConflict {
		t.Errorf("second cleanup = %d, want 409", rec.Code)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not stop when its request was cancelled")
	}
	if rec := postJSON(s.h.Check, context.Background(), ""); rec.Code != http.StatusOK {
		t.Errorf("check after the cancel = %d, want 200: the lock was not released", rec.Code)
	}
}

// A cleanup that outlives its deadline is stopped and says so.
func TestDBCleanupDeadlineStopsTheRepair(t *testing.T) {
	s := newStubbed()
	s.h.cleanupTimeout = 50 * time.Millisecond
	s.h.repair = func(ctx context.Context) (db.RepairReport, error) {
		<-ctx.Done()
		return db.RepairReport{}, ctx.Err()
	}
	p := s.preview(t)
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", rec.Code)
	}
	if rec := postJSON(s.h.Check, context.Background(), ""); rec.Code != http.StatusOK {
		t.Errorf("lock not released after the deadline: %d", rec.Code)
	}
}

// A repeated confirmation of the same preview does nothing the second time: the
// database no longer matches it.
func TestDBCleanupReplayIsRefused(t *testing.T) {
	s := newStubbed()
	p := s.preview(t)
	if rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken)); rec.Code != http.StatusOK {
		t.Fatalf("first = %d", rec.Code)
	}
	if rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken)); rec.Code != http.StatusConflict {
		t.Errorf("replay = %d, want 409", rec.Code)
	}
	if s.repairs.Load() != 1 {
		t.Errorf("repairs = %d, want exactly 1", s.repairs.Load())
	}
}

// End to end on a real database file: the preview, the backup file (which holds
// the rows as they were), the repair, and the verifying scan.
func TestDBCleanupEndToEndOnARealDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bindery.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO book_files (book_id, format, path) VALUES (501, 'ebook', '/l/a.epub')`,
		`INSERT INTO book_files (book_id, format, path) VALUES (502, 'ebook', '/l/b.epub')`,
		`INSERT INTO downloads (guid, book_id, title, nzb_url) VALUES ('g1', 503, 'R', 'http://x')`,
		`INSERT INTO authors (foreign_id, name, sort_name, owner_user_id) VALUES ('OL1A', 'A', 'A', 999)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	h := NewDBIntegrityHandler(conn, path, NewBackupHandler(conn, path, dir))

	rec := postJSON(h.Preview, context.Background(), "")
	var p dbCleanupPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body.String())
	}
	if p.Fixable != 3 || p.Unfixable != 1 {
		t.Fatalf("preview = %+v", p)
	}

	rec = postJSON(h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("cleanup = %d %s", rec.Code, rec.Body.String())
	}
	var resp dbCleanupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Deleted != 2 || resp.Cleared != 1 || resp.Skipped != 1 {
		t.Errorf("result = %+v", resp)
	}
	if !resp.Verified || resp.RemainingTotal != 1 || resp.Remaining[0].ChildTable != "authors" {
		t.Errorf("remaining = %+v, want only the NO ACTION author left", resp)
	}

	// The backup exists, is in the data dir's backups folder, is mode 0600, and
	// still holds the rows the cleanup removed.
	st, err := os.Stat(resp.BackupPath)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if filepath.Dir(resp.BackupPath) != filepath.Join(dir, "backups") || st.Mode().Perm() != 0o600 {
		t.Errorf("backup at %s mode %v", resp.BackupPath, st.Mode())
	}
	bk, err := db.OpenForMaintenance(context.Background(), resp.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer bk.Close()
	var n int
	if err := bk.QueryRow(`SELECT COUNT(*) FROM book_files WHERE book_id IN (501, 502)`).Scan(&n); err != nil || n != 2 {
		t.Errorf("the backup holds %d of the 2 orphan rows (err %v): it was not taken before the repair", n, err)
	}
	// The live database lost them.
	if err := conn.QueryRow(`SELECT COUNT(*) FROM book_files WHERE book_id IN (501, 502)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("live database still has %d orphan book_files", n)
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM downloads WHERE guid = 'g1' AND book_id IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the download row was not kept with a cleared reference")
	}
}

// The cleanup body is size limited: a huge body is a 400, not a buffered read.
func TestDBCleanupBodyIsLimited(t *testing.T) {
	s := newStubbed()
	big := `{"confirm": true, "previewToken": "` + strings.Repeat("a", 1<<20) + `"}`
	rec := httptest.NewRecorder()
	s.h.Cleanup(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte(big))))
	if rec.Code != http.StatusBadRequest || s.repairs.Load() != 0 {
		t.Errorf("status = %d, repairs = %d", rec.Code, s.repairs.Load())
	}
}

// The server's WriteTimeout bounds the whole response. A cleanup that outlasts
// it used to finish and then lose its answer (a 5 million row database took
// 194 s against the 120 s limit). The handler moves its own deadline out.
func TestDBCleanupResponseSurvivesTheServerWriteTimeout(t *testing.T) {
	s := newStubbed()
	s.h.repair = func(context.Context) (db.RepairReport, error) {
		time.Sleep(700 * time.Millisecond)
		s.repairs.Add(1)
		return db.RepairReport{Deleted: 6}, nil
	}
	p := s.preview(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(s.h.Cleanup))
	srv.Config.WriteTimeout = 250 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(confirmBody(p.PreviewToken)))
	if err != nil {
		t.Fatalf("the response was lost to the server write timeout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// The preview does not promise more than the repair will do: rows it cannot
// address or confirm are counted as not fixable, and a relationship with none
// repairable is shown as skipped rather than "delete".
func TestDBCleanupPreviewDoesNotOverstate(t *testing.T) {
	s := newStubbed()
	counts := []int{0, 1, 0} // authors (keep), book_files 1 of 4, downloads 0 of 2
	s.h.fixable = func(context.Context, []db.ForeignKeyDrift) ([]int, error) { return counts, nil }
	p := s.preview(t)
	byTable := map[string]dbCleanupRelationship{}
	for _, r := range p.Relationships {
		byTable[r.ChildTable] = r
	}
	if r := byTable["book_files"]; r.Action != db.DriftActionDelete || r.Fixable != 1 || r.Violations != 4 {
		t.Errorf("book_files = %+v, want delete with 1 of 4 fixable", r)
	}
	if r := byTable["downloads"]; r.Action != dbActionSkipped || r.Fixable != 0 {
		t.Errorf("downloads = %+v, want skipped with none fixable", r)
	}
	if p.Fixable != 1 || p.Unfixable != 6 {
		t.Errorf("totals fixable %d unfixable %d, want 1 and 6", p.Fixable, p.Unfixable)
	}
	if p.PreviewToken == "" {
		t.Error("one fixable row is still worth a token")
	}

	counts = []int{0, 0, 0}
	if p := s.preview(t); p.PreviewToken != "" || p.Fixable != 0 {
		t.Errorf("nothing repairable but the preview offers a token: %+v", p)
	}
}

// After a repair, anything left was not repaired, whatever the schema says:
// the result shows it as skipped (or keep where the schema has no rule).
func TestDBCleanupRemainingIsHonestAboutSkippedRows(t *testing.T) {
	s := newStubbed()
	s.after = []db.ForeignKeyDrift{
		{ChildTable: "authors", ParentTable: "users", OnDelete: "NO ACTION", Violations: 1},
		{ChildTable: "book_files", ParentTable: "books", OnDelete: "CASCADE", Violations: 3},
	}
	p := s.preview(t)
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(p.PreviewToken))
	var resp dbCleanupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := map[string]string{}
	for _, r := range resp.Remaining {
		got[r.ChildTable] = r.Action
		// Nothing left over was repaired, so none of it is "fixable".
		if r.Fixable != 0 {
			t.Errorf("remaining %s fixable = %d, want 0", r.ChildTable, r.Fixable)
		}
	}
	if got["authors"] != db.DriftActionKeep || got["book_files"] != dbActionSkipped {
		t.Errorf("remaining actions = %v", got)
	}
}

// A cleanup that the database would refuse for every row (SET NULL on a NOT
// NULL column) is not offered: fixable 0, action skipped, no token, so no
// backup and no no-op repair.
func TestDBCleanupPreviewOfARefusedRelationshipOffersNoToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bindery.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, st := range []string{
		`CREATE TABLE zz_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE zz_nn (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES zz_parent(id) ON DELETE SET NULL)`,
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO zz_nn (id, pid) VALUES (1, 9), (2, 9), (3, 9)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := conn.Exec(st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	h := NewDBIntegrityHandler(conn, path, NewBackupHandler(conn, path, dir))
	rec := postJSON(h.Preview, context.Background(), "")
	var p dbCleanupPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body.String())
	}
	if p.Total != 3 || p.Fixable != 0 || p.Unfixable != 3 || p.PreviewToken != "" {
		t.Errorf("preview = %+v, want 3 rows, none fixable, no token", p)
	}
	if len(p.Relationships) != 1 || p.Relationships[0].Action != dbActionSkipped || p.Relationships[0].Fixable != 0 {
		t.Errorf("relationships = %+v, want one skipped relationship", p.Relationships)
	}
}

// A client that disconnects mid repair is a stop, logged at INFO as cancelled,
// not an ERROR "failed".
func TestDBCleanupCancelIsLoggedAsCancelledNotFailed(t *testing.T) {
	var buf syncBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)

	s := newStubbed()
	inRepair := make(chan struct{})
	s.h.repair = func(ctx context.Context) (db.RepairReport, error) {
		close(inRepair)
		<-ctx.Done()
		return db.RepairReport{}, ctx.Err()
	}
	p := s.preview(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { postJSON(s.h.Cleanup, ctx, confirmBody(p.PreviewToken)); close(done) }()
	<-inRepair
	cancel()
	<-done
	out := buf.String()
	if !strings.Contains(out, "database drift cleanup cancelled") || !strings.Contains(out, "level=INFO") {
		t.Errorf("no INFO cancelled line in the log:\n%s", out)
	}
	if strings.Contains(out, "level=ERROR") || strings.Contains(out, "cleanup failed") {
		t.Errorf("a client disconnect was logged as a failure:\n%s", out)
	}
}

// syncBuffer is a bytes.Buffer safe for a logger written from another goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Defense in depth: even a validly signed token for a state with nothing
// fixable (the preview never issues one) cannot start a backup and repair.
func TestDBCleanupWithNothingFixableDoesNothing(t *testing.T) {
	s := newStubbed()
	s.drift = s.drift[:1] // only the NO ACTION row
	token := s.h.signPreview(cleanupNow.Add(time.Minute), driftDigest(s.drift))
	rec := postJSON(s.h.Cleanup, context.Background(), confirmBody(token))
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if s.backups.Load() != 0 || s.repairs.Load() != 0 {
		t.Errorf("ran %d backups, %d repairs for a state with nothing to fix", s.backups.Load(), s.repairs.Load())
	}
}
