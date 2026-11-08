package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// recordingExecutor passes every statement through to the database and keeps
// the read queries, so a plan test can EXPLAIN exactly what a repo method ran
// rather than a copy of its SQL that could drift from it.
type recordingExecutor struct {
	db      *sql.DB
	queries []recordedQuery
}

type recordedQuery struct {
	sql  string
	args []any
}

func (e *recordingExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return e.db.ExecContext(ctx, query, args...)
}

func (e *recordingExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	e.queries = append(e.queries, recordedQuery{query, args})
	return e.db.QueryContext(ctx, query, args...)
}

func (e *recordingExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	e.queries = append(e.queries, recordedQuery{query, args})
	return e.db.QueryRowContext(ctx, query, args...)
}

// Migrations 127 and 128 each add one index; both must be there after
// migrating.
func TestMigrate127And128IndexesExist(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, index := range []string{"idx_books_language_excluded", "idx_calibre_snapshots_created_book_file"} {
		var n int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("index %s is missing after migrating", index)
		}
	}
}

// assertBooksByLanguageIndex fails unless every access to books in plan goes
// through idx_books_language_excluded, never a scan of the table or another
// index.
func assertBooksByLanguageIndex(t *testing.T, name, plan string) {
	t.Helper()
	if !strings.Contains(plan, "idx_books_language_excluded") {
		t.Errorf("%s does not use idx_books_language_excluded:\n%s", name, plan)
	}
	for _, line := range strings.Split(plan, "\n") {
		if strings.HasPrefix(line, "SCAN books") && !strings.Contains(line, "idx_books_language_excluded") {
			t.Errorf("%s scans books: %q\n%s", name, line, plan)
		}
	}
}

// LanguageCounts runs on every Books page mount (#1160). Without migration 127
// it scanned every book; it now reads the narrower covering index instead.
func TestLanguageCounts_PlanUsesLanguageIndex(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, userID := range []int64{0, 7} {
		rec := &recordingExecutor{db: database}
		repo := NewBookRepo(database)
		repo.exec = rec
		if _, err := repo.LanguageCounts(context.Background(), userID); err != nil {
			t.Fatal(err)
		}
		if len(rec.queries) != 1 {
			t.Fatalf("LanguageCounts ran %d queries, want 1", len(rec.queries))
		}
		q := rec.queries[0]
		plan := strings.Join(explainPlan(t, database, q.sql, q.args...), "\n")
		t.Logf("LanguageCounts user %d plan:\n%s", userID, plan)
		if userID == 0 {
			assertBooksByLanguageIndex(t, "LanguageCounts", plan)
			continue
		}
		// Scoped to one user, the owner index narrows the rows further and
		// the planner may keep it; either way the table is not scanned.
		for _, line := range strings.Split(plan, "\n") {
			if line == "SCAN books" {
				t.Errorf("scoped LanguageCounts scans books:\n%s", plan)
			}
		}
	}
}

// A Books page filtered to one language, and the default page's count, both
// read books through idx_books_language_excluded: at 50,000 books the filtered
// page went from 14 ms to 0.26 ms and the default count from 5.8 ms to 2.4 ms.
func TestListPageFiltered_LanguagePlanUsesLanguageIndex(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, userID := range []int64{0, 7} {
		rec := &recordingExecutor{db: database}
		repo := NewBookRepo(database)
		repo.exec = rec
		if _, _, err := repo.ListPageFiltered(context.Background(), BookListFilter{UserID: userID, Language: "en"}, 50, 0); err != nil {
			t.Fatal(err)
		}
		if len(rec.queries) != 2 {
			t.Fatalf("ListPageFiltered ran %d queries, want 2 (count and page)", len(rec.queries))
		}
		for i, name := range []string{"filtered count", "filtered page"} {
			q := rec.queries[i]
			plan := strings.Join(explainPlan(t, database, q.sql, q.args...), "\n")
			assertBooksByLanguageIndex(t, name, plan)
			t.Logf("%s user %d plan:\n%s", name, userID, plan)
		}
	}

	// The default page's count has only excluded = 0 and the owner scope.
	rec := &recordingExecutor{db: database}
	repo := NewBookRepo(database)
	repo.exec = rec
	if _, _, err := repo.ListPageFiltered(context.Background(), BookListFilter{}, 50, 0); err != nil {
		t.Fatal(err)
	}
	q := rec.queries[0]
	if !strings.HasPrefix(q.sql, "SELECT COUNT(*)") {
		t.Fatalf("first query is not the count: %s", q.sql)
	}
	plan := strings.Join(explainPlan(t, database, q.sql, q.args...), "\n")
	assertBooksByLanguageIndex(t, "default count", plan)
	t.Logf("default count plan:\n%s", plan)
}

// A page that names no language must not be drawn to idx_books_language_excluded.
// Every Books page query carries excluded = 0, which matches nearly every row,
// and Bindery never runs ANALYZE, so the planner takes an equality on a leading
// column as selective. With the index's columns in the other order (excluded
// first) the default page dropped its ordered walk of idx_books_sort_key and
// sorted the whole library instead (7 ms to 34 ms at 50,000 books), and the
// status and format filters lost their own indexes.
func TestListPageFiltered_NoLanguagePageKeepsItsIndexes(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for name, f := range map[string]BookListFilter{
		"default":   {},
		"wanted":    {Status: "wanted"},
		"audiobook": {MediaType: "audiobook"},
		"newest":    {Sort: "date-new"},
	} {
		rec := &recordingExecutor{db: database}
		repo := NewBookRepo(database)
		repo.exec = rec
		if _, _, err := repo.ListPageFiltered(context.Background(), f, 50, 0); err != nil {
			t.Fatal(err)
		}
		if len(rec.queries) != 2 {
			t.Fatalf("%s: ListPageFiltered ran %d queries, want 2 (count and page)", name, len(rec.queries))
		}
		q := rec.queries[1]
		plan := strings.Join(explainPlan(t, database, q.sql, q.args...), "\n")
		if strings.Contains(plan, "idx_books_language_excluded") {
			t.Errorf("%s page reads books through idx_books_language_excluded:\n%s", name, plan)
		}
		if name == "default" && !strings.Contains(plan, "idx_books_sort_key") {
			t.Errorf("default page no longer walks idx_books_sort_key:\n%s", plan)
		}
	}
}
