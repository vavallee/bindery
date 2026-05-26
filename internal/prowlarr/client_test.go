package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNew_DefaultTimeout(t *testing.T) {
	c := New("http://prowlarr.local:9696", "key")
	if c.http.Timeout != 60*time.Second {
		t.Errorf("default timeout = %v, want 60s", c.http.Timeout)
	}
}

func TestNewWithTimeout(t *testing.T) {
	c := NewWithTimeout("http://prowlarr.local:9696", "key", 30*time.Second)
	if c.http.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", c.http.Timeout)
	}
}

func TestNew_StripTrailingSlash(t *testing.T) {
	c := New("http://prowlarr.local:9696/", "key")
	if strings.HasSuffix(c.baseURL, "/") {
		t.Errorf("baseURL should have trailing slash stripped, got %q", c.baseURL)
	}
}

func TestFetchIndexers_HappyPath(t *testing.T) {
	body := `[
		{"id":1,"name":"Tracker1","protocol":"torrent","supportsSearch":true,"categories":[{"id":7000},{"id":7020}]},
		{"id":2,"name":"NZBHydra","protocol":"usenet","supportsSearch":false,"categories":[]}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Errorf("expected X-Api-Key header, got %q", r.Header.Get("X-Api-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret")
	infos, err := c.FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 infos, got %d", len(infos))
	}

	t1 := infos[0]
	if t1.ProwlarrID != 1 {
		t.Errorf("ProwlarrID = %d, want 1", t1.ProwlarrID)
	}
	if t1.Name != "Tracker1" {
		t.Errorf("Name = %q, want Tracker1", t1.Name)
	}
	if t1.Protocol != "torrent" {
		t.Errorf("Protocol = %q, want torrent", t1.Protocol)
	}
	if !t1.SupportsSearch {
		t.Errorf("SupportsSearch = false, want true")
	}
	if t1.TorznabURL != srv.URL+"/1/api" {
		t.Errorf("TorznabURL = %q, want %q", t1.TorznabURL, srv.URL+"/1/api")
	}
	if t1.APIKey != "secret" {
		t.Errorf("APIKey = %q, want secret", t1.APIKey)
	}
	if len(t1.Categories) != 2 || t1.Categories[0] != 7000 || t1.Categories[1] != 7020 {
		t.Errorf("Categories = %v, want [7000 7020]", t1.Categories)
	}

	// Second indexer: no categories → empty slice
	t2 := infos[1]
	if t2.Name != "NZBHydra" {
		t.Errorf("Name = %q, want NZBHydra", t2.Name)
	}
	if len(t2.Categories) != 0 {
		t.Errorf("Categories = %v, want []", t2.Categories)
	}
}

func TestFetchIndexers_AppliesApplicationCategoryScopes(t *testing.T) {
	indexerBody := `[
		{
			"id":3,
			"name":"ScopedBookTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"tags":[3],
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":2000,"name":"Movies","subCategories":[{"id":2010,"name":"Movies/HD"}]},
					{"id":3000,"name":"Audio","subCategories":[{"id":3030,"name":"Audio/Audiobook"}]},
					{"id":7000,"name":"Books","subCategories":[{"id":7020,"name":"Books/EBook"}]},
					{"id":7010,"name":"Books/Mags"},
					{"id":7030,"name":"Books/Comics"},
					{"id":100060,"name":"Ebooks - General Fiction"}
				]
			}
		}
	]`
	applicationsBody := `[
		{
			"enable":true,
			"syncLevel":"fullSync",
			"tags":[3],
			"fields":[{"name":"syncCategories","value":[3030,7000,7010,7020,7030,7040]}]
		},
		{
			"enable":true,
			"syncLevel":"fullSync",
			"fields":[{"name":"syncCategories","value":[2000,2010]}]
		}
	]`
	srv := prowlarrClientStub(t, indexerBody, applicationsBody)
	defer srv.Close()

	infos, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	assertCategoryIDs(t, infos[0].Categories, []int{3030, 7000, 7010, 7020, 7030})
}

func TestFetchIndexers_AppliesApplicationChildOnlyCategoryScopes(t *testing.T) {
	indexerBody := `[
		{
			"id":3,
			"name":"ChildOnlyBookTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"tags":[3],
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":7010,"name":"Books/Mags"},
					{"id":7030,"name":"Books/Comics"},
					{"id":7040,"name":"Books/Technical"}
				]
			}
		},
		{
			"id":4,
			"name":"ChildOnlyAudioTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"tags":[4],
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":3010,"name":"Audio/MP3"},
					{"id":3030,"name":"Audio/Audiobook"}
				]
			}
		}
	]`
	applicationsBody := `[
		{
			"enable":true,
			"syncLevel":"fullSync",
			"tags":[3],
			"fields":[{"name":"syncCategories","value":[7010,7030,7040]}]
		},
		{
			"enable":true,
			"syncLevel":"fullSync",
			"tags":[4],
			"fields":[{"name":"syncCategories","value":[3010,3030]}]
		}
	]`
	srv := prowlarrClientStub(t, indexerBody, applicationsBody)
	defer srv.Close()

	infos, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 infos, got %d", len(infos))
	}
	assertCategoryIDs(t, infos[0].Categories, []int{7010, 7030, 7040})
	assertCategoryIDs(t, infos[1].Categories, []int{3010, 3030})
}

func TestFetchIndexers_RequiresApplicationTagMatchForCapabilityCategories(t *testing.T) {
	indexerBody := `[
		{
			"id":3,
			"name":"UntaggedBookTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"tags":[5],
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":7000,"name":"Books","subCategories":[{"id":7020,"name":"Books/EBook"}]}
				]
			}
		}
	]`
	applicationsBody := `[
		{
			"enable":true,
			"syncLevel":"fullSync",
			"tags":[3],
			"fields":[{"name":"syncCategories","value":[7000,7020]}]
		}
	]`
	srv := prowlarrClientStub(t, indexerBody, applicationsBody)
	defer srv.Close()

	infos, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	if len(infos[0].Categories) != 0 {
		t.Fatalf("categories = %v, want []", infos[0].Categories)
	}
}

func TestFetchIndexers_FallsBackToCapabilitiesWithOnlyNonBookApplicationScope(t *testing.T) {
	// Issue #763: the only registered application is non-book (e.g. Radarr),
	// so there is no book-scoped application to consult. The indexer's own
	// book capabilities must still be used rather than dropped.
	indexerBody := `[
		{
			"id":3,
			"name":"BookTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":7000,"name":"Books","subCategories":[{"id":7020,"name":"Books/EBook"}]}
				]
			}
		}
	]`
	applicationsBody := `[
		{
			"enable":true,
			"syncLevel":"fullSync",
			"fields":[{"name":"syncCategories","value":[2000,2010]}]
		}
	]`
	srv := prowlarrClientStub(t, indexerBody, applicationsBody)
	defer srv.Close()

	infos, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	assertCategoryIDs(t, infos[0].Categories, []int{7000, 7020})
}

func TestFetchIndexers_FallsBackToCapabilitiesWhenNoApplications(t *testing.T) {
	// Issue #763: standalone Prowlarr with no applications registered. The
	// indexer's book/audiobook capabilities are the only signal and must be
	// used; non-book capabilities (movies) are dropped.
	indexerBody := `[
		{
			"id":3,
			"name":"MyAnonamouse",
			"protocol":"torrent",
			"supportsSearch":true,
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":2000,"name":"Movies","subCategories":[{"id":2010,"name":"Movies/HD"}]},
					{"id":3000,"name":"Audio","subCategories":[{"id":3030,"name":"Audio/Audiobook"}]},
					{"id":7000,"name":"Books","subCategories":[{"id":7020,"name":"Books/EBook"}]}
				]
			}
		}
	]`
	srv := prowlarrClientStub(t, indexerBody, `[]`)
	defer srv.Close()

	infos, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	assertCategoryIDs(t, infos[0].Categories, []int{3000, 3030, 7000, 7020})
}

func TestFetchIndexers_NonBookIndexerStaysEmptyWithoutApplications(t *testing.T) {
	// A movie/TV-only indexer has no book or audiobook capabilities, so the
	// standalone fallback leaves it with no categories (the syncer drops it).
	indexerBody := `[
		{
			"id":4,
			"name":"MovieTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":2000,"name":"Movies","subCategories":[{"id":2010,"name":"Movies/HD"}]},
					{"id":5000,"name":"TV","subCategories":[{"id":5040,"name":"TV/HD"}]}
				]
			}
		}
	]`
	srv := prowlarrClientStub(t, indexerBody, `[]`)
	defer srv.Close()

	infos, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	if len(infos[0].Categories) != 0 {
		t.Fatalf("categories = %v, want []", infos[0].Categories)
	}
}

func TestFetchIndexers_AppliesApplicationScopeWithAnyMatchingTag(t *testing.T) {
	indexerBody := `[
		{
			"id":3,
			"name":"PartiallyTaggedBookTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"tags":[3],
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":7000,"name":"Books","subCategories":[{"id":7020,"name":"Books/EBook"}]}
				]
			}
		}
	]`
	applicationsBody := `[
		{
			"enable":true,
			"syncLevel":"fullSync",
			"tags":[3,4],
			"fields":[{"name":"syncCategories","value":[7000,7020]}]
		}
	]`
	srv := prowlarrClientStub(t, indexerBody, applicationsBody)
	defer srv.Close()

	infos, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	assertCategoryIDs(t, infos[0].Categories, []int{7000, 7020})
}

func TestFetchIndexers_ReturnsApplicationScopeError(t *testing.T) {
	indexerBody := `[
		{
			"id":3,
			"name":"ScopedBookTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"categories":[],
			"capabilities":{
				"categories":[
					{"id":7000,"name":"Books","subCategories":[{"id":7020,"name":"Books/EBook"}]}
				]
			}
		}
	]`
	srv := prowlarrClientStub(t, indexerBody, `not json`)
	defer srv.Close()

	_, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err == nil {
		t.Fatal("expected application scope error, got nil")
		return
	}
	if !strings.Contains(err.Error(), "fetch prowlarr applications") {
		t.Fatalf("error = %v, want fetch prowlarr applications", err)
	}
}

func TestFetchIndexers_TopLevelCategoriesTakePrecedence(t *testing.T) {
	body := `[
		{
			"id":4,
			"name":"ConfiguredTracker",
			"protocol":"torrent",
			"supportsSearch":true,
			"categories":[{"id":7020}],
			"capabilities":{
				"categories":[
					{"id":3000,"name":"Audio","subCategories":[{"id":3030,"name":"Audio/Audiobook"}]},
					{"id":7000,"name":"Books","subCategories":[{"id":7020,"name":"Books/EBook"}]},
					{"id":100060,"name":"Ebooks - General Fiction"}
				]
			}
		}
	]`
	srv := prowlarrStub(t, body)
	defer srv.Close()

	infos, err := New(srv.URL, "key").FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	assertCategoryIDs(t, infos[0].Categories, []int{7020})
}

func TestFetchIndexers_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	infos, err := c.FetchIndexers(context.Background())
	if err != nil {
		t.Fatalf("FetchIndexers: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected empty slice, got %d", len(infos))
	}
}

func TestFetchIndexers_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "wrong")
	_, err := c.FetchIndexers(context.Background())
	if err == nil {
		t.Fatal("expected error on 401, got nil")
		return
	}
	if !strings.Contains(err.Error(), "invalid Prowlarr API key") {
		t.Errorf("unexpected error %v", err)
	}
}

func TestFetchIndexers_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	_, err := c.FetchIndexers(context.Background())
	if err == nil {
		t.Fatal("expected error on 500, got nil")
		return
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got %v", err)
	}
}

func TestFetchIndexers_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	_, err := c.FetchIndexers(context.Background())
	if err == nil {
		t.Fatal("expected error on bad JSON, got nil")
		return
	}
	if !strings.Contains(err.Error(), "decode prowlarr indexers") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestTest_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/system/status" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":"1.2.3"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	version, err := c.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", version)
	}
}

func TestTest_BadJSON_ReturnsEmptyVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not valid json`))
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	version, err := c.Test(context.Background())
	if err != nil {
		t.Fatalf("expected nil error on bad JSON, got %v", err)
	}
	if version != "" {
		t.Errorf("expected empty version on bad JSON, got %q", version)
	}
}

func TestTest_NetworkError(t *testing.T) {
	// Start and immediately close a server so the port is unreachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	c := New(addr, "key")
	_, err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error on connection refused, got nil")
		return
	}
	if !strings.Contains(err.Error(), "could not reach Prowlarr") {
		t.Errorf("expected 'could not reach Prowlarr' in error, got %v", err)
	}
}

func prowlarrClientStub(t *testing.T, indexerBody, applicationsBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			_, _ = w.Write([]byte(indexerBody))
		case "/api/v1/applications":
			_, _ = w.Write([]byte(applicationsBody))
		default:
			http.NotFound(w, r)
		}
	}))
}

func assertCategoryIDs(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("categories = %v, want %v", got, want)
		}
	}
}
