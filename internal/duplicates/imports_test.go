package duplicates

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIngestionPackagesNeverImportThis enforces the #1970 boundary: the
// aggressive detection layer may only be consumed by internal/api (the
// read-only surface a human reviews). No indexing, ingestion, metadata,
// database, or command package may import this package, or the aggressive
// normalization would leak into book-creation identity and trade
// missed-duplicate noise for silent false merges (#940, #2042).
//
// The test source-scans the whole repo tree rather than relying on review:
// a future import is a CI failure, not a judgment call.
func TestIngestionPackagesNeverImportThis(t *testing.T) {
	root := filepath.Join("..", "..")
	const forbidden = `"github.com/vavallee/bindery/internal/duplicates"`

	var offenders []string
	for _, dir := range []string{filepath.Join(root, "internal"), filepath.Join(root, "cmd")} {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				t.Logf("skipping %s: %v", path, err)
				return nil
			}
			if info.IsDir() {
				if path == filepath.Join(root, "internal", "duplicates") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			if strings.HasPrefix(rel, "internal/api/") {
				return nil // the one allowed consumer
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", rel, perr)
				return nil
			}
			for _, imp := range f.Imports {
				if imp.Path.Value == forbidden {
					offenders = append(offenders, rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("import boundary violated — these files import internal/duplicates outside internal/api:\n%s",
			strings.Join(offenders, "\n"))
	}
}
