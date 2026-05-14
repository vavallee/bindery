package grimmory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// NormalizeBaseURL
// ---------------------------------------------------------------------------

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no scheme",
			input:   "example.com/path",
			wantErr: true,
		},
		{
			name:    "ftp scheme",
			input:   "ftp://example.com",
			wantErr: true,
		},
		{
			name:    "missing host",
			input:   "http:///path",
			wantErr: true,
		},
		{
			name:  "trailing slash stripped",
			input: "http://example.com/",
			want:  "http://example.com",
		},
		{
			name:  "query stripped",
			input: "http://example.com/api?foo=bar",
			want:  "http://example.com/api",
		},
		{
			name:  "fragment stripped",
			input: "http://example.com/api#section",
			want:  "http://example.com/api",
		},
		{
			name:  "path trailing slash stripped",
			input: "http://example.com/grimmory/",
			want:  "http://example.com/grimmory",
		},
		{
			name:  "valid http",
			input: "http://example.com",
			want:  "http://example.com",
		},
		{
			name:  "valid https",
			input: "https://example.com",
			want:  "https://example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("NormalizeBaseURL(%q): want error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeBaseURL(%q): unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NormalizeAPIKey
// ---------------------------------------------------------------------------

func TestNormalizeAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "empty string ok",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace trimmed",
			input: "  mykey  ",
			want:  "mykey",
		},
		{
			name:    "control character 0x01",
			input:   "key\x01bad",
			wantErr: true,
		},
		{
			name:    "control character 0x7f",
			input:   "key\x7fbad",
			wantErr: true,
		},
		{
			name:  "clean key ok",
			input: "abc123-XYZ_valid.key",
			want:  "abc123-XYZ_valid.key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAPIKey(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("NormalizeAPIKey(%q): want error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeAPIKey(%q): unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeAPIKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UserAgent
// ---------------------------------------------------------------------------

func TestUserAgent(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{
			name:    "empty version",
			version: "",
			want:    "bindery/dev",
		},
		{
			name:    "whitespace-only version",
			version: "   ",
			want:    "bindery/dev",
		},
		{
			name:    "non-empty version",
			version: "1.2.3",
			want:    "bindery/1.2.3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UserAgent(tt.version)
			if got != tt.want {
				t.Errorf("UserAgent(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewClient
// ---------------------------------------------------------------------------

func TestNewClient_Errors(t *testing.T) {
	t.Run("bad URL", func(t *testing.T) {
		_, err := NewClient("not-a-url", "validkey")
		if err == nil {
			t.Error("want error for bad URL, got nil")
		}
	})
	t.Run("bad API key", func(t *testing.T) {
		_, err := NewClient("http://example.com", "bad\x01key")
		if err == nil {
			t.Error("want error for bad API key, got nil")
		}
	})
}

func TestNewClient_Valid(t *testing.T) {
	c, err := NewClient("http://example.com", "mykey")
	if err != nil {
		t.Fatalf("NewClient: unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("NewClient returned nil client")
	}
}

// ---------------------------------------------------------------------------
// WithUserAgent
// ---------------------------------------------------------------------------

func TestWithUserAgent(t *testing.T) {
	c, err := NewClient("http://example.com", "mykey")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	const ua = "custom-agent/9.9"
	got := c.WithUserAgent(ua)
	if got.userAgent != ua {
		t.Errorf("WithUserAgent: got %q, want %q", got.userAgent, ua)
	}
}

// ---------------------------------------------------------------------------
// Ping (HTTP tests)
// ---------------------------------------------------------------------------

func TestPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(StatusResponse{Status: "ok", Version: "1.0"})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "testkey")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := c.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping: unexpected error: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("Status: got %q, want %q", resp.Status, "ok")
	}
	if resp.Version != "1.0" {
		t.Errorf("Version: got %q, want %q", resp.Version, "1.0")
	}
}

func TestPing_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "badkey")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping: want error for 401, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode: got %d, want %d", apiErr.StatusCode, http.StatusUnauthorized)
	}
}

func TestPing_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// empty body — fallback to status text
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "anykey")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping: want error for 500, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode: got %d, want %d", apiErr.StatusCode, http.StatusInternalServerError)
	}
	if apiErr.Message == "" {
		t.Error("Message: want non-empty fallback, got empty string")
	}
}

// ---------------------------------------------------------------------------
// do — header verification
// ---------------------------------------------------------------------------

func TestDo_SetsAuthHeader(t *testing.T) {
	const apiKey = "my-secret-key"
	const customUA = "bindery/2.0.0"

	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, apiKey)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.WithUserAgent(customUA)
	// Invoke via Ping which calls do internally.
	// We don't care about the response body error here.
	_, _ = c.Ping(context.Background())

	wantAuth := "Bearer " + apiKey
	if gotAuth != wantAuth {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, wantAuth)
	}
	// WithUserAgent returns c (mutates in place), so userAgent field was updated.
	if gotUA != customUA {
		t.Errorf("User-Agent header: got %q, want %q", gotUA, customUA)
	}
}

// ---------------------------------------------------------------------------
// APIError.Error()
// ---------------------------------------------------------------------------

func TestAPIError_Error(t *testing.T) {
	t.Run("with message", func(t *testing.T) {
		e := &APIError{StatusCode: 403, Message: "forbidden"}
		if got := e.Error(); got != "forbidden" {
			t.Errorf("Error() = %q, want %q", got, "forbidden")
		}
	})
	t.Run("empty message fallback", func(t *testing.T) {
		e := &APIError{StatusCode: 404, Message: ""}
		want := "grimmory api error (404)"
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}
