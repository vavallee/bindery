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
	return QueryScopeFor("owner_user_id", where, userID, args...)
}

// QueryScopeFor is QueryScope but lets the caller name a qualified column.
// Used when the query joins multiple tables that each have an owner_user_id
// column (e.g. books JOIN authors, post-#882), where the bare reference
// would be ambiguous. Pass e.g. "books.owner_user_id".
func QueryScopeFor(column, where string, userID int64, args ...any) (string, []any) {
	if userID == 0 {
		return where, args
	}
	if where == "" {
		return "WHERE " + column + " = ?", append([]any{userID}, args...)
	}
	return where + " AND " + column + " = ?", append(args, userID)
}
