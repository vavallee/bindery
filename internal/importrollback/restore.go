package importrollback

import (
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// Each Restore* function reverts *target to before, but only when *target
// still equals after, the value the import run wrote. A value that differs
// was edited after the run and is left alone. changed is set to true when the
// target actually moved.

func RestoreString(target *string, before, after string, changed *bool) {
	if *target != after {
		return
	}
	if *target != before {
		*changed = true
	}
	*target = before
}

// RestoreLanguage is RestoreString for a language code. The import run's
// snapshot can predate the startup backfill that rewrote every stored language
// into its ISO 639-2/B form (#3086), so "en" recorded by the run and "eng" on
// the row now are the same value: the row still holds what the run wrote, and
// is restored. What goes back is normalized too, so a rollback never
// reintroduces a spelling the backfill removed.
func RestoreLanguage(target *string, before, after string, changed *bool) {
	if models.NormalizeLanguageCode(*target) != models.NormalizeLanguageCode(after) {
		return
	}
	before = models.NormalizeLanguageCode(before)
	if *target != before {
		*changed = true
	}
	*target = before
}

func RestoreInt(target *int, before, after int, changed *bool) {
	if *target != after {
		return
	}
	if *target != before {
		*changed = true
	}
	*target = before
}

func RestoreInt64(target *int64, before, after int64, changed *bool) {
	if *target != after {
		return
	}
	if *target != before {
		*changed = true
	}
	*target = before
}

func RestoreFloat64(target *float64, before, after float64, changed *bool) {
	if *target != after {
		return
	}
	if *target != before {
		*changed = true
	}
	*target = before
}

func RestoreBool(target *bool, before, after bool, changed *bool) {
	if *target != after {
		return
	}
	if *target != before {
		*changed = true
	}
	*target = before
}

func RestoreStrings(target *[]string, before, after []string, changed *bool) {
	if !EqualStrings(*target, after) {
		return
	}
	if !EqualStrings(*target, before) {
		*changed = true
	}
	*target = append([]string(nil), before...)
}

func RestoreInt64Ptr(target **int64, before, after *int64, changed *bool) {
	if !EqualInt64Ptr(*target, after) {
		return
	}
	if !EqualInt64Ptr(*target, before) {
		*changed = true
	}
	*target = CloneInt64Ptr(before)
}

func RestoreTimePtr(target **time.Time, before, after *time.Time, changed *bool) {
	if !EqualTimePtr(*target, after) {
		return
	}
	if !EqualTimePtr(*target, before) {
		*changed = true
	}
	*target = CloneTimePtr(before)
}

func EqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for idx := range a {
		if a[idx] != b[idx] {
			return false
		}
	}
	return true
}

func EqualInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func EqualTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func CloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func CloneStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func CloneTimePtr(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
