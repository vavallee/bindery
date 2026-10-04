package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/textutil"
)

// Unmatched unit states. See migration 088 for what each one means.
const (
	UnmatchedStatePending  = "pending"
	UnmatchedStateAdopting = "adopting"
	UnmatchedStateAdopted  = "adopted"
	UnmatchedStateUndoing  = "undoing"
	UnmatchedStateIgnored  = "ignored"
)

// Unit kinds: a single file (an ebook, or loose audio at a library root), or
// a folder that stands for one book (an audiobook folder or a disc set).
const (
	UnmatchedKindFile   = "file"
	UnmatchedKindFolder = "folder"
)

const (
	// reconcileChunkSize bounds how long one scan tail holds SQLite's single
	// writer lock (P3). Imports and the queue get the lock between chunks.
	reconcileChunkSize = 500
	// unmatchedPurgeAge is how long an ignored or adopted row may go unseen by
	// a scan before it is purged.
	unmatchedPurgeAge = 30 * 24 * time.Hour
	// UnmatchedClaimTimeout is how old a claim must be before it counts as
	// abandoned by a request that died. No live request holds a row anywhere
	// near this long.
	UnmatchedClaimTimeout = 15 * time.Minute
)

// unitTimeLayout is fixed width, so two stored times compare correctly as
// text. RFC3339Nano trims trailing zeros, and "05Z" sorts after "05.1Z".
const unitTimeLayout = "2006-01-02T15:04:05.000000000Z"

func unitTime(t time.Time) string { return t.UTC().Format(unitTimeLayout) }

// UnmatchedCandidate is one catalogue book the scan thought this unit might
// be, with the title similarity that put it there.
type UnmatchedCandidate struct {
	BookID int64   `json:"bookId"`
	Score  float64 `json:"score"`
	// FolderAuthorOnly marks, on a unit whose files name another author than
	// their folder, a book offered only because the folder's author wrote it
	// (#2942). It never sets the unit's top score.
	FolderAuthorOnly bool `json:"folderAuthorOnly,omitempty"`
}

// UnmatchedUnitScan is what a library scan reports for one unit.
type UnmatchedUnitScan struct {
	UnitPath     string
	UnitKind     string
	Format       string
	FileCount    int
	SizeBytes    int64
	RootPath     string
	RelPath      string
	AuthorFolder string
	ParsedTitle  string
	ParsedAuthor string
	// FilesAuthor is the author the unit's files name when that is not its
	// author folder's (#2942); "" when they agree or say nothing.
	FilesAuthor string
	Reason      string
	MemberPaths []string
	Candidates  []UnmatchedCandidate
}

// RegisteredFile is one book_files row an adoption inserted: its path and the
// book it was registered to. Undo removes the row only while it still belongs
// to that book.
type RegisteredFile struct {
	Path   string `json:"path"`
	BookID int64  `json:"bookId"`
}

// UnmatchedUnit is one stored row.
type UnmatchedUnit struct {
	ID              int64
	UnitPath        string
	UnitKind        string
	Format          string
	FileCount       int
	SizeBytes       int64
	RootPath        string
	RelPath         string
	AuthorFolder    string
	ParsedTitle     string
	ParsedAuthor    string
	FilesAuthor     string
	Reason          string
	MemberPaths     []string
	Candidates      []UnmatchedCandidate
	TopScore        float64
	State           string
	BookID          int64
	CreatedBookID   int64
	CreatedAuthorID int64
	// CreatedBookFingerprint is BookFingerprint of the created book as the
	// adoption left it; empty when no book was created or the adoption did
	// not get that far.
	CreatedBookFingerprint string
	Registered             []RegisteredFile
	ScanGeneration         int64
	FirstSeenAt            time.Time
	LastSeenAt             time.Time
	ResolvedAt             *time.Time
	ClaimedAt              *time.Time
	// ClaimToken identifies the request holding an adopting or undoing row.
	// Every write that belongs to that request is scoped to it, so a request
	// whose claim was recovered and taken by another cannot write into it.
	ClaimToken string
}

// ReconcileScanOptions tunes one ReconcileScan call.
type ReconcileScanOptions struct {
	// StartedAt is when the scan read the tracked paths. An adopted row the
	// scan still found unmatched returns to pending only when it was adopted
	// before this, so an adopt that lands while a long walk is running is not
	// undone by that walk's stale view.
	StartedAt time.Time
	// SkipDeletion keeps every existing row: no unseen pending row is removed
	// and nothing is purged. The scanner sets it when the scan was truncated,
	// because a unit beyond the cap was not unseen, only unlisted. (A scan that
	// found no files at all, or failed, does not call ReconcileScan.)
	SkipDeletion bool
	// RootsWithFiles lists the scanned roots that produced at least one book
	// file. Ignored rows are purged only under these, so an unmounted
	// audiobook root does not forget its ignores while the library root scans.
	RootsWithFiles []string
	// ConfiguredRoots lists every root the scan was configured with, files or
	// not. An old ignored row under a root that is no longer configured at
	// all is purged too; no future scan would ever see its files again.
	ConfiguredRoots []string
	// Now is the clock; zero means time.Now.
	Now time.Time
}

// ReconcileScanResult reports what a reconcile did and the counts after it.
type ReconcileScanResult struct {
	Generation     int64
	Upserted       int
	RemovedPending int64
	Purged         int64
	Pending        int
	Ignored        int
}

// UnmatchedUnitRepo stores library adoption rows.
type UnmatchedUnitRepo struct {
	db *sql.DB
}

// NewUnmatchedUnitRepo returns a repo over database.
func NewUnmatchedUnitRepo(database *sql.DB) *UnmatchedUnitRepo {
	return &UnmatchedUnitRepo{db: database}
}

const upsertUnmatchedUnitSQL = `
INSERT INTO unmatched_units (
    unit_path, unit_kind, format, file_count, size_bytes, root_path, rel_path,
    author_folder, parsed_title, parsed_author, files_author, reason, search_key,
    member_paths_json, candidates_json, top_score, scan_generation,
    first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(unit_path) DO UPDATE SET
    unit_kind = excluded.unit_kind,
    format = excluded.format,
    file_count = excluded.file_count,
    size_bytes = excluded.size_bytes,
    root_path = excluded.root_path,
    rel_path = excluded.rel_path,
    author_folder = excluded.author_folder,
    parsed_title = excluded.parsed_title,
    parsed_author = excluded.parsed_author,
    files_author = excluded.files_author,
    reason = excluded.reason,
    search_key = excluded.search_key,
    member_paths_json = excluded.member_paths_json,
    candidates_json = excluded.candidates_json,
    top_score = excluded.top_score,
    scan_generation = excluded.scan_generation,
    last_seen_at = excluded.last_seen_at,
    updated_at = excluded.updated_at,
    -- An adoption whose files the scan no longer sees as tracked has been
    -- undone some other way (the book was deleted, a file row was removed),
    -- so the unit is a decision to make again. Ignored stays ignored, and a
    -- row adopted after this scan started is left alone.
    state = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                 THEN 'pending' ELSE unmatched_units.state END,
    book_id = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                   THEN NULL ELSE unmatched_units.book_id END,
    created_book_id = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                           THEN NULL ELSE unmatched_units.created_book_id END,
    created_author_id = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                             THEN NULL ELSE unmatched_units.created_author_id END,
    registered_paths_json = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                                 THEN '[]' ELSE unmatched_units.registered_paths_json END,
    created_book_fingerprint = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                                    THEN '' ELSE unmatched_units.created_book_fingerprint END,
    resolved_at = CASE WHEN unmatched_units.state = 'adopted' AND unmatched_units.resolved_at < ?
                       THEN NULL ELSE unmatched_units.resolved_at END`

// ReconcileScan records one scan's units. Upserts are keyed by unit_path and
// committed every reconcileChunkSize rows (P3). A decision already taken on a
// row is kept: the upsert never moves an ignored row, and moves an adopted row
// back to pending only as described in upsertUnmatchedUnitSQL, and never
// touches a row an adopt or undo holds (those are recovered with their side
// effects by the adoption handler, not here). After the upserts, one short
// transaction removes pending rows this scan did not see, purges adopted rows
// whose book is gone, and purges ignored rows unseen for 30 days under a root
// that produced files. The generation number keeps this correct if
// the process dies between chunks: the next scan's generation supersedes it.
func (r *UnmatchedUnitRepo) ReconcileScan(ctx context.Context, units []UnmatchedUnitScan, opts ReconcileScanOptions) (ReconcileScanResult, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	started := opts.StartedAt
	if started.IsZero() {
		started = now
	}
	var res ReconcileScanResult
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(scan_generation), 0) + 1 FROM unmatched_units`).Scan(&res.Generation); err != nil {
		return res, fmt.Errorf("unmatched units: next generation: %w", err)
	}
	nowText := unitTime(now)
	startedText := unitTime(started)

	for start := 0; start < len(units); start += reconcileChunkSize {
		end := min(start+reconcileChunkSize, len(units))
		if err := r.upsertChunk(ctx, units[start:end], res.Generation, nowText, startedText); err != nil {
			return res, err
		}
		res.Upserted += end - start
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("unmatched units: begin cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if !opts.SkipDeletion {
		out, err := tx.ExecContext(ctx,
			`DELETE FROM unmatched_units WHERE state = 'pending' AND scan_generation < ?`, res.Generation)
		if err != nil {
			return res, fmt.Errorf("unmatched units: remove unseen pending: %w", err)
		}
		res.RemovedPending, _ = out.RowsAffected()
		// An adopted row's files are tracked, so no scan sees it again; its age
		// says nothing. It stays while its book exists, which is what keeps
		// Undo available, and goes once the book is deleted.
		out, err = tx.ExecContext(ctx, `DELETE FROM unmatched_units WHERE state = 'adopted' AND book_id IS NULL`)
		if err != nil {
			return res, fmt.Errorf("unmatched units: purge orphaned adoptions: %w", err)
		}
		purged, _ := out.RowsAffected()
		res.Purged += purged
		if cond, args := ignoredPurgeScope(opts); cond != "" {
			//nolint:gosec // G202: cond is generated ? placeholders; every root is bound
			out, err = tx.ExecContext(ctx,
				`DELETE FROM unmatched_units WHERE state = 'ignored' AND last_seen_at < ? AND (`+cond+`)`,
				append([]any{unitTime(now.Add(-unmatchedPurgeAge))}, args...)...)
			if err != nil {
				return res, fmt.Errorf("unmatched units: purge old ignores: %w", err)
			}
			purged, _ = out.RowsAffected()
			res.Purged += purged
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("unmatched units: commit cleanup: %w", err)
	}

	summary, err := r.Summary(ctx)
	if err != nil {
		return res, err
	}
	res.Pending = summary.Pending
	res.Ignored = summary.Ignored
	return res, nil
}

func (r *UnmatchedUnitRepo) upsertChunk(ctx context.Context, chunk []UnmatchedUnitScan, generation int64, nowText, startedText string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("unmatched units: begin chunk: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, upsertUnmatchedUnitSQL)
	if err != nil {
		return fmt.Errorf("unmatched units: prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range chunk {
		u := &chunk[i]
		members, err := json.Marshal(nonNilStrings(u.MemberPaths))
		if err != nil {
			return fmt.Errorf("unmatched units: encode members: %w", err)
		}
		cands := u.Candidates
		if cands == nil {
			cands = []UnmatchedCandidate{}
		}
		candJSON, err := json.Marshal(cands)
		if err != nil {
			return fmt.Errorf("unmatched units: encode candidates: %w", err)
		}
		// A folder author's look alike on a conflicting unit does not rank
		// the unit as a close match (#2942).
		var top float64
		for _, c := range cands {
			if !c.FolderAuthorOnly {
				top = max(top, c.Score)
			}
		}
		fileCount := max(u.FileCount, 1)
		searchKey := textutil.FoldForSearch(u.ParsedTitle + " " + u.ParsedAuthor + " " + u.RelPath)
		if _, err := stmt.ExecContext(ctx,
			u.UnitPath, u.UnitKind, u.Format, fileCount, u.SizeBytes, u.RootPath, u.RelPath,
			u.AuthorFolder, u.ParsedTitle, u.ParsedAuthor, u.FilesAuthor, u.Reason, searchKey,
			string(members), string(candJSON), top, generation,
			nowText, nowText, nowText,
			startedText, startedText, startedText, startedText, startedText, startedText, startedText,
		); err != nil {
			return fmt.Errorf("unmatched units: upsert %q: %w", u.UnitPath, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("unmatched units: commit chunk: %w", err)
	}
	return nil
}

// ignoredPurgeScope builds the root condition for purging old ignored rows:
// under a root that produced files in this scan, or under a root that is no
// longer configured at all. A configured root that produced no files (an
// unmounted volume) is in neither and keeps its rows.
func ignoredPurgeScope(opts ReconcileScanOptions) (string, []any) {
	var parts []string
	var args []any
	placeholders := func(roots []string) string {
		for _, r := range roots {
			args = append(args, r)
		}
		return strings.TrimSuffix(strings.Repeat("?,", len(roots)), ",")
	}
	if len(opts.RootsWithFiles) > 0 {
		parts = append(parts, "root_path IN ("+placeholders(opts.RootsWithFiles)+")")
	}
	if len(opts.ConfiguredRoots) > 0 {
		parts = append(parts, "root_path NOT IN ("+placeholders(opts.ConfiguredRoots)+")")
	}
	return strings.Join(parts, " OR "), args
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// UnmatchedSummary is the headline counts, cheap enough for a nav badge.
type UnmatchedSummary struct {
	Pending      int `json:"pending"`
	PendingFiles int `json:"pendingFiles"`
	Ignored      int `json:"ignored"`
	Adopted      int `json:"adopted"`
}

// Summary counts rows by state. The adopting and undoing states count with
// the state they came from, since that is what the row still is to a viewer.
func (r *UnmatchedUnitRepo) Summary(ctx context.Context) (UnmatchedSummary, error) {
	var s UnmatchedSummary
	rows, err := r.db.QueryContext(ctx,
		`SELECT state, COUNT(*), COALESCE(SUM(file_count), 0) FROM unmatched_units GROUP BY state`)
	if err != nil {
		return s, fmt.Errorf("unmatched units: summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n, files int
		if err := rows.Scan(&state, &n, &files); err != nil {
			return s, fmt.Errorf("unmatched units: summary scan: %w", err)
		}
		switch state {
		case UnmatchedStatePending, UnmatchedStateAdopting:
			s.Pending += n
			s.PendingFiles += files
		case UnmatchedStateAdopted, UnmatchedStateUndoing:
			s.Adopted += n
		case UnmatchedStateIgnored:
			s.Ignored += n
		}
	}
	return s, rows.Err()
}

const unmatchedUnitColumns = `id, unit_path, unit_kind, format, file_count, size_bytes, root_path, rel_path,
    author_folder, parsed_title, parsed_author, files_author, reason, member_paths_json, candidates_json, top_score,
    state, book_id, created_book_id, created_author_id, registered_paths_json, created_book_fingerprint,
    scan_generation, first_seen_at, last_seen_at, resolved_at, claimed_at`

func scanUnmatchedUnit(scan func(...any) error) (*UnmatchedUnit, error) {
	var u UnmatchedUnit
	var members, cands, registered string
	var bookID, createdBook, createdAuthor sql.NullInt64
	var first, last, resolved, claimed sql.NullString
	if err := scan(&u.ID, &u.UnitPath, &u.UnitKind, &u.Format, &u.FileCount, &u.SizeBytes, &u.RootPath, &u.RelPath,
		&u.AuthorFolder, &u.ParsedTitle, &u.ParsedAuthor, &u.FilesAuthor, &u.Reason, &members, &cands, &u.TopScore,
		&u.State, &bookID, &createdBook, &createdAuthor, &registered, &u.CreatedBookFingerprint,
		&u.ScanGeneration, &first, &last, &resolved, &claimed); err != nil {
		return nil, err
	}
	u.BookID, u.CreatedBookID, u.CreatedAuthorID = bookID.Int64, createdBook.Int64, createdAuthor.Int64
	if err := json.Unmarshal([]byte(members), &u.MemberPaths); err != nil {
		return nil, fmt.Errorf("unmatched unit %d members: %w", u.ID, err)
	}
	if err := json.Unmarshal([]byte(cands), &u.Candidates); err != nil {
		return nil, fmt.Errorf("unmatched unit %d candidates: %w", u.ID, err)
	}
	if err := json.Unmarshal([]byte(registered), &u.Registered); err != nil {
		return nil, fmt.Errorf("unmatched unit %d registered paths: %w", u.ID, err)
	}
	u.FirstSeenAt = parseFlexibleTimeValue(first, "unmatched_units.first_seen_at")
	u.LastSeenAt = parseFlexibleTimeValue(last, "unmatched_units.last_seen_at")
	if t, err := parseFlexibleTime(resolved); err == nil {
		u.ResolvedAt = t
	}
	if claimed.Valid {
		u.ClaimToken = claimed.String
		stamp, _, _ := strings.Cut(claimed.String, "#")
		if t, err := parseFlexibleTime(sql.NullString{String: stamp, Valid: true}); err == nil {
			u.ClaimedAt = t
		}
	}
	return &u, nil
}

// Get returns one row, or nil when there is none.
func (r *UnmatchedUnitRepo) Get(ctx context.Context, id int64) (*UnmatchedUnit, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+unmatchedUnitColumns+` FROM unmatched_units WHERE id = ?`, id)
	u, err := scanUnmatchedUnit(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("unmatched units: get %d: %w", id, err)
	}
	return u, nil
}

// Claim moves a row into adopting or undoing only if it is still in from, and
// returns the claim token that scopes every later write of this request ("" if
// another request got there first). The token starts with the fixed width
// claim time, so it still orders as a time for StaleClaims.
func (r *UnmatchedUnitRepo) Claim(ctx context.Context, id int64, from, to string) (string, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("unmatched units: claim token: %w", err)
	}
	now := unitTime(time.Now())
	token := now + "#" + hex.EncodeToString(nonce)
	out, err := r.db.ExecContext(ctx,
		`UPDATE unmatched_units SET state = ?, claimed_at = ?, updated_at = ? WHERE id = ? AND state = ?`,
		to, token, now, id, from)
	if err != nil {
		return "", fmt.Errorf("unmatched units: claim %d %s to %s: %w", id, from, to, err)
	}
	if n, err := out.RowsAffected(); err != nil || n != 1 {
		return "", err
	}
	return token, nil
}

// ReleaseClaim moves a row this request holds (token) back to a settled state
// without touching its record.
func (r *UnmatchedUnitRepo) ReleaseClaim(ctx context.Context, id int64, from, to, token string) (bool, error) {
	out, err := r.db.ExecContext(ctx,
		`UPDATE unmatched_units SET state = ?, claimed_at = NULL, updated_at = ? WHERE id = ? AND state = ? AND claimed_at IS ?`,
		to, unitTime(time.Now()), id, from, token)
	if err != nil {
		return false, fmt.Errorf("unmatched units: release %d: %w", id, err)
	}
	n, err := out.RowsAffected()
	return n == 1, err
}

// ClaimState moves a row from one state to another only if it is still in
// the first. It is the compare and swap every adopt, undo and ignore goes
// through (S9): a single UPDATE is atomic in SQLite, so of two requests racing
// for one row exactly one sees a row affected.
func (r *UnmatchedUnitRepo) ClaimState(ctx context.Context, id int64, from, to string) (bool, error) {
	if to == UnmatchedStateAdopting || to == UnmatchedStateUndoing {
		token, err := r.Claim(ctx, id, from, to)
		return token != "", err
	}
	now := unitTime(time.Now())
	out, err := r.db.ExecContext(ctx,
		`UPDATE unmatched_units SET state = ?, claimed_at = NULL, updated_at = ? WHERE id = ? AND state = ?`,
		to, now, id, from)
	if err != nil {
		return false, fmt.Errorf("unmatched units: claim %d %s to %s: %w", id, from, to, err)
	}
	n, err := out.RowsAffected()
	return n == 1, err
}

// AdoptionRecord is what an adopt has written so far, and what undo or claim
// recovery reverses.
type AdoptionRecord struct {
	BookID                 int64
	CreatedBookID          int64
	CreatedAuthorID        int64
	CreatedBookFingerprint string
	Registered             []RegisteredFile
}

func (rec AdoptionRecord) registeredJSON() (string, error) {
	files := rec.Registered
	if files == nil {
		files = []RegisteredFile{}
	}
	b, err := json.Marshal(files)
	return string(b), err
}

// RecordAdoptionProgress writes what an adopt has done so far into the row it
// holds, before its next side effect, so a request that dies part way leaves
// the row saying exactly what to reverse. It reports false when the row is no
// longer held by an adopt.
func (r *UnmatchedUnitRepo) RecordAdoptionProgress(ctx context.Context, id int64, token string, rec AdoptionRecord) (bool, error) {
	return r.writeAdoption(ctx, id, token, rec, false)
}

// CompleteAdoption moves a claimed row to adopted with the full record. It
// reports false when the row is no longer held by the adopt.
func (r *UnmatchedUnitRepo) CompleteAdoption(ctx context.Context, id int64, token string, rec AdoptionRecord) (bool, error) {
	return r.writeAdoption(ctx, id, token, rec, true)
}

func (r *UnmatchedUnitRepo) writeAdoption(ctx context.Context, id int64, token string, rec AdoptionRecord, complete bool) (bool, error) {
	paths, err := rec.registeredJSON()
	if err != nil {
		return false, err
	}
	now := unitTime(time.Now())
	query := `UPDATE unmatched_units
		SET book_id = ?, created_book_id = ?, created_author_id = ?, registered_paths_json = ?,
		    created_book_fingerprint = ?, updated_at = ?
		WHERE id = ? AND state = 'adopting' AND claimed_at IS ?`
	args := []any{nullID(rec.BookID), nullID(rec.CreatedBookID), nullID(rec.CreatedAuthorID), paths, rec.CreatedBookFingerprint, now, id, token}
	if complete {
		query = `UPDATE unmatched_units
			SET state = 'adopted', book_id = ?, created_book_id = ?, created_author_id = ?, registered_paths_json = ?,
			    created_book_fingerprint = ?, updated_at = ?, resolved_at = ?, claimed_at = NULL
			WHERE id = ? AND state = 'adopting' AND claimed_at IS ?`
		args = []any{nullID(rec.BookID), nullID(rec.CreatedBookID), nullID(rec.CreatedAuthorID), paths, rec.CreatedBookFingerprint, now, now, id, token}
	}
	out, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("unmatched units: write adoption %d: %w", id, err)
	}
	n, err := out.RowsAffected()
	return n == 1, err
}

// ResetToPending returns a row the request holding token has in state from
// (undoing, or adopting) to pending and clears the adoption. Call it only once
// every side effect has been reversed; the record is what a retry needs.
func (r *UnmatchedUnitRepo) ResetToPending(ctx context.Context, id int64, from, token string) (bool, error) {
	out, err := r.db.ExecContext(ctx, `
		UPDATE unmatched_units
		SET state = 'pending', book_id = NULL, created_book_id = NULL, created_author_id = NULL,
		    registered_paths_json = '[]', created_book_fingerprint = '', resolved_at = NULL, claimed_at = NULL,
		    updated_at = ?
		WHERE id = ? AND state = ? AND claimed_at IS ?`, unitTime(time.Now()), id, from, token)
	if err != nil {
		return false, fmt.Errorf("unmatched units: reset %d: %w", id, err)
	}
	n, err := out.RowsAffected()
	return n == 1, err
}

// CompleteUndo returns an undoing row to pending.
func (r *UnmatchedUnitRepo) CompleteUndo(ctx context.Context, id int64, token string) (bool, error) {
	return r.ResetToPending(ctx, id, UnmatchedStateUndoing, token)
}

// StaleClaims lists rows an adopt or undo claimed before the cutoff: requests
// that died holding them.
func (r *UnmatchedUnitRepo) StaleClaims(ctx context.Context, before time.Time) ([]UnmatchedUnit, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+unmatchedUnitColumns+` FROM unmatched_units
		 WHERE state IN ('adopting', 'undoing') AND (claimed_at IS NULL OR claimed_at < ?)`, unitTime(before))
	if err != nil {
		return nil, fmt.Errorf("unmatched units: stale claims: %w", err)
	}
	defer rows.Close()
	var out []UnmatchedUnit
	for rows.Next() {
		u, err := scanUnmatchedUnit(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("unmatched units: stale claims scan: %w", err)
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// BookFingerprint summarises the parts of a book a person changes by using it:
// its row (updated_at moves on any edit, monitor toggle or refresh), its
// Calibre link, and the history, downloads, pending releases, blocklist
// entries, series links, editions and provider identifiers hanging off it.
// An empty string means the book does not exist.
//
// It errs towards "used". A status refresh, a cover write or a manual author
// refresh also move updated_at without anyone touching the book, and Undo then
// keeps a book it could have removed. That is the safe direction: an extra
// unmonitored book is visible and deletable, a removed one that was in use is
// not recoverable. Do not narrow it to chase those.
func (r *UnmatchedUnitRepo) BookFingerprint(ctx context.Context, bookID int64) (string, error) {
	var monitored bool
	var updated sql.NullString
	var calibreID sql.NullInt64
	var history, downloads, pending, blocklist, series, editions, identifiers int
	err := r.db.QueryRowContext(ctx, `
		SELECT b.monitored, b.updated_at, b.calibre_id,
		       (SELECT COUNT(*) FROM history WHERE book_id = b.id),
		       (SELECT COUNT(*) FROM downloads WHERE book_id = b.id),
		       (SELECT COUNT(*) FROM pending_releases WHERE book_id = b.id),
		       (SELECT COUNT(*) FROM blocklist WHERE book_id = b.id),
		       (SELECT COUNT(*) FROM series_books WHERE book_id = b.id),
		       (SELECT COUNT(*) FROM editions WHERE book_id = b.id),
		       (SELECT COUNT(*) FROM book_identifiers WHERE book_id = b.id)
		FROM books b WHERE b.id = ?`, bookID).Scan(&monitored, &updated, &calibreID,
		&history, &downloads, &pending, &blocklist, &series, &editions, &identifiers)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("unmatched units: book fingerprint %d: %w", bookID, err)
	}
	return fmt.Sprintf("m=%t u=%s c=%d h=%d d=%d p=%d b=%d s=%d e=%d i=%d",
		monitored, updated.String, calibreID.Int64, history, downloads, pending, blocklist, series, editions, identifiers), nil
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// IgnorePending marks pending rows ignored, selected by id or by author
// folder (exactly one of the two). Rows in any other state are left alone.
func (r *UnmatchedUnitRepo) IgnorePending(ctx context.Context, ids []int64, authorFolder string) (int64, error) {
	now := unitTime(time.Now())
	var out sql.Result
	var err error
	switch {
	case len(ids) > 0:
		ph := make([]string, len(ids))
		args := make([]any, 0, len(ids)+1)
		args = append(args, now)
		for i, id := range ids {
			ph[i] = "?"
			args = append(args, id)
		}
		//nolint:gosec // G202: the IN list is generated ? placeholders; every id is bound
		out, err = r.db.ExecContext(ctx,
			`UPDATE unmatched_units SET state = 'ignored', updated_at = ? WHERE state = 'pending' AND id IN (`+strings.Join(ph, ",")+`)`,
			args...)
	case authorFolder != "":
		out, err = r.db.ExecContext(ctx,
			`UPDATE unmatched_units SET state = 'ignored', updated_at = ? WHERE state = 'pending' AND author_folder = ?`,
			now, authorFolder)
	default:
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("unmatched units: ignore: %w", err)
	}
	return out.RowsAffected()
}

// BookReferencedElsewhere reports whether any row other than exceptID still
// points at bookID, as its adopted book or as a book it created.
func (r *UnmatchedUnitRepo) BookReferencedElsewhere(ctx context.Context, bookID, exceptID int64) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM unmatched_units WHERE id <> ? AND (book_id = ? OR created_book_id = ?) LIMIT 1`,
		exceptID, bookID, bookID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AuthorReferencedElsewhere is BookReferencedElsewhere for a created author.
func (r *UnmatchedUnitRepo) AuthorReferencedElsewhere(ctx context.Context, authorID, exceptID int64) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM unmatched_units WHERE id <> ? AND created_author_id = ? LIMIT 1`,
		exceptID, authorID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
