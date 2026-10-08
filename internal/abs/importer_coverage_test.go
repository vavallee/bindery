package abs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func covImpConfig(libraryID string) ImportConfig {
	return ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: libraryID,
		Label:     "Shelf",
		Enabled:   true,
	}
}

func TestCovImpImporter_ClientFactoryAndUserAgent(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	imp := env.importer

	if got := imp.WithVersion("9.9.9").userAgent; !strings.Contains(got, "9.9.9") {
		t.Fatalf("WithVersion user agent = %q, want version embedded", got)
	}
	imp.WithUserAgent("  covimp-agent/1  ")
	if imp.userAgent != "covimp-agent/1" {
		t.Fatalf("WithUserAgent = %q, want trimmed value", imp.userAgent)
	}
	client, err := imp.defaultClient("https://abs.example.com", "secret")
	if err != nil {
		t.Fatalf("defaultClient: %v", err)
	}
	if c, ok := client.(*Client); !ok || c.userAgent != "covimp-agent/1" {
		t.Fatalf("defaultClient = %#v, want *Client carrying the importer user agent", client)
	}
	imp.WithUserAgent("   ")
	if imp.userAgent != UserAgent("") {
		t.Fatalf("blank WithUserAgent = %q, want default", imp.userAgent)
	}
	if _, err := imp.defaultClient("ftp://abs.example.com", "secret"); err == nil {
		t.Fatal("defaultClient accepted a non-http base URL")
	}

	// A factory failure fails the run and records the run as failed.
	imp.newClient = func(string, string) (enumerationClient, error) { return nil, errors.New("covimp no client") }
	if _, err := imp.Run(context.Background(), covImpConfig("lib-books")); err == nil || !strings.Contains(err.Error(), "covimp no client") {
		t.Fatalf("Run err = %v, want client factory error", err)
	}
	runs, err := imp.RecentRuns(context.Background(), 1)
	if err != nil || len(runs) != 1 || runs[0].Status != runStatusFailed {
		t.Fatalf("runs = %+v err=%v, want one failed run", runs, err)
	}
	if !strings.Contains(runs[0].SummaryJSON, "covimp no client") {
		t.Fatalf("summary = %s, want the error recorded", runs[0].SummaryJSON)
	}
}

func TestCovImpImporter_RunGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("already running", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.importer.running = true
		if _, err := env.importer.Run(ctx, covImpConfig("lib")); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("Run err = %v, want ErrAlreadyRunning", err)
		}
		if err := env.importer.Start(ctx, covImpConfig("lib")); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("Start err = %v, want ErrAlreadyRunning", err)
		}
	})

	t.Run("invalid config", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		cfg := covImpConfig("lib")
		cfg.Enabled = false
		if _, err := env.importer.Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("Run err = %v, want disabled", err)
		}
		if p := env.importer.Progress(); p.Running || p.Message != "failed" {
			t.Fatalf("progress = %+v, want stopped and failed", p)
		}
	})

	t.Run("alias cleanup failure", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.exec(t, "ALTER TABLE author_aliases RENAME TO covimp_aliases_gone")
		if _, err := env.importer.Run(ctx, covImpConfig("lib")); err == nil || !strings.Contains(err.Error(), "cleanup abs-sourced author aliases") {
			t.Fatalf("Run err = %v, want alias cleanup failure", err)
		}
	})

	t.Run("author matcher failure", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.exec(t, "ALTER TABLE authors RENAME TO covimp_authors_gone")
		if _, err := env.importer.Run(ctx, covImpConfig("lib")); err == nil {
			t.Fatal("Run with broken authors table returned nil error")
		}
		if _, err := env.importer.ImportReview(ctx, covImpConfig("lib"), sampleABSItem()); err == nil {
			t.Fatal("ImportReview with broken authors table returned nil error")
		}
	})

	t.Run("run create failure", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.failOn(t, "covimp_run_insert", "INSERT", "abs_import_runs", "")
		env.importer.enumerateFn = func(context.Context, string, func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
			t.Fatal("enumerator must not run without a run row")
			return EnumerationStats{}, nil
		}
		if _, err := env.importer.Run(ctx, covImpConfig("lib")); err == nil || !strings.Contains(err.Error(), "covimp boom") {
			t.Fatalf("Run err = %v, want run insert failure", err)
		}
	})

	t.Run("cancelled before item", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		env.importer.enumerateFn = func(_ context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
			return EnumerationStats{}, fn(cancelled, sampleABSItem())
		}
		_, err := env.importer.Run(ctx, covImpConfig("lib-books"))
		if !errors.Is(err, context.Canceled) && (err == nil || !strings.Contains(err.Error(), "canceled")) {
			t.Fatalf("Run err = %v, want cancellation", err)
		}
		if n := env.count(t, "SELECT COUNT(*) FROM books"); n != 0 {
			t.Fatalf("books = %d, a cancelled item must not import", n)
		}
	})
}

func TestCovImpImporter_ItemTimeoutMarksItemFailedAndContinues(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	env.importer.itemTimeout = time.Nanosecond
	stats, err := env.importer.Run(context.Background(), func() ImportConfig {
		env.importer.enumerateFn = func(ctx context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
			return EnumerationStats{ItemsSeen: 1}, fn(ctx, sampleABSItem())
		}
		return covImpConfig("lib-books")
	}())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want the timed-out item counted failed", stats)
	}
	results := env.importer.Progress().Results
	if len(results) != 1 || results[0].Outcome != itemOutcomeFailed || !strings.Contains(results[0].Message, "timed out after") {
		t.Fatalf("results = %+v, want one timed-out failure", results)
	}
	if got := env.importer.effectiveItemTimeout(); got != time.Nanosecond {
		t.Fatalf("effectiveItemTimeout = %v", got)
	}
	if got := (&Importer{}).effectiveItemTimeout(); got != defaultItemTimeout {
		t.Fatalf("default effectiveItemTimeout = %v, want %v", got, defaultItemTimeout)
	}

	already := markItemTimedOut(ImportItemResult{Outcome: itemOutcomeFailed}, &ImportStats{Failed: 1}, time.Second)
	if already.Outcome != itemOutcomeFailed {
		t.Fatalf("markItemTimedOut = %+v", already)
	}
}

func TestCovImpImporter_ResumeInterrupted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	seedRunning := func(t *testing.T, env *covImpEnv, run models.ABSImportRun) *models.ABSImportRun {
		t.Helper()
		run.Status = runStatusRunning
		if run.SummaryJSON == "" {
			run.SummaryJSON = "{}"
		}
		if err := env.runs.Create(ctx, &run); err != nil {
			t.Fatalf("Create run: %v", err)
		}
		return &run
	}

	t.Run("no runs repo", func(t *testing.T) {
		t.Parallel()
		if resumed, err := (&Importer{}).ResumeInterrupted(ctx, ImportConfig{}); resumed || err != nil {
			t.Fatalf("ResumeInterrupted = %v %v", resumed, err)
		}
	})

	t.Run("nothing to resume", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		if resumed, err := env.importer.ResumeInterrupted(ctx, ImportConfig{}); resumed || err != nil {
			t.Fatalf("ResumeInterrupted = %v %v", resumed, err)
		}
	})

	t.Run("lookup error", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.exec(t, "ALTER TABLE abs_import_runs RENAME TO covimp_runs_gone")
		if _, err := env.importer.ResumeInterrupted(ctx, ImportConfig{}); err == nil {
			t.Fatal("ResumeInterrupted with broken table returned nil error")
		}
	})

	t.Run("malformed checkpoint", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		seedRunning(t, env, models.ABSImportRun{LibraryID: "lib", BaseURL: "https://abs.example.com", CheckpointJSON: "{broken"})
		resumed, err := env.importer.ResumeInterrupted(ctx, ImportConfig{})
		if !resumed || err == nil || !strings.Contains(err.Error(), "decode interrupted abs import checkpoint") {
			t.Fatalf("ResumeInterrupted = %v %v, want decode error", resumed, err)
		}
	})

	t.Run("checkpoint restore fails", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		seedRunning(t, env, models.ABSImportRun{LibraryID: "lib", BaseURL: "https://abs.example.com", CheckpointJSON: `{"libraryId":"lib","page":2}`})
		env.failOn(t, "covimp_settings_insert", "INSERT", "settings", "")
		resumed, err := env.importer.ResumeInterrupted(ctx, ImportConfig{})
		if !resumed || err == nil || !strings.Contains(err.Error(), "restore abs import checkpoint") {
			t.Fatalf("ResumeInterrupted = %v %v, want restore error", resumed, err)
		}
	})

	t.Run("finish fails", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		seedRunning(t, env, models.ABSImportRun{LibraryID: "lib", BaseURL: "https://abs.example.com", CheckpointJSON: `{"libraryId":"lib","page":2}`})
		env.failOn(t, "covimp_runs_update", "UPDATE", "abs_import_runs", "")
		resumed, err := env.importer.ResumeInterrupted(ctx, ImportConfig{})
		if !resumed || err == nil || !strings.Contains(err.Error(), "failed") {
			t.Fatalf("ResumeInterrupted = %v %v, want finish error", resumed, err)
		}
	})

	t.Run("invalid resumed config", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		run := seedRunning(t, env, models.ABSImportRun{LibraryID: "lib", CheckpointJSON: `{"libraryId":"lib","page":2}`})
		resumed, err := env.importer.ResumeInterrupted(ctx, ImportConfig{Enabled: true})
		if !resumed || err == nil || !strings.Contains(err.Error(), "resume abs import run") {
			t.Fatalf("ResumeInterrupted = %v %v, want validation error", resumed, err)
		}
		got, _ := env.runs.GetByID(ctx, run.ID)
		if got == nil || got.Status != runStatusFailed {
			t.Fatalf("interrupted run = %+v, want marked failed before validation", got)
		}
	})

	t.Run("start refused", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		seedRunning(t, env, models.ABSImportRun{LibraryID: "lib", BaseURL: "https://abs.example.com", CheckpointJSON: `{"libraryId":"lib","page":2}`})
		env.importer.running = true
		resumed, err := env.importer.ResumeInterrupted(ctx, ImportConfig{APIKey: "secret", Enabled: true})
		if !resumed || !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("ResumeInterrupted = %v %v, want ErrAlreadyRunning", resumed, err)
		}
	})
}

func TestCovImpImporter_SmallHelpers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for reason, want := range map[string]string{
		reviewReasonAmbiguousAuthor: "author match is ambiguous",
		reviewReasonUnmatchedAuthor: "author match not found",
		reviewReasonAmbiguousBook:   "book match is ambiguous",
		reviewReasonUnmatchedBook:   "book match not found",
		"other":                     "review required",
	} {
		if got := (reviewRequiredError{Reason: reason}).Error(); got != want {
			t.Fatalf("reviewRequiredError(%q) = %q, want %q", reason, got, want)
		}
	}

	item := sampleABSItem()
	for reason, want := range map[string]string{
		reviewReasonUnmatchedAuthor: `no confident author match for "Andy Weir"`,
		reviewReasonAmbiguousAuthor: `multiple author matches for "Andy Weir"`,
		reviewReasonUnmatchedBook:   `no confident book match for "Project Hail Mary"`,
		reviewReasonAmbiguousBook:   `multiple book matches for "Project Hail Mary"`,
		"other":                     "queued for review",
	} {
		if got := reviewQueueMessage(reason, item); !strings.Contains(got, want) {
			t.Fatalf("reviewQueueMessage(%q) = %q, want %q", reason, got, want)
		}
	}

	var reviewErr reviewRequiredError
	if err := (&Importer{}).queueReviewItem(ctx, 0, ImportConfig{}, item, reviewReasonUnmatchedBook); !errors.As(err, &reviewErr) || reviewErr.Reason != reviewReasonUnmatchedBook {
		t.Fatalf("queueReviewItem without repo = %v, want reviewRequiredError", err)
	}

	if got := libraryStartIndex([]string{"a", "b"}, "  "); got != 0 {
		t.Fatalf("libraryStartIndex blank = %d", got)
	}
	if got := libraryStartIndex([]string{"a", "b"}, "b"); got != 1 {
		t.Fatalf("libraryStartIndex b = %d", got)
	}
	if got := libraryStartIndex([]string{"a", "b"}, "zzz"); got != 0 {
		t.Fatalf("libraryStartIndex unknown = %d", got)
	}
	if s := snapshotImportStats(nil); s.String() != (ImportStats{}).String() {
		t.Fatalf("snapshotImportStats(nil) = %+v", s)
	}
	if d := diffImportStats(ImportStats{Failed: 3}, nil); d.Failed != 0 || d.String() != (ImportStats{}).String() {
		t.Fatalf("diffImportStats(nil) = %+v", d)
	}
	if s := (ImportStats{BooksCreated: 2}).String(); !strings.Contains(s, `"booksCreated":2`) {
		t.Fatalf("ImportStats.String = %s", s)
	}

	if cp, err := loadImportCheckpoint(ctx, nil); cp != nil || err != nil {
		t.Fatalf("loadImportCheckpoint(nil) = %v %v", cp, err)
	}
	if _, err := decodeImportCheckpoint("{nope"); err == nil {
		t.Fatal("decodeImportCheckpoint accepted malformed JSON")
	}
	if cp, err := decodeImportCheckpoint(" null "); cp != nil || err != nil {
		t.Fatalf("decodeImportCheckpoint(null) = %v %v", cp, err)
	}

	env := covImpNewEnv(t)
	if err := env.settings.Set(ctx, SettingABSImportCheckpoint, `{"libraryId":"lib-b","page":4}`); err != nil {
		t.Fatalf("Set checkpoint: %v", err)
	}
	if got := resumeLibraryIDFromCheckpoint(ctx, env.settings, []string{"lib-a", "lib-b"}, "lib-a"); got != "lib-b" {
		t.Fatalf("resume library = %q, want lib-b", got)
	}
	if got := resumeLibraryIDFromCheckpoint(ctx, env.settings, []string{"lib-a"}, "lib-a"); got != "lib-a" {
		t.Fatalf("resume library outside list = %q, want fallback", got)
	}
	if err := env.settings.Set(ctx, SettingABSImportCheckpoint, `{"libraryId":"  ","page":4}`); err != nil {
		t.Fatalf("Set checkpoint: %v", err)
	}
	if got := resumeLibraryIDFromCheckpoint(ctx, env.settings, []string{"lib-a"}, "lib-a"); got != "lib-a" {
		t.Fatalf("resume library blank = %q, want fallback", got)
	}

	if err := env.importer.recordRunEntity(ctx, 1, ImportConfig{}, "lib", "item", entityTypeBook, "ext", 1, itemOutcomeCreated, map[string]any{"bad": make(chan int)}); err == nil || !strings.Contains(err.Error(), "encode abs import run entity metadata") {
		t.Fatalf("recordRunEntity err = %v, want encode failure", err)
	}
	if got, err := encodeJSON(nil); got != "{}" || err != nil {
		t.Fatalf("encodeJSON(nil) = %q %v", got, err)
	}
}

func TestCovImpResumeConfigFromRun(t *testing.T) {
	t.Parallel()
	fallback := ImportConfig{SourceID: "fb", BaseURL: "https://fallback.example.com", APIKey: "k", Label: "Fallback", Enabled: true}

	// Malformed source JSON falls back to the run's columns.
	cfg := resumeConfigFromRun(models.ABSImportRun{
		SourceID: "src", BaseURL: "https://run.example.com", LibraryID: "lib-run", SourceLabel: "Run Label",
		SourceConfigJSON: "{broken", DryRun: true,
	}, fallback)
	if cfg.SourceID != "src" || cfg.BaseURL != "https://run.example.com" || cfg.LibraryID != "lib-run" || cfg.Label != "Run Label" || !cfg.DryRun {
		t.Fatalf("cfg = %+v, want run columns", cfg)
	}
	if cfg.APIKey != "k" {
		t.Fatalf("api key = %q, want fallback kept", cfg.APIKey)
	}

	// Empty run columns keep the fallback.
	cfg = resumeConfigFromRun(models.ABSImportRun{SourceConfigJSON: "null"}, fallback)
	if cfg.SourceID != "fb" || cfg.BaseURL != "https://fallback.example.com" || cfg.Label != "Fallback" {
		t.Fatalf("cfg = %+v, want fallback", cfg)
	}
}

func TestCovImpReviewFileMappingWithoutPaths(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	got := env.importer.ReviewFileMapping(context.Background(), covImpConfig("lib"), NormalizedLibraryItem{ItemID: "x", Path: "/abs/x"})
	if got.Found || got.Message != "no ABS file paths available" {
		t.Fatalf("ReviewFileMapping = %+v, want no paths message", got)
	}
}

func TestCovImpEnrichAudiobookFromASINGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	// No metadata aggregator: nothing happens, and nothing panics.
	book := &models.Book{ID: 1, ASIN: "B0", MediaType: models.MediaTypeAudiobook}
	env.importer.enrichAudiobookFromASIN(ctx, book)
	env.importer.enrichAudiobookFromASIN(ctx, nil)
	if book.Narrator != "" || book.DurationSeconds != 0 {
		t.Fatalf("book = %+v, want untouched", book)
	}
}
