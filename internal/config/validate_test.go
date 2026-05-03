package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// discardLogger returns an slog.Logger that discards all output, suitable for
// tests that only care about the returned error.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

// TestValidate_OIDCRedirectBaseURL_Valid checks that valid OIDC URLs are
// accepted without error.
func TestValidate_OIDCRedirectBaseURL_Valid(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"http with path", "http://bindery.example.com"},
		{"https bare", "https://bindery.example.com"},
		{"https with path", "https://bindery.example.com/app"},
		{"http with port", "http://localhost:8787"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				LibraryDir:          t.TempDir(),
				OIDCRedirectBaseURL: tc.url,
			}
			if err := cfg.Validate(discardLogger()); err != nil {
				t.Errorf("expected no error for valid URL %q, got: %v", tc.url, err)
			}
		})
	}
}

// TestValidate_OIDCRedirectBaseURL_Invalid checks that broken OIDC URLs
// cause Validate to return a non-nil error (so the caller can treat it as
// fatal and refuse to start).
func TestValidate_OIDCRedirectBaseURL_Invalid(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string
	}{
		{
			name:    "no scheme",
			url:     "bindery.example.com",
			wantErr: "http or https",
		},
		{
			name:    "relative path only",
			url:     "/api/v1",
			wantErr: "http or https",
		},
		{
			name:    "ftp scheme",
			url:     "ftp://bindery.example.com",
			wantErr: "http or https",
		},
		{
			name:    "empty host",
			url:     "https://",
			wantErr: "no host",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				LibraryDir:          t.TempDir(),
				OIDCRedirectBaseURL: tc.url,
			}
			err := cfg.Validate(discardLogger())
			if err == nil {
				t.Fatalf("expected error for invalid URL %q, got nil", tc.url)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidate_OIDCRedirectBaseURL_Empty checks that an empty OIDC URL
// (the common case where OIDC is not configured) does not produce an error.
func TestValidate_OIDCRedirectBaseURL_Empty(t *testing.T) {
	cfg := &Config{
		LibraryDir:          t.TempDir(),
		OIDCRedirectBaseURL: "",
	}
	if err := cfg.Validate(discardLogger()); err != nil {
		t.Errorf("expected no error when OIDC URL is empty, got: %v", err)
	}
}

// TestValidate_LibraryDir_Missing checks that a missing library directory
// produces a warning but does NOT cause Validate to return an error (the
// directory may be created on first import).
func TestValidate_LibraryDir_Missing(t *testing.T) {
	cfg := &Config{
		LibraryDir: filepath.Join(t.TempDir(), "does-not-exist"),
	}
	// Validate should warn but not fail.
	if err := cfg.Validate(discardLogger()); err != nil {
		t.Errorf("missing library dir should warn, not fail; got: %v", err)
	}
}

// TestValidate_LibraryDir_NotWritable checks that a library directory that
// exists but is not writable produces a warning but does NOT fail. This test
// is skipped when running as root (root can always write).
func TestValidate_LibraryDir_NotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission checks do not apply")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	cfg := &Config{LibraryDir: dir}
	if err := cfg.Validate(discardLogger()); err != nil {
		t.Errorf("non-writable library dir should warn, not fail; got: %v", err)
	}
}

// TestValidate_AudiobookDir_Separate checks that a separately configured
// audiobook directory that does not exist produces a warning but not an error.
func TestValidate_AudiobookDir_Separate(t *testing.T) {
	cfg := &Config{
		LibraryDir:   t.TempDir(),
		AudiobookDir: filepath.Join(t.TempDir(), "audiobooks-missing"),
	}
	if err := cfg.Validate(discardLogger()); err != nil {
		t.Errorf("missing audiobook dir should warn, not fail; got: %v", err)
	}
}

// TestValidate_AudiobookDir_SameAsLibrary checks that when AudiobookDir
// equals LibraryDir the directory is only probed once and no error is returned.
func TestValidate_AudiobookDir_SameAsLibrary(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		LibraryDir:   dir,
		AudiobookDir: dir,
	}
	if err := cfg.Validate(discardLogger()); err != nil {
		t.Errorf("audiobook dir == library dir should not fail; got: %v", err)
	}
}

// TestValidate_DownloadPathRemap_Valid checks accepted remap formats.
func TestValidate_DownloadPathRemap_Valid(t *testing.T) {
	cases := []string{
		"/downloads:/media",
		"/downloads:/media,/nzb:/nzb-media",
		"",
	}
	for _, raw := range cases {
		cfg := &Config{
			LibraryDir:        t.TempDir(),
			DownloadPathRemap: raw,
		}
		if err := cfg.Validate(discardLogger()); err != nil {
			t.Errorf("remap %q should be valid, got: %v", raw, err)
		}
	}
}

// TestValidate_DownloadPathRemap_Invalid checks that a malformed remap value
// produces a warning but not an error (the server can still start, and the
// broken entry will simply be skipped by the importer).
func TestValidate_DownloadPathRemap_Invalid(t *testing.T) {
	cfg := &Config{
		LibraryDir:        t.TempDir(),
		DownloadPathRemap: "nodivider",
	}
	// A bad remap should produce a log warning, not a fatal error.
	if err := cfg.Validate(discardLogger()); err != nil {
		t.Errorf("invalid remap should warn, not fail; got: %v", err)
	}
}

// TestValidateAbsURL unit-tests the URL validator directly.
func TestValidateAbsURL(t *testing.T) {
	good := []string{
		"http://host",
		"https://host/path",
		"http://localhost:9000",
	}
	for _, u := range good {
		if err := validateAbsURL("X", u); err != nil {
			t.Errorf("validateAbsURL(%q) unexpected error: %v", u, err)
		}
	}

	bad := []struct {
		url     string
		wantErr string
	}{
		{"not-a-url", "http or https"},
		{"ftp://host", "http or https"},
		{"https://", "no host"},
		{"/path/only", "http or https"},
	}
	for _, tc := range bad {
		err := validateAbsURL("X", tc.url)
		if err == nil {
			t.Errorf("validateAbsURL(%q) expected error, got nil", tc.url)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("validateAbsURL(%q) error %q does not contain %q", tc.url, err.Error(), tc.wantErr)
		}
	}
}

// TestValidateDownloadPathRemap unit-tests the remap format checker directly.
func TestValidateDownloadPathRemap(t *testing.T) {
	good := []string{
		"/a:/b",
		"/a:/b,/c:/d",
		"",
	}
	for _, r := range good {
		if err := validateDownloadPathRemap(r); err != nil {
			t.Errorf("validateDownloadPathRemap(%q) unexpected error: %v", r, err)
		}
	}

	bad := []string{
		"nodivider",
		":empty-from",
	}
	for _, r := range bad {
		if err := validateDownloadPathRemap(r); err == nil {
			t.Errorf("validateDownloadPathRemap(%q) expected error, got nil", r)
		}
	}
}

// TestValidate_NilLogger checks that passing a nil logger doesn't panic
// (it should fall back to slog.Default()).
func TestValidate_NilLogger(t *testing.T) {
	cfg := &Config{
		LibraryDir: t.TempDir(),
	}
	if err := cfg.Validate(nil); err != nil {
		t.Errorf("nil logger should not cause error; got: %v", err)
	}
}
