package migrate

import (
	"errors"
	"strings"

	"github.com/vavallee/bindery/internal/db"
)

func isAuthorCreateConflict(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		errors.Is(err, db.ErrAuthorIdentifierConflict)
}
