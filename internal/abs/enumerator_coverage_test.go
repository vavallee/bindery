package abs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func covImpPage(libraryID string, items ...LibraryItem) *LibraryItemsPage {
	return &LibraryItemsPage{MediaType: "book", Results: items, Limit: 10, Total: len(items)}
}

func TestCovImpEnumerator_ConstructionDefaults(t *testing.T) {
	t.Parallel()
	e := NewEnumerator(&fakeEnumerationClient{}, nil, 0)
	if e.pageSize != 50 {
		t.Fatalf("pageSize = %d, want default 50", e.pageSize)
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e.WithClock(nil)
	if e.now == nil {
		t.Fatal("WithClock(nil) cleared the clock")
	}
	e.WithClock(func() time.Time { return fixed })
	if !e.now().Equal(fixed) {
		t.Fatalf("clock = %v, want injected %v", e.now(), fixed)
	}
	cp := newCheckpointer(1, time.Hour, nil, func(context.Context, ImportCheckpoint) error { return nil })
	if cp.now == nil || cp.lastWriteAt.IsZero() {
		t.Fatal("newCheckpointer(nil clock) must fall back to time.Now")
	}
}

func TestCovImpEnumerator_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	noop := func(context.Context, NormalizedLibraryItem) error { return nil }
	item := sampleLibraryItemForLibrary("lib", "item-1", "Book One")

	t.Run("blank library", func(t *testing.T) {
		t.Parallel()
		if _, err := NewEnumerator(&fakeEnumerationClient{}, nil, 10).Enumerate(ctx, "  ", noop); err == nil || !strings.Contains(err.Error(), "library_id is required") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := NewEnumerator(&fakeEnumerationClient{}, nil, 10).Enumerate(cancelled, "lib", noop)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("list error", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("covimp list down")
		client := &fakeEnumerationClient{errs: map[string]map[int]error{"lib": {0: boom}}}
		if _, err := NewEnumerator(client, nil, 10).Enumerate(ctx, "lib", noop); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})

	t.Run("nil page", func(t *testing.T) {
		t.Parallel()
		if err := validateBookLibraryPage("lib", nil); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("non-book item", func(t *testing.T) {
		t.Parallel()
		podcast := item
		podcast.MediaType = "podcast"
		client := &fakeEnumerationClient{pages: map[string]map[int]*LibraryItemsPage{"lib": {0: covImpPage("lib", podcast)}}}
		_, err := NewEnumerator(client, nil, 10).Enumerate(ctx, "lib", noop)
		if err == nil || !strings.Contains(err.Error(), `contains "podcast" item`) {
			t.Fatalf("err = %v, want non-book item refusal", err)
		}
	})

	t.Run("detail fetch error", func(t *testing.T) {
		t.Parallel()
		folder := item
		folder.IsFile = false
		client := &fakeEnumerationClient{pages: map[string]map[int]*LibraryItemsPage{"lib": {0: covImpPage("lib", folder)}}}
		calls := 0
		_, err := NewEnumerator(client, nil, 10).Enumerate(ctx, "lib", func(context.Context, NormalizedLibraryItem) error {
			calls++
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "unexpected detail fetch") || calls != 0 {
			t.Fatalf("err = %v calls=%d, want detail error before callback", err, calls)
		}
	})

	t.Run("callback error", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("covimp callback")
		client := &fakeEnumerationClient{pages: map[string]map[int]*LibraryItemsPage{"lib": {0: covImpPage("lib", item)}}}
		stats, err := NewEnumerator(client, nil, 10).Enumerate(ctx, "lib", func(context.Context, NormalizedLibraryItem) error { return boom })
		if !errors.Is(err, boom) || stats.ItemsNormalized != 0 || stats.ItemsSeen != 1 {
			t.Fatalf("stats=%+v err=%v", stats, err)
		}
	})

	t.Run("checkpoint load errors", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		if err := env.settings.Set(ctx, SettingABSImportCheckpoint, "{garbage"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		e := NewEnumerator(&fakeEnumerationClient{}, env.settings, 10)
		if _, err := e.Enumerate(ctx, "lib", noop); err == nil || !strings.Contains(err.Error(), "decode abs checkpoint") {
			t.Fatalf("err = %v, want decode failure", err)
		}
		env.exec(t, "ALTER TABLE settings RENAME TO covimp_settings_gone")
		if _, err := e.Enumerate(ctx, "lib", noop); err == nil {
			t.Fatal("Enumerate with broken settings table returned nil error")
		}
	})

	t.Run("checkpoint write error", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.failOn(t, "covimp_settings_insert", "INSERT", "settings", "")
		client := &fakeEnumerationClient{pages: map[string]map[int]*LibraryItemsPage{"lib": {0: covImpPage("lib", item)}}}
		e := NewEnumerator(client, env.settings, 10).WithCheckpointDebounce(1, time.Hour)
		if _, err := e.Enumerate(ctx, "lib", noop); err == nil || !strings.Contains(err.Error(), "covimp boom") {
			t.Fatalf("err = %v, want checkpoint write failure", err)
		}
	})

	t.Run("page boundary write error", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		// Item checkpoints are debounced away; the page-boundary offer is the
		// first write, because the clock jumps past the interval between them.
		env.failOn(t, "covimp_settings_insert", "INSERT", "settings", "")
		client := &fakeEnumerationClient{pages: map[string]map[int]*LibraryItemsPage{"lib": {0: covImpPage("lib", item)}}}
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		calls := 0
		clock := func() time.Time {
			calls++
			if calls > 3 {
				return start.Add(time.Hour)
			}
			return start
		}
		e := NewEnumerator(client, env.settings, 10).WithCheckpointDebounce(100, time.Minute).WithClock(clock)
		if _, err := e.Enumerate(ctx, "lib", noop); err == nil || !strings.Contains(err.Error(), "covimp boom") {
			t.Fatalf("err = %v, want page boundary write failure", err)
		}
	})

	t.Run("checkpoint clear error", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		env.failOn(t, "covimp_settings_delete", "DELETE", "settings", "")
		client := &fakeEnumerationClient{pages: map[string]map[int]*LibraryItemsPage{"lib": {0: covImpPage("lib", item)}}}
		// Write a checkpoint per item so there is a row for the clear to delete.
		e := NewEnumerator(client, env.settings, 10).WithCheckpointDebounce(1, time.Hour)
		if _, err := e.Enumerate(ctx, "lib", noop); err == nil || !strings.Contains(err.Error(), "covimp boom") {
			t.Fatalf("err = %v, want clear failure", err)
		}
	})
}

func TestCovImpEnumerator_ObserverWithoutSettingsAndResumeMiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	items := []LibraryItem{
		sampleLibraryItemForLibrary("lib", "item-1", "One"),
		sampleLibraryItemForLibrary("lib", "item-2", "Two"),
	}
	client := &fakeEnumerationClient{pages: map[string]map[int]*LibraryItemsPage{"lib": {0: covImpPage("lib", items...)}}}

	// A checkpoint pointing at an item that is no longer on the page
	// reprocesses the whole page instead of skipping it.
	if err := env.settings.Set(ctx, SettingABSImportCheckpoint, `{"libraryId":"lib","page":0,"lastItemId":"vanished"}`); err != nil {
		t.Fatalf("Set: %v", err)
	}
	var seen []string
	stats, err := NewEnumerator(client, env.settings, 10).Enumerate(ctx, "lib", func(_ context.Context, it NormalizedLibraryItem) error {
		seen = append(seen, it.ItemID)
		return nil
	})
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if strings.Join(seen, ",") != "item-1,item-2" || stats.ItemsNormalized != 2 {
		t.Fatalf("seen = %v stats=%+v, want full page reprocessed", seen, stats)
	}
	if s, _ := env.settings.Get(ctx, SettingABSImportCheckpoint); s != nil && strings.TrimSpace(s.Value) != "" {
		t.Fatalf("checkpoint = %+v, want cleared after completion", s)
	}

	// Without a settings repo the observer still sees every write.
	var observed []ImportCheckpoint
	e := NewEnumerator(client, nil, 10).WithCheckpointDebounce(1, time.Hour).WithCheckpointObserver(func(cp ImportCheckpoint) {
		observed = append(observed, cp)
	})
	if _, err := e.Enumerate(ctx, "lib", func(context.Context, NormalizedLibraryItem) error { return nil }); err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(observed) < 2 || observed[0].LastItemID != "item-1" {
		t.Fatalf("observed = %+v, want per-item checkpoints", observed)
	}
}

func TestCovImpSnapshots_GuardsAndDecoding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if bookSnapshot(nil) != nil || authorSnapshot(nil) != nil {
		t.Fatal("nil entities must produce nil snapshots")
	}
	if payload, err := marshalSnapshotPayload((*bookRollbackSnapshot)(nil)); payload != nil || err != nil {
		t.Fatalf("typed-nil book payload = %s %v", payload, err)
	}
	if payload, err := marshalSnapshotPayload((*authorRollbackSnapshot)(nil)); payload != nil || err != nil {
		t.Fatalf("typed-nil author payload = %s %v", payload, err)
	}
	if _, err := marshalSnapshotPayload(map[string]any{"c": make(chan int)}); err == nil {
		t.Fatal("marshalSnapshotPayload accepted an unencodable value")
	}

	for _, raw := range []string{
		"",
		"{nope",
		`{"kind":"other","version":1}`,
		`{"kind":"` + runEntityMetadataKind + `","version":1,"snapshot":{"entityType":"series","before":{},"after":{}}}`,
		`{"kind":"` + runEntityMetadataKind + `","version":1,"snapshot":{"entityType":"book","after":{}}}`,
		`{"kind":"` + runEntityMetadataKind + `","version":1,"snapshot":{"entityType":"book","before":"str","after":{}}}`,
		`{"kind":"` + runEntityMetadataKind + `","version":1,"snapshot":{"entityType":"book","before":{},"after":"str"}}`,
	} {
		if _, _, ok := bookRollbackSnapshotFromMetadata(raw); ok {
			t.Fatalf("bookRollbackSnapshotFromMetadata(%q) ok, want rejected", raw)
		}
		authorRaw := strings.Replace(raw, `"entityType":"book"`, `"entityType":"author"`, 1)
		if _, _, ok := authorRollbackSnapshotFromMetadata(authorRaw); ok {
			t.Fatalf("authorRollbackSnapshotFromMetadata(%q) ok, want rejected", authorRaw)
		}
	}
	if runEntityMetadataVersion != 1 {
		t.Fatalf("test fixtures assume metadata version 1, got %d", runEntityMetadataVersion)
	}
	if got := parseJSONObject("   "); len(got) != 0 {
		t.Fatalf("parseJSONObject(blank) = %v", got)
	}
	if got := runEntityMetadataData(`{"bookId":3}`); metadataBookID(got) != 3 {
		t.Fatalf("legacy metadata = %v, want bookId 3", got)
	}

	env := covImpNewEnv(t)
	item := sampleABSItem()
	cfg := covImpConfig(item.LibraryID)
	dry := cfg
	dry.DryRun = true
	imp := env.importer
	if err := imp.recordBookBeforeSnapshot(ctx, 1, dry, item, "x", nil, "", nil); err != nil {
		t.Fatalf("dry before snapshot: %v", err)
	}
	if err := imp.recordBookAfterSnapshot(ctx, 1, cfg, item, 0, "", nil); err != nil {
		t.Fatalf("zero-id after snapshot: %v", err)
	}
	if err := imp.recordBookAfterSnapshot(ctx, 1, cfg, item, 424242, "", nil); err != nil {
		t.Fatalf("missing book after snapshot: %v", err)
	}
	if err := imp.recordAuthorBeforeSnapshot(ctx, 1, dry, item, "x", nil, "", nil); err != nil {
		t.Fatalf("dry author before snapshot: %v", err)
	}
	if err := imp.recordAuthorAfterSnapshot(ctx, 1, cfg, item, "x", 0, "", nil); err != nil {
		t.Fatalf("zero-id author after snapshot: %v", err)
	}
	if err := imp.recordAuthorAfterSnapshot(ctx, 1, cfg, item, "x", 424242, "", nil); err != nil {
		t.Fatalf("missing author after snapshot: %v", err)
	}
	if n := env.count(t, "SELECT COUNT(*) FROM abs_import_run_entities"); n != 0 {
		t.Fatalf("run entities = %d, guards must not record anything", n)
	}

	author := covImpCreateAuthor(t, env, "OL-SNAP", "Snap Author")
	if err := env.authors.UpsertAuthorIdentifier(ctx, author.ID, "  hc:snap  "); err != nil {
		t.Fatalf("UpsertAuthorIdentifier: %v", err)
	}
	snap, err := imp.authorSnapshot(ctx, author)
	if err != nil || snap.IdentifierForeignIDs == nil {
		t.Fatalf("authorSnapshot = %+v %v", snap, err)
	}
	found := false
	for _, id := range *snap.IdentifierForeignIDs {
		if id == "hc:snap" {
			found = true
		}
	}
	if !found {
		t.Fatalf("identifiers = %v, want hc:snap", *snap.IdentifierForeignIDs)
	}

	env.exec(t, "ALTER TABLE author_identifiers RENAME TO covimp_identifiers_gone")
	if _, err := imp.authorSnapshot(ctx, author); err == nil {
		t.Fatal("authorSnapshot with broken identifiers table returned nil error")
	}
	if err := imp.recordAuthorBeforeSnapshot(ctx, 1, cfg, item, "x", author, itemOutcomeLinked, nil); err == nil {
		t.Fatal("recordAuthorBeforeSnapshot swallowed the identifier error")
	}
	if err := imp.recordAuthorAfterSnapshot(ctx, 1, cfg, item, "x", author.ID, itemOutcomeLinked, nil); err == nil {
		t.Fatal("recordAuthorAfterSnapshot swallowed the identifier error")
	}
	env.exec(t, "ALTER TABLE books RENAME TO covimp_books_gone")
	if err := imp.recordBookAfterSnapshot(ctx, 1, cfg, item, 1, "", nil); err == nil {
		t.Fatal("recordBookAfterSnapshot swallowed the lookup error")
	}
	env.exec(t, "ALTER TABLE authors RENAME TO covimp_authors_gone")
	if err := imp.recordAuthorAfterSnapshot(ctx, 1, cfg, item, "x", author.ID, "", nil); err == nil {
		t.Fatal("recordAuthorAfterSnapshot swallowed the author lookup error")
	}
}
