package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// mockIndexerSearcher implements indexerSearcher for unit tests.
type mockIndexerSearcher struct {
	ebookResults []newznab.SearchResult
	audioResults []newznab.SearchResult
}

func (m *mockIndexerSearcher) SearchBookWithDebug(_ context.Context, _ []models.Indexer, c indexer.MatchCriteria) ([]newznab.SearchResult, *indexer.SearchDebug) {
	switch c.MediaType {
	case models.MediaTypeEbook:
		return m.ebookResults, nil
	case models.MediaTypeAudiobook:
		return m.audioResults, nil
	default:
		return append(m.ebookResults, m.audioResults...), nil
	}
}

func (m *mockIndexerSearcher) SearchQuery(_ context.Context, _ []models.Indexer, _ string) []newznab.SearchResult {
	return nil
}

func indexerFixture(t *testing.T) *IndexerHandler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewIndexerHandler(
		db.NewIndexerRepo(database),
		db.NewBookRepo(database),
		db.NewAuthorRepo(database),
		db.NewMetadataProfileRepo(database),
		nil, // searcher — not needed for CRUD tests
		db.NewSettingsRepo(database),
		db.NewBlocklistRepo(database),
	)
}

func TestIndexerList_Empty(t *testing.T) {
	h := indexerFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/indexer", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []models.Indexer
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 0 {
		t.Errorf("expected empty list, got %d items", len(out))
	}
}

func TestIndexerCRUD(t *testing.T) {
	h := indexerFixture(t)

	// Create
	body := `{"name":"NZBGeek","url":"https://api.nzbgeek.info","apiKey":"testkey","type":"newznab"}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.Indexer
	json.NewDecoder(rec.Body).Decode(&created)
	if created.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	// Default categories should be set
	if len(created.Categories) == 0 {
		t.Error("expected default categories to be populated")
	}

	// List
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/indexer", nil))
	var list []models.Indexer
	json.NewDecoder(rec.Body).Decode(&list)
	if len(list) != 1 {
		t.Errorf("expected 1 indexer, got %d", len(list))
	}

	// Get
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/indexer/1", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", rec.Code)
	}

	// Get — not found
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/indexer/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get missing: expected 404, got %d", rec.Code)
	}

	// Update
	update := `{"name":"NZBGeek Updated","url":"https://api.nzbgeek.info","apiKey":"newkey","type":"newznab","categories":[7000]}`
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/indexer/1", bytes.NewBufferString(update)), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Errorf("update: expected 200, got %d", rec.Code)
	}

	// Update — not found
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/indexer/999", bytes.NewBufferString(update)), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("update missing: expected 404, got %d", rec.Code)
	}

	// Delete
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/indexer/1", nil), "id", "1"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", rec.Code)
	}
}

func TestIndexerCreate_Validation(t *testing.T) {
	h := indexerFixture(t)
	for _, tc := range []struct {
		body string
		desc string
	}{
		{`{}`, "empty body"},
		{`{"name":"x"}`, "missing url"},
		{`{"url":"https://example.com"}`, "missing name"},
		{`not-json`, "invalid json"},
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer", bytes.NewBufferString(tc.body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", tc.desc, rec.Code)
		}
	}
}

func TestIndexerCreate_DuplicateURL(t *testing.T) {
	h := indexerFixture(t)
	body := `{"name":"NZBGeek","url":"https://api.nzbgeek.info","apiKey":"k"}`
	// First create succeeds
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d", rec.Code)
	}
	// Second create with same URL should conflict
	rec = httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer", bytes.NewBufferString(body)))
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate url: expected 409, got %d", rec.Code)
	}
}

func TestIndexerTest_NotFound(t *testing.T) {
	h := indexerFixture(t)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/indexer/999/test", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestIndexerSearchQuery_MissingQ(t *testing.T) {
	h := indexerFixture(t)
	rec := httptest.NewRecorder()
	h.SearchQuery(rec, httptest.NewRequest(http.MethodGet, "/indexer/search", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing q param, got %d", rec.Code)
	}
}

func TestLangFilterFromAllowed(t *testing.T) {
	for _, tc := range []struct {
		langs []string
		want  string
		desc  string
	}{
		{[]string{"en"}, "en", "English-only (en)"},
		{[]string{"eng"}, "en", "English-only (eng)"},
		{[]string{"en", "fr"}, "", "multi-language — no filter"},
		{[]string{"fr"}, "", "French-only — no English filter"},
		{nil, "", "nil — no filter"},
		{[]string{}, "", "empty — no filter"},
	} {
		got := langFilterFromAllowed(tc.langs)
		if got != tc.want {
			t.Errorf("%s: langFilterFromAllowed(%v) = %q, want %q", tc.desc, tc.langs, got, tc.want)
		}
	}
}

func TestSearchBook_DualFormat_MediaTypeTagging(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()

	authorRepo := db.NewAuthorRepo(database)
	author := &models.Author{
		ForeignID: "OL1A", Name: "Jane Doe", SortName: "Doe, Jane",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	bookRepo := db.NewBookRepo(database)
	book := &models.Book{
		Title:     "Test Book",
		ForeignID: "OL1M",
		AuthorID:  author.ID,
		MediaType: models.MediaTypeBoth,
		Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	mock := &mockIndexerSearcher{
		ebookResults: []newznab.SearchResult{{GUID: "eb1", Title: "Test Book epub"}},
		audioResults: []newznab.SearchResult{{GUID: "au1", Title: "Test Book mp3"}},
	}

	h := NewIndexerHandler(
		db.NewIndexerRepo(database),
		bookRepo,
		authorRepo,
		db.NewMetadataProfileRepo(database),
		mock,
		db.NewSettingsRepo(database),
		db.NewBlocklistRepo(database),
	)

	rec := httptest.NewRecorder()
	req := withURLParam(
		httptest.NewRequest(http.MethodGet, "/indexer/book/1/search", nil),
		"id", strconv.FormatInt(book.ID, 10),
	)
	h.SearchBook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Results []struct {
			GUID      string `json:"guid"`
			MediaType string `json:"mediaType"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byGUID := make(map[string]string, len(resp.Results))
	for _, r := range resp.Results {
		byGUID[r.GUID] = r.MediaType
	}
	if byGUID["eb1"] != "ebook" {
		t.Errorf("ebook result: got mediaType=%q, want %q", byGUID["eb1"], "ebook")
	}
	if byGUID["au1"] != "audiobook" {
		t.Errorf("audiobook result: got mediaType=%q, want %q", byGUID["au1"], "audiobook")
	}
}

// slowSearcher records peak concurrency to verify parallel dispatch.
type slowSearcher struct {
	mu           sync.Mutex
	inFlight     int
	peakFlight   int
	delay        time.Duration
	ebookResults []newznab.SearchResult
	audioResults []newznab.SearchResult
}

func (s *slowSearcher) SearchBookWithDebug(_ context.Context, _ []models.Indexer, c indexer.MatchCriteria) ([]newznab.SearchResult, *indexer.SearchDebug) {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.peakFlight {
		s.peakFlight = s.inFlight
	}
	s.mu.Unlock()

	time.Sleep(s.delay)

	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()

	switch c.MediaType {
	case models.MediaTypeEbook:
		return s.ebookResults, nil
	case models.MediaTypeAudiobook:
		return s.audioResults, nil
	default:
		return nil, nil
	}
}

func (s *slowSearcher) SearchQuery(_ context.Context, _ []models.Indexer, _ string) []newznab.SearchResult {
	return nil
}

// TestSearchBook_DualFormat_ParallelDispatch verifies that the two
// per-format searches for a MediaTypeBoth book run concurrently rather
// than sequentially. The slowSearcher records peak in-flight count:
// parallel dispatch yields 2; sequential yields 1.
func TestSearchBook_DualFormat_ParallelDispatch(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()

	authorRepo := db.NewAuthorRepo(database)
	author := &models.Author{
		ForeignID: "OL2A", Name: "Test Author", SortName: "Author, Test",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	bookRepo := db.NewBookRepo(database)
	book := &models.Book{
		Title: "Parallel Book", ForeignID: "OL2M",
		AuthorID: author.ID, MediaType: models.MediaTypeBoth, Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	slow := &slowSearcher{
		delay:        30 * time.Millisecond,
		ebookResults: []newznab.SearchResult{{GUID: "pe1", Title: "Parallel Ebook"}},
		audioResults: []newznab.SearchResult{{GUID: "pa1", Title: "Parallel Audio"}},
	}

	h := NewIndexerHandler(
		db.NewIndexerRepo(database),
		bookRepo,
		authorRepo,
		db.NewMetadataProfileRepo(database),
		slow,
		db.NewSettingsRepo(database),
		db.NewBlocklistRepo(database),
	)

	rec := httptest.NewRecorder()
	req := withURLParam(
		httptest.NewRequest(http.MethodGet, "/indexer/book/1/search", nil),
		"id", strconv.FormatInt(book.ID, 10),
	)
	h.SearchBook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if slow.peakFlight < 2 {
		t.Errorf("dual-format search ran sequentially: peak concurrent calls = %d, want ≥ 2", slow.peakFlight)
	}
}
