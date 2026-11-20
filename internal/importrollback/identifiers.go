package importrollback

import (
	"context"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// IdentifierLister is the one author repository method identifier capture
// needs. *db.AuthorRepo satisfies it.
type IdentifierLister interface {
	ListAuthorIdentifiers(ctx context.Context, authorID int64) ([]models.AuthorIdentifier, error)
}

// CaptureIdentifierForeignIDs returns the author's identifier foreign IDs,
// trimmed and sorted, with blanks dropped. The result is never nil on
// success, so an author with no identifiers records an empty list rather
// than "not captured".
func CaptureIdentifierForeignIDs(ctx context.Context, lister IdentifierLister, authorID int64) (*[]string, error) {
	identifiers, err := lister.ListAuthorIdentifiers(ctx, authorID)
	if err != nil {
		return nil, err
	}
	foreignIDs := make([]string, 0, len(identifiers))
	for _, identifier := range identifiers {
		if foreignID := strings.TrimSpace(identifier.ForeignID); foreignID != "" {
			foreignIDs = append(foreignIDs, foreignID)
		}
	}
	sort.Strings(foreignIDs)
	return &foreignIDs, nil
}

// IdentifiersToRemove lists the identifier foreign IDs the run added: present
// in after, absent from before, and not the author's current primary foreign
// ID. It returns nil when either side was not captured (nil pointer), which
// is how snapshots written before identifier capture decode.
func IdentifiersToRemove(before, after *[]string, currentPrimary string) []string {
	if before == nil || after == nil {
		return nil
	}
	beforeSet := make(map[string]struct{}, len(*before))
	for _, foreignID := range *before {
		foreignID = strings.TrimSpace(foreignID)
		if foreignID != "" {
			beforeSet[foreignID] = struct{}{}
		}
	}
	currentPrimary = strings.TrimSpace(currentPrimary)
	var out []string
	for _, foreignID := range *after {
		foreignID = strings.TrimSpace(foreignID)
		if foreignID == "" {
			continue
		}
		if _, existedBefore := beforeSet[foreignID]; existedBefore {
			continue
		}
		if strings.EqualFold(foreignID, currentPrimary) {
			continue
		}
		out = append(out, foreignID)
	}
	return out
}
