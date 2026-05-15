package abs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type retryNetError struct {
	timeout   bool
	temporary bool
}

func (e retryNetError) Error() string {
	return fmt.Sprintf("timeout=%t temporary=%t", e.timeout, e.temporary)
}

func (e retryNetError) Timeout() bool {
	return e.timeout
}

func (e retryNetError) Temporary() bool {
	return e.temporary
}

func TestUserAgent(t *testing.T) {
	t.Parallel()

	// abs.UserAgent delegates to internal/useragent.Build, which appends
	// "(<GOOS>)" and strips a leading "v". Assert the stable prefix.
	tests := []struct {
		version string
		wantPfx string
	}{
		{version: "", wantPfx: "bindery/dev ("},
		{version: "v1.2.3", wantPfx: "bindery/1.2.3 ("},
		{version: "1.2.3", wantPfx: "bindery/1.2.3 ("},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			t.Parallel()

			if got := UserAgent(tt.version); !strings.HasPrefix(got, tt.wantPfx) {
				t.Fatalf("UserAgent(%q) = %q, want prefix %q", tt.version, got, tt.wantPfx)
			}
		})
	}
}

func TestClientAuthorizeInjectsBearerHeader(t *testing.T) {
	t.Parallel()

	sawAuth := ""
	sawAgent := ""
	sawRawQuery := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawAgent = r.Header.Get("User-Agent")
		sawRawQuery = r.URL.RawQuery
		if r.URL.Path != "/api/authorize" {
			t.Fatalf("path = %s, want /api/authorize", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":{"id":"root","username":"root","type":"root","librariesAccessible":[],"permissions":{"accessAllLibraries":true}},"userDefaultLibraryId":"lib_main","serverSettings":{"version":"2.33.1"},"Source":"docker"}`))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Authorize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer secret-key" {
		t.Fatalf("Authorization header = %q", sawAuth)
	}
	if !strings.HasPrefix(sawAgent, "bindery/dev (") {
		t.Fatalf("User-Agent header = %q, want prefix bindery/dev (", sawAgent)
	}
	if sawRawQuery != "" {
		t.Fatalf("query = %q, want api key absent from URL query", sawRawQuery)
	}
	if resp.ServerSettings.Version != "2.33.1" {
		t.Fatalf("version = %q", resp.ServerSettings.Version)
	}
}

func TestClientAcceptsPrintableAPIKeyPunctuation(t *testing.T) {
	t.Parallel()

	key := "secret!#$%&'*+.^_`|~?=;:,/[]{}()"
	sawAuth := ""
	sawURL := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":{"id":"root","username":"root","type":"root","librariesAccessible":[],"permissions":{"accessAllLibraries":true}},"userDefaultLibraryId":"lib_main","serverSettings":{"version":"2.33.1"},"Source":"docker"}`))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, " \t"+key+" \n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Authorize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer "+key {
		t.Fatalf("Authorization header = %q", sawAuth)
	}
	if strings.Contains(sawURL, key) {
		t.Fatalf("request URL leaked api key: %q", sawURL)
	}
}

func TestClientRejectsAPIKeyControlCharacters(t *testing.T) {
	t.Parallel()

	tests := []string{
		"bad\r\nX-Test: injected",
		"bad\x00secret",
		"bad\x7fsecret",
	}

	for _, key := range tests {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			t.Parallel()
			_, err := NewClient("https://abs.example.com", key)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), key) {
				t.Fatalf("error leaked api key: %q", err.Error())
			}
		})
	}
}

func TestClientAuthorizeReturnsAPIErrorOnUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "bad-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Authorize(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", apiErr.StatusCode)
	}
	if apiErr.Message != "invalid api key" {
		t.Fatalf("message = %q", apiErr.Message)
	}
}

func TestClientListLibraryItems_UsesPagingAndMinifiedQuery(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/api/libraries/lib-books/items" {
			t.Fatalf("path = %s, want /api/libraries/lib-books/items", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"total":0,"limit":50,"page":1,"mediaType":"book","minified":true}`))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ListLibraryItems(context.Background(), "lib-books", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "limit=50&minified=1&page=1" && gotQuery != "limit=50&page=1&minified=1" && gotQuery != "minified=1&limit=50&page=1" {
		t.Fatalf("query = %q", gotQuery)
	}
	if page.Page != 1 || page.Limit != 50 {
		t.Fatalf("page = %+v", page)
	}
}

func TestClientGetLibraryItem_UsesExpandedAuthorsQuery(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/api/items/li_test" {
			t.Fatalf("path = %s, want /api/items/li_test", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"li_test","libraryId":"lib-books","mediaType":"book","media":{"metadata":{"title":"Test"}}}`))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	item, err := client.GetLibraryItem(context.Background(), "li_test")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "expanded=1&include=authors" && gotQuery != "include=authors&expanded=1" {
		t.Fatalf("query = %q", gotQuery)
	}
	if item.ID != "li_test" {
		t.Fatalf("item = %+v", item)
	}
}

func TestClientScanLibrary_PostsToCorrectEndpoint(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ScanLibrary(context.Background(), "lib-audiobooks"); err != nil {
		t.Fatalf("ScanLibrary: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/libraries/lib-audiobooks/scan" {
		t.Errorf("path = %q, want /api/libraries/lib-audiobooks/scan", gotPath)
	}
}

func TestClientScanLibrary_EmptyLibraryIDReturnsError(t *testing.T) {
	t.Parallel()

	client, err := NewClient("http://abs.example.com", "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ScanLibrary(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty library_id")
	}
}

func TestShouldRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "context canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "net error timeout",
			err:  retryNetError{timeout: true},
			want: true,
		},
		{
			name: "temporary-only net error",
			err:  retryNetError{temporary: true},
			want: false,
		},
		{
			name: "generic permanent error",
			err:  errors.New("permanent"),
			want: false,
		},
		{
			name: "permanent net error",
			err:  retryNetError{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := shouldRetry(tt.err); got != tt.want {
				t.Fatalf("shouldRetry(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}
