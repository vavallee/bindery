package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestIsForeignKeyViolation matches on SQLite's result code rather than its
// message text, so this pins the behaviour against a real rejection from the
// driver rather than a string we made up.
func TestIsForeignKeyViolation(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	ctx := context.Background()

	// books.author_id references authors(id), and foreign keys are enforced on
	// every connection (connectionPragmaDSN), so an insert naming an author
	// that does not exist is rejected with SQLITE_CONSTRAINT_FOREIGNKEY.
	_, fkErr := database.ExecContext(ctx,
		"INSERT INTO books (foreign_id, author_id, title, sort_title) VALUES ('OL1W', 99999, 'Orphan', 'orphan')")
	if fkErr == nil {
		t.Fatal("expected a foreign key rejection; is enforcement off?")
	}
	if !isForeignKeyViolation(fkErr) {
		t.Errorf("isForeignKeyViolation(%v) = false, want true", fkErr)
	}

	// A wrapped one is still one, which is what lets callers wrap with context
	// before classifying.
	if !isForeignKeyViolation(fmt.Errorf("delete user: %w", fkErr)) {
		t.Error("a wrapped foreign key error was not recognised")
	}

	// A different constraint must not be mistaken for one. users.username is
	// UNIQUE, so a duplicate is SQLITE_CONSTRAINT_UNIQUE.
	users := NewUserRepo(database)
	if _, err := users.Create(ctx, "dupe", "pw"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, uniqueErr := users.Create(ctx, "dupe", "pw")
	if uniqueErr == nil {
		t.Fatal("expected a uniqueness rejection")
	}
	if isForeignKeyViolation(uniqueErr) {
		t.Errorf("a uniqueness violation was classified as a foreign key one: %v", uniqueErr)
	}

	for _, e := range []error{nil, errors.New("not a sqlite error at all")} {
		if isForeignKeyViolation(e) {
			t.Errorf("isForeignKeyViolation(%v) = true, want false", e)
		}
	}
}
