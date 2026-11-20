package importrollback

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestRestoreLanguage_MatchesAcrossTheBackfill: a run recorded before the
// language backfill (#3086) holds the spelling it wrote ("en"), while the row
// now holds the normalized one ("eng"). They are the same value, so the
// rollback still restores; a language somebody changed after the run is a
// different value and is left alone.
func TestRestoreLanguage_MatchesAcrossTheBackfill(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, current, before, after, want string
		changed                            bool
	}{
		{"run wrote en, backfilled to eng, restores the empty original", "eng", "", "en", "", true},
		{"run replaced fr with en, restores it normalized", "eng", "fr", "en", "fre", true},
		{"run left the language alone", "eng", "en", "en", "eng", false},
		{"edited after the run", "ger", "", "en", "ger", false},
		{"no backfill in between", "eng", "fre", "eng", "fre", true},
	} {
		current, changed := tc.current, false
		RestoreLanguage(&current, tc.before, tc.after, &changed)
		if current != tc.want || changed != tc.changed {
			t.Errorf("%s: language = %q (changed %v), want %q (changed %v)", tc.name, current, changed, tc.want, tc.changed)
		}
	}
}

// restoreCase drives one Restore* function through the three outcomes every
// one of them shares: the live value still equals "after" and moves to
// "before"; the live value was edited after the run and is untouched; the run
// changed nothing (before == after) so nothing is reported as changed.
type restoreCase[T any] struct {
	name                     string
	restore                  func(target *T, before, after T, changed *bool)
	equal                    func(a, b T) bool
	original, imported, edit T
	// noEdit skips the post-import edit leg for a type with only two values.
	noEdit bool
}

func runRestoreCase[T any](t *testing.T, tc restoreCase[T]) {
	t.Helper()
	t.Run(tc.name, func(t *testing.T) {
		live, changed := tc.imported, false
		tc.restore(&live, tc.original, tc.imported, &changed)
		if !tc.equal(live, tc.original) || !changed {
			t.Errorf("restore of an untouched import = %v (changed %v), want original and changed", live, changed)
		}

		if !tc.noEdit {
			live, changed = tc.edit, false
			tc.restore(&live, tc.original, tc.imported, &changed)
			if !tc.equal(live, tc.edit) || changed {
				t.Errorf("restore over a post-import edit = %v (changed %v), want edit kept and unchanged", live, changed)
			}
		}

		live, changed = tc.imported, false
		tc.restore(&live, tc.imported, tc.imported, &changed)
		if !tc.equal(live, tc.imported) || changed {
			t.Errorf("restore of a field the run did not change = %v (changed %v), want unchanged", live, changed)
		}
	})
}

func eq[T comparable](a, b T) bool { return a == b }

func TestRestoreFunctions_EveryFieldType(t *testing.T) {
	t.Parallel()
	one, two, three := int64(1), int64(2), int64(3)
	t1 := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	t2 := time.Date(2022, 12, 31, 23, 59, 58, 0, time.UTC)
	t3 := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	runRestoreCase(t, restoreCase[string]{"string", RestoreString, eq[string], "a", "b", "c", false})
	runRestoreCase(t, restoreCase[int]{"int", RestoreInt, eq[int], 1, 2, 3, false})
	runRestoreCase(t, restoreCase[int64]{"int64", RestoreInt64, eq[int64], 1, 2, 3, false})
	runRestoreCase(t, restoreCase[float64]{"float64", RestoreFloat64, eq[float64], 1.5, 2.5, 3.5, false})
	runRestoreCase(t, restoreCase[bool]{"bool", RestoreBool, eq[bool], false, true, true, true})
	runRestoreCase(t, restoreCase[[]string]{"strings", RestoreStrings, EqualStrings,
		[]string{"a"}, []string{"b", "c"}, []string{"d"}, false})
	runRestoreCase(t, restoreCase[*int64]{"int64 pointer", RestoreInt64Ptr, EqualInt64Ptr, &one, &two, &three, false})
	runRestoreCase(t, restoreCase[*int64]{"int64 pointer, nil original", RestoreInt64Ptr, EqualInt64Ptr, nil, &two, &three, false})
	runRestoreCase(t, restoreCase[*time.Time]{"time pointer", RestoreTimePtr, EqualTimePtr, &t1, &t2, &t3, false})
	runRestoreCase(t, restoreCase[*time.Time]{"time pointer, nil original", RestoreTimePtr, EqualTimePtr, nil, &t2, &t3, false})
}

func TestRestoreBool_EditedBackToOriginalIsNotAChange(t *testing.T) {
	t.Parallel()
	// Live value differs from "after" so it is left alone even though it
	// happens to equal "before".
	live, changed := false, false
	RestoreBool(&live, false, true, &changed)
	if live || changed {
		t.Fatalf("live = %v changed = %v, want false false", live, changed)
	}
}

func TestRestoreStrings_CopiesBefore(t *testing.T) {
	t.Parallel()
	before := []string{"a"}
	live, changed := []string{"x"}, false
	RestoreStrings(&live, before, []string{"x"}, &changed)
	live[0] = "mutated"
	if before[0] != "a" {
		t.Fatal("RestoreStrings aliased the snapshot slice")
	}
}

func TestRestorePointers_DoNotAliasSnapshot(t *testing.T) {
	t.Parallel()
	before := int64(1)
	live, changed := int64(2), false
	liveP := &live
	RestoreInt64Ptr(&liveP, &before, &live, &changed)
	if liveP == &before {
		t.Fatal("RestoreInt64Ptr aliased the snapshot pointer")
	}

	bt := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	lt := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)
	ltP := &lt
	RestoreTimePtr(&ltP, &bt, &lt, &changed)
	if ltP == &bt {
		t.Fatal("RestoreTimePtr aliased the snapshot pointer")
	}
}

func TestEqualAndClonePointerHelpers(t *testing.T) {
	t.Parallel()
	a, b, c := int64(5), int64(5), int64(6)
	for _, tc := range []struct {
		name string
		x, y *int64
		want bool
	}{
		{"both nil", nil, nil, true}, {"x nil", nil, &a, false}, {"y nil", &a, nil, false},
		{"equal", &a, &b, true}, {"unequal", &a, &c, false},
	} {
		if got := EqualInt64Ptr(tc.x, tc.y); got != tc.want {
			t.Errorf("EqualInt64Ptr %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	t1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	t1zone := t1.In(time.FixedZone("X", 3600))
	t2 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		x, y *time.Time
		want bool
	}{
		{"both nil", nil, nil, true}, {"x nil", nil, &t1, false}, {"y nil", &t1, nil, false},
		{"same instant, other zone", &t1, &t1zone, true}, {"unequal", &t1, &t2, false},
	} {
		if got := EqualTimePtr(tc.x, tc.y); got != tc.want {
			t.Errorf("EqualTimePtr %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	if EqualStrings([]string{"a"}, []string{"b"}) || EqualStrings([]string{"a"}, nil) || !EqualStrings(nil, []string{}) {
		t.Error("EqualStrings disagrees on content or length")
	}

	if CloneInt64Ptr(nil) != nil || CloneStringPtr(nil) != nil || CloneTimePtr(nil) != nil {
		t.Error("Clone of nil must be nil")
	}
	s, now := "hi", time.Now()
	if ci := CloneInt64Ptr(&a); ci == &a || *ci != 5 {
		t.Errorf("CloneInt64Ptr aliased or wrong: %v", ci)
	}
	if cs := CloneStringPtr(&s); cs == &s || *cs != "hi" {
		t.Errorf("CloneStringPtr aliased or wrong: %v", cs)
	}
	if ct := CloneTimePtr(&now); ct == &now || !ct.Equal(now) {
		t.Errorf("CloneTimePtr aliased or wrong: %v", ct)
	}
}

type testSnapshot struct {
	Title string `json:"title"`
	N     int    `json:"n"`
}

func TestMarshalPayload(t *testing.T) {
	t.Parallel()
	// Nothing to snapshot, including a typed nil pointer, must be a nil
	// payload and not JSON "null".
	var typedNil *testSnapshot
	for name, v := range map[string]any{"untyped nil": nil, "typed nil": typedNil} {
		if got, err := MarshalPayload("x", v); err != nil || got != nil {
			t.Errorf("%s: got %s, %v, want nil, nil", name, got, err)
		}
	}
	got, err := MarshalPayload("x", &testSnapshot{Title: "t", N: 2})
	if err != nil || string(got) != `{"title":"t","n":2}` {
		t.Fatalf("got %s, %v", got, err)
	}
	_, err = MarshalPayload("calibre", map[string]any{"c": make(chan int)})
	if err == nil || !strings.Contains(err.Error(), "encode calibre rollback snapshot") {
		t.Fatalf("unencodable value: err = %v", err)
	}
}

func TestParseEnvelope(t *testing.T) {
	t.Parallel()
	const kind = "k"
	for name, raw := range map[string]string{
		"empty":         "",
		"blank":         "  \n",
		"not json":      "{",
		"wrong kind":    `{"kind":"other","version":1}`,
		"wrong version": `{"kind":"k","version":2}`,
	} {
		if _, ok := ParseEnvelope(raw, kind, 1); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	env, ok := ParseEnvelope(`{"kind":"k","version":1}`, kind, 1)
	if !ok || env.Data == nil || len(env.Data) != 0 {
		t.Fatalf("ok = %v data = %#v, want ok with an empty non-nil map", ok, env.Data)
	}
}

// TestEnvelope_StoredFormat pins the JSON layout of the envelope. Both
// importers persist it, so a change here would strand existing rollback runs.
func TestEnvelope_StoredFormat(t *testing.T) {
	t.Parallel()
	before, _ := MarshalPayload("x", &testSnapshot{Title: "old"})
	after, _ := MarshalPayload("x", &testSnapshot{Title: "new"})
	env := NewEnvelope("k", 1, "book", map[string]any{"matchedBy": "title"}, before, after)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"kind":"k","version":1,"data":{"matchedBy":"title"},"snapshot":{"entityType":"book","before":{"title":"old","n":0},"after":{"title":"new","n":0}}}`
	if string(raw) != want {
		t.Fatalf("stored JSON changed:\n got %s\nwant %s", raw, want)
	}

	b, a, ok := DecodeSnapshots[testSnapshot](want, "k", 1, "book")
	if !ok || b.Title != "old" || a.Title != "new" {
		t.Fatalf("decode of the stored form = %+v %+v %v", b, a, ok)
	}
}

func TestDecodeSnapshots_Rejects(t *testing.T) {
	t.Parallel()
	wrap := func(snapshot string) string {
		return `{"kind":"k","version":1,"snapshot":` + snapshot + `}`
	}
	for name, raw := range map[string]string{
		"blank":           "",
		"no snapshot":     `{"kind":"k","version":1}`,
		"other entity":    wrap(`{"entityType":"author","before":{},"after":{}}`),
		"missing before":  wrap(`{"entityType":"book","after":{}}`),
		"missing after":   wrap(`{"entityType":"book","before":{}}`),
		"bad before":      wrap(`{"entityType":"book","before":[],"after":{}}`),
		"bad after":       wrap(`{"entityType":"book","before":{},"after":[]}`),
		"other importer":  `{"kind":"x","version":1,"snapshot":{"entityType":"book","before":{},"after":{}}}`,
		"unknown version": `{"kind":"k","version":9,"snapshot":{"entityType":"book","before":{},"after":{}}}`,
	} {
		if b, a, ok := DecodeSnapshots[testSnapshot](raw, "k", 1, "book"); ok || b != nil || a != nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, ok := DecodeSnapshots[testSnapshot](wrap(`{"entityType":"book","before":{},"after":{}}`), "k", 1, "book"); !ok {
		t.Error("valid empty snapshots were rejected")
	}
}

type fakeLister struct {
	rows []models.AuthorIdentifier
	err  error
	id   int64
}

func (f *fakeLister) ListAuthorIdentifiers(_ context.Context, authorID int64) ([]models.AuthorIdentifier, error) {
	f.id = authorID
	return f.rows, f.err
}

func TestCaptureIdentifierForeignIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	f := &fakeLister{rows: []models.AuthorIdentifier{{ForeignID: " b "}, {ForeignID: ""}, {ForeignID: "a"}, {ForeignID: "   "}}}
	got, err := CaptureIdentifierForeignIDs(ctx, f, 7)
	if err != nil || f.id != 7 || got == nil || !reflect.DeepEqual(*got, []string{"a", "b"}) {
		t.Fatalf("got %v, %v (author id %d), want [a b]", got, err, f.id)
	}

	// Empty is a recorded empty list, not "not captured".
	got, err = CaptureIdentifierForeignIDs(ctx, &fakeLister{}, 1)
	if err != nil || got == nil || len(*got) != 0 {
		t.Fatalf("empty: got %v, %v, want a non-nil empty list", got, err)
	}
	if raw, _ := json.Marshal(got); string(raw) != "[]" {
		t.Fatalf("empty list encodes as %s, want []", raw)
	}

	boom := errors.New("boom")
	if got, err := CaptureIdentifierForeignIDs(ctx, &fakeLister{err: boom}, 1); !errors.Is(err, boom) || got != nil {
		t.Fatalf("error: got %v, %v", got, err)
	}
}

func TestIdentifiersToRemove(t *testing.T) {
	t.Parallel()
	ids := func(s ...string) *[]string { return &s }
	for _, tc := range []struct {
		name          string
		before, after *[]string
		primary       string
		want          []string
	}{
		{"run added two", ids("a"), ids("a", "b", "c"), "x", []string{"b", "c"}},
		{"nothing added", ids("a"), ids("a"), "x", nil},
		{"added id is now the primary, case insensitive", ids("a"), ids("a", "B"), " b ", nil},
		{"blank and padded entries", ids(" a "), ids("a", " ", " c "), "x", []string{"c"}},
		{"before not captured", nil, ids("a"), "x", nil},
		{"after not captured", ids("a"), nil, "x", nil},
		{"empty before", ids(), ids("a"), "x", []string{"a"}},
	} {
		if got := IdentifiersToRemove(tc.before, tc.after, tc.primary); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
