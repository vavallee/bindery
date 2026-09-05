package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// captureLogs redirects the default slog logger into a buffer for the duration
// of the test and returns the buffer.
//
// The level is deliberately Info, not Debug: #2154 asks for evidence at the
// level an operator actually runs at, so a completion line demoted to Debug
// must fail these tests rather than pass them.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func newEmptyScheduler(t *testing.T) *Scheduler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Scheduler{
		searcher:  indexer.NewSearcher(),
		indexers:  db.NewIndexerRepo(database),
		authors:   db.NewAuthorRepo(database),
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
	}
}

// A search that finds nothing used to return in silence, so a user could not
// tell a sweep that ran and found nothing from one that never ran at all
// (#2154). Every automatic search must leave exactly one completion line.
func TestSearchAndGrabBook_LogsCompletionWhenNothingIsFound(t *testing.T) {
	s := newEmptyScheduler(t)
	buf := captureLogs(t)

	s.SearchAndGrabBook(context.Background(), models.Book{
		ID: 42, Title: "Dune", MediaType: models.MediaTypeEbook,
	})

	out := buf.String()
	if n := strings.Count(out, "book search finished"); n != 1 {
		t.Fatalf("want exactly one completion line, got %d:\n%s", n, out)
	}
	for _, want := range []string{
		"book=Dune", "book_id=42", "format=ebook",
		"indexers=0", "raw_results=0", "after_filters=0", "approved=0",
		`outcome="no results"`, "elapsed_ms=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("completion line is missing %q:\n%s", want, out)
		}
	}
}

// The origin is the field that answers "did the thing I just clicked run?",
// so it has to survive the trip from the handler's fan-out into the scheduler.
func TestSearchAndGrabBook_LogsTheOriginItWasGiven(t *testing.T) {
	cases := map[string]indexer.SearchOrigin{
		"scheduled":      indexer.OriginScheduled,
		"bulk":           indexer.OriginBulk,
		"series-fill":    indexer.OriginSeriesFill,
		"author":         indexer.OriginAuthor,
		"book":           indexer.OriginBook,
		"recommendation": indexer.OriginRecommendation,
		"requeue":        indexer.OriginRequeue,
	}
	for want, origin := range cases {
		t.Run(want, func(t *testing.T) {
			s := newEmptyScheduler(t)
			buf := captureLogs(t)

			ctx := indexer.WithSearchOrigin(context.Background(), origin)
			s.SearchAndGrabBook(ctx, models.Book{ID: 1, Title: "Dune", MediaType: models.MediaTypeEbook})

			if got := buf.String(); !strings.Contains(got, "origin="+want) {
				t.Fatalf("want origin=%s in the completion line, got:\n%s", want, got)
			}
		})
	}
}

// An untagged caller must still produce a line. "unknown" is a real value
// rather than an empty field so a gap in the tagging reads as a gap.
func TestSearchAndGrabBook_UntaggedCallerLogsUnknownOrigin(t *testing.T) {
	s := newEmptyScheduler(t)
	buf := captureLogs(t)

	s.SearchAndGrabBook(context.Background(), models.Book{ID: 1, Title: "Dune", MediaType: models.MediaTypeEbook})

	if got := buf.String(); !strings.Contains(got, "origin=unknown") {
		t.Fatalf("want origin=unknown, got:\n%s", got)
	}
}

// The kill switch returns before searchAndGrabFormat is reached, so it keeps
// its own dedicated line and must not also claim a search finished.
func TestSearchAndGrabBook_DisabledAutoGrabDoesNotClaimASearchRan(t *testing.T) {
	s := newEmptyScheduler(t)
	if err := s.settings.Set(context.Background(), "autoGrab.enabled", "false"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	buf := captureLogs(t)

	s.SearchAndGrabBook(context.Background(), models.Book{ID: 1, Title: "Dune", MediaType: models.MediaTypeEbook})

	out := buf.String()
	if strings.Contains(out, "book search finished") {
		t.Fatalf("a skipped search must not log a completion line:\n%s", out)
	}
	if !strings.Contains(out, "auto-grab disabled globally") {
		t.Fatalf("the skip must still be logged:\n%s", out)
	}
}

// A "both" book searches twice, once per format, and each search is its own
// event: one line each, not one line for the pair.
func TestSearchAndGrabBook_LogsOncePerFormat(t *testing.T) {
	s := newEmptyScheduler(t)
	buf := captureLogs(t)

	s.SearchAndGrabBook(context.Background(), models.Book{
		ID: 7, Title: "Dune", MediaType: models.MediaTypeBoth,
	})

	out := buf.String()
	if n := strings.Count(out, "book search finished"); n != 2 {
		t.Fatalf("want one completion line per format, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "format=ebook") || !strings.Contains(out, "format=audiobook") {
		t.Fatalf("both formats must be named:\n%s", out)
	}
}

// TestEverySearchCallSiteTagsItsOrigin is the drift guard for the origin field.
//
// The completion line is only useful if "origin" says something. A new fan-out
// added without a WithSearchOrigin wrapper still logs, but logs "unknown",
// which is exactly the ambiguity #2154 was filed about. Rather than trust every
// future caller to remember, assert the structural property: in production
// code, every call of SearchAndGrabBook passes a context that was tagged on the
// spot.
//
// The check is textual because the call sites live in three packages and the
// tag is applied inline at each one. A caller that legitimately cannot tag on
// the same line should tag its context earlier and be added to the exemption
// list here, with a comment saying why.
func TestEverySearchCallSiteTagsItsOrigin(t *testing.T) {
	// Only the two source trees that ship. Walking the repo root would also
	// pick up .claude/worktrees, which holds stale copies of these same files.
	roots := []string{filepath.Join("..", "..", "cmd"), filepath.Join("..", "..", "internal")}
	var untagged []string

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path) //nolint:gosec // test-only walk of the repo's own source
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(raw), "\n") {
				trimmed := strings.TrimSpace(line)
				if !strings.Contains(trimmed, "SearchAndGrabBook(") {
					continue
				}
				switch {
				case strings.HasPrefix(trimmed, "//"):
					// Prose about the call, not the call.
					continue
				case strings.Contains(trimmed, "func "):
					// The method declaration.
					continue
				case strings.Contains(trimmed, "context.Context"):
					// The BookSearcher interface method in api/helpers.go.
					continue
				case strings.Contains(trimmed, "WithSearchOrigin("):
					continue
				}
				untagged = append(untagged, fmt.Sprintf("%s:%d: %s", path, i+1, trimmed))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v (this guard needs the full checkout)", root, err)
		}
	}

	if len(untagged) > 0 {
		t.Fatalf("these searches would log origin=unknown; wrap the context in indexer.WithSearchOrigin (#2154):\n  %s",
			strings.Join(untagged, "\n  "))
	}
}
