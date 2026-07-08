package db

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func openLogDB(t *testing.T) (*LogRepo, func()) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	return NewLogRepo(database), func() { database.Close() }
}

func insertEntry(t *testing.T, repo *LogRepo, ts time.Time, level, component, msg string) {
	t.Helper()
	err := repo.Insert(context.Background(), LogEntry{
		TS:        ts,
		Level:     level,
		Component: component,
		Message:   msg,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestLogRepo_InsertAndQuery(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	now := time.Now().UTC()
	insertEntry(t, repo, now.Add(-2*time.Second), "INFO", "scheduler", "job started")
	insertEntry(t, repo, now.Add(-1*time.Second), "WARN", "downloader", "retry")
	insertEntry(t, repo, now, "ERROR", "scheduler", "job failed")

	entries, err := repo.Query(context.Background(), LogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// newest-first
	if entries[0].Level != "ERROR" {
		t.Errorf("expected first entry ERROR, got %s", entries[0].Level)
	}
}

func TestLogRepo_QueryByLevel(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	now := time.Now().UTC()
	insertEntry(t, repo, now.Add(-3*time.Second), "DEBUG", "api", "debug msg")
	insertEntry(t, repo, now.Add(-2*time.Second), "INFO", "api", "info msg")
	insertEntry(t, repo, now.Add(-1*time.Second), "WARN", "api", "warn msg")
	insertEntry(t, repo, now, "ERROR", "api", "error msg")

	entries, err := repo.Query(context.Background(), LogFilter{
		HasLevel: true,
		Level:    slog.LevelWarn,
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries >= WARN, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Level != "WARN" && e.Level != "ERROR" {
			t.Errorf("unexpected level %q", e.Level)
		}
	}
}

func TestLogRepo_QueryByComponent(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	now := time.Now().UTC()
	insertEntry(t, repo, now.Add(-2*time.Second), "INFO", "scheduler", "msg1")
	insertEntry(t, repo, now.Add(-1*time.Second), "INFO", "downloader", "msg2")
	insertEntry(t, repo, now, "INFO", "scheduler", "msg3")

	entries, err := repo.Query(context.Background(), LogFilter{
		Component: "scheduler",
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 scheduler entries, got %d", len(entries))
	}
}

func TestLogRepo_QueryByDateRange(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	base := time.Now().UTC().Truncate(time.Second)
	insertEntry(t, repo, base.Add(-10*time.Minute), "INFO", "", "old")
	insertEntry(t, repo, base.Add(-5*time.Minute), "INFO", "", "mid")
	insertEntry(t, repo, base, "INFO", "", "new")

	entries, err := repo.Query(context.Background(), LogFilter{
		FromTS: base.Add(-6 * time.Minute),
		ToTS:   base.Add(-4 * time.Minute),
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry in range, got %d", len(entries))
	}
	if entries[0].Message != "mid" {
		t.Errorf("unexpected message %q", entries[0].Message)
	}
}

func TestLogRepo_QueryFullText(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	now := time.Now().UTC()
	insertEntry(t, repo, now.Add(-2*time.Second), "INFO", "", "book download started")
	insertEntry(t, repo, now.Add(-1*time.Second), "INFO", "", "metadata refresh")
	insertEntry(t, repo, now, "ERROR", "", "book download failed")

	entries, err := repo.Query(context.Background(), LogFilter{
		Q:     "download",
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries matching 'download', got %d", len(entries))
	}
}

// A search term containing LIKE metacharacters (%, _) must match literally,
// not act as a wildcard (#1466).
func TestLogRepo_QueryEscapesLikeWildcards(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	now := time.Now().UTC()
	insertEntry(t, repo, now.Add(-2*time.Second), "INFO", "", "job 100% complete")
	insertEntry(t, repo, now.Add(-1*time.Second), "INFO", "", "job aborted at start")
	insertEntry(t, repo, now, "INFO", "", "user_id resolved")
	// Decoy: an unescaped "_" wildcard would also match this "userXid" row.
	insertEntry(t, repo, now, "INFO", "", "userXid resolved")

	// "%" must be literal: it should match only the "100% complete" row, not
	// every row (which is what an unescaped "%...%" wildcard would do).
	entries, err := repo.Query(context.Background(), LogFilter{Q: "100%", Limit: 100})
	if err != nil {
		t.Fatalf("query %%: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry matching literal '100%%', got %d", len(entries))
	}
	if entries[0].Message != "job 100% complete" {
		t.Errorf("matched wrong row: %q", entries[0].Message)
	}

	// "_" must be literal: it should match "user_id", not "userXid".
	entries, err = repo.Query(context.Background(), LogFilter{Q: "user_id", Limit: 100})
	if err != nil {
		t.Fatalf("query _: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry matching literal 'user_id', got %d", len(entries))
	}
}

func TestLogRepo_Trim(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	now := time.Now().UTC()
	insertEntry(t, repo, now.Add(-20*24*time.Hour), "INFO", "", "old entry")
	insertEntry(t, repo, now.Add(-10*24*time.Hour), "INFO", "", "borderline entry")
	insertEntry(t, repo, now, "INFO", "", "new entry")

	cutoff := now.Add(-15 * 24 * time.Hour)
	if err := repo.Trim(context.Background(), cutoff); err != nil {
		t.Fatalf("trim: %v", err)
	}

	entries, err := repo.Query(context.Background(), LogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("query after trim: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after trim, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Message == "old entry" {
			t.Errorf("old entry survived trim")
		}
	}
}

func TestLogRepo_Fields(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	err := repo.Insert(context.Background(), LogEntry{
		TS:      time.Now().UTC(),
		Level:   "INFO",
		Message: "test fields",
		Fields:  map[string]string{"key": "value", "book": "Dune"},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	entries, err := repo.Query(context.Background(), LogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Fields["book"] != "Dune" {
		t.Errorf("expected Fields[book]=Dune, got %q", entries[0].Fields["book"])
	}
}

func TestLogRepo_ErrorSummary(t *testing.T) {
	repo, cleanup := openLogDB(t)
	defer cleanup()

	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)

	t.Run("empty store", func(t *testing.T) {
		errs, warns, top, err := repo.ErrorSummary(context.Background(), since, 5)
		if err != nil {
			t.Fatalf("ErrorSummary: %v", err)
		}
		if errs != 0 || warns != 0 || len(top) != 0 {
			t.Errorf("expected all-zero summary, got errs=%d warns=%d top=%v", errs, warns, top)
		}
	})

	// Inside the window: 3x "indexer search failed", 2x "import failed",
	// 1x "db locked", plus 2 WARNs and 1 INFO (which must not count).
	for i := 0; i < 3; i++ {
		insertEntry(t, repo, now.Add(-time.Duration(i+1)*time.Minute), "ERROR", "indexer", "indexer search failed")
	}
	for i := 0; i < 2; i++ {
		insertEntry(t, repo, now.Add(-time.Duration(i+1)*time.Hour), "ERROR", "importer", "import failed")
	}
	insertEntry(t, repo, now.Add(-2*time.Hour), "ERROR", "db", "db locked")
	insertEntry(t, repo, now.Add(-3*time.Hour), "WARN", "downloader", "retrying download")
	insertEntry(t, repo, now.Add(-4*time.Hour), "WARN", "downloader", "retrying download")
	insertEntry(t, repo, now.Add(-5*time.Hour), "INFO", "scheduler", "job started")

	// Outside the window: must be excluded.
	insertEntry(t, repo, now.Add(-25*time.Hour), "ERROR", "indexer", "indexer search failed")
	insertEntry(t, repo, now.Add(-48*time.Hour), "WARN", "downloader", "retrying download")

	t.Run("counts and ordering", func(t *testing.T) {
		errs, warns, top, err := repo.ErrorSummary(context.Background(), since, 5)
		if err != nil {
			t.Fatalf("ErrorSummary: %v", err)
		}
		if errs != 6 {
			t.Errorf("error count = %d, want 6", errs)
		}
		if warns != 2 {
			t.Errorf("warn count = %d, want 2", warns)
		}
		want := []LogMessageCount{
			{Message: "indexer search failed", Count: 3},
			{Message: "import failed", Count: 2},
			{Message: "db locked", Count: 1},
		}
		if len(top) != len(want) {
			t.Fatalf("top = %v, want %v", top, want)
		}
		for i := range want {
			if top[i] != want[i] {
				t.Errorf("top[%d] = %v, want %v", i, top[i], want[i])
			}
		}
	})

	t.Run("topN limit", func(t *testing.T) {
		_, _, top, err := repo.ErrorSummary(context.Background(), since, 2)
		if err != nil {
			t.Fatalf("ErrorSummary: %v", err)
		}
		if len(top) != 2 {
			t.Fatalf("expected 2 top entries, got %d", len(top))
		}
		if top[0].Message != "indexer search failed" || top[1].Message != "import failed" {
			t.Errorf("unexpected top-2 ordering: %v", top)
		}
	})

	t.Run("deterministic tie-break", func(t *testing.T) {
		// "db locked" and a new "abs sync failed" both have count 1; the
		// tie must break alphabetically so repeated pings are stable.
		insertEntry(t, repo, now.Add(-6*time.Hour), "ERROR", "abs", "abs sync failed")
		_, _, top, err := repo.ErrorSummary(context.Background(), since, 5)
		if err != nil {
			t.Fatalf("ErrorSummary: %v", err)
		}
		if len(top) != 4 {
			t.Fatalf("expected 4 top entries, got %d", len(top))
		}
		if top[2].Message != "abs sync failed" || top[3].Message != "db locked" {
			t.Errorf("tie-break ordering wrong: %v", top)
		}
	})
}
