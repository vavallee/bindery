package calibre

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner captures every invocation so tests can assert on the full
// argv, not just the exit status. Swapping out the runner is the only way
// to table-test argument construction without requiring calibredb on the
// CI machine.
type fakeRunner struct {
	stdout []byte
	err    error
	bin    string
	args   []string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.bin = name
	f.args = args
	return f.stdout, f.err
}

func newTestClient(cfg Config, out string, runErr error) (*Client, *fakeRunner) {
	c := New(cfg)
	fr := &fakeRunner{stdout: []byte(out), err: runErr}
	c.run = fr.run
	return c, fr
}

func TestAdd_DisabledReturnsErrDisabled(t *testing.T) {
	c := New(Config{Enabled: false})
	_, err := c.Add(context.Background(), "/tmp/x.epub")
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

// TestAdd_ArgConstruction is the table-test asked for in the scope. Each
// case exercises a different permutation of binary path, library path, and
// input file so future refactors cannot silently rearrange the argv.
func TestAdd_ArgConstruction(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		file     string
		wantBin  string
		wantArgs []string
	}{
		{
			name:     "default binary, bare filename",
			cfg:      Config{Enabled: true, LibraryPath: "/calibre/lib"},
			file:     "book.epub",
			wantBin:  "calibredb",
			wantArgs: []string{"add", "--with-library", "/calibre/lib", "book.epub"},
		},
		{
			name:     "explicit binary path",
			cfg:      Config{Enabled: true, BinaryPath: "/usr/local/bin/calibredb", LibraryPath: "/books"},
			file:     "/downloads/b.epub",
			wantBin:  "/usr/local/bin/calibredb",
			wantArgs: []string{"add", "--with-library", "/books", "/downloads/b.epub"},
		},
		{
			name:     "library path with spaces",
			cfg:      Config{Enabled: true, LibraryPath: "/mnt/My Calibre"},
			file:     "/x.mobi",
			wantBin:  "calibredb",
			wantArgs: []string{"add", "--with-library", "/mnt/My Calibre", "/x.mobi"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fr := newTestClient(tc.cfg, "Added book ids: 42\n", nil)
			id, err := c.Add(context.Background(), tc.file)
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			if id != 42 {
				t.Errorf("id = %d, want 42", id)
			}
			if fr.bin != tc.wantBin {
				t.Errorf("bin = %q, want %q", fr.bin, tc.wantBin)
			}
			if len(fr.args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", fr.args, tc.wantArgs)
			}
			for i := range fr.args {
				if fr.args[i] != tc.wantArgs[i] {
					t.Errorf("arg[%d] = %q, want %q", i, fr.args[i], tc.wantArgs[i])
				}
			}
		})
	}
}

func TestAdd_EmptyLibraryPath(t *testing.T) {
	c, _ := newTestClient(Config{Enabled: true}, "", nil)
	_, err := c.Add(context.Background(), "/x.epub")
	if err == nil || !strings.Contains(err.Error(), "library_path") {
		t.Fatalf("expected library_path error, got %v", err)
	}
}

func TestAdd_ParsesMultiIDList(t *testing.T) {
	c, _ := newTestClient(Config{Enabled: true, LibraryPath: "/lib"}, "Added book ids: 7, 8, 9\n", nil)
	id, err := c.Add(context.Background(), "/x.epub")
	if err != nil {
		t.Fatal(err)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7 (first of list)", id)
	}
}

func TestAdd_WrapsRunnerError(t *testing.T) {
	runErr := errors.New("boom")
	c, _ := newTestClient(Config{Enabled: true, LibraryPath: "/lib"}, "calibredb: not found", runErr)
	_, err := c.Add(context.Background(), "/x.epub")
	if err == nil {
		t.Fatal("expected error")
	}
	// Both the runner error and the stderr payload must appear in the
	// wrapped message — operators rely on stderr to diagnose permission
	// and path issues.
	if !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should include cause + stderr: %v", err)
	}
}

func TestAdd_UnparseableOutput(t *testing.T) {
	c, _ := newTestClient(Config{Enabled: true, LibraryPath: "/lib"}, "Some unrelated chatter", nil)
	_, err := c.Add(context.Background(), "/x.epub")
	if err == nil || !strings.Contains(err.Error(), "Added book ids") {
		t.Fatalf("expected parse error mentioning Added book ids, got %v", err)
	}
}

func TestTest_DisabledReturnsErrDisabled(t *testing.T) {
	c := New(Config{Enabled: false})
	_, err := c.Test(context.Background())
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestTest_RejectsNonDirLibraryPath(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(file, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := newTestClient(Config{Enabled: true, LibraryPath: file}, "calibre (6.0.0)", nil)
	_, err := c.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected not-a-directory error, got %v", err)
	}
}

func TestTest_MissingLibraryPath(t *testing.T) {
	c, _ := newTestClient(Config{Enabled: true, LibraryPath: "/nope/does/not/exist"}, "", nil)
	_, err := c.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "library_path") {
		t.Fatalf("expected library_path error, got %v", err)
	}
}

func TestTest_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	c, fr := newTestClient(Config{Enabled: true, LibraryPath: tmp}, "calibre (6.0.0)\n", nil)
	v, err := c.Test(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "calibre (6.0.0)" {
		t.Errorf("version = %q", v)
	}
	if len(fr.args) != 1 || fr.args[0] != "--version" {
		t.Errorf("expected single --version arg, got %v", fr.args)
	}
}

func TestEnabled_NilSafe(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Error("nil client must report not enabled")
	}
}

func TestParseAddedID(t *testing.T) {
	cases := []struct {
		in    string
		want  int64
		fails bool
	}{
		{"Added book ids: 12\n", 12, false},
		{"Added book ids: 7, 8, 9\n", 7, false},
		{"Added book ids:   42  \n", 42, false},
		{"some preamble\nAdded book ids: 1\nmore output", 1, false},
		{"", 0, true},
		{"nothing to see", 0, true},
		{"Added book ids: not-a-number", 0, true},
	}
	for _, tc := range cases {
		got, err := parseAddedID([]byte(tc.in))
		if tc.fails {
			if err == nil {
				t.Errorf("parseAddedID(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseAddedID(%q) err = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseAddedID(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
