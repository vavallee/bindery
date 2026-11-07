package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// sourcesFixture wires a manual import handler whose only static root is
// library, with import.source_folders read from a real settings repo (#2168).
func sourcesFixture(t *testing.T, library string, sources ...string) (*ManualImportHandler, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSettingsRepo(database)
	if len(sources) > 0 {
		if err := repo.Set(t.Context(), SettingImportSourceFolders, strings.Join(sources, "\n")); err != nil {
			t.Fatal(err)
		}
	}
	h, _, _, _, _ := manualImportFixture(t)
	h.WithRoots(NewLibraryRoots(nil, library).WithImportSources(repo))
	return h, repo
}

func TestImportSources_ConfiguredFolderIsScannableAndBrowsable(t *testing.T) {
	library, source := resolvedTempDir(t), resolvedTempDir(t)
	if err := os.Mkdir(filepath.Join(source, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	h, _ := sourcesFixture(t, library, source)

	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(source))
	if rec.Code != http.StatusOK {
		t.Fatalf("scan of a configured source = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.BrowseFolders(rec, browseFoldersRequest(""))
	var top ImportFolderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatal(err)
	}
	if len(top.Directories) != 2 {
		t.Fatalf("top level = %v, want library and source", top.Directories)
	}

	rec = httptest.NewRecorder()
	h.BrowseFolders(rec, browseFoldersRequest(source))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "old") {
		t.Fatalf("browse source = %d %s", rec.Code, rec.Body)
	}
}

func TestImportSources_UnlistedFolderRefused(t *testing.T) {
	library, source, other := resolvedTempDir(t), resolvedTempDir(t), resolvedTempDir(t)
	h, _ := sourcesFixture(t, library, source)

	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(other))
	if rec.Code == http.StatusOK {
		t.Error("scan of an unlisted folder = 200, want refusal")
	}
	rec = httptest.NewRecorder()
	h.BrowseFolders(rec, browseFoldersRequest(other))
	if rec.Code == http.StatusOK {
		t.Error("browse of an unlisted folder = 200, want refusal")
	}

	// A sibling that merely shares the prefix of a source is not inside it.
	sibling := source + "-evil"
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(sibling) })
	rec = httptest.NewRecorder()
	h.Scan(rec, scanRequest(sibling))
	if rec.Code == http.StatusOK {
		t.Error("prefix sibling of a source was accepted")
	}
}

func TestImportSources_SymlinkEscapeRefused(t *testing.T) {
	library, source, secret := resolvedTempDir(t), resolvedTempDir(t), resolvedTempDir(t)
	if err := os.Symlink(secret, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	h, _ := sourcesFixture(t, library, source)

	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(filepath.Join(source, "escape")))
	if rec.Code == http.StatusOK {
		t.Errorf("scan through an escaping symlink = 200: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	h.Scan(rec, scanRequest(source+"/../"+filepath.Base(secret)))
	if rec.Code == http.StatusOK {
		t.Error("traversal out of a source was accepted")
	}
	rec = httptest.NewRecorder()
	h.BrowseFolders(rec, browseFoldersRequest(source))
	if strings.Contains(rec.Body.String(), "escape") {
		t.Errorf("browse listed an escaping symlink: %s", rec.Body)
	}
}

// A source that is itself a symlink keeps working, and removing it from the
// setting takes effect without a restart.
func TestImportSources_ResolvedPerRequest(t *testing.T) {
	library, real := resolvedTempDir(t), resolvedTempDir(t)
	link := filepath.Join(resolvedTempDir(t), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	h, repo := sourcesFixture(t, library, link)
	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(real))
	if rec.Code != http.StatusOK {
		t.Fatalf("symlinked source = %d: %s", rec.Code, rec.Body)
	}
	if err := repo.Set(t.Context(), SettingImportSourceFolders, ""); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.Scan(rec, scanRequest(real))
	if rec.Code == http.StatusOK {
		t.Error("source still readable after removal from the setting")
	}
}

// Import sources must never leak into the delete roots.
func TestImportSources_NotLibraryRoots(t *testing.T) {
	library, source := resolvedTempDir(t), resolvedTempDir(t)
	_, repo := sourcesFixture(t, library, source)
	libraryOnly := NewLibraryRoots(nil, library)
	if libraryOnly.Contains(t.Context(), filepath.Join(source, "a.epub")) {
		t.Error("library roots contain an import source")
	}
	withSources := NewLibraryRoots(nil, library).WithImportSources(repo)
	if !withSources.Contains(t.Context(), filepath.Join(source, "a.epub")) {
		t.Error("import roots do not contain the source")
	}
}

func TestImportSources_NonAdminRefused(t *testing.T) {
	library, source := resolvedTempDir(t), resolvedTempDir(t)
	h, repo := sourcesFixture(t, library, source)
	settings := NewSettingsHandler(repo)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/folders", h.BrowseFolders)
		r.Get("/scan", h.Scan)
		r.Put("/setting/{key}", settings.Set)
	})
	do := func(role, method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req = req.WithContext(auth.WithUserRole(req.Context(), role))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	for _, role := range []string{auth.RoleUser, auth.RoleRequester, ""} {
		for _, c := range [][3]string{
			{"GET", "/folders?path=" + source, ""},
			{"GET", "/scan?path=" + source, ""},
			{"PUT", "/setting/" + SettingImportSourceFolders, `{"value":"` + source + `"}`},
		} {
			if rec := do(role, c[0], c[1], c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("role %q %s %s = %d, want 403", role, c[0], c[1], rec.Code)
			}
		}
	}
	if rec := do(auth.RoleAdmin, "GET", "/folders?path="+source, ""); rec.Code != http.StatusOK {
		t.Errorf("admin browse = %d", rec.Code)
	}
	// And the stored value is not readable by a non admin.
	if rec := getAs(settings, auth.RoleUser, SettingImportSourceFolders); rec.Code != http.StatusNotFound {
		t.Errorf("non admin read of the setting = %d, want 404", rec.Code)
	}
}

func TestImportSources_SettingValidation(t *testing.T) {
	dir := resolvedTempDir(t)
	file := filepath.Join(dir, "f")
	writeTestFile(t, file)
	for name, tc := range map[string]struct {
		value string
		bad   bool
	}{
		"empty":     {"", false},
		"directory": {dir, false},
		"two lines": {dir + "\n" + dir + "/", false},
		"relative":  {"books/old", true},
		"missing":   {filepath.Join(dir, "nope"), true},
		"file":      {file, true},
		"root":      {"/", true},
	} {
		if err := validateSettingValue(SettingImportSourceFolders, tc.value); (err != nil) != tc.bad {
			t.Errorf("%s: err = %v, want bad=%v", name, err, tc.bad)
		}
	}
}

func TestImportSources_SymlinkToRootOrDataDirRefused(t *testing.T) {
	_, repo := sourcesFixture(t, resolvedTempDir(t))
	h := NewSettingsHandler(repo)
	data := resolvedTempDir(t)
	h.WithProtectedDirs(data, filepath.Join(data, "db"))
	linkDir := resolvedTempDir(t)
	toRoot := filepath.Join(linkDir, "toroot")
	toData := filepath.Join(linkDir, "todata")
	if err := os.Symlink("/", toRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, toData); err != nil {
		t.Fatal(err)
	}
	inData := filepath.Join(data, "sub")
	if err := os.Mkdir(inData, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"link to root": toRoot, "link to data dir": toData, "inside data dir": inData, "parent of data dir": filepath.Dir(data)} {
		if err := h.validateImportSourceTargets(v); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := h.validateImportSourceTargets(resolvedTempDir(t)); err != nil {
		t.Errorf("unrelated folder refused: %v", err)
	}
}

func TestImportSources_ListCap(t *testing.T) {
	dir := resolvedTempDir(t)
	lines := make([]string, 0, 33)
	for i := 0; i < 33; i++ {
		lines = append(lines, dir)
	}
	if err := validateSettingValue(SettingImportSourceFolders, strings.Join(lines[:32], "\n")); err != nil {
		t.Errorf("32 paths refused: %v", err)
	}
	if err := validateSettingValue(SettingImportSourceFolders, strings.Join(lines, "\n")); err == nil {
		t.Error("33 paths accepted")
	}
}
