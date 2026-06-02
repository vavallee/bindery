package abs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

func loadItemFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestEnumerator_SingleFileAudiobookDoesNotFetchDetail(t *testing.T) {
	t.Parallel()

	var detailCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/libraries/lib-books/items":
			_, _ = w.Write(loadItemFixture(t, "library_items_single_file_page_v2_33_2.json"))
		case "/api/items/li_single_file":
			detailCalls++
			http.Error(w, "unexpected detail fetch", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	enumerator := NewEnumerator(client, nil, 50)

	var items []NormalizedLibraryItem
	stats, err := enumerator.Enumerate(context.Background(), "lib-books", func(_ context.Context, item NormalizedLibraryItem) error {
		items = append(items, item)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if detailCalls != 0 {
		t.Fatalf("detailCalls = %d, want 0", detailCalls)
	}
	if stats.ItemsNormalized != 1 {
		t.Fatalf("itemsNormalized = %d, want 1", stats.ItemsNormalized)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].Title != "Project Hail Mary" {
		t.Fatalf("title = %q", items[0].Title)
	}
	if items[0].DurationSeconds == 0 || items[0].SizeBytes == 0 {
		t.Fatalf("normalized item missing duration/size: %+v", items[0])
	}
	if len(items[0].AudioFiles) != 1 {
		t.Fatalf("audioFiles = %d, want 1", len(items[0].AudioFiles))
	}
}

func TestEnumerator_FolderBackedItemFetchesDetail(t *testing.T) {
	t.Parallel()

	var detailCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/libraries/lib-books/items":
			_, _ = w.Write(loadItemFixture(t, "library_items_folder_multi_file_page_v2_33_2.json"))
		case "/api/items/li_folder_multi":
			detailCalls++
			_, _ = w.Write(loadItemFixture(t, "library_item_folder_multi_file_detail_v2_33_2.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	enumerator := NewEnumerator(client, nil, 50)

	var got NormalizedLibraryItem
	stats, err := enumerator.Enumerate(context.Background(), "lib-books", func(_ context.Context, item NormalizedLibraryItem) error {
		got = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if detailCalls != 1 {
		t.Fatalf("detailCalls = %d, want 1", detailCalls)
	}
	if stats.ItemsDetailFetched != 1 {
		t.Fatalf("itemsDetailFetched = %d, want 1", stats.ItemsDetailFetched)
	}
	if !got.DetailFetched {
		t.Fatal("expected DetailFetched=true")
	}
	if got.DurationSeconds == 0 || got.SizeBytes == 0 {
		t.Fatalf("detail merge failed: %+v", got)
	}
	if len(got.Series) != 1 || got.Series[0].Name != "The Stormlight Archive" {
		t.Fatalf("series = %+v", got.Series)
	}
	if len(got.AudioFiles) != 2 {
		t.Fatalf("audioFiles = %d, want 2", len(got.AudioFiles))
	}
}

func TestEnumerator_MissingSizeDurationFetchesDetail(t *testing.T) {
	t.Parallel()

	var detailCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/libraries/lib-books/items":
			_, _ = w.Write(loadItemFixture(t, "library_items_missing_size_duration_page_v2_33_2.json"))
		case "/api/items/li_missing_stats":
			detailCalls++
			_, _ = w.Write(loadItemFixture(t, "library_item_missing_size_duration_detail_v2_33_2.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	enumerator := NewEnumerator(client, nil, 50)

	var got NormalizedLibraryItem
	_, err = enumerator.Enumerate(context.Background(), "lib-books", func(_ context.Context, item NormalizedLibraryItem) error {
		got = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if detailCalls != 1 {
		t.Fatalf("detailCalls = %d, want 1", detailCalls)
	}
	if got.DurationSeconds == 0 || got.SizeBytes == 0 {
		t.Fatalf("expected detail-filled duration/size, got %+v", got)
	}
}

func TestEnumerator_RejectsNonBookLibrary(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/libraries/lib-podcasts/items" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"results":[],"total":0,"limit":50,"page":0,"mediaType":"podcast"}`))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	enumerator := NewEnumerator(client, nil, 50)

	_, err = enumerator.Enumerate(context.Background(), "lib-podcasts", func(context.Context, NormalizedLibraryItem) error {
		t.Fatal("unexpected item import callback")
		return nil
	})
	if err == nil || err.Error() != `library "lib-podcasts" is "podcast", expected book` {
		t.Fatalf("err = %v, want non-book library error", err)
	}
}

func TestEnumerator_ResumeFromCheckpoint(t *testing.T) {
	t.Parallel()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	settings := db.NewSettingsRepo(database)
	pages := map[string][]byte{
		"0": loadItemFixture(t, "library_items_resume_page_0_v2_33_2.json"),
		"1": loadItemFixture(t, "library_items_resume_page_1_v2_33_2.json"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/libraries/lib-books/items" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(pages[r.URL.Query().Get("page")])
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	enumerator := NewEnumerator(client, settings, 2)

	stopErr := errors.New("stop after first page")
	var firstRun []string
	_, err = enumerator.Enumerate(context.Background(), "lib-books", func(_ context.Context, item NormalizedLibraryItem) error {
		firstRun = append(firstRun, item.ItemID)
		if len(firstRun) == 2 {
			return stopErr
		}
		return nil
	})
	if !errors.Is(err, stopErr) {
		t.Fatalf("err = %v, want stopErr", err)
	}
	if !slices.Equal(firstRun, []string{"li_resume_1", "li_resume_2"}) {
		t.Fatalf("firstRun = %v", firstRun)
	}

	setting, err := settings.Get(context.Background(), SettingABSImportCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if setting == nil {
		t.Fatal("expected checkpoint to be stored")
		return
	}
	var checkpoint ImportCheckpoint
	if err := json.Unmarshal([]byte(setting.Value), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Page != 0 || checkpoint.LastItemID != "li_resume_1" {
		t.Fatalf("checkpoint = %+v, want page 0 lastItemID li_resume_1", checkpoint)
	}

	var resumed []string
	stats, err := enumerator.Enumerate(context.Background(), "lib-books", func(_ context.Context, item NormalizedLibraryItem) error {
		resumed = append(resumed, item.ItemID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resumed, []string{"li_resume_2", "li_resume_3", "li_resume_4"}) {
		t.Fatalf("resumed = %v", resumed)
	}
	if stats.ItemsNormalized != 3 {
		t.Fatalf("itemsNormalized = %d, want 3", stats.ItemsNormalized)
	}
	setting, err = settings.Get(context.Background(), SettingABSImportCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if setting != nil {
		t.Fatalf("checkpoint should be cleared, got %+v", setting)
	}
}

func TestCheckpointer_WritesEveryN(t *testing.T) {
	t.Parallel()

	var writes int
	clock := time.Unix(0, 0)
	cp := newCheckpointer(100, time.Hour, func() time.Time { return clock }, func(_ context.Context, _ ImportCheckpoint) error {
		writes++
		return nil
	})

	for i := 0; i < 99; i++ {
		if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: strconv.Itoa(i)}); err != nil {
			t.Fatalf("offer %d: %v", i, err)
		}
	}
	if writes != 0 {
		t.Fatalf("writes after 99 offers = %d, want 0", writes)
	}
	if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: "99"}); err != nil {
		t.Fatalf("offer 100: %v", err)
	}
	if writes != 1 {
		t.Fatalf("writes after 100 offers = %d, want 1", writes)
	}

	for i := 100; i < 199; i++ {
		if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: strconv.Itoa(i)}); err != nil {
			t.Fatalf("offer %d: %v", i, err)
		}
	}
	if writes != 1 {
		t.Fatalf("writes after 199 offers = %d, want 1", writes)
	}
	if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: "199"}); err != nil {
		t.Fatalf("offer 200: %v", err)
	}
	if writes != 2 {
		t.Fatalf("writes after 200 offers = %d, want 2", writes)
	}
}

func TestCheckpointer_WritesOnTimeElapsed(t *testing.T) {
	t.Parallel()

	var writes int
	var lastWritten ImportCheckpoint
	clock := time.Unix(0, 0)
	cp := newCheckpointer(1_000_000, 5*time.Second, func() time.Time { return clock }, func(_ context.Context, c ImportCheckpoint) error {
		writes++
		lastWritten = c
		return nil
	})

	if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: "a"}); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("writes after first offer = %d, want 0", writes)
	}
	clock = clock.Add(4 * time.Second)
	if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: "b"}); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("writes after offer at +4s = %d, want 0", writes)
	}
	clock = clock.Add(2 * time.Second)
	if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: "c"}); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("writes after offer at +6s = %d, want 1", writes)
	}
	if lastWritten.LastItemID != "c" {
		t.Fatalf("lastWritten = %q, want %q", lastWritten.LastItemID, "c")
	}
}

func TestCheckpointer_FlushAlwaysWrites(t *testing.T) {
	t.Parallel()

	var writes int
	var lastWritten ImportCheckpoint
	clock := time.Unix(0, 0)
	cp := newCheckpointer(1_000_000, time.Hour, func() time.Time { return clock }, func(_ context.Context, c ImportCheckpoint) error {
		writes++
		lastWritten = c
		return nil
	})

	if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: "pending"}); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("writes pre-flush = %d, want 0", writes)
	}
	if err := cp.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || lastWritten.LastItemID != "pending" {
		t.Fatalf("flush: writes=%d lastWritten=%q", writes, lastWritten.LastItemID)
	}

	// Second flush with nothing pending is a no-op.
	if err := cp.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("idle flush wrote: writes = %d, want 1", writes)
	}
}

func TestCheckpointer_FlushPropagatesError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("settings write failed")
	cp := newCheckpointer(1, time.Hour, time.Now, func(_ context.Context, _ ImportCheckpoint) error {
		return wantErr
	})
	if err := cp.offer(context.Background(), ImportCheckpoint{LastItemID: "x"}); !errors.Is(err, wantErr) {
		t.Fatalf("offer err = %v, want %v", err, wantErr)
	}
}

// fakeEnumerationServer serves a synthetic ABS library of size totalItems split
// into pages of pageSize. Used for the debounce integration test below.
func fakeEnumerationServer(t *testing.T, libraryID string, totalItems, pageSize int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/libraries/"+libraryID+"/items" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		page, err := strconv.Atoi(q.Get("page"))
		if err != nil {
			page = 0
		}
		limit := pageSize
		if l := q.Get("limit"); l != "" {
			if v, err := strconv.Atoi(l); err == nil && v > 0 {
				limit = v
			}
		}
		start := page * limit
		end := start + limit
		if end > totalItems {
			end = totalItems
		}
		results := make([]map[string]any, 0, end-start)
		for i := start; i < end; i++ {
			id := fmt.Sprintf("li_%04d", i)
			results = append(results, map[string]any{
				"id":        id,
				"libraryId": libraryID,
				"path":      "/audiobooks/Author/" + id + ".m4b",
				"relPath":   "Author/" + id + ".m4b",
				"isFile":    true,
				"mediaType": "book",
				"media": map[string]any{
					"libraryItemId": id,
					"metadata": map[string]any{
						"title": "Title " + id,
						"authors": []map[string]any{
							{"id": "au_1", "name": "Author"},
						},
					},
					"duration": 100,
					"size":     1000,
					"audioFiles": []map[string]any{
						{
							"ino":   "ino_" + id,
							"index": 1,
							"path":  "/audiobooks/Author/" + id + ".m4b",
						},
					},
				},
			})
		}
		payload := map[string]any{
			"results":   results,
			"total":     totalItems,
			"limit":     limit,
			"page":      page,
			"mediaType": "book",
			"minified":  true,
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
}

func TestEnumerator_DebouncesSettingsWritesOnLargeLibrary(t *testing.T) {
	t.Parallel()

	const (
		total    = 250
		pageSize = 50
	)
	srv := fakeEnumerationServer(t, "lib-books", total, pageSize)
	defer srv.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	settings := db.NewSettingsRepo(database)
	client, err := NewClient(srv.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}

	var observerCalls int64
	enumerator := NewEnumerator(client, settings, pageSize).
		WithCheckpointObserver(func(ImportCheckpoint) {
			atomic.AddInt64(&observerCalls, 1)
		})

	var processed int
	stats, err := enumerator.Enumerate(context.Background(), "lib-books", func(context.Context, NormalizedLibraryItem) error {
		processed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if processed != total {
		t.Fatalf("processed = %d, want %d", processed, total)
	}
	if stats.ItemsNormalized != total {
		t.Fatalf("ItemsNormalized = %d, want %d", stats.ItemsNormalized, total)
	}

	// Each observer call corresponds to one underlying settings.Set. With the
	// default debounce thresholds of 100 items / 5s, a 250-item synchronous
	// import should write roughly 2-3 times (at item 100, item 200, and
	// possibly one page-boundary write). Without debouncing it would be 250+.
	writes := atomic.LoadInt64(&observerCalls)
	if writes < 1 || writes > 10 {
		t.Fatalf("checkpoint writes = %d for %d items, want between 1 and 10", writes, total)
	}

	// On successful completion the checkpoint row is cleared.
	setting, err := settings.Get(context.Background(), SettingABSImportCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if setting != nil {
		t.Fatalf("checkpoint should be cleared on success, got %+v", setting)
	}
}

func TestEnumerator_FlushesPendingCheckpointOnCallbackError(t *testing.T) {
	t.Parallel()

	const (
		total    = 50
		pageSize = 50
	)
	srv := fakeEnumerationServer(t, "lib-books", total, pageSize)
	defer srv.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	settings := db.NewSettingsRepo(database)
	client, err := NewClient(srv.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}

	// minItems=1_000_000 + minInterval=1h means the debounce will never fire
	// from offer() alone; only the flush-on-return path should write.
	enumerator := NewEnumerator(client, settings, pageSize).
		WithCheckpointDebounce(1_000_000, time.Hour)

	stopErr := errors.New("stop after item 5")
	var seen []string
	_, err = enumerator.Enumerate(context.Background(), "lib-books", func(_ context.Context, item NormalizedLibraryItem) error {
		seen = append(seen, item.ItemID)
		if len(seen) == 6 {
			return stopErr
		}
		return nil
	})
	if !errors.Is(err, stopErr) {
		t.Fatalf("err = %v, want stopErr", err)
	}

	setting, err := settings.Get(context.Background(), SettingABSImportCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if setting == nil {
		t.Fatal("expected pending checkpoint to be flushed on callback error")
		return
	}
	var checkpoint ImportCheckpoint
	if err := json.Unmarshal([]byte(setting.Value), &checkpoint); err != nil {
		t.Fatal(err)
	}
	// fn returned stopErr on the 6th item (index 5). The last successfully
	// processed item was index 4 (li_0004), which is what should be durable.
	if checkpoint.LastItemID != "li_0004" {
		t.Fatalf("checkpoint.LastItemID = %q, want li_0004", checkpoint.LastItemID)
	}
}
