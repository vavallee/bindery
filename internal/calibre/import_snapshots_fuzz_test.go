package calibre

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// FuzzRollbackMetadataDecode exercises the serialized rollback-metadata decode
// path with arbitrary strings. The metadata is JSON stored in
// calibre_entity_snapshots.metadata_json; a corrupt or attacker-tampered row
// must never panic the rollback worker — it must decode cleanly or report
// "not usable" (ok=false). We additionally feed any decoded snapshot straight
// into the restore functions so a malformed-but-parseable envelope can't crash
// the field-restore step either. Runs only the seed corpus under `go test`
// (bounded); doubles as an OpenSSF Scorecard fuzz target.
func FuzzRollbackMetadataDecode(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"null",
		"{}",
		"[]",
		"not json",
		`{"kind":"calibre_run_entity_metadata","version":1}`,
		`{"kind":"calibre_run_entity_metadata","version":1,"data":{"x":1}}`,
		`{"kind":"wrong","version":1}`,
		`{"kind":"calibre_run_entity_metadata","version":2}`,
		`{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":{"title":"a"},"after":{"title":"b"}}}`,
		`{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"author","before":{"name":"a"},"after":{"name":"b"}}}`,
		// Snapshot present but before/after empty (must yield ok=false).
		`{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book"}}`,
		// Mismatched entity type for each decoder.
		`{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"edition","before":{},"after":{}}}`,
		// Type-confused inner payloads: before/after that won't unmarshal into the snapshot struct.
		`{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":"oops","after":42}}`,
		`{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"author","before":[1,2,3],"after":{}}}`,
		// Deeply nested / oversized-ish field values.
		`{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":{"releaseDate":"not-a-time"},"after":{"calibreId":"x"}}}`,
		"\x00\x01\x02",
		`{"kind":"calibre_run_entity_metadata","version":1` + string(rune(0)),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		// Must never panic on any input. Results are intentionally ignored
		// except to thread decoded snapshots through the restore step below.
		bBefore, bAfter, bOK := bookRollbackSnapshotFromMetadata(raw)
		aBefore, aAfter, aOK := authorRollbackSnapshotFromMetadata(raw)
		_, _ = parseRunEntityMetadata(raw)

		// A decode that reports usable (ok=true) must return non-nil snapshots,
		// otherwise the rollback worker would dereference nil. Guard the
		// invariant rather than assume it.
		if bOK {
			if bBefore == nil || bAfter == nil {
				t.Fatalf("book decode ok=true but nil snapshot(s): before=%v after=%v", bBefore, bAfter)
			}
			// Restoring with a decoded snapshot must not panic either.
			restoreBookFromSnapshot(&models.Book{}, bBefore, bAfter)
		}
		if aOK {
			if aBefore == nil || aAfter == nil {
				t.Fatalf("author decode ok=true but nil snapshot(s): before=%v after=%v", aBefore, aAfter)
			}
			restoreAuthorFromSnapshot(&models.Author{}, aBefore, aAfter)
		}
	})
}
