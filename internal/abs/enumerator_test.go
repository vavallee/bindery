package abs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
