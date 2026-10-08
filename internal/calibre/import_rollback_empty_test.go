package calibre

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A run with nothing to undo must still serialise "actions" as an empty
// array. The settings modal reads actions.length, and a null there replaced
// the whole Calibre tab with the error page.
func TestImporter_PreviewRollback_NothingToUndoReturnsEmptyActions(t *testing.T) {
	imp, _, _, _, _, runsRepo, _, _ := newRollbackFixture(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		dryRun bool
	}{
		{"dry run", true},
		{"live run without snapshots", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &models.CalibreImportRun{LibraryPath: "/lib", Status: "completed", DryRun: tc.dryRun}
			if err := runsRepo.Create(ctx, run); err != nil {
				t.Fatalf("create run: %v", err)
			}
			result, err := imp.PreviewRollback(ctx, run.ID)
			if err != nil {
				t.Fatalf("PreviewRollback: %v", err)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(raw), `"actions":[]`) {
				t.Fatalf("preview JSON = %s, want \"actions\":[]", raw)
			}
		})
	}
}
