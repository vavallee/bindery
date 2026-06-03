package calibre

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// ----------------------------------------------------------------------------
// Snapshot -> mutate -> restore round-trip (highest-value coverage).
//
// The restore* family only reverts a field when the live value still equals
// the post-import ("after") snapshot. So a faithful round-trip mimics the real
// rollback flow: snapshot the original ("before"), mutate the entity to its
// post-import state ("after"), snapshot that, then restore the mutated entity
// and assert every field is back to the original.
// ----------------------------------------------------------------------------

// int64Ptr / strPtr already exist in the package's test files; reuse them.
// tPtr returns a *time.Time with full time-of-day precision (the existing
// ptrTime helper only takes y/m/d).
func tPtr(t time.Time) *time.Time { return &t }

func TestBookSnapshotRestore_RoundTrip(t *testing.T) {
	rel := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	cid := int64(99)

	original := &models.Book{
		ID:               7,
		ForeignID:        "fid-original",
		AuthorID:         11,
		Title:            "Original Title",
		SortTitle:        "Original, Title",
		ReleaseDate:      tPtr(rel),
		Language:         "en",
		Status:           "released",
		FilePath:         "/lib/orig.epub",
		MetadataProvider: "calibre",
		CalibreID:        int64Ptr(cid),
		MediaType:        "ebook",
		AnyEditionOK:     true,
		Monitored:        false,
	}

	// Snapshot the original ("before"). Deep-copy so later mutation of the
	// live book can't leak through the snapshot's pointer fields.
	before := bookSnapshot(original)
	if before == nil {
		t.Fatal("bookSnapshot(original) = nil")
	}

	// Mutate the live book to its post-import state and snapshot that
	// ("after"). Touch every field, including the pointer ones, so the
	// restore exercises every helper.
	mutated := *original
	newRel := time.Date(2022, 12, 31, 23, 59, 58, 0, time.UTC)
	newCID := int64(12345)
	mutated.ForeignID = "fid-imported"
	mutated.AuthorID = 22
	mutated.Title = "Imported Title"
	mutated.SortTitle = "Imported, Title"
	mutated.ReleaseDate = tPtr(newRel)
	mutated.Language = "de"
	mutated.Status = "in_progress"
	mutated.FilePath = "/lib/imported.epub"
	mutated.MetadataProvider = "google"
	mutated.CalibreID = int64Ptr(newCID)
	mutated.MediaType = "audiobook"
	mutated.AnyEditionOK = false
	mutated.Monitored = true

	after := bookSnapshot(&mutated)
	if after == nil {
		t.Fatal("bookSnapshot(mutated) = nil")
	}

	// The live book currently sits at the "after" state. Restore it.
	live := mutated
	changed := restoreBookFromSnapshot(&live, before, after)
	if !changed {
		t.Fatal("restoreBookFromSnapshot reported no change, but every field was mutated")
	}

	// Assert every snapshotted field is back to the original.
	if live.ForeignID != original.ForeignID {
		t.Errorf("ForeignID = %q, want %q", live.ForeignID, original.ForeignID)
	}
	if live.AuthorID != original.AuthorID {
		t.Errorf("AuthorID = %d, want %d", live.AuthorID, original.AuthorID)
	}
	if live.Title != original.Title {
		t.Errorf("Title = %q, want %q", live.Title, original.Title)
	}
	if live.SortTitle != original.SortTitle {
		t.Errorf("SortTitle = %q, want %q", live.SortTitle, original.SortTitle)
	}
	if !equalTimePtr(live.ReleaseDate, original.ReleaseDate) {
		t.Errorf("ReleaseDate = %v, want %v", live.ReleaseDate, original.ReleaseDate)
	}
	if live.Language != original.Language {
		t.Errorf("Language = %q, want %q", live.Language, original.Language)
	}
	if live.Status != original.Status {
		t.Errorf("Status = %q, want %q", live.Status, original.Status)
	}
	if live.FilePath != original.FilePath {
		t.Errorf("FilePath = %q, want %q", live.FilePath, original.FilePath)
	}
	if live.MetadataProvider != original.MetadataProvider {
		t.Errorf("MetadataProvider = %q, want %q", live.MetadataProvider, original.MetadataProvider)
	}
	if !equalInt64Ptr(live.CalibreID, original.CalibreID) {
		t.Errorf("CalibreID = %v, want %v", live.CalibreID, original.CalibreID)
	}
	if live.MediaType != original.MediaType {
		t.Errorf("MediaType = %q, want %q", live.MediaType, original.MediaType)
	}
	if live.AnyEditionOK != original.AnyEditionOK {
		t.Errorf("AnyEditionOK = %v, want %v", live.AnyEditionOK, original.AnyEditionOK)
	}
	if live.Monitored != original.Monitored {
		t.Errorf("Monitored = %v, want %v", live.Monitored, original.Monitored)
	}

	// Restoring CalibreID must not alias the snapshot's pointer.
	if live.CalibreID == before.CalibreID {
		t.Error("restored CalibreID aliases the snapshot pointer; clone expected")
	}
}

// Round-trip with nil pointer fields in the ORIGINAL: import sets them, restore
// must put them back to nil. Covers the restore*Ptr helpers' nil branch and
// equal*Ptr (one-nil) paths.
func TestBookSnapshotRestore_RoundTrip_NilToValueToNil(t *testing.T) {
	original := &models.Book{
		ID:          1,
		Title:       "T",
		ReleaseDate: nil,
		CalibreID:   nil,
	}
	before := bookSnapshot(original)

	mutated := *original
	mutated.ReleaseDate = tPtr(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	mutated.CalibreID = int64Ptr(42)
	after := bookSnapshot(&mutated)

	live := mutated
	if !restoreBookFromSnapshot(&live, before, after) {
		t.Fatal("expected restore to report change (ptr value -> nil)")
	}
	if live.ReleaseDate != nil {
		t.Errorf("ReleaseDate = %v, want nil", live.ReleaseDate)
	}
	if live.CalibreID != nil {
		t.Errorf("CalibreID = %v, want nil", live.CalibreID)
	}
}

// If the user edited a field AFTER import (live no longer equals "after"),
// restore must leave that field untouched.
func TestRestoreBookFromSnapshot_PreservesPostImportEdit(t *testing.T) {
	before := &bookRollbackSnapshot{Title: "Original", CalibreID: int64Ptr(1)}
	after := &bookRollbackSnapshot{Title: "Imported", CalibreID: int64Ptr(2)}

	// Live book: user renamed the title after import, but CalibreID still
	// matches the imported value.
	live := &models.Book{Title: "User Rename", CalibreID: int64Ptr(2)}
	changed := restoreBookFromSnapshot(live, before, after)
	if !changed {
		t.Fatal("expected CalibreID restore to report change")
	}
	if live.Title != "User Rename" {
		t.Errorf("Title = %q, want preserved %q", live.Title, "User Rename")
	}
	if !equalInt64Ptr(live.CalibreID, int64Ptr(1)) {
		t.Errorf("CalibreID = %v, want restored to 1", live.CalibreID)
	}
}

// No-op restore: before == after means nothing to revert, changed must be
// false even though every field "matches".
func TestRestoreBookFromSnapshot_NoChangeWhenBeforeEqualsAfter(t *testing.T) {
	snap := &bookRollbackSnapshot{Title: "Same", CalibreID: int64Ptr(5), ReleaseDate: tPtr(time.Unix(0, 0).UTC())}
	live := &models.Book{Title: "Same", CalibreID: int64Ptr(5), ReleaseDate: tPtr(time.Unix(0, 0).UTC())}
	if restoreBookFromSnapshot(live, snap, snap) {
		t.Error("restore reported change when before == after")
	}
}

// restoreTimePtr / restoreInt64Ptr must NOT touch a pointer field the user
// edited after import (live != after). Covers the early-return branch of the
// pointer restorers.
func TestRestorePtr_PreservesPostImportEdit(t *testing.T) {
	origRel := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	importedRel := time.Date(2005, 1, 1, 0, 0, 0, 0, time.UTC)
	userRel := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	before := &bookRollbackSnapshot{ReleaseDate: tPtr(origRel), CalibreID: int64Ptr(1)}
	after := &bookRollbackSnapshot{ReleaseDate: tPtr(importedRel), CalibreID: int64Ptr(2)}

	// Live values differ from both before and after (user re-edited).
	live := &models.Book{ReleaseDate: tPtr(userRel), CalibreID: int64Ptr(9)}
	if restoreBookFromSnapshot(live, before, after) {
		t.Error("restore reported change but user-edited pointers should be preserved")
	}
	if !live.ReleaseDate.Equal(userRel) {
		t.Errorf("ReleaseDate = %v, want preserved %v", live.ReleaseDate, userRel)
	}
	if !equalInt64Ptr(live.CalibreID, int64Ptr(9)) {
		t.Errorf("CalibreID = %v, want preserved 9", live.CalibreID)
	}
}

func TestRestoreBookFromSnapshot_NilArgs(t *testing.T) {
	b := &models.Book{Title: "x"}
	if restoreBookFromSnapshot(nil, &bookRollbackSnapshot{}, &bookRollbackSnapshot{}) {
		t.Error("nil book should report no change")
	}
	if restoreBookFromSnapshot(b, nil, &bookRollbackSnapshot{}) {
		t.Error("nil before should report no change")
	}
	if restoreBookFromSnapshot(b, &bookRollbackSnapshot{}, nil) {
		t.Error("nil after should report no change")
	}
}

func TestAuthorSnapshotRestore_RoundTrip(t *testing.T) {
	original := &models.Author{
		ID:               3,
		ForeignID:        "afid-orig",
		Name:             "Original Name",
		SortName:         "Name, Original",
		MetadataProvider: "calibre",
		Monitored:        false,
	}
	before := authorRollbackSnapshotFromAuthor(original)

	mutated := *original
	mutated.ForeignID = "afid-imported"
	mutated.Name = "Imported Name"
	mutated.SortName = "Name, Imported"
	mutated.MetadataProvider = "google"
	mutated.Monitored = true
	after := authorRollbackSnapshotFromAuthor(&mutated)

	live := mutated
	if !restoreAuthorFromSnapshot(&live, before, after) {
		t.Fatal("restoreAuthorFromSnapshot reported no change")
	}
	if live.ForeignID != original.ForeignID {
		t.Errorf("ForeignID = %q, want %q", live.ForeignID, original.ForeignID)
	}
	if live.Name != original.Name {
		t.Errorf("Name = %q, want %q", live.Name, original.Name)
	}
	if live.SortName != original.SortName {
		t.Errorf("SortName = %q, want %q", live.SortName, original.SortName)
	}
	if live.MetadataProvider != original.MetadataProvider {
		t.Errorf("MetadataProvider = %q, want %q", live.MetadataProvider, original.MetadataProvider)
	}
	if live.Monitored != original.Monitored {
		t.Errorf("Monitored = %v, want %v", live.Monitored, original.Monitored)
	}
}

func TestRestoreAuthorFromSnapshot_NilArgs(t *testing.T) {
	a := &models.Author{Name: "x"}
	if restoreAuthorFromSnapshot(nil, &authorRollbackSnapshot{}, &authorRollbackSnapshot{}) {
		t.Error("nil author should report no change")
	}
	if restoreAuthorFromSnapshot(a, nil, &authorRollbackSnapshot{}) {
		t.Error("nil before should report no change")
	}
	if restoreAuthorFromSnapshot(a, &authorRollbackSnapshot{}, nil) {
		t.Error("nil after should report no change")
	}
}

// authorRollbackSnapshotFromAuthor mirrors bookSnapshot for authors. The
// production code only builds author snapshots inline (no exported helper), so
// the test constructs the same shape directly.
func authorRollbackSnapshotFromAuthor(a *models.Author) *authorRollbackSnapshot {
	return &authorRollbackSnapshot{
		ForeignID:        a.ForeignID,
		Name:             a.Name,
		SortName:         a.SortName,
		MetadataProvider: a.MetadataProvider,
		Monitored:        a.Monitored,
	}
}

// ----------------------------------------------------------------------------
// editionSnapshot: covers cloneStringPtr / cloneTimePtr value + nil branches.
// ----------------------------------------------------------------------------

func TestEditionSnapshot_ClonesPointers(t *testing.T) {
	pub := time.Date(2010, 5, 5, 0, 0, 0, 0, time.UTC)
	e := &models.Edition{
		ID:          4,
		ForeignID:   "efid",
		BookID:      8,
		Title:       "Ed",
		ISBN13:      strPtr("9780000000001"),
		PublishDate: tPtr(pub),
		Format:      "EPUB",
		Language:    "en",
		ImageURL:    "http://img",
		IsEbook:     true,
		Monitored:   true,
	}
	snap := editionSnapshot(e)
	if snap == nil {
		t.Fatal("editionSnapshot = nil")
	}
	if snap.ISBN13 == nil || *snap.ISBN13 != *e.ISBN13 {
		t.Errorf("ISBN13 = %v, want %v", snap.ISBN13, e.ISBN13)
	}
	if snap.ISBN13 == e.ISBN13 {
		t.Error("ISBN13 aliases source pointer; clone expected")
	}
	if snap.PublishDate == nil || !snap.PublishDate.Equal(pub) {
		t.Errorf("PublishDate = %v, want %v", snap.PublishDate, pub)
	}
	if snap.PublishDate == e.PublishDate {
		t.Error("PublishDate aliases source pointer; clone expected")
	}

	// nil pointer fields stay nil.
	enil := &models.Edition{ID: 5, ISBN13: nil, PublishDate: nil}
	snil := editionSnapshot(enil)
	if snil.ISBN13 != nil || snil.PublishDate != nil {
		t.Errorf("nil pointers not preserved: ISBN13=%v PublishDate=%v", snil.ISBN13, snil.PublishDate)
	}
}

func TestSnapshot_NilInputs(t *testing.T) {
	if bookSnapshot(nil) != nil {
		t.Error("bookSnapshot(nil) != nil")
	}
	if editionSnapshot(nil) != nil {
		t.Error("editionSnapshot(nil) != nil")
	}
}

// ----------------------------------------------------------------------------
// marshalSnapshotPayload + parseRunEntityMetadata round-trip.
// ----------------------------------------------------------------------------

func TestBookSnapshotMetadata_RoundTrip(t *testing.T) {
	before := &bookRollbackSnapshot{
		ForeignID:   "b",
		Title:       "Before",
		CalibreID:   int64Ptr(1),
		ReleaseDate: tPtr(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)),
		Monitored:   true,
	}
	after := &bookRollbackSnapshot{
		ForeignID: "b",
		Title:     "After",
		CalibreID: int64Ptr(2),
	}
	env, err := bookSnapshotMetadata(map[string]any{"note": "imported"}, before, after)
	if err != nil {
		t.Fatalf("bookSnapshotMetadata: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	gotBefore, gotAfter, ok := bookRollbackSnapshotFromMetadata(string(raw))
	if !ok {
		t.Fatal("bookRollbackSnapshotFromMetadata: ok = false")
	}
	if gotBefore.Title != "Before" || gotAfter.Title != "After" {
		t.Errorf("titles = %q/%q, want Before/After", gotBefore.Title, gotAfter.Title)
	}
	if !equalInt64Ptr(gotBefore.CalibreID, int64Ptr(1)) {
		t.Errorf("before CalibreID = %v, want 1", gotBefore.CalibreID)
	}
	if !equalTimePtr(gotBefore.ReleaseDate, before.ReleaseDate) {
		t.Errorf("before ReleaseDate = %v, want %v", gotBefore.ReleaseDate, before.ReleaseDate)
	}
	if !gotBefore.Monitored {
		t.Error("before Monitored = false, want true")
	}

	// parseRunEntityMetadata directly: Data must survive, Kind/Version checked.
	parsed, ok := parseRunEntityMetadata(string(raw))
	if !ok {
		t.Fatal("parseRunEntityMetadata: ok = false")
	}
	if parsed.Kind != runEntityMetadataKind || parsed.Version != runEntityMetadataVersion {
		t.Errorf("kind/version = %q/%d", parsed.Kind, parsed.Version)
	}
	if parsed.Data["note"] != "imported" {
		t.Errorf("Data[note] = %v, want imported", parsed.Data["note"])
	}
}

func TestEditionSnapshotMetadata_RoundTrip(t *testing.T) {
	before := &editionRollbackSnapshot{ForeignID: "e", Title: "Ed Before", ISBN13: strPtr("9781111111111")}
	after := &editionRollbackSnapshot{ForeignID: "e", Title: "Ed After"}
	env, err := editionSnapshotMetadata(nil, before, after)
	if err != nil {
		t.Fatalf("editionSnapshotMetadata: %v", err)
	}
	if env.Snapshot == nil || env.Snapshot.EntityType != entityTypeEdition {
		t.Fatalf("envelope snapshot = %+v", env.Snapshot)
	}
	var gotBefore editionRollbackSnapshot
	if err := json.Unmarshal(env.Snapshot.Before, &gotBefore); err != nil {
		t.Fatalf("unmarshal before: %v", err)
	}
	if gotBefore.Title != "Ed Before" || gotBefore.ISBN13 == nil || *gotBefore.ISBN13 != "9781111111111" {
		t.Errorf("before = %+v", gotBefore)
	}
}

func TestMarshalSnapshotPayload_TypedNil(t *testing.T) {
	// Typed-nil pointers must marshal to a nil RawMessage, NOT JSON "null",
	// so the restorers can distinguish "no snapshot" from a real one.
	var b *bookRollbackSnapshot
	var a *authorRollbackSnapshot
	var e *editionRollbackSnapshot
	for name, v := range map[string]any{"book": b, "author": a, "edition": e} {
		got, err := marshalSnapshotPayload(v)
		if err != nil {
			t.Fatalf("%s: marshalSnapshotPayload err = %v", name, err)
		}
		if got != nil {
			t.Errorf("%s: payload = %s, want nil", name, string(got))
		}
	}
	// Untyped nil too.
	if got, err := marshalSnapshotPayload(nil); err != nil || got != nil {
		t.Errorf("nil: got %s err %v, want nil/nil", string(got), err)
	}
	// Non-nil marshals real JSON.
	got, err := marshalSnapshotPayload(&bookRollbackSnapshot{Title: "x"})
	if err != nil || len(got) == 0 {
		t.Fatalf("non-nil snapshot: got %s err %v", string(got), err)
	}
}

func TestParseRunEntityMetadata_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"whitespace":    "   ",
		"not json":      "{not json",
		"wrong kind":    `{"kind":"other","version":1}`,
		"wrong version": `{"kind":"calibre_run_entity_metadata","version":99}`,
	}
	for name, raw := range cases {
		if _, ok := parseRunEntityMetadata(raw); ok {
			t.Errorf("%s: parseRunEntityMetadata ok = true, want false", name)
		}
	}
	// Valid envelope with nil data: parse must default Data to a non-nil map.
	valid := `{"kind":"calibre_run_entity_metadata","version":1}`
	env, ok := parseRunEntityMetadata(valid)
	if !ok {
		t.Fatal("valid envelope rejected")
	}
	if env.Data == nil {
		t.Error("parsed Data is nil; want defaulted empty map")
	}
}

func TestRollbackSnapshotFromMetadata_Rejects(t *testing.T) {
	// Snapshot present but EntityType mismatched.
	mismatch := `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"author","before":{},"after":{}}}`
	if _, _, ok := bookRollbackSnapshotFromMetadata(mismatch); ok {
		t.Error("book-from-metadata accepted an author snapshot")
	}
	// Missing before/after halves.
	missingAfter := `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":{"title":"x"}}}`
	if _, _, ok := bookRollbackSnapshotFromMetadata(missingAfter); ok {
		t.Error("book-from-metadata accepted snapshot missing after half")
	}
	// No snapshot envelope at all.
	noSnap := `{"kind":"calibre_run_entity_metadata","version":1}`
	if _, _, ok := bookRollbackSnapshotFromMetadata(noSnap); ok {
		t.Error("book-from-metadata accepted envelope with no snapshot")
	}
	if _, _, ok := authorRollbackSnapshotFromMetadata(noSnap); ok {
		t.Error("author-from-metadata accepted envelope with no snapshot")
	}
	// Garbage input.
	if _, _, ok := bookRollbackSnapshotFromMetadata("garbage"); ok {
		t.Error("book-from-metadata accepted garbage")
	}
	// Malformed "before" payload (not an object) must fail the inner unmarshal.
	badBefore := `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":123,"after":{"title":"x"}}}`
	if _, _, ok := bookRollbackSnapshotFromMetadata(badBefore); ok {
		t.Error("book-from-metadata accepted malformed before payload")
	}
	// Malformed "after" payload (string where object expected).
	badAfter := `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":{"title":"x"},"after":"oops"}}`
	if _, _, ok := bookRollbackSnapshotFromMetadata(badAfter); ok {
		t.Error("book-from-metadata accepted malformed after payload")
	}
	// Same for the author path.
	badAuthorBefore := `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"author","before":7,"after":{"name":"x"}}}`
	if _, _, ok := authorRollbackSnapshotFromMetadata(badAuthorBefore); ok {
		t.Error("author-from-metadata accepted malformed before payload")
	}
	badAuthorAfter := `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"author","before":{"name":"x"},"after":7}}`
	if _, _, ok := authorRollbackSnapshotFromMetadata(badAuthorAfter); ok {
		t.Error("author-from-metadata accepted malformed after payload")
	}
	// Author path rejects a book snapshot too.
	bookSnap := `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":{},"after":{}}}`
	if _, _, ok := authorRollbackSnapshotFromMetadata(bookSnap); ok {
		t.Error("author-from-metadata accepted a book snapshot")
	}
	// Author path rejects missing before half.
	authorMissingBefore := `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"author","after":{"name":"x"}}}`
	if _, _, ok := authorRollbackSnapshotFromMetadata(authorMissingBefore); ok {
		t.Error("author-from-metadata accepted snapshot missing before half")
	}
}

func TestAuthorRollbackSnapshotFromMetadata_RoundTrip(t *testing.T) {
	before := &authorRollbackSnapshot{Name: "Before", Monitored: true}
	after := &authorRollbackSnapshot{Name: "After"}
	beforeRaw, _ := marshalSnapshotPayload(before)
	afterRaw, _ := marshalSnapshotPayload(after)
	env := runEntityMetadataEnvelope{
		Kind:    runEntityMetadataKind,
		Version: runEntityMetadataVersion,
		Snapshot: &runEntitySnapshotEnvelope{
			EntityType: entityTypeAuthor,
			Before:     beforeRaw,
			After:      afterRaw,
		},
	}
	raw, _ := json.Marshal(env)
	gotBefore, gotAfter, ok := authorRollbackSnapshotFromMetadata(string(raw))
	if !ok {
		t.Fatal("authorRollbackSnapshotFromMetadata: ok = false")
	}
	if gotBefore.Name != "Before" || !gotBefore.Monitored {
		t.Errorf("before = %+v", gotBefore)
	}
	if gotAfter.Name != "After" {
		t.Errorf("after = %+v", gotAfter)
	}
}

// ----------------------------------------------------------------------------
// clone* helpers, direct.
// ----------------------------------------------------------------------------

func TestClonePtrHelpers(t *testing.T) {
	if cloneInt64Ptr(nil) != nil {
		t.Error("cloneInt64Ptr(nil) != nil")
	}
	if cloneStringPtr(nil) != nil {
		t.Error("cloneStringPtr(nil) != nil")
	}
	if cloneTimePtr(nil) != nil {
		t.Error("cloneTimePtr(nil) != nil")
	}
	i := int64(7)
	ci := cloneInt64Ptr(&i)
	if ci == &i || *ci != 7 {
		t.Errorf("cloneInt64Ptr aliased or wrong: %v", ci)
	}
	s := "hi"
	cs := cloneStringPtr(&s)
	if cs == &s || *cs != "hi" {
		t.Errorf("cloneStringPtr aliased or wrong: %v", cs)
	}
	now := time.Now()
	ct := cloneTimePtr(&now)
	if ct == &now || !ct.Equal(now) {
		t.Errorf("cloneTimePtr aliased or wrong: %v", ct)
	}
}

// ----------------------------------------------------------------------------
// equalInt64Ptr / equalTimePtr edge cases, direct.
// ----------------------------------------------------------------------------

func TestEqualInt64Ptr(t *testing.T) {
	a, b := int64(5), int64(5)
	c := int64(6)
	cases := []struct {
		name string
		x, y *int64
		want bool
	}{
		{"both nil", nil, nil, true},
		{"x nil", nil, &a, false},
		{"y nil", &a, nil, false},
		{"equal", &a, &b, true},
		{"unequal", &a, &c, false},
	}
	for _, tc := range cases {
		if got := equalInt64Ptr(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: equalInt64Ptr = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEqualTimePtr(t *testing.T) {
	t1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	t1same := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	// Same instant, different zone: Equal must treat these as equal.
	t1other := t1.In(time.FixedZone("X", 3600))
	t2 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		x, y *time.Time
		want bool
	}{
		{"both nil", nil, nil, true},
		{"x nil", nil, &t1, false},
		{"y nil", &t1, nil, false},
		{"equal same zone", &t1, &t1same, true},
		{"equal diff zone", &t1, &t1other, true},
		{"unequal", &t1, &t2, false},
	}
	for _, tc := range cases {
		if got := equalTimePtr(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: equalTimePtr = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ----------------------------------------------------------------------------
// RecentRuns / GetRun via the Importer.
// ----------------------------------------------------------------------------

func TestImporter_RecentRuns_OrderAndGetRun(t *testing.T) {
	imp, _, _, _, _, runsRepo, _, _ := newRollbackFixture(t)
	ctx := context.Background()

	// Seed three runs; started_at is set by Create() to now(), and ListRecent
	// orders by started_at DESC, id DESC, so the most-recently-created id wins.
	var ids []int64
	for i := 0; i < 3; i++ {
		run := &models.CalibreImportRun{LibraryPath: "/lib", Status: runStatusCompleted}
		if err := runsRepo.Create(ctx, run); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
		ids = append(ids, run.ID)
	}

	recent, err := imp.RecentRuns(ctx, 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("RecentRuns len = %d, want 3", len(recent))
	}
	// Highest id (newest) must be first given the id DESC tiebreaker.
	if recent[0].ID != ids[2] {
		t.Errorf("RecentRuns[0].ID = %d, want newest %d", recent[0].ID, ids[2])
	}

	// Limit is honoured.
	limited, err := imp.RecentRuns(ctx, 1)
	if err != nil {
		t.Fatalf("RecentRuns(1): %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("RecentRuns(1) len = %d, want 1", len(limited))
	}

	// GetRun fetches an existing run.
	got, err := imp.GetRun(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.ID != ids[0] {
		t.Errorf("GetRun(%d) = %+v", ids[0], got)
	}

	// GetRun on a missing id returns nil, nil (not an error).
	missing, err := imp.GetRun(ctx, 999999)
	if err != nil {
		t.Errorf("GetRun(missing) err = %v, want nil", err)
	}
	if missing != nil {
		t.Errorf("GetRun(missing) = %+v, want nil", missing)
	}
}

// When the importer was constructed without run tracking, both methods must
// no-op to (nil, nil) rather than panic.
func TestImporter_RecentRuns_NoRunTracking(t *testing.T) {
	imp := &Importer{}
	runs, err := imp.RecentRuns(context.Background(), 5)
	if err != nil || runs != nil {
		t.Errorf("RecentRuns without tracking = %v / %v, want nil/nil", runs, err)
	}
	run, err := imp.GetRun(context.Background(), 1)
	if err != nil || run != nil {
		t.Errorf("GetRun without tracking = %v / %v, want nil/nil", run, err)
	}
}
