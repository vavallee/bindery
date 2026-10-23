package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// manualImportCalls runs one path through every manual-import route that
// resolves a path, so a wording or status regression in any of them shows.
func manualImportCalls(h *ManualImportHandler, path string) map[string]*httptest.ResponseRecorder {
	out := map[string]*httptest.ResponseRecorder{}

	rec := httptest.NewRecorder()
	h.Lookup(rec, lookupRequest(path))
	out["Lookup"] = rec

	q := url.Values{"path": {path}, "targetBookId": {"1"}}.Encode()
	rec = httptest.NewRecorder()
	h.ReassignPreview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/queue/manual-import/reassign/preview?"+q, nil))
	out["ReassignPreview"] = rec

	body, _ := json.Marshal(map[string]any{"path": path, "targetBookId": 1})
	rec = httptest.NewRecorder()
	h.Reassign(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/reassign", bytes.NewReader(body)))
	out["Reassign"] = rec

	body, _ = json.Marshal(map[string]any{"path": path, "bookId": 1})
	rec = httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import", bytes.NewReader(body)))
	out["Import"] = rec
	return out
}

func assertUnreadable(t *testing.T, calls map[string]*httptest.ResponseRecorder, name string) {
	t.Helper()
	for route, rec := range calls {
		body := rec.Body.String()
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404; body = %s", route, rec.Code, body)
		}
		if !strings.Contains(body, "Bindery cannot read this file: "+name+".") {
			t.Errorf("%s body = %q, want the cannot-read message naming %q", route, body, name)
		}
		if strings.Contains(body, "outside the configured library roots") {
			t.Errorf("%s body = %q must not claim the path is outside the roots", route, body)
		}
	}
}

func assertOutside(t *testing.T, calls map[string]*httptest.ResponseRecorder) {
	t.Helper()
	for route, rec := range calls {
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403; body = %s", route, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "outside the configured library roots") {
			t.Errorf("%s body = %q, want the outside-roots message", route, rec.Body.String())
		}
	}
}

// A file the database remembers but the disk no longer has, written inside a
// root, used to be reported as outside it (#3332).
func TestManualImport_MissingInRootFileSaysCannotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	h := containmentHandler(root)
	missing := filepath.Join(root, "Martha Wells", "Book 1", "All Systems Red (Unabridged).mp3")
	calls := manualImportCalls(h, missing)
	assertUnreadable(t, calls, "All Systems Red (Unabridged).mp3")
	// The message names the file only, never the directory it was looked for in.
	if strings.Contains(calls["Lookup"].Body.String(), "Martha Wells") {
		t.Errorf("message leaks directory: %s", calls["Lookup"].Body.String())
	}
}

func TestManualImport_DanglingLinkIsOutside(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	link := filepath.Join(root, "gone.mp3")
	if err := os.Symlink(filepath.Join(root, "nowhere.mp3"), link); err != nil {
		t.Fatal(err)
	}
	assertOutside(t, manualImportCalls(containmentHandler(root), link))
}

func TestManualImport_UnreadableInRootFileSaysCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "book.mp3")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	assertUnreadable(t, manualImportCalls(containmentHandler(root), file), "book.mp3")
}

// Real escapes keep the 403 and the roots list, whether or not the target exists.
func TestManualImport_RealEscapesStayForbidden(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.mp3")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.mp3")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	h := containmentHandler(root)
	t.Run("symlink out of the root", func(t *testing.T) { assertOutside(t, manualImportCalls(h, link)) })
	t.Run("existing file outside", func(t *testing.T) { assertOutside(t, manualImportCalls(h, secret)) })
	t.Run("missing file outside", func(t *testing.T) {
		assertOutside(t, manualImportCalls(h, filepath.Join(outside, "missing.mp3")))
	})
	t.Run("dot dot out of the root", func(t *testing.T) {
		assertOutside(t, manualImportCalls(h, filepath.Join(root, "..", filepath.Base(outside), "secret.mp3")))
	})
	t.Run("sibling sharing the root prefix", func(t *testing.T) {
		assertOutside(t, manualImportCalls(h, root+"-other/book.mp3"))
	})
}

// Honest check of the other suspects from the report: a real, readable file
// that sits in a root must be accepted however the root or path is spelled.
func TestManualImport_ReadableInRootFileAccepted(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "Author"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(real, "Author", "book.mp3")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "linkroot")
	if err := os.Symlink(real, linkRoot); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		root string
		path string
	}{
		{"plain", real, file},
		{"root with trailing slash", real + "/", file},
		{"root with dot segments", real + "/./", file},
		{"path with doubled separators", real, real + "//Author//book.mp3"},
		{"root is a symlink, path via real", linkRoot, file},
		{"root is a symlink, path via link", linkRoot, filepath.Join(linkRoot, "Author", "book.mp3")},
		{"root is real, path via link", real, filepath.Join(linkRoot, "Author", "book.mp3")},
		{"root is the file's directory itself", filepath.Join(real, "Author"), file},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			roots := NewLibraryRoots(nil, c.root)
			if _, ok := roots.ResolveContained(t.Context(), c.path); !ok {
				t.Errorf("ResolveContained(%q) refused a readable file under root %q", c.path, c.root)
			}
		})
	}
}

// The cannot-read answer must never tell a caller anything about files outside
// the roots. Every request that resolves outside every root, existing or not,
// directly or through a link inside a root, must get the same 403 body.
func TestManualImport_OutsideProbesAreIndistinguishable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "passwd"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "later"), filepath.Join(root, "danglingout")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "etc"), filepath.Join(root, "chain")); err != nil {
		t.Fatal(err)
	}
	h := containmentHandler(root)
	paths := map[string]string{
		"existing direct":                 filepath.Join(outside, "passwd"),
		"missing direct":                  filepath.Join(outside, "nonexistent"),
		"missing deeper direct":           filepath.Join(outside, "nonexist", "x"),
		"existing via escaping link":      filepath.Join(root, "etc", "passwd"),
		"missing leaf via escaping link":  filepath.Join(root, "etc", "nonexistent"),
		"deeper missing via escaping":     filepath.Join(root, "etc", "nonexist", "x"),
		"dangling link to outside target": filepath.Join(root, "danglingout"),
		"below dangling outside link":     filepath.Join(root, "danglingout", "sub", "f.mp3"),
		"via a chain of links":            filepath.Join(root, "chain", "nonexistent"),
	}
	want := map[string]string{}
	for route, rec := range manualImportCalls(h, filepath.Join(outside, "passwd")) {
		want[route] = rec.Body.String()
	}
	for name, p := range paths {
		for route, rec := range manualImportCalls(h, p) {
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: status = %d, want 403; body = %s", name, route, rec.Code, rec.Body.String())
			}
			if rec.Body.String() != want[route] {
				t.Errorf("%s %s: body %q differs from %q", name, route, rec.Body.String(), want[route])
			}
		}
	}
}

// A missing file below a symlinked folder inside the root, and a dangling link
// whose target is inside the root, are still the honest cannot-read answer.
func TestManualImport_InRootThroughLinksSaysCannotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	h := containmentHandler(root)
	assertUnreadable(t, manualImportCalls(h, filepath.Join(root, "alias", "gone.mp3")), "gone.mp3")
}

// A relative dangling link is never followed by string arithmetic: the kernel
// applies ".." after resolving a symlink component, so joining and cleaning
// the target would misplace it. Every shape below lands outside the roots (or
// cannot be decided) and must answer the same 403 whether or not the target
// exists, so nothing one directory above the root can be probed.
func TestManualImport_RelativeLinkProbesAreIndistinguishable(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	for _, d := range []string{root, filepath.Join(base, "outside"), filepath.Join(root, "sub"), filepath.Join(root, "lib")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) string {
		p := filepath.Join(root, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	link("../outside", "relout")
	link("sub", "relin")
	rel2 := link("relout/../zz", "rel2")
	rel4 := link("relout/../outside/zz", "rel4")
	rel5 := link("relin/../lib/zz", "rel5")
	loop := link("loopb", "loopa")
	link("loopa", "loopb")
	h := containmentHandler(root)
	// Missing first, then the same names once their targets exist.
	probe := func() map[string]string {
		out := map[string]string{}
		for name, p := range map[string]string{"rel2": rel2, "rel4": rel4, "rel5": rel5, "loop": loop, "below loop": filepath.Join(loop, "x")} {
			for route, rec := range manualImportCalls(h, p) {
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s %s: status = %d, want 403; body = %s", name, route, rec.Code, rec.Body.String())
				}
				out[name+" "+route] = rec.Body.String()
			}
		}
		return out
	}
	missing := probe()
	for _, f := range []string{filepath.Join(base, "zz"), filepath.Join(base, "outside", "zz")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	existing := probe()
	for k, v := range missing {
		// Byte identical to the outside answer, existing target or not. rel5
		// lands inside the root and is only accepted once its target exists.
		if strings.HasPrefix(k, "rel5") {
			continue
		}
		if existing[k] != v {
			t.Errorf("%s: missing and existing targets differ: %q vs %q", k, v, existing[k])
		}
	}
	for k, v := range missing {
		if !strings.Contains(v, "outside the configured library roots") {
			t.Errorf("%s: body %q is not the outside roots text", k, v)
		}
	}
}

// A symlink loop must be refused quickly and with the same 403.
func TestManualImport_SymlinkLoopIsFastAndForbidden(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Symlink("loopb", filepath.Join(root, "loopa")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loopa", filepath.Join(root, "loopb")); err != nil {
		t.Fatal(err)
	}
	h := containmentHandler(root)
	start := time.Now()
	assertOutside(t, manualImportCalls(h, filepath.Join(root, "loopa")))
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("four loop requests took %v, want well under 100ms", d)
	}
}

// The wording check lists a parent directory only when that parent resolves
// inside a configured root, so no directory outside the library is ever read.
// Not parallel: it swaps the package level listing seam.
func TestManualImport_WordingNeverListsOutsideTheRoots(t *testing.T) {
	// Resolved, because the check lists resolved directories and the prefix
	// test below compares strings: on macOS t.TempDir() is under /var, a link
	// to /private/var.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(outside, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "later"), filepath.Join(root, "danglingout")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var listed []string
	orig := readDirForWording
	readDirForWording = func(name string) ([]os.DirEntry, error) {
		mu.Lock()
		listed = append(listed, name)
		mu.Unlock()
		return orig(name)
	}
	t.Cleanup(func() { readDirForWording = orig })
	h := containmentHandler(root)

	for _, p := range []string{
		filepath.Join(outside, "nonexistent"),
		filepath.Join(outside, "sub", "nope", "x"),
		filepath.Join(root, "etc", "nonexistent"),
		filepath.Join(root, "etc", "sub", "nope"),
		filepath.Join(root, "danglingout"),
	} {
		manualImportCalls(h, p)
	}
	for _, d := range listed {
		if !strings.HasPrefix(d, root) {
			t.Errorf("listed %q, a directory outside the roots", d)
		}
	}

	// Control: an in-root miss does list its parent, so the seam is live.
	listed = nil
	manualImportCalls(h, filepath.Join(root, "missing.mp3"))
	if len(listed) == 0 {
		t.Error("an in-root missing file never listed its parent; the seam is not exercised")
	}
}
