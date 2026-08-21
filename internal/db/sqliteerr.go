package db

import (
	"errors"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// isForeignKeyViolation reports whether err is SQLite rejecting a statement
// because a foreign key constraint would be broken (result code 787,
// SQLITE_CONSTRAINT_FOREIGNKEY).
//
// Enforcement is on for every connection (see connectionPragmaDSN), so this is
// a live failure mode rather than a theoretical one. Matching on the result
// code keeps callers off the driver's message text, which is not part of any
// stability promise.
func isForeignKeyViolation(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	return serr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
}
