package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

type AuthorRepo struct {
	db   *sql.DB
	exec dbExecutor
}

func NewAuthorRepo(db *sql.DB) *AuthorRepo {
	return &AuthorRepo{db: db, exec: db}
}

// WithTx returns a clone of this repo whose tx-aware methods route through tx
// instead of the bare *sql.DB. Used by calibre.Rollback to wrap a multi-repo
// operation in one atomic transaction. Methods that begin their own transaction
// (e.g. SetMonitoredSeriesIDs) stay on *sql.DB.
func (r *AuthorRepo) WithTx(tx *sql.Tx) *AuthorRepo {
	clone := *r
	clone.exec = tx
	return &clone
}

// ErrAuthorIdentifierConflict indicates an author identifier is already owned
// by a different author row.
var ErrAuthorIdentifierConflict = errors.New("author identifier already belongs to another author")

// AuthorIdentifierConflictError reports which existing author owns the
// requested identifier.
type AuthorIdentifierConflictError struct {
	ForeignID string
	AuthorID  int64
}

func (e *AuthorIdentifierConflictError) Error() string {
	if e == nil {
		return ErrAuthorIdentifierConflict.Error()
	}
	return fmt.Sprintf("author identifier %q already belongs to author %d", e.ForeignID, e.AuthorID)
}

func (e *AuthorIdentifierConflictError) Unwrap() error {
	return ErrAuthorIdentifierConflict
}

const authorSelectCols = `id, foreign_id, name, sort_name, description, image_url, disambiguation,
	       ratings_count, average_rating, monitored, quality_profile_id, metadata_profile_id, root_folder_id,
	       audiobook_root_folder_id, monitor_mode, monitor_latest_count, monitor_new_items, metadata_provider, last_metadata_refresh_at,
	       created_at, updated_at, COALESCE(owner_user_id, 0)`
const authorSelectColsA = `a.id, a.foreign_id, a.name, a.sort_name, a.description, a.image_url, a.disambiguation,
		       a.ratings_count, a.average_rating, a.monitored, a.quality_profile_id, a.metadata_profile_id, a.root_folder_id,
		       a.audiobook_root_folder_id, a.monitor_mode, a.monitor_latest_count, a.monitor_new_items, a.metadata_provider, a.last_metadata_refresh_at,
		       a.created_at, a.updated_at, COALESCE(a.owner_user_id, 0)`

func (r *AuthorRepo) List(ctx context.Context) ([]models.Author, error) {
	return r.ListByUser(ctx, 0)
}

const (
	// sort_key is the accent-folded ordering key (migration 058, #1347); the
	// sort_name tiebreaker keeps order deterministic when two folded keys collide.
	listAuthorsAll = "SELECT " + authorSelectCols + " FROM authors ORDER BY sort_key, sort_name COLLATE NOCASE"
	// Include rows with NULL owner_user_id — these are authors created before the
	// multi-user migration ran its backfill (migration 025) or imported without a
	// user context. Excluding them causes the list to silently drop visible authors.
	listAuthorsByUser = "SELECT " + authorSelectCols + " FROM authors WHERE owner_user_id = ? OR owner_user_id IS NULL ORDER BY sort_key, sort_name COLLATE NOCASE"
)

func (r *AuthorRepo) ListByUser(ctx context.Context, userID int64) ([]models.Author, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if userID == 0 {
		rows, err = r.db.QueryContext(ctx, listAuthorsAll)
	} else {
		rows, err = r.db.QueryContext(ctx, listAuthorsByUser, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("list authors: %w", err)
	}
	defer rows.Close()

	var authors []models.Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, err
		}
		authors = append(authors, a)
	}
	return authors, rows.Err()
}

// AuthorListFilter narrows ListPageFiltered. The zero value selects every
// visible author in sort_name order (identical to the old ListPage).
type AuthorListFilter struct {
	UserID int64 // 0 = unscoped; otherwise owner_user_id = UserID OR NULL
	// Search is a case-insensitive substring match on the author name. Empty
	// disables the filter.
	Search string
	// Monitored, when non-nil, restricts to authors with that monitored flag.
	Monitored *bool
	// Sort is one of "az" (sort_name asc, default), "za" (sort_name desc),
	// "recent" (created_at desc), or one of the column-header sorts added for
	// #1349: "books-asc"/"books-desc", "rating-asc"/"rating-desc",
	// "monitored-asc"/"monitored-desc". Unknown values fall back to "az".
	Sort string
}

// escapeLike escapes the LIKE metacharacters (%, _, and the \ escape itself)
// so a user-typed search term matches literally under `LIKE ? ESCAPE '\'`.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// authorSortOrder maps a whitelisted sort key to a fixed ORDER BY clause. The
// value never contains user input, so it is safe to interpolate.
//
// Ordering is on sort_key — the accent-folded, lowercased key (migration 058,
// authorSortKey) — so case AND diacritics fold uniformly. #1312 used COLLATE
// NOCASE, which only folds ASCII A-Z, leaving accented leading letters (Ö, Ł,
// Ø…) sorting after "Z" (#1347). sort_name is the tiebreaker for a stable order
// when two folded keys collide. The "recent" sort keys on created_at (a
// timestamp) and is unaffected.
// The column-header sorts (#1349) all carry `sort_key ASC` as a tiebreaker:
// book counts, ratings and the monitored flag are heavily non-unique, and
// without a stable secondary key SQLite is free to return ties in any order,
// which makes a paginated list drop and repeat rows between pages.
func authorSortOrder(sort string) string {
	switch sort {
	case "za":
		return "sort_key DESC, sort_name COLLATE NOCASE DESC"
	case "recent":
		return "created_at DESC, id DESC"
	case "books-asc":
		return "book_count ASC, sort_key ASC"
	case "books-desc":
		return "book_count DESC, sort_key ASC"
	case "rating-asc":
		return "average_rating ASC, sort_key ASC"
	case "rating-desc":
		return "average_rating DESC, sort_key ASC"
	case "monitored-asc":
		return "monitored ASC, sort_key ASC"
	case "monitored-desc":
		return "monitored DESC, sort_key ASC"
	default:
		return "sort_key ASC, sort_name COLLATE NOCASE ASC"
	}
}

// ListPage returns one page of the authors visible to userID, ordered by
// sort_name, alongside the total row count. Thin wrapper over ListPageFiltered
// with no search/monitored/sort filtering — kept for existing callers.
func (r *AuthorRepo) ListPage(ctx context.Context, userID int64, limit, offset int) ([]models.Author, int, error) {
	return r.ListPageFiltered(ctx, AuthorListFilter{UserID: userID}, limit, offset)
}

// ListPageFiltered returns one page of authors matching f, ordered per f.Sort,
// alongside the total row count that matches the same filter (the count ignores
// limit/offset so the UI can paginate). limit must be positive; offset is
// clamped at 0.
//
// The sort_key order is backed by idx_authors_sort_key (migration 058).
func (r *AuthorRepo) ListPageFiltered(ctx context.Context, f AuthorListFilter, limit, offset int) ([]models.Author, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var (
		conds []string
		args  []any
	)
	if f.UserID != 0 {
		// Match ListByUser: include NULL-owner rows so pre-multiuser-migration
		// authors stay visible to every user instead of silently disappearing.
		conds = append(conds, "(owner_user_id = ? OR owner_user_id IS NULL)")
		args = append(args, f.UserID)
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		// Match the canonical name OR any of the author's aliases (#1176), so
		// searching a pen name / AKA (e.g. "Cassandra Clare" stored as an alias
		// of Holly Black) still surfaces the author that owns it.
		like := "%" + escapeLike(s) + "%"
		conds = append(conds, "(name LIKE ? ESCAPE '\\' COLLATE NOCASE OR id IN (SELECT author_id FROM author_aliases WHERE name LIKE ? ESCAPE '\\' COLLATE NOCASE))")
		args = append(args, like, like)
	}
	if f.Monitored != nil {
		conds = append(conds, "monitored = ?")
		args = append(args, boolToInt(*f.Monitored))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM authors"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count authors: %w", err)
	}

	// book_count is a correlated subquery rather than a stored column so it
	// cannot drift from the books table. It serves two purposes: the Authors
	// list renders it as the Books column (which showed "—" for every author
	// before this, because nothing ever populated Author.Statistics on a row
	// read back from the database), and "books-asc"/"books-desc" order by it.
	//
	// Scoped to the same rows the caller may see. An unscoped COUNT would let a
	// user infer how many books another user's author has — a small leak, but
	// the same class as the one #1872 closed, and free to avoid here. NULL-owned
	// books stay visible to everyone, matching listAuthorsByUser.
	bookCountExpr := "(SELECT COUNT(*) FROM books b WHERE b.author_id = authors.id) AS book_count"
	var selectArgs []any
	if f.UserID != 0 {
		bookCountExpr = "(SELECT COUNT(*) FROM books b WHERE b.author_id = authors.id" +
			" AND (b.owner_user_id = ? OR b.owner_user_id IS NULL)) AS book_count"
		selectArgs = append(selectArgs, f.UserID)
	}

	//nolint:gosec // query is built only from static columns, a parameterised WHERE, and a whitelisted ORDER BY (authorSortOrder); all user values are bound via args
	listQuery := "SELECT " + authorSelectCols + ", " + bookCountExpr + " FROM authors" + where +
		" ORDER BY " + authorSortOrder(f.Sort) + " LIMIT ? OFFSET ?"
	// Order matters: the subquery's placeholder sits in the SELECT list, ahead
	// of every WHERE placeholder.
	pageArgs := append(append(append([]any{}, selectArgs...), args...), limit, offset)

	rows, err := r.db.QueryContext(ctx, listQuery, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list authors page: %w", err)
	}
	defer rows.Close()

	var authors []models.Author
	for rows.Next() {
		var bookCount int
		a, err := scanAuthorFrom(trailingScanner{s: rows, extra: []any{&bookCount}})
		if err != nil {
			return nil, 0, err
		}
		a.Statistics = &models.AuthorStats{BookCount: bookCount}
		authors = append(authors, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return authors, total, nil
}

func (r *AuthorRepo) GetByID(ctx context.Context, id int64) (*models.Author, error) {
	row := r.exec.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE id = ?`, id)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author %d: %w", id, err)
	}
	return &a, nil
}

// GetByIDForUser returns the author with id when it is visible to userID.
// Unowned rows remain visible so pre-multiuser and import-created authors keep
// their legacy behavior. userID 0 performs an unscoped lookup.
func (r *AuthorRepo) GetByIDForUser(ctx context.Context, id, userID int64) (*models.Author, error) {
	if userID == 0 {
		return r.GetByID(ctx, id)
	}
	row := r.exec.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE id = ? AND (owner_user_id = ? OR owner_user_id IS NULL)`, id, userID)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author %d for user %d: %w", id, userID, err)
	}
	return &a, nil
}

func (r *AuthorRepo) GetByForeignID(ctx context.Context, foreignID string) (*models.Author, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE foreign_id = ?`, foreignID)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by foreign_id %s: %w", foreignID, err)
	}
	return &a, nil
}

// GetByForeignIDForUser returns the author with the given foreign_id that is
// visible to userID — i.e. owned by that user or with a NULL owner. When
// userID is 0 the search is global (same as GetByForeignID).
func (r *AuthorRepo) GetByForeignIDForUser(ctx context.Context, foreignID string, userID int64) (*models.Author, error) {
	if userID == 0 {
		return r.GetByForeignID(ctx, foreignID)
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE foreign_id = ? AND (owner_user_id = ? OR owner_user_id IS NULL)`, foreignID, userID)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by foreign_id %s: %w", foreignID, err)
	}
	return &a, nil
}

// GetByAnyForeignID returns the author whose primary foreign_id or alternate
// identifier matches foreignID.
func (r *AuthorRepo) GetByAnyForeignID(ctx context.Context, foreignID string) (*models.Author, error) {
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" {
		return nil, nil
	}
	if author, err := r.GetByForeignID(ctx, foreignID); err != nil || author != nil {
		return author, err
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectColsA+`
		FROM author_identifiers ai
		JOIN authors a ON a.id = ai.author_id
		WHERE ai.foreign_id = ?`, foreignID)
	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by identifier %s: %w", foreignID, err)
	}
	return &a, nil
}

// GetByAnyForeignIDForUser is the user-scoped form of GetByAnyForeignID.
func (r *AuthorRepo) GetByAnyForeignIDForUser(ctx context.Context, foreignID string, userID int64) (*models.Author, error) {
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" {
		return nil, nil
	}
	if userID == 0 {
		return r.GetByAnyForeignID(ctx, foreignID)
	}
	if author, err := r.GetByForeignIDForUser(ctx, foreignID, userID); err != nil || author != nil {
		return author, err
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectColsA+`
		FROM author_identifiers ai
		JOIN authors a ON a.id = ai.author_id
		WHERE ai.foreign_id = ? AND (a.owner_user_id = ? OR a.owner_user_id IS NULL)`, foreignID, userID)
	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by identifier %s: %w", foreignID, err)
	}
	return &a, nil
}

func (r *AuthorRepo) Create(ctx context.Context, a *models.Author) error {
	return r.CreateForUser(ctx, a, 0)
}

func (r *AuthorRepo) CreateForUser(ctx context.Context, a *models.Author, ownerUserID int64) error {
	now := time.Now().UTC()
	normalizeAuthorMonitorDefaults(a)
	var ownerArg any
	if ownerUserID != 0 {
		ownerArg = ownerUserID
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create author: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO authors (foreign_id, name, sort_name, sort_key, description, image_url, disambiguation,
		                     ratings_count, average_rating, monitored, quality_profile_id, metadata_profile_id, root_folder_id,
		                     audiobook_root_folder_id, monitor_mode, monitor_latest_count, monitor_new_items, metadata_provider, owner_user_id,
		                     created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ForeignID, a.Name, a.SortName, authorSortKey(a.SortName), a.Description, a.ImageURL, a.Disambiguation,
		a.RatingsCount, a.AverageRating, a.Monitored, a.QualityProfileID, a.MetadataProfileID, a.RootFolderID,
		a.AudiobookRootFolderID, a.MonitorMode, a.MonitorLatestCount, a.MonitorNewItems, a.MetadataProvider, ownerArg, timeValueArg(now), timeValueArg(now))
	if err != nil {
		return fmt.Errorf("create author: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get author id: %w", err)
	}
	a.ID = id
	a.CreatedAt = now
	a.UpdatedAt = now
	// Reflect the persisted owner back onto the struct, exactly as
	// QualityProfileRepo.CreateForUser and MetadataProfileRepo.CreateForUser
	// already do. Without this the caller holds an author it believes is
	// NULL-owned while the row is owned by ownerUserID, and every downstream
	// use of that snapshot inherits the wrong tenancy: FetchAuthorBooks copies
	// author.OwnerUserID onto each book it creates, so an author added by user
	// 7 produced a whole catalogue of NULL-owned (globally visible) book rows
	// (#1872).
	a.OwnerUserID = ownerUserID
	if err := r.upsertIdentifierTx(ctx, tx, a.ID, a.ForeignID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create author: %w", err)
	}
	return nil
}

// GetAuthorIdentifier returns the owner row for foreignID, or nil when unknown.
func (r *AuthorRepo) GetAuthorIdentifier(ctx context.Context, foreignID string) (*models.AuthorIdentifier, error) {
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" {
		return nil, nil
	}
	row := r.exec.QueryRowContext(ctx, `
		SELECT author_id, provider, foreign_id, created_at, updated_at
		FROM author_identifiers WHERE foreign_id = ?`, foreignID)
	var out models.AuthorIdentifier
	if err := row.Scan(&out.AuthorID, &out.Provider, &out.ForeignID, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get author identifier %q: %w", foreignID, err)
	}
	return &out, nil
}

func (r *AuthorRepo) ListAuthorIdentifiers(ctx context.Context, authorID int64) ([]models.AuthorIdentifier, error) {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT author_id, provider, foreign_id, created_at, updated_at
		FROM author_identifiers WHERE author_id = ? ORDER BY provider, foreign_id`, authorID)
	if err != nil {
		return nil, fmt.Errorf("list author identifiers %d: %w", authorID, err)
	}
	defer rows.Close()
	out := []models.AuthorIdentifier{}
	for rows.Next() {
		var identifier models.AuthorIdentifier
		if err := rows.Scan(&identifier.AuthorID, &identifier.Provider, &identifier.ForeignID, &identifier.CreatedAt, &identifier.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan author identifier: %w", err)
		}
		out = append(out, identifier)
	}
	return out, rows.Err()
}

func (r *AuthorRepo) UpsertAuthorIdentifier(ctx context.Context, authorID int64, foreignID string) error {
	return r.upsertIdentifierTx(ctx, r.exec, authorID, foreignID, time.Now().UTC())
}

func (r *AuthorRepo) DeleteAuthorIdentifier(ctx context.Context, authorID int64, foreignID string) error {
	foreignID = strings.TrimSpace(foreignID)
	if authorID == 0 || foreignID == "" {
		return nil
	}
	if _, err := r.exec.ExecContext(ctx, `
		DELETE FROM author_identifiers
		WHERE author_id = ? AND foreign_id = ?`, authorID, foreignID); err != nil {
		return fmt.Errorf("delete author identifier %q for author %d: %w", foreignID, authorID, err)
	}
	return nil
}

func (r *AuthorRepo) upsertIdentifierTx(ctx context.Context, exec dbExecutor, authorID int64, foreignID string, now time.Time) error {
	foreignID = strings.TrimSpace(foreignID)
	if authorID == 0 || foreignID == "" {
		return nil
	}
	provider := models.AuthorProviderFromForeignID(foreignID)
	result, err := exec.ExecContext(ctx, `
		INSERT INTO author_identifiers (author_id, provider, foreign_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(foreign_id) DO UPDATE SET
			provider = excluded.provider,
			updated_at = excluded.updated_at
		WHERE author_identifiers.author_id = excluded.author_id`,
		authorID, provider, foreignID, now, now)
	if err != nil {
		return fmt.Errorf("upsert author identifier %q: %w", foreignID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check author identifier %q: %w", foreignID, err)
	}
	if affected > 0 {
		return nil
	}
	var ownerID int64
	row := exec.QueryRowContext(ctx, "SELECT author_id FROM author_identifiers WHERE foreign_id = ?", foreignID)
	if err := row.Scan(&ownerID); err != nil {
		return fmt.Errorf("read author identifier owner %q: %w", foreignID, err)
	}
	return &AuthorIdentifierConflictError{ForeignID: foreignID, AuthorID: ownerID}
}

// GetByDNBSyntheticName returns the synthetic DNB-only author row (one whose
// foreign_id starts with "dnb:author:") that matches the given sort_name
// case-insensitively and is visible to userID. Returns (nil, nil) when none
// exists.
//
// This is used by AddBook to detect when a canonical author (OpenLibrary /
// Hardcover) is being added for a SortName that was previously persisted as
// a synthetic DNB row — see UpgradeSyntheticDNB.
//
// When userID is 0 the lookup is unscoped; in practice multi-user installs
// always pass the requesting user's ID so they don't migrate another user's
// row.
func (r *AuthorRepo) GetByDNBSyntheticName(ctx context.Context, sortName string, userID int64) (*models.Author, error) {
	if sortName == "" {
		return nil, nil
	}
	// The SQL narrows to synthetic DNB rows only; the name comparison happens
	// in Go. It used to be `LOWER(sort_name) = LOWER(?)`, and SQLite's LOWER()
	// folds ASCII only — but worse, the two sides are produced by different
	// code. DNB stores SortName verbatim from MARC 100 $a ("Tolkien, J. R. R."),
	// while the canonical side is sortName(), a strings.Fields last-token flip
	// ("Tolkien, J.R.R." from OpenLibrary's "J.R.R. Tolkien"). Those never
	// compare equal, so this function returned nothing and the duplicate it
	// exists to prevent was created anyway: a leftover synthetic
	// dnb:author:<slug> row holding the German books alongside a new
	// OpenLibrary row (#1647). textutil.MatchAuthorName scores that pair Exact.
	q := `SELECT ` + authorSelectCols + `
			FROM authors
			WHERE foreign_id LIKE 'dnb:author:%'`
	args := []any{}
	if userID != 0 {
		q += ` AND (owner_user_id = ? OR owner_user_id IS NULL)`
		args = append(args, userID)
	}
	q += ` ORDER BY id`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get author by dnb-synthetic sort_name %q: %w", sortName, err)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, fmt.Errorf("get author by dnb-synthetic sort_name %q: %w", sortName, err)
		}
		if textutil.MatchAuthorName(sortName, a.SortName).Kind == textutil.AuthorMatchExact {
			return &a, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get author by dnb-synthetic sort_name %q: %w", sortName, err)
	}
	return nil, nil
}

// UpgradeSyntheticDNB migrates a synthetic DNB-only author row to a canonical
// provider identity. The row identified by currentForeignID has its
// foreign_id, metadata_provider and (when non-empty in target) descriptive
// fields replaced. Existing relations (books, aliases) keep pointing at the
// same primary-key row so the user keeps one author record.
//
// currentForeignID is matched by exact equality; pass the value previously
// returned by GetByDNBSyntheticName. target carries the canonical fields.
// Returns an error only on a SQL failure — a no-op update (e.g. row gone)
// is silent.
func (r *AuthorRepo) UpgradeSyntheticDNB(ctx context.Context, currentForeignID string, target *models.Author) error {
	if currentForeignID == "" || target == nil || target.ForeignID == "" {
		return fmt.Errorf("upgrade synthetic dnb: missing currentForeignID or target")
	}
	now := time.Now().UTC()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upgrade synthetic dnb: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
			UPDATE authors
			SET foreign_id        = ?,
			    name              = COALESCE(NULLIF(?, ''), name),
		    sort_name         = COALESCE(NULLIF(?, ''), sort_name),
		    sort_key          = CASE WHEN ? != '' THEN ? ELSE sort_key END,
		    description       = CASE WHEN ? != '' THEN ? ELSE description END,
		    image_url         = CASE WHEN ? != '' THEN ? ELSE image_url END,
		    disambiguation    = CASE WHEN ? != '' THEN ? ELSE disambiguation END,
		    metadata_provider = COALESCE(NULLIF(?, ''), metadata_provider),
		    updated_at        = ?
		WHERE foreign_id = ?`,
		target.ForeignID,                                // foreign_id =
		target.Name,                                     // name = COALESCE(NULLIF(?,''), name)
		target.SortName,                                 // sort_name = COALESCE(NULLIF(?,''), sort_name)
		target.SortName, authorSortKey(target.SortName), // sort_key CASE WHEN ?!='' THEN ? ELSE sort_key
		target.Description, target.Description, // description CASE WHEN ? != '' THEN ?
		target.ImageURL, target.ImageURL, // image_url
		target.Disambiguation, target.Disambiguation, // disambiguation
		target.MetadataProvider, // metadata_provider = COALESCE(NULLIF(?,''), metadata_provider)
		timeValueArg(now),       // updated_at
		currentForeignID)        // WHERE foreign_id = ?
	if err != nil {
		return fmt.Errorf("upgrade synthetic dnb author %q -> %q: %w", currentForeignID, target.ForeignID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check upgraded dnb author %q: %w", currentForeignID, err)
	}
	if affected == 0 {
		return nil
	}
	var authorID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM authors WHERE foreign_id = ?", target.ForeignID).Scan(&authorID); err != nil {
		return fmt.Errorf("lookup upgraded dnb author %q: %w", target.ForeignID, err)
	}
	if err := r.upsertIdentifierTx(ctx, tx, authorID, target.ForeignID, now); err != nil {
		return err
	}
	if err := r.upsertIdentifierTx(ctx, tx, authorID, currentForeignID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upgrade synthetic dnb: %w", err)
	}
	return nil
}

func (r *AuthorRepo) Update(ctx context.Context, a *models.Author) error {
	now := time.Now().UTC()
	normalizeAuthorMonitorDefaults(a)

	if _, ok := r.exec.(*sql.Tx); ok {
		if err := r.update(ctx, r.exec, a, now); err != nil {
			return err
		}
		a.UpdatedAt = now
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update author %d: %w", a.ID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := r.update(ctx, tx, a, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update author %d: %w", a.ID, err)
	}
	a.UpdatedAt = now
	return nil
}

func (r *AuthorRepo) update(ctx context.Context, exec dbExecutor, a *models.Author, now time.Time) error {
	_, err := exec.ExecContext(ctx, `
		UPDATE authors SET foreign_id=?, name=?, sort_name=?, sort_key=?, description=?, image_url=?, disambiguation=?,
		                   ratings_count=?, average_rating=?, monitored=?, quality_profile_id=?,
		                   metadata_profile_id=?, root_folder_id=?, audiobook_root_folder_id=?, monitor_mode=?,
		                   monitor_latest_count=?, monitor_new_items=?, metadata_provider=?, last_metadata_refresh_at=?, updated_at=?
		WHERE id=?`,
		a.ForeignID, a.Name, a.SortName, authorSortKey(a.SortName), a.Description, a.ImageURL, a.Disambiguation,
		a.RatingsCount, a.AverageRating, a.Monitored, a.QualityProfileID,
		a.MetadataProfileID, a.RootFolderID, a.AudiobookRootFolderID, a.MonitorMode,
		a.MonitorLatestCount, a.MonitorNewItems, a.MetadataProvider, timeArg(a.LastMetadataRefreshAt), timeValueArg(now), a.ID)
	if err != nil {
		return fmt.Errorf("update author %d: %w", a.ID, err)
	}
	if err := r.upsertIdentifierTx(ctx, exec, a.ID, a.ForeignID, now); err != nil {
		return err
	}
	return nil
}

// Delete removes the author and its author_identifiers rows.
//
// The identifier rows are deleted explicitly rather than left to the
// ON DELETE CASCADE declared in migration 052 (#1728). The cascade only fires
// while PRAGMA foreign_keys is on, which is connection state — see #1727 — so
// relying on it made recreating a deleted author fail forever: foreign_id is
// NOT NULL UNIQUE, and the surviving orphan row collides with the new author's
// identifier, surfacing as a misleading 409 "author already exists" for an
// author that no longer exists in `authors`. Deleting both here keeps the
// invariant true regardless of pragma state; the cascade is now redundancy.
func (r *AuthorRepo) Delete(ctx context.Context, id int64) error {
	if _, ok := r.exec.(*sql.Tx); ok {
		return r.deleteWithIdentifiers(ctx, r.exec, id)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete author %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := r.deleteWithIdentifiers(ctx, tx, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete author %d: %w", id, err)
	}
	return nil
}

func (r *AuthorRepo) deleteWithIdentifiers(ctx context.Context, exec dbExecutor, id int64) error {
	if _, err := exec.ExecContext(ctx, "DELETE FROM author_identifiers WHERE author_id=?", id); err != nil {
		return fmt.Errorf("delete author %d identifiers: %w", id, err)
	}
	if _, err := exec.ExecContext(ctx, "DELETE FROM authors WHERE id=?", id); err != nil {
		return fmt.Errorf("delete author %d: %w", id, err)
	}
	return nil
}

// CataloguePopulatedAt reports when this author first had a book, or nil if
// they never have. BookRepo.Create stamps it — every book-creation path flows
// through there, so it covers importers, list syncs and manual adds, not only
// catalogue syncs.
//
// It is the discriminator the refresh discovery policy needs (#1815, #1816): a
// refresh may populate an author who has no books, because repairing an import
// that resolved the author but no catalogue is what bulk "Refresh metadata"
// exists for — but an author whose books the user deleted is also an author
// with no books, and refilling that one is the bug, not the repair. Books
// present says nothing about the second case; this column does.
//
// Deliberately not part of authorSelectCols: nothing but the sync gate reads
// it, and threading it through the full scan/update surface would add a column
// to every author query for one caller.
func (r *AuthorRepo) CataloguePopulatedAt(ctx context.Context, authorID int64) (*time.Time, error) {
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT catalogue_populated_at FROM authors WHERE id = ?`, authorID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read catalogue_populated_at for author %d: %w", authorID, err)
	}
	when, perr := parseFlexibleTime(raw)
	if perr != nil {
		slog.Warn("unparseable author.catalogue_populated_at, treating as never populated",
			"author_id", authorID, "value", raw.String, "error", perr)
		return nil, nil
	}
	return when, nil
}

// MarkCataloguePopulated stamps the author as having had a catalogue, if it
// isn't stamped already. Write-once: the value records that it happened at
// all, and re-stamping later would erase the distinction it exists to draw.
// BookRepo.Create does this inline on every insert; this is the explicit form,
// used by tests and by any caller that needs to assert the fact directly.
func (r *AuthorRepo) MarkCataloguePopulated(ctx context.Context, authorID int64, when time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE authors SET catalogue_populated_at = ? WHERE id = ? AND catalogue_populated_at IS NULL`,
		timeValueArg(when.UTC()), authorID)
	if err != nil {
		return fmt.Errorf("mark author %d catalogue populated: %w", authorID, err)
	}
	return nil
}

// ListMonitoredSeriesIDs returns the series IDs the author is pinned to when
// MonitorMode == AuthorMonitorModeSeries. Returns an empty slice (not nil)
// when nothing is pinned so the JSON encoder produces `[]` rather than null,
// which keeps the UI's chip list happy.
func (r *AuthorRepo) ListMonitoredSeriesIDs(ctx context.Context, authorID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT series_id FROM author_monitored_series WHERE author_id = ? ORDER BY series_id`, authorID)
	if err != nil {
		return nil, fmt.Errorf("list monitored series ids for author %d: %w", authorID, err)
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan monitored series id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetMonitoredSeriesIDs replaces the author's monitored-series selection
// atomically. Passing an empty slice clears the selection. Callers must
// validate that every ID belongs to a series the author actually has books in
// before calling — this repo trusts its inputs.
func (r *AuthorRepo) SetMonitoredSeriesIDs(ctx context.Context, authorID int64, seriesIDs []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set monitored series tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM author_monitored_series WHERE author_id = ?`, authorID); err != nil {
		return fmt.Errorf("clear monitored series for author %d: %w", authorID, err)
	}

	if len(seriesIDs) > 0 {
		now := time.Now().UTC()
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO author_monitored_series (author_id, series_id, created_at) VALUES (?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare insert monitored series: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		// Dedupe input — callers may pass duplicates and we want a clean PK insert.
		seen := make(map[int64]struct{}, len(seriesIDs))
		for _, id := range seriesIDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			if _, err := stmt.ExecContext(ctx, authorID, id, timeValueArg(now)); err != nil {
				return fmt.Errorf("insert monitored series (%d, %d): %w", authorID, id, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set monitored series: %w", err)
	}
	return nil
}

// rowScanner is the common subset of *sql.Rows and *sql.Row used by the
// author scan helpers so a single implementation handles both shapes.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAuthor(rows *sql.Rows) (models.Author, error) {
	return scanAuthorFrom(rows)
}

func scanAuthorRow(row *sql.Row) (models.Author, error) {
	return scanAuthorFrom(row)
}

// trailingScanner lets a query select the standard author columns plus extra
// derived ones without duplicating scanAuthorFrom's 22-destination list, which
// would then be free to drift from authorSelectCols on the next column added.
type trailingScanner struct {
	s     rowScanner
	extra []any
}

func (t trailingScanner) Scan(dest ...any) error {
	return t.s.Scan(append(dest, t.extra...)...)
}

func scanAuthorFrom(s rowScanner) (models.Author, error) {
	var a models.Author
	var monitored int
	// Time columns scanned as strings + parseFlexibleTime so legacy rows
	// written by Go's default time.String() shape (#914) still load.
	var lastMetadataRefreshAtStr, createdAtStr, updatedAtStr sql.NullString
	err := s.Scan(&a.ID, &a.ForeignID, &a.Name, &a.SortName, &a.Description, &a.ImageURL,
		&a.Disambiguation, &a.RatingsCount, &a.AverageRating, &monitored,
		&a.QualityProfileID, &a.MetadataProfileID, &a.RootFolderID, &a.AudiobookRootFolderID,
		&a.MonitorMode, &a.MonitorLatestCount, &a.MonitorNewItems, &a.MetadataProvider,
		&lastMetadataRefreshAtStr, &createdAtStr, &updatedAtStr, &a.OwnerUserID)
	if err != nil {
		return a, err
	}
	if lm, perr := parseFlexibleTime(lastMetadataRefreshAtStr); perr != nil {
		slog.Warn("unparseable author.last_metadata_refresh_at, leaving nil", "author_id", a.ID, "value", lastMetadataRefreshAtStr.String, "error", perr)
	} else {
		a.LastMetadataRefreshAt = lm
	}
	a.CreatedAt = parseFlexibleTimeValue(createdAtStr, "authors.created_at")
	a.UpdatedAt = parseFlexibleTimeValue(updatedAtStr, "authors.updated_at")
	a.Monitored = monitored == 1
	normalizeAuthorMonitorDefaults(&a)
	return a, nil
}

func normalizeAuthorMonitorDefaults(a *models.Author) {
	if a == nil {
		return
	}
	if !models.IsAuthorMonitorModeValid(a.MonitorMode) {
		a.MonitorMode = models.DefaultAuthorMonitorMode
	}
	if a.MonitorLatestCount <= 0 {
		a.MonitorLatestCount = models.DefaultAuthorMonitorLatestCount
	}
	a.MonitorNewItems = models.NormalizeAuthorMonitorNewItems(a.MonitorNewItems)
}
