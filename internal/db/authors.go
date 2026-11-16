package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/auth"
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
	       created_at, updated_at, COALESCE(owner_user_id, 0), CAST(updated_at AS TEXT), description_source, image_source`
const authorSelectColsA = `a.id, a.foreign_id, a.name, a.sort_name, a.description, a.image_url, a.disambiguation,
		       a.ratings_count, a.average_rating, a.monitored, a.quality_profile_id, a.metadata_profile_id, a.root_folder_id,
		       a.audiobook_root_folder_id, a.monitor_mode, a.monitor_latest_count, a.monitor_new_items, a.metadata_provider, a.last_metadata_refresh_at,
		       a.created_at, a.updated_at, COALESCE(a.owner_user_id, 0), CAST(a.updated_at AS TEXT), a.description_source, a.image_source`

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
	// "name-az"/"name-za" (display-name asc/desc, #2102),
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

// authorSearchRank returns the ORDER BY prefix that ranks a search result set,
// and the values its placeholders bind. See searchrank.go for the tiers.
//
// The name column alone is ranked. An author matched only through an alias
// therefore lands in the weakest tier, below everyone matched on the name they
// are filed under — the alias is why they are in the result set at all (#1176),
// not evidence that they are the best answer.
func authorSearchRank(folded string) (string, []any) {
	return searchRankClause("authors.search_key") + "length(authors.search_key), ",
		searchRankArgs(folded, 1)
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
	// Unrated authors (0) sort last in both directions, as books do:
	// "Lowest rated" listed every unrated author first.
	case "rating-asc":
		return "average_rating <= 0, average_rating ASC, sort_key ASC"
	case "rating-desc":
		return "average_rating <= 0, average_rating DESC, sort_key ASC"
	case "monitored-asc":
		return "monitored ASC, sort_key ASC"
	case "monitored-desc":
		return "monitored DESC, sort_key ASC"
	// Display-name ordering (#2102): name_sort_key is the sibling of sort_key,
	// folded from `name` instead of sort_name (migration 079), so "Andy Weir"
	// sorts under A rather than W and accented first names stay in place.
	case "name-az":
		return "name_sort_key ASC, name COLLATE NOCASE ASC"
	case "name-za":
		return "name_sort_key DESC, name COLLATE NOCASE DESC"
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
	searchOrder, rankArgs := "", []any(nil)
	if folded := textutil.FoldForSearch(f.Search); folded != "" {
		// A query that folds to nothing (all punctuation, or an emoji) leaves
		// the filter off and returns the whole list, where matching the raw
		// text used to return nothing. That is the honest answer: the fold is
		// what defines a searchable character here, so "???" carries no search
		// terms at all rather than being a term that matches no row.
		// Match the canonical name OR any of the author's aliases (#1176), so
		// searching a pen name / AKA (e.g. "Cassandra Clare" stored as an alias
		// of Holly Black) still surfaces the author that owns it.
		//
		// Both sides are folded by textutil.FoldForSearch. The previous
		// `LIKE ? COLLATE NOCASE` against the raw name folded ASCII only, so
		// "ostergaard" never found "Østergaard" (#1660) — the search half of
		// the limitation migration 058 already worked around for ordering.
		//
		// One clause per token: every word the user typed must appear, in the
		// name or in one alias. Capped at maxSearchTokens (searchrank.go) so
		// the statement's size is bounded by that constant, not by the input.
		for _, tok := range searchTokens(folded) {
			like := "%" + escapeLike(tok) + "%"
			conds = append(conds, "(authors.search_key LIKE ? ESCAPE '\\' OR EXISTS (SELECT 1 FROM author_aliases al WHERE al.author_id = authors.id AND al.search_key LIKE ? ESCAPE '\\'))")
			args = append(args, like, like)
		}
		searchOrder, rankArgs = authorSearchRank(folded)
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
	//
	// available_count is the subset the library already holds, by
	// InLibraryPredicate: imported, or wanted with at least one tracked file, the
	// same rule as the Books page and the author page. The predicate has no bind
	// parameters, so it adds nothing to selectArgs below.
	bookScope := "b.author_id = authors.id"
	var selectArgs []any
	if f.UserID != 0 {
		bookScope += " AND (b.owner_user_id = ? OR b.owner_user_id IS NULL)"
		// One bind per subquery below.
		selectArgs = append(selectArgs, f.UserID, f.UserID)
	}
	bookCountExpr := "(SELECT COUNT(*) FROM books b WHERE " + bookScope + ") AS book_count, " +
		"(SELECT COUNT(*) FROM books b WHERE " + bookScope + " AND " + InLibraryPredicate("b", "") + ") AS available_count"

	//nolint:gosec // query is built only from static columns, a parameterised WHERE, and a whitelisted ORDER BY (authorSortOrder); all user values are bound via args
	listQuery := "SELECT " + authorSelectCols + ", " + bookCountExpr + " FROM authors" + where +
		" ORDER BY " + searchOrder + authorSortOrder(f.Sort) + " LIMIT ? OFFSET ?"
	// Order matters: the subqueries' placeholders sit in the SELECT list, ahead
	// of every WHERE placeholder, and the ranking placeholders sit in ORDER BY,
	// which the parser reaches last. The count query has neither, so it takes
	// args alone.
	pageArgs := append(append(append(append([]any{}, selectArgs...), args...), rankArgs...), limit, offset)

	rows, err := r.db.QueryContext(ctx, listQuery, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list authors page: %w", err)
	}
	defer rows.Close()

	var authors []models.Author
	for rows.Next() {
		var bookCount, availableCount int
		a, err := scanAuthorFrom(trailingScanner{s: rows, extra: []any{&bookCount, &availableCount}})
		if err != nil {
			return nil, 0, err
		}
		a.Statistics = &models.AuthorStats{BookCount: bookCount, AvailableBooks: availableCount}
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

// unscopedAuthorOrder picks one row when several accounts hold their own row
// for the same provider id (#3555): a shared row (no owner) first, then the
// oldest. The oldest is the row the instance would have held alone before
// authors were keyed per owner, so an unscoped caller (an import, an admin, a
// lookup with tenancy off) keeps resolving to the author it always did.
const unscopedAuthorOrder = ` ORDER BY (owner_user_id IS NOT NULL), id LIMIT 1`

// scopedAuthorOrder is the user scoped form: the caller's own row before a
// shared one, then the oldest.
const scopedAuthorOrder = ` ORDER BY (owner_user_id IS NULL), id LIMIT 1`

// GetByForeignID returns the author whose primary provider id is foreignID,
// from any account. When several accounts hold their own row for it, the
// shared row wins, then the oldest (unscopedAuthorOrder). Code acting for a
// user should call GetByForeignIDForUser.
func (r *AuthorRepo) GetByForeignID(ctx context.Context, foreignID string) (*models.Author, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE foreign_id = ?`+unscopedAuthorOrder, foreignID)

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
// visible to userID, that is owned by that user or with a NULL owner, the
// user's own row first. When userID is 0 the search is global (same as
// GetByForeignID).
func (r *AuthorRepo) GetByForeignIDForUser(ctx context.Context, foreignID string, userID int64) (*models.Author, error) {
	if userID == 0 {
		return r.GetByForeignID(ctx, foreignID)
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE foreign_id = ? AND (owner_user_id = ? OR owner_user_id IS NULL)`+scopedAuthorOrder, foreignID, userID)

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
		WHERE ai.foreign_id = ?
		ORDER BY (a.owner_user_id IS NOT NULL), a.id LIMIT 1`, foreignID)
	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by identifier %s: %w", foreignID, err)
	}
	return &a, nil
}

// LibraryIDsByAnyForeignIDsForUser maps each of the given foreign ids that
// resolves to an author visible to the user, by primary foreign_id or by an
// alternate identifier, to that author's id in one query. It is the batch form
// of GetByAnyForeignIDForUser, used to stamp metadata search results with the
// library author they already correspond to (#1227). Scoping matches the
// single-row form: owner equal to userID or NULL when userID > 0, global
// otherwise. When both a primary id and an alternate identifier match the
// same foreign id the primary wins.
func (r *AuthorRepo) LibraryIDsByAnyForeignIDsForUser(ctx context.Context, foreignIDs []string, userID int64) (map[string]int64, error) {
	out := make(map[string]int64, len(foreignIDs))
	ids := make([]string, 0, len(foreignIDs))
	seen := make(map[string]bool, len(foreignIDs))
	for _, id := range foreignIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	inArgs := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		inArgs[i] = id
	}
	in := "(" + strings.Join(placeholders, ",") + ")"
	scope := ""
	var scopeArgs []any
	// pref orders the rows of one foreign id the way the single row lookups
	// do (#3555): unscoped, a shared row before an owned one; scoped, the
	// user's own row before a shared one. Then the oldest.
	pref := "(a.owner_user_id IS NOT NULL)"
	if userID != 0 {
		scope = " AND (a.owner_user_id = ? OR a.owner_user_id IS NULL)"
		scopeArgs = []any{userID}
		pref = "(a.owner_user_id IS NULL)"
	}
	// Primary ids sort first (rank 0) so the first row per foreign id wins.
	//nolint:gosec // G202: in is generated ? placeholders, scope a fixed owner predicate and pref a fixed expression; every foreign id and the user id are bound via args
	query := `SELECT a.foreign_id, a.id, 0 AS rank, ` + pref + ` AS pref FROM authors a WHERE a.foreign_id IN ` + in + scope + `
		UNION ALL
		SELECT ai.foreign_id, a.id, 1 AS rank, ` + pref + ` AS pref FROM author_identifiers ai
		JOIN authors a ON a.id = ai.author_id WHERE ai.foreign_id IN ` + in + scope + `
		ORDER BY rank, pref, 2`
	args := make([]any, 0, 2*(len(inArgs)+len(scopeArgs)))
	args = append(args, inArgs...)
	args = append(args, scopeArgs...)
	args = append(args, inArgs...)
	args = append(args, scopeArgs...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("library ids by author identifier: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var foreignID string
		var id int64
		var rank, pref int
		if err := rows.Scan(&foreignID, &id, &rank, &pref); err != nil {
			return nil, fmt.Errorf("scan library author id: %w", err)
		}
		if _, dup := out[foreignID]; !dup {
			out[foreignID] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("library ids by author identifier: %w", err)
	}
	return out, nil
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
		WHERE ai.foreign_id = ? AND (a.owner_user_id = ? OR a.owner_user_id IS NULL)
		ORDER BY (a.owner_user_id IS NULL), a.id LIMIT 1`, foreignID, userID)
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
	// Stamp the default metadata profile when the caller left it unset (#1803).
	//
	// Six author-creation paths reached this insert with a nil profile: ABS
	// import (twice), the Calibre importer, the Goodreads migration, the CSV
	// importer, and Hardcover list sync before #1783 stamped it there by hand.
	// Migration 071 backfilled the NULLs those had already produced, but a
	// one-shot cleanup in front of an ongoing leak just means the next import
	// starts refilling it, so the stamp belongs at the single point they all
	// funnel through rather than in each of them.
	//
	// Deliberately here and not in normalizeAuthorMonitorDefaults, which also
	// runs on Update and on scan. Doing it on scan would make a NULL row report
	// a profile it does not have, which hides the leak instead of closing it.
	//
	// This is currently invisible: ResolveAuthorMetadataProfile and its two
	// sibling readers all fall back to DefaultMetadataProfileID already, so
	// filtering behaviour is unchanged either way. That is exactly why it would
	// rot quietly, until a fourth reader arrives without the fallback. The
	// column is a bare INTEGER with no REFERENCES clause, so an id pointing at
	// a profile the user has since deleted stores fine and every reader still
	// falls back, the same as NULL does today.
	if a != nil && a.MetadataProfileID == nil {
		id := models.DefaultMetadataProfileID
		a.MetadataProfileID = &id
	}
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
		INSERT INTO authors (foreign_id, name, sort_name, sort_key, name_sort_key, search_key, description, image_url, disambiguation,
		                     ratings_count, average_rating, monitored, quality_profile_id, metadata_profile_id, root_folder_id,
		                     audiobook_root_folder_id, monitor_mode, monitor_latest_count, monitor_new_items, metadata_provider, owner_user_id,
		                     created_at, updated_at, description_source, image_source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ForeignID, a.Name, a.SortName, authorSortKey(a.SortName), authorSortKey(a.Name), textutil.FoldForSearch(a.Name), a.Description, a.ImageURL, a.Disambiguation,
		a.RatingsCount, a.AverageRating, a.Monitored, a.QualityProfileID, a.MetadataProfileID, a.RootFolderID,
		a.AudiobookRootFolderID, a.MonitorMode, a.MonitorLatestCount, a.MonitorNewItems, a.MetadataProvider, ownerArg, timeValueArg(now), timeValueArg(now),
		encodeProfileSource(a.DescriptionSource), encodeProfileSource(a.ImageSource))
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
	a.UpdatedAtRaw = timeValueText(now)
	// Reflect the persisted owner back onto the struct, exactly as
	// QualityProfileRepo.CreateForUser and MetadataProfileRepo.CreateForUser
	// already do. Without this the caller holds an author it believes is
	// NULL-owned while the row is owned by ownerUserID, and every downstream
	// use of that snapshot inherits the wrong tenancy: FetchAuthorBooks copies
	// author.OwnerUserID onto each book it creates, so an author added by user
	// 7 produced a whole catalogue of NULL-owned (globally visible) book rows
	// (#1872).
	a.OwnerUserID = ownerUserID
	if err := r.upsertIdentifierTx(ctx, tx, a.ID, a.ForeignID, now, true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create author: %w", err)
	}
	return nil
}

// GetAuthorIdentifier returns the holder row for foreignID, or nil when
// unknown. Under tenancy several accounts' authors can hold the same id
// (#3555); the lowest author id is returned. To ask whether one particular
// author holds it, use ListAuthorIdentifiers or DeleteAuthorIdentifier, which
// take the author.
func (r *AuthorRepo) GetAuthorIdentifier(ctx context.Context, foreignID string) (*models.AuthorIdentifier, error) {
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" {
		return nil, nil
	}
	row := r.exec.QueryRowContext(ctx, `
		SELECT author_id, provider, foreign_id, created_at, updated_at
		FROM author_identifiers WHERE foreign_id = ?
		ORDER BY author_id LIMIT 1`, foreignID)
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
	return r.upsertIdentifierTx(ctx, r.exec, authorID, foreignID, time.Now().UTC(), false)
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

func (r *AuthorRepo) upsertIdentifierTx(ctx context.Context, exec dbExecutor, authorID int64, foreignID string, now time.Time, recheck bool) error {
	foreignID = strings.TrimSpace(foreignID)
	if authorID == 0 || foreignID == "" {
		return nil
	}
	held, err := authorHoldsIdentifier(ctx, exec, authorID, foreignID)
	if err != nil {
		return err
	}
	if !held || recheck {
		// A row the author already has is only refreshed, which adds no
		// duplicate, so it is not checked again. That keeps an author
		// editable when the rule has a pair it would now refuse, such as two
		// accounts' copies left behind after tenancy was turned off (#3555).
		// recheck is for a write that makes foreignID the author's primary id
		// (a create, a changed foreign_id, a DNB upgrade): an identifier row
		// the author already holds must not let a primary collide with
		// another author's, which the per owner index cannot see across
		// owners.
		holder, err := identifierHolder(ctx, exec, authorID, foreignID)
		if err != nil {
			return err
		}
		if holder != 0 {
			return &AuthorIdentifierConflictError{ForeignID: foreignID, AuthorID: holder}
		}
	}
	provider := models.AuthorProviderFromForeignID(foreignID)
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO author_identifiers (author_id, provider, foreign_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(author_id, foreign_id) DO UPDATE SET
			provider = excluded.provider,
			updated_at = excluded.updated_at`,
		authorID, provider, foreignID, now, now); err != nil {
		return fmt.Errorf("upsert author identifier %q: %w", foreignID, err)
	}
	return nil
}

// identifierHolder returns the id of another author that already holds
// foreignID in a way authorID may not share, or 0 when none does. It is the
// uniqueness rule author_identifiers.foreign_id and authors.foreign_id used to
// enforce on their own before authors were keyed per owner (#3555):
//
//   - With tenancy on, a provider id belongs to one author per owner, a NULL
//     owner counting as owner 0 (the same key as the unique index on
//     authors). Another account's row for the same author does not conflict.
//   - With tenancy off, a provider id belongs to one author in the whole
//     instance, as it always did: another author's identifier row conflicts,
//     and so does another author's primary foreign_id when foreignID is
//     authorID's own primary (the old authors.foreign_id UNIQUE).
//
// An identifier row whose author is gone conflicts in either mode, as it did
// under the old UNIQUE. Callers run this inside the transaction that writes
// the row, and the pool is a single connection, so the check and the write
// cannot interleave with another writer.
func identifierHolder(ctx context.Context, exec dbExecutor, authorID int64, foreignID string) (int64, error) {
	return identifierHolderExcept(ctx, exec, authorID, foreignID, 0)
}

// identifierHolderExcept is identifierHolder ignoring exceptID as a holder:
// the author whose identifiers a merge or a fold is handing to authorID, which
// still exists while they move.
func identifierHolderExcept(ctx context.Context, exec dbExecutor, authorID int64, foreignID string, exceptID int64) (int64, error) {
	var holder int64
	var err error
	if auth.EnforceTenancy() {
		err = exec.QueryRowContext(ctx, `
			SELECT ai.author_id
			FROM author_identifiers ai
			LEFT JOIN authors a ON a.id = ai.author_id
			WHERE ai.foreign_id = ? AND ai.author_id <> ? AND ai.author_id <> ?
			  AND (a.id IS NULL OR COALESCE(a.owner_user_id, 0) =
			       (SELECT COALESCE(owner_user_id, 0) FROM authors WHERE id = ?))
			ORDER BY ai.author_id LIMIT 1`,
			foreignID, authorID, exceptID, authorID).Scan(&holder)
	} else {
		err = exec.QueryRowContext(ctx, `
			SELECT holder FROM (
				SELECT author_id AS holder FROM author_identifiers
				WHERE foreign_id = ? AND author_id <> ? AND author_id <> ?
				UNION ALL
				SELECT id FROM authors
				WHERE foreign_id = ? AND id <> ? AND id <> ?
				  AND foreign_id = (SELECT foreign_id FROM authors WHERE id = ?)
			) ORDER BY holder LIMIT 1`,
			foreignID, authorID, exceptID, foreignID, authorID, exceptID, authorID).Scan(&holder)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("check author identifier holder %q: %w", foreignID, err)
	}
	return holder, nil
}

// authorHoldsIdentifier reports whether authorID already has an identifier
// row for foreignID.
func authorHoldsIdentifier(ctx context.Context, exec dbExecutor, authorID int64, foreignID string) (bool, error) {
	var n int
	if err := exec.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM author_identifiers WHERE author_id = ? AND foreign_id = ?",
		authorID, foreignID).Scan(&n); err != nil {
		return false, fmt.Errorf("check author identifier %q for author %d: %w", foreignID, authorID, err)
	}
	return n > 0, nil
}

// listAuthorIdentifierIDs returns the provider ids authorID holds, read in
// full and closed before the caller writes in the same transaction.
func listAuthorIdentifierIDs(ctx context.Context, exec dbExecutor, authorID int64) ([]string, error) {
	rows, err := exec.QueryContext(ctx,
		"SELECT foreign_id FROM author_identifiers WHERE author_id = ? ORDER BY foreign_id", authorID)
	if err != nil {
		return nil, fmt.Errorf("list identifiers of author %d: %w", authorID, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan identifier of author %d: %w", authorID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list identifiers of author %d: %w", authorID, err)
	}
	return ids, nil
}

// moveAuthorIdentifiersTx hands fromID's identifier rows to intoID, for a
// merge or a user delete folding one author into another. Each identifier
// goes through the rule every other write uses (identifierHolder): one that
// another author in intoID's scope already holds is dropped rather than
// moved, because moving it would leave two authors holding it and every later
// Update of the other one would fail (#3555). One intoID already has is just
// removed from fromID. Returns the identifiers dropped because another author
// holds them.
func moveAuthorIdentifiersTx(ctx context.Context, exec dbExecutor, fromID, intoID int64, now time.Time) ([]string, error) {
	ids, err := listAuthorIdentifierIDs(ctx, exec, fromID)
	if err != nil {
		return nil, err
	}
	if _, err := exec.ExecContext(ctx, "DELETE FROM author_identifiers WHERE author_id = ?", fromID); err != nil {
		return nil, fmt.Errorf("drop identifiers of author %d: %w", fromID, err)
	}
	var dropped []string
	for _, id := range ids {
		held, err := authorHoldsIdentifier(ctx, exec, intoID, id)
		if err != nil {
			return nil, err
		}
		if held {
			continue
		}
		holder, err := identifierHolderExcept(ctx, exec, intoID, id, fromID)
		if err != nil {
			return nil, err
		}
		if holder != 0 {
			dropped = append(dropped, id)
			continue
		}
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO author_identifiers (author_id, provider, foreign_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`,
			intoID, models.AuthorProviderFromForeignID(id), id, now, now); err != nil {
			return nil, fmt.Errorf("move identifier %q to author %d: %w", id, intoID, err)
		}
	}
	return dropped, nil
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
// authorID and currentForeignID identify the row; pass the id and foreign id
// of the author GetByDNBSyntheticName returned. Both must match, so the
// upgrade touches that one row and never another account's row carrying the
// same synthetic id (#3555). target carries the canonical fields.
// Returns an error only on a SQL failure — a no-op update (e.g. row gone)
// is silent.
func (r *AuthorRepo) UpgradeSyntheticDNB(ctx context.Context, authorID int64, currentForeignID string, target *models.Author) error {
	if authorID == 0 || currentForeignID == "" || target == nil || target.ForeignID == "" {
		return fmt.Errorf("upgrade synthetic dnb: missing author, currentForeignID or target")
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
		    name_sort_key     = CASE WHEN ? != '' THEN ? ELSE name_sort_key END,
		    search_key        = CASE WHEN ? != '' THEN ? ELSE search_key END,
		    description       = CASE WHEN ? != '' AND COALESCE(json_extract(NULLIF(description_source, ''), '$.provider'), '') != ? THEN ? ELSE description END,
		    description_source = CASE WHEN ? != '' AND COALESCE(json_extract(NULLIF(description_source, ''), '$.provider'), '') != ? THEN '' ELSE description_source END,
		    image_url         = CASE WHEN ? != '' AND COALESCE(json_extract(NULLIF(image_source, ''), '$.provider'), '') != ? THEN ? ELSE image_url END,
		    image_source      = CASE WHEN ? != '' AND COALESCE(json_extract(NULLIF(image_source, ''), '$.provider'), '') != ? THEN '' ELSE image_source END,
		    disambiguation    = CASE WHEN ? != '' THEN ? ELSE disambiguation END,
		    metadata_provider = COALESCE(NULLIF(?, ''), metadata_provider),
		    updated_at        = ?
		WHERE id = ? AND foreign_id = ?`,
		target.ForeignID,                                // foreign_id =
		target.Name,                                     // name = COALESCE(NULLIF(?,''), name)
		target.SortName,                                 // sort_name = COALESCE(NULLIF(?,''), sort_name)
		target.SortName, authorSortKey(target.SortName), // sort_key CASE WHEN ?!='' THEN ? ELSE sort_key
		target.Name, authorSortKey(target.Name), // name_sort_key CASE WHEN ?!='' THEN ? ELSE name_sort_key
		target.Name, textutil.FoldForSearch(target.Name), // search_key CASE WHEN ?!='' THEN ? ELSE search_key
		// A bio or photo the user set by hand (provider models.ProfileSourceUser
		// in its credit column) is kept (#3287); a provider value that does
		// replace the stored one drops its credit, as models.MergeProfileField
		// does for a primary value.
		target.Description, models.ProfileSourceUser, target.Description, // description
		target.Description, models.ProfileSourceUser, // description_source cleared with it
		target.ImageURL, models.ProfileSourceUser, target.ImageURL, // image_url
		target.ImageURL, models.ProfileSourceUser, // image_source cleared with it
		target.Disambiguation, target.Disambiguation, // disambiguation
		target.MetadataProvider, // metadata_provider = COALESCE(NULLIF(?,''), metadata_provider)
		timeValueArg(now),       // updated_at
		authorID,                // WHERE id = ?
		currentForeignID)        // AND foreign_id = ?
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
	if err := r.upsertIdentifierTx(ctx, tx, authorID, target.ForeignID, now, true); err != nil {
		return err
	}
	if err := r.upsertIdentifierTx(ctx, tx, authorID, currentForeignID, now, false); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upgrade synthetic dnb: %w", err)
	}
	return nil
}

func (r *AuthorRepo) Update(ctx context.Context, a *models.Author) error {
	_, err := r.updateAuthor(ctx, a, "")
	return err
}

// UpdateIfUnchanged is Update guarded on the row not having changed since a
// was read: expectedUpdatedAt is the snapshot's UpdatedAtRaw, compared as
// stored text like BookRepo.UpdateIfUnchanged. It reports false, without
// writing anything (the author_identifiers row included) or reloading a,
// when a concurrent write got there first. For callers that write an author
// after a provider call, so an edit saved during the call is not undone
// (#2926).
func (r *AuthorRepo) UpdateIfUnchanged(ctx context.Context, a *models.Author, expectedUpdatedAt string) (bool, error) {
	if a == nil || a.ID == 0 || expectedUpdatedAt == "" {
		return false, fmt.Errorf("update author: invalid author snapshot")
	}
	return r.updateAuthor(ctx, a, expectedUpdatedAt)
}

// updateAuthor writes every column of a in one transaction with its
// identifier row. A non-empty expectedUpdatedAt adds the stored-text
// updated_at precondition.
func (r *AuthorRepo) updateAuthor(ctx context.Context, a *models.Author, expectedUpdatedAt string) (bool, error) {
	now := time.Now().UTC()
	normalizeAuthorMonitorDefaults(a)

	if _, ok := r.exec.(*sql.Tx); ok {
		updated, err := r.update(ctx, r.exec, a, now, expectedUpdatedAt)
		if err != nil || !updated {
			return false, err
		}
		a.UpdatedAt = now
		a.UpdatedAtRaw = timeValueText(now)
		return true, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin update author %d: %w", a.ID, err)
	}
	defer func() { _ = tx.Rollback() }()

	updated, err := r.update(ctx, tx, a, now, expectedUpdatedAt)
	if err != nil || !updated {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit update author %d: %w", a.ID, err)
	}
	a.UpdatedAt = now
	a.UpdatedAtRaw = timeValueText(now)
	return true, nil
}

func (r *AuthorRepo) update(ctx context.Context, exec dbExecutor, a *models.Author, now time.Time, expectedUpdatedAt string) (bool, error) {
	var oldPrimary string
	if err := exec.QueryRowContext(ctx, "SELECT foreign_id FROM authors WHERE id=?", a.ID).Scan(&oldPrimary); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read author %d: %w", a.ID, err)
	}
	query := `
		UPDATE authors SET foreign_id=?, name=?, sort_name=?, sort_key=?, name_sort_key=?, search_key=?, description=?, image_url=?, disambiguation=?,
		                   ratings_count=?, average_rating=?, monitored=?, quality_profile_id=?,
		                   metadata_profile_id=?, root_folder_id=?, audiobook_root_folder_id=?, monitor_mode=?,
		                   monitor_latest_count=?, monitor_new_items=?, metadata_provider=?, last_metadata_refresh_at=?, updated_at=?,
		                   description_source=?, image_source=?
		WHERE id=?`
	args := []any{
		a.ForeignID, a.Name, a.SortName, authorSortKey(a.SortName), authorSortKey(a.Name), textutil.FoldForSearch(a.Name), a.Description, a.ImageURL, a.Disambiguation,
		a.RatingsCount, a.AverageRating, a.Monitored, a.QualityProfileID,
		a.MetadataProfileID, a.RootFolderID, a.AudiobookRootFolderID, a.MonitorMode,
		a.MonitorLatestCount, a.MonitorNewItems, a.MetadataProvider, timeArg(a.LastMetadataRefreshAt), timeValueArg(now),
		encodeProfileSource(a.DescriptionSource), encodeProfileSource(a.ImageSource), a.ID,
	}
	if expectedUpdatedAt != "" {
		query += ` AND CAST(updated_at AS TEXT)=?`
		args = append(args, expectedUpdatedAt)
	}
	res, err := exec.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("update author %d: %w", a.ID, err)
	}
	if expectedUpdatedAt != "" {
		n, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("check update for author %d: %w", a.ID, err)
		}
		if n == 0 {
			return false, nil
		}
	}
	if err := r.upsertIdentifierTx(ctx, exec, a.ID, a.ForeignID, now, strings.TrimSpace(a.ForeignID) != strings.TrimSpace(oldPrimary)); err != nil {
		return false, err
	}
	return true, nil
}

// HasBooksNotOwnedBy reports whether any book filed under authorID, excluded
// books included, is owned by someone other than userID or by nobody. Deleting
// an author cascades to its books and merging one reparents them, so the API
// asks this before letting a user who is not an admin do either.
func (r *AuthorRepo) HasBooksNotOwnedBy(ctx context.Context, authorID, userID int64) (bool, error) {
	var n int
	if err := r.exec.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM books
		WHERE author_id = ? AND (owner_user_id IS NULL OR owner_user_id <> ?)`,
		authorID, userID).Scan(&n); err != nil {
		return false, fmt.Errorf("count books of author %d not owned by user %d: %w", authorID, userID, err)
	}
	return n > 0, nil
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
// derived ones without duplicating scanAuthorFrom's 23-destination list, which
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
	// updatedAtRaw is CAST(updated_at AS TEXT): the driver reformats a
	// DATETIME column it can parse, so only the cast keeps the stored text a
	// guarded write compares against (UpdateIfUnchanged).
	var lastMetadataRefreshAtStr, createdAtStr, updatedAtStr, updatedAtRaw sql.NullString
	var descriptionSource, imageSource string
	err := s.Scan(&a.ID, &a.ForeignID, &a.Name, &a.SortName, &a.Description, &a.ImageURL,
		&a.Disambiguation, &a.RatingsCount, &a.AverageRating, &monitored,
		&a.QualityProfileID, &a.MetadataProfileID, &a.RootFolderID, &a.AudiobookRootFolderID,
		&a.MonitorMode, &a.MonitorLatestCount, &a.MonitorNewItems, &a.MetadataProvider,
		&lastMetadataRefreshAtStr, &createdAtStr, &updatedAtStr, &a.OwnerUserID, &updatedAtRaw,
		&descriptionSource, &imageSource)
	if err != nil {
		return a, err
	}
	a.DescriptionSource = decodeProfileSource(descriptionSource)
	a.ImageSource = decodeProfileSource(imageSource)
	if lm, perr := parseFlexibleTime(lastMetadataRefreshAtStr); perr != nil {
		slog.Warn("unparseable author.last_metadata_refresh_at, leaving nil", "author_id", a.ID, "value", lastMetadataRefreshAtStr.String, "error", perr)
	} else {
		a.LastMetadataRefreshAt = lm
	}
	a.CreatedAt = parseFlexibleTimeValue(createdAtStr, "authors.created_at")
	a.UpdatedAt = parseFlexibleTimeValue(updatedAtStr, "authors.updated_at")
	a.UpdatedAtRaw = updatedAtRaw.String
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

// UnmonitoredAuthorIDs returns the set of author ids whose monitored flag is
// off. It is the whole table in one query rather than a per book lookup,
// because both callers need the answer for a whole list at once: the wanted
// sweep decides it for every wanted book on one tick (#2370 made loading a
// sweep invariant once rather than per book the rule), and the Wanted page
// decides it for every row of one response.
//
// Returns a set rather than a slice because every caller asks "is this id in
// it". Unmonitored is the smaller half in practice, and it is the half that
// changes behaviour, so a miss costs nothing.
func (r *AuthorRepo) UnmonitoredAuthorIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM authors WHERE monitored = 0`)
	if err != nil {
		return nil, fmt.Errorf("list unmonitored author ids: %w", err)
	}
	defer rows.Close()
	ids := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan unmonitored author id: %w", err)
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// encodeProfileSource stores an author profile credit (#3287) as JSON text,
// and nil as the empty string the column defaults to.
func encodeProfileSource(src *models.ProfileSource) string {
	if src == nil {
		return ""
	}
	b, err := json.Marshal(src)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeProfileSource reads a credit back. Empty or unreadable text is no
// credit: a malformed row must not stop the author from loading.
func decodeProfileSource(text string) *models.ProfileSource {
	if text == "" {
		return nil
	}
	var src models.ProfileSource
	if err := json.Unmarshal([]byte(text), &src); err != nil || src.Provider == "" {
		return nil
	}
	return &src
}
