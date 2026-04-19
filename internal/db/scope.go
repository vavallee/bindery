package db

// QueryScope appends a per-user ownership filter to a SQL fragment.
// base must be a complete WHERE clause (or empty string if there is no
// existing WHERE). If userID is 0 no filter is added (admin / background
// jobs that operate across all users).
//
// Usage:
//
//	where, args := db.QueryScope("WHERE excluded = 0", userID, existingArgs...)
//	rows, err := r.db.QueryContext(ctx, "SELECT ... FROM books "+where, args...)
func QueryScope(where string, userID int64, args ...any) (string, []any) {
	if userID == 0 {
		return where, args
	}
	if where == "" {
		return "WHERE owner_user_id = ?", append([]any{userID}, args...)
	}
	return where + " AND owner_user_id = ?", append(args, userID)
}
