package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// dbIntegrityTimeout bounds one drift scan. The scan is a full pass over every
// table with a foreign key, so it gets its own deadline rather than waiting on
// whatever the HTTP server allows.
const dbIntegrityTimeout = 60 * time.Second

// dbCleanupTimeout bounds one cleanup: a confirming scan, the backup, the
// batched repair and the verifying scan. It is far longer than a single scan
// because on a very large database each of those is a full pass. The repair is
// safe to stop at any batch boundary and to run again, so the limit protects
// the instance from a runaway job rather than the data.
const dbCleanupTimeout = 15 * time.Minute

// dbPreviewTTL is how long a preview stays valid for confirming. The operator
// reads the preview and clicks, so minutes; an hour old preview describes a
// database that may no longer look like that.
const dbPreviewTTL = 15 * time.Minute

// dbCleanupBackupLabel tags the backup the cleanup takes first.
const dbCleanupBackupLabel = "pre-fk-cleanup"

// DBIntegrityHandler serves the foreign key drift report (#3356, part 1 of
// #1973) and its cleanup (#3357, part 2).
//
// The scan runs on its own read only connection opened from the database file
// (db.ForeignKeyDriftReportAt), not on the shared pool. The pool has ONE
// connection, so a scan on it would stall every other request for the whole
// scan, including /health and the auth middleware's settings reads. That also
// defeated the single flight guard: a second POST sat in the auth middleware
// until the first scan finished and then ran a second full scan.
//
// The cleanup writes, so it cannot avoid the pool. It keeps the app responsive
// by working in small transactions and giving the connection back between
// them (db.RepairForeignKeyDrift), and by taking its backup and both scans on
// read only connections.
type DBIntegrityHandler struct {
	// scan, repair and backup are set by the constructor; tests swap them to
	// hold a step open, make it slow or make it fail.
	scan   func(ctx context.Context) ([]db.ForeignKeyDrift, error)
	repair func(ctx context.Context) (db.RepairReport, error)
	// fixable counts, per relationship, the rows a repair can resolve. Nil
	// means every row of a CASCADE or SET NULL relationship.
	fixable func(ctx context.Context, found []db.ForeignKeyDrift) ([]int, error)
	backup  func(ctx context.Context) (BackupSnapshot, error)
	timeout time.Duration
	// cleanupTimeout bounds one cleanup.
	cleanupTimeout time.Duration
	// running is taken with TryLock, never Lock, by the check, the preview and
	// the cleanup alike: a second request while any of them is in flight is
	// refused with 409 rather than queued behind it. Because none of them hold
	// the app's connection for their duration, the second request really does
	// reach this guard while the first is still running.
	running sync.Mutex
	now     func() time.Time
	hook    func()
	// repairHook runs after each committed repair batch (tests only).
	repairHook func()

	// tokenKey signs preview tokens. It is random per process, so a token does
	// not survive a restart, which is the right lifetime for a preview.
	tokenKey []byte
	tokenTTL time.Duration
}

// NewDBIntegrityHandler builds the handler. dbPath is the database file; with
// an empty, ":memory:" or "file:" path (tests and in-memory databases, which
// cannot be reopened from a path) the scans fall back to the shared connection,
// accepting that they then block the pool for their duration. backups provides
// the backup the cleanup takes first; with none, the cleanup refuses to run.
func NewDBIntegrityHandler(database *sql.DB, dbPath string, backups *BackupHandler) *DBIntegrityHandler {
	h := &DBIntegrityHandler{
		timeout:        dbIntegrityTimeout,
		cleanupTimeout: dbCleanupTimeout,
		now:            time.Now,
		tokenTTL:       dbPreviewTTL,
	}
	h.tokenKey = make([]byte, 32)
	if _, err := rand.Read(h.tokenKey); err != nil {
		// No entropy means no safe token. Leave the key empty so every
		// confirmation is refused rather than signed with a guessable key.
		h.tokenKey = nil
	}
	h.scan = func(ctx context.Context) ([]db.ForeignKeyDrift, error) {
		if db.IsReopenablePath(dbPath) {
			return db.ForeignKeyDriftReportAt(ctx, dbPath, h.hook)
		}
		return db.ForeignKeyDriftReport(ctx, database, h.hook)
	}
	h.fixable = func(ctx context.Context, found []db.ForeignKeyDrift) ([]int, error) {
		if db.IsReopenablePath(dbPath) {
			return db.ForeignKeyDriftFixableAt(ctx, dbPath, found)
		}
		if database == nil {
			return nil, nil // nothing to ask: every CASCADE or SET NULL row counts
		}
		return db.ForeignKeyDriftFixable(ctx, database, found)
	}
	h.repair = func(ctx context.Context) (db.RepairReport, error) {
		return db.RepairForeignKeyDrift(ctx, database, db.RepairOptions{
			Path: dbPath,
			AfterBatch: func(int) {
				if h.repairHook != nil {
					h.repairHook()
				}
			},
		})
	}
	h.backup = func(ctx context.Context) (BackupSnapshot, error) {
		if backups == nil {
			return BackupSnapshot{}, errors.New("no backup handler configured")
		}
		return backups.CreateSnapshot(ctx, dbCleanupBackupLabel)
	}
	return h
}

// SetScanHook registers a function run while the scan's statement is open.
// For tests that need to hold a scan in flight.
func (h *DBIntegrityHandler) SetScanHook(f func()) { h.hook = f }

// SetRepairHook registers a function run after each committed repair batch.
// For tests that need to hold a repair in flight or cancel it part way.
func (h *DBIntegrityHandler) SetRepairHook(f func()) { h.repairHook = f }

type dbIntegrityRelationship struct {
	ChildTable  string `json:"childTable"`
	ParentTable string `json:"parentTable"`
	// OnDelete is the declared ON DELETE action (CASCADE, SET NULL, ...):
	// what a cleanup would do to these rows. Empty when unknown.
	OnDelete   string `json:"onDelete"`
	Violations int    `json:"violations"`
}

type dbIntegrityResponse struct {
	Relationships []dbIntegrityRelationship `json:"relationships"`
	Total         int                       `json:"total"`
	CheckedAt     time.Time                 `json:"checkedAt"`
}

// Check handles POST /api/v1/system/db-integrity.
//
// It is a POST although it changes nothing: the scan is expensive, and a GET
// can be fired by link prefetchers, crawlers and browser prefetch. A POST also
// has to carry the CSRF header and token, so only the UI or a deliberate API
// call starts one.
func (h *DBIntegrityHandler) Check(w http.ResponseWriter, r *http.Request) {
	if !h.running.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a database check is already running"})
		return
	}
	defer h.running.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	found, err := h.scan(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil {
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "the database check took too long and was stopped; try again when Bindery is idle"})
			return
		}
		if r.Context().Err() != nil {
			// The client went away; nobody is left to read an answer.
			slog.Info("database drift check cancelled by the client")
			return
		}
		writeServerError(w, r, err)
		return
	}

	resp := dbIntegrityResponse{Relationships: make([]dbIntegrityRelationship, 0, len(found)), CheckedAt: h.now().UTC()}
	for _, d := range found {
		resp.Relationships = append(resp.Relationships, dbIntegrityRelationship{
			ChildTable: d.ChildTable, ParentTable: d.ParentTable, OnDelete: d.OnDelete, Violations: d.Violations,
		})
		resp.Total += d.Violations
	}
	writeJSON(w, http.StatusOK, resp)
}

// dbCleanupRelationship is one relationship in a preview: the report row plus
// what a cleanup would do with it.
type dbCleanupRelationship struct {
	dbIntegrityRelationship
	// Action is "delete" (remove the rows), "clear" (clear the reference, keep
	// the rows), "keep" (the schema gives no safe rule, reported and left
	// alone) or "skipped" (the schema has a rule but no row of this
	// relationship can be repaired automatically).
	Action string `json:"action"`
	// Fixable is how many of Violations a cleanup can resolve. Less than
	// Violations when some rows cannot be addressed (a WITHOUT ROWID table) or
	// do not pass the repair's own orphan test; those are skipped.
	Fixable int `json:"fixable"`
}

type dbCleanupPreviewResponse struct {
	Relationships []dbCleanupRelationship `json:"relationships"`
	Total         int                     `json:"total"`
	// Fixable counts the rows a cleanup would delete or clear; Unfixable the
	// rows it would leave alone.
	Fixable   int       `json:"fixable"`
	Unfixable int       `json:"unfixable"`
	CheckedAt time.Time `json:"checkedAt"`
	// PreviewToken binds a confirmation to exactly this preview. Absent when
	// there is nothing a cleanup could do.
	PreviewToken string     `json:"previewToken,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

// dbActionSkipped marks a relationship whose rows a cleanup cannot repair
// although the schema declares a rule for them.
const dbActionSkipped = "skipped"

// cleanupRelationships classifies a scan. counts, when given, holds per
// relationship how many rows are really repairable (db.ForeignKeyDriftFixable);
// without it every row of a relationship with a CASCADE or SET NULL rule counts
// as repairable.
func cleanupRelationships(found []db.ForeignKeyDrift, counts []int) ([]dbCleanupRelationship, int, int, int) {
	out := make([]dbCleanupRelationship, 0, len(found))
	var total, fixable, unfixable int
	for i, d := range found {
		act := db.DriftAction(d.OnDelete)
		fix := 0
		if act != db.DriftActionKeep {
			fix = d.Violations
			if counts != nil && i < len(counts) {
				fix = min(counts[i], d.Violations)
			}
			if fix == 0 {
				act = dbActionSkipped
			}
		}
		out = append(out, dbCleanupRelationship{
			dbIntegrityRelationship: dbIntegrityRelationship{
				ChildTable: d.ChildTable, ParentTable: d.ParentTable, OnDelete: d.OnDelete, Violations: d.Violations,
			},
			Action:  act,
			Fixable: fix,
		})
		total += d.Violations
		fixable += fix
		unfixable += d.Violations - fix
	}
	return out, total, fixable, unfixable
}

// driftDigest fingerprints a scan: every relationship with its action and its
// count. Two scans with the same digest would be cleaned up identically.
func driftDigest(found []db.ForeignKeyDrift) string {
	h := sha256.New()
	for _, d := range found {
		_, _ = fmt.Fprintf(h, "%s|%s|%s|%d\n", d.ChildTable, d.ParentTable, strings.ToUpper(d.OnDelete), d.Violations)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// signPreview returns "<expiry unix>.<digest>.<hmac>". The digest travels in
// the token so a mismatch can be told apart from a forgery: a bad signature is
// a bad request, a good signature over numbers that no longer match the
// database is a conflict that needs a fresh preview.
func (h *DBIntegrityHandler) signPreview(expires time.Time, digest string) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, h.tokenKey)
	mac.Write([]byte(exp + "." + digest))
	return exp + "." + digest + "." + hex.EncodeToString(mac.Sum(nil))
}

var (
	errPreviewTokenInvalid = errors.New("invalid preview token")
	errPreviewTokenExpired = errors.New("expired preview token")
)

// verifyPreview checks the signature and the expiry and returns the digest the
// token was issued for.
func (h *DBIntegrityHandler) verifyPreview(token string) (string, error) {
	if len(h.tokenKey) == 0 {
		return "", errPreviewTokenInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errPreviewTokenInvalid
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "", errPreviewTokenInvalid
	}
	mac := hmac.New(sha256.New, h.tokenKey)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return "", errPreviewTokenInvalid
	}
	if !h.now().Before(time.Unix(exp, 0)) {
		return "", errPreviewTokenExpired
	}
	return parts[1], nil
}

// scanFailed answers a failed scan or repair step and reports whether the
// caller should stop (always true). A client that went away gets no answer.
func scanFailed(w http.ResponseWriter, r *http.Request, ctx context.Context, err error, what string, extra map[string]string) {
	if r.Context().Err() != nil {
		slog.Info("database drift "+what+" cancelled by the client", "user_id", auth.UserIDFromContext(r.Context()))
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		body := map[string]string{"error": "the database " + what + " took too long and was stopped; try again when Bindery is idle"}
		for k, v := range extra {
			body[k] = v
		}
		writeJSON(w, http.StatusGatewayTimeout, body)
		return
	}
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	body := map[string]string{"error": "internal server error"}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, http.StatusInternalServerError, body)
}

// Preview handles POST /api/v1/system/db-integrity/cleanup/preview. It scans
// exactly as Check does and adds what a cleanup would do to each relationship,
// plus a token that a cleanup must present. It changes nothing.
func (h *DBIntegrityHandler) Preview(w http.ResponseWriter, r *http.Request) {
	if !h.running.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a database check is already running"})
		return
	}
	defer h.running.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	found, err := h.scan(ctx)
	if err != nil {
		scanFailed(w, r, ctx, err, "check", nil)
		return
	}

	var counts []int
	if h.fixable != nil {
		var err error
		if counts, err = h.fixable(ctx, found); err != nil {
			scanFailed(w, r, ctx, err, "check", nil)
			return
		}
	}
	rels, total, fixable, unfixable := cleanupRelationships(found, counts)
	now := h.now().UTC()
	resp := dbCleanupPreviewResponse{
		Relationships: rels, Total: total, Fixable: fixable, Unfixable: unfixable, CheckedAt: now,
	}
	if fixable > 0 && len(h.tokenKey) > 0 {
		exp := now.Add(h.tokenTTL)
		resp.ExpiresAt = &exp
		resp.PreviewToken = h.signPreview(exp, driftDigest(found))
	}
	writeJSON(w, http.StatusOK, resp)
}

type dbCleanupRequest struct {
	Confirm      bool   `json:"confirm"`
	PreviewToken string `json:"previewToken"`
}

type dbCleanupResponse struct {
	Deleted   int      `json:"deleted"`
	Cleared   int      `json:"cleared"`
	Skipped   int      `json:"skipped"`
	SkipNotes []string `json:"skipNotes"`
	// Remaining is what a fresh scan still finds after the repair: the rows no
	// safe action covers, plus anything the repair could not resolve. Verified
	// is false when that scan could not run, in which case Remaining is empty
	// and means nothing.
	Remaining      []dbCleanupRelationship `json:"remaining"`
	RemainingTotal int                     `json:"remainingTotal"`
	Verified       bool                    `json:"verified"`
	BackupPath     string                  `json:"backupPath"`
	BackupName     string                  `json:"backupName"`
	DurationMs     int64                   `json:"durationMs"`
	CompletedAt    time.Time               `json:"completedAt"`
}

// Cleanup handles POST /api/v1/system/db-integrity/cleanup.
//
// It deletes rows, so it is built to refuse by default. It needs, in order:
//
//  1. an explicit {"confirm": true} AND the previewToken from a preview, so a
//     stray or replayed POST does nothing;
//  2. the token to be genuine and fresh (400 otherwise, 409 once it expired);
//  3. no other check, preview or cleanup running (409 straight away);
//  4. a fresh scan that still matches what the operator was shown (409
//     otherwise: the data changed, preview again);
//  5. a backup of the database, taken first. No backup, no repair.
//
// Only then does it repair, using the same per row logic as `bindery
// db-repair`, and it ends with a verifying scan whose leftovers it returns.
func (h *DBIntegrityHandler) Cleanup(w http.ResponseWriter, r *http.Request) {
	uid := auth.UserIDFromContext(r.Context())

	var req dbCleanupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send {\"confirm\": true, \"previewToken\": ...} from a preview"})
		return
	}
	if !req.Confirm || req.PreviewToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the cleanup needs confirm set to true and the token from a preview"})
		return
	}
	previewDigest, err := h.verifyPreview(req.PreviewToken)
	switch {
	case errors.Is(err, errPreviewTokenExpired):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the preview has expired; preview again before cleaning up"})
		return
	case err != nil:
		slog.Warn("database drift cleanup refused: invalid preview token", "user_id", uid)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the preview token is not valid; preview again"})
		return
	}

	if !h.running.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a database check or cleanup is already running"})
		return
	}
	defer h.running.Unlock()

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), h.cleanupTimeout)
	defer cancel()

	// The server's WriteTimeout (120 s) bounds the whole response, and a
	// cleanup on a very large database outlasts it: measured at 194 s for 5
	// million orphan rows, after which the answer was silently dropped even
	// though the cleanup had finished. Move this response's deadline out to
	// cover the work (the same technique as the file download handler). A
	// writer that cannot move its deadline keeps the server's.
	_ = http.NewResponseController(w).SetWriteDeadline(start.Add(h.cleanupTimeout + h.timeout + 30*time.Second))

	// Confirm the data still looks like the preview BEFORE touching anything.
	before, err := h.scan(ctx)
	if err != nil {
		scanFailed(w, r, ctx, err, "cleanup", nil)
		return
	}
	if driftDigest(before) != previewDigest {
		slog.Warn("database drift cleanup refused: the data changed since the preview", "user_id", uid)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the database changed since the preview; preview again"})
		return
	}
	_, totalBefore, fixable, _ := cleanupRelationships(before, nil)
	if fixable == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "there is nothing a cleanup can fix"})
		return
	}

	// No backup, no repair.
	snap, err := h.backup(ctx)
	if err != nil {
		scanFailed(w, r, ctx, err, "cleanup", nil)
		slog.Error("database drift cleanup not started: the backup failed", "user_id", uid)
		return
	}
	slog.Info("audit: database drift cleanup started",
		"event", "db_drift_cleanup", "stage", "started", "user_id", uid, "role", auth.UserRoleFromContext(r.Context()),
		"violations", totalBefore, "fixable", fixable, "backup", snap.Name)

	report, err := h.repair(ctx)
	if err != nil {
		if r.Context().Err() != nil {
			// The client went away: a stop, not a failure.
			slog.Info("audit: database drift cleanup cancelled",
				"event", "db_drift_cleanup", "stage", "cancelled", "user_id", uid, "backup", snap.Name,
				"deleted", report.Deleted, "cleared", report.Nulled)
		} else {
			slog.Error("audit: database drift cleanup failed",
				"event", "db_drift_cleanup", "stage", "failed", "user_id", uid, "backup", snap.Name,
				"deleted", report.Deleted, "cleared", report.Nulled, "error", err)
		}
		scanFailed(w, r, ctx, err, "cleanup", map[string]string{"backupPath": snap.Path})
		return
	}

	resp := dbCleanupResponse{
		Deleted: report.Deleted, Cleared: report.Nulled, Skipped: report.Skipped,
		SkipNotes:  report.SkipNotes,
		Remaining:  []dbCleanupRelationship{},
		BackupPath: snap.Path, BackupName: snap.Name,
	}
	if resp.SkipNotes == nil {
		resp.SkipNotes = []string{}
	}
	// The verifying scan gets a fresh budget: the repair may have used most of
	// the cleanup's.
	vctx, vcancel := context.WithTimeout(r.Context(), h.timeout)
	defer vcancel()
	after, err := h.scan(vctx)
	if err == nil {
		resp.Remaining, resp.RemainingTotal, _, _ = cleanupRelationships(after, nil)
		// Anything still here after a repair was not repaired: whatever the
		// schema says about it, it was left as it is.
		for i := range resp.Remaining {
			if resp.Remaining[i].Action != db.DriftActionKeep {
				resp.Remaining[i].Action = dbActionSkipped
				resp.Remaining[i].Fixable = 0
			}
		}
		resp.Verified = true
	} else {
		slog.Warn("database drift cleanup could not run its verifying scan", "error", err)
	}
	resp.DurationMs = time.Since(start).Milliseconds()
	resp.CompletedAt = h.now().UTC()

	slog.Info("audit: database drift cleanup finished",
		"event", "db_drift_cleanup", "stage", "finished", "user_id", uid, "backup", snap.Name,
		"deleted", resp.Deleted, "cleared", resp.Cleared, "skipped", resp.Skipped,
		"remaining", resp.RemainingTotal, "verified", resp.Verified, "duration_ms", resp.DurationMs)
	writeJSON(w, http.StatusOK, resp)
}
