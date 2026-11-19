package models

import "testing"

func TestEditionLocks(t *testing.T) {
	want := []string{EditionFieldPublisher, EditionFieldPublishDate, EditionFieldISBN13, EditionFieldISBN10, EditionFieldASIN}
	if len(LockableEditionFields) != len(want) {
		t.Fatalf("LockableEditionFields = %v, want %v", LockableEditionFields, want)
	}
	for i, f := range want {
		if LockableEditionFields[i] != f {
			t.Fatalf("LockableEditionFields[%d] = %q, want %q", i, LockableEditionFields[i], f)
		}
		if !IsLockableEditionField(f) {
			t.Fatalf("IsLockableEditionField(%q) = false", f)
		}
	}
	if IsLockableEditionField("title") {
		t.Fatal("title is not a lockable edition field")
	}

	var nilEdition *Edition
	if !nilEdition.CanWrite(EditionFieldPublisher) {
		t.Fatal("a nil edition must be writable")
	}

	e := &Edition{}
	if e.IsFieldLocked(EditionFieldISBN13) || !e.CanWrite(EditionFieldISBN13) {
		t.Fatal("fresh edition must have no locks")
	}
	e.LockField(EditionFieldISBN13)
	e.LockField(EditionFieldISBN13)
	if len(e.LockedFields) != 1 || e.LockedFields[0] != EditionFieldISBN13 {
		t.Fatalf("LockField must be idempotent; got %v", e.LockedFields)
	}
	if e.CanWrite(EditionFieldISBN13) || !e.CanWrite(EditionFieldPublisher) {
		t.Fatalf("lock on isbn13 should block isbn13 only; locked=%v", e.LockedFields)
	}
}
