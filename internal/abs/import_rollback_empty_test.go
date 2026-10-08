package abs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A run with nothing to undo must still serialise "actions" as an empty
// array, not null, so the settings UI can read its length.
func TestRollbackPreview_NothingToUndoReturnsEmptyActions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)

	for _, tc := range []struct {
		name   string
		dryRun bool
	}{
		{"dry run", true},
		{"live run without entities", false},
	} {
		run := &models.ABSImportRun{SourceID: DefaultSourceID, LibraryID: "lib-books", Status: "completed", DryRun: tc.dryRun}
		if err := env.runs.Create(ctx, run); err != nil {
			t.Fatalf("%s: create run: %v", tc.name, err)
		}
		result, err := env.importer.RollbackPreview(ctx, run.ID)
		if err != nil {
			t.Fatalf("%s: RollbackPreview: %v", tc.name, err)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if !strings.Contains(string(raw), `"actions":[]`) {
			t.Fatalf("%s: preview JSON = %s, want \"actions\":[]", tc.name, raw)
		}
	}
}
