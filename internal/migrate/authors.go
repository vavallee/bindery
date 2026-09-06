package migrate

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func isAuthorCreateConflict(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		errors.Is(err, db.ErrAuthorIdentifierConflict)
}

// resolveAndCreateAuthor is the author-resolution step every bulk importer
// runs: search the metadata providers for a name, take the top match, skip it
// if the library already has that foreign id, fetch the full record, stamp the
// monitor defaults, and create it. It records its own outcome on res, so the
// caller only has to handle the created author.
//
// The CSV and Readarr importers carried byte-for-byte copies of this block,
// which meant #2332 (the hardcoded provider stamp below) had to be fixed twice
// and could be fixed half way. That is why it lives here now (#2366).
//
// source is the importer's name, used only in the "search failed" log line.
// Returns nil when the name was skipped or failed; res already carries why.
func resolveAndCreateAuthor(
	ctx context.Context,
	source, name string,
	monitored bool,
	authors *db.AuthorRepo,
	settings *db.SettingsRepo,
	agg *metadata.Aggregator,
	res *Result,
) *models.Author {
	// Resolve via OpenLibrary. Top match wins.
	matches, err := agg.SearchAuthors(ctx, name)
	if err != nil {
		slog.Warn(source+" import: search failed", "name", name, "error", err)
		res.fail(name, "metadata lookup failed: "+err.Error())
		return nil
	}
	if len(matches) == 0 {
		res.fail(name, "no OpenLibrary match")
		return nil
	}
	top := matches[0]

	// Skip if already present.
	if existing, _ := authors.GetByAnyForeignID(ctx, top.ForeignID); existing != nil {
		res.Skipped++
		return nil
	}

	// Fetch full metadata (description, image). Soft-fail if it errors: the
	// search hit is already a usable author record.
	full, ferr := agg.GetAuthor(ctx, top.ForeignID)
	if ferr != nil || full == nil {
		full = &top
	}
	full.Monitored = monitored
	// #2332: this stamp is hardcoded and ignores which provider actually
	// answered, so a provider timeout binds the author to the wrong one. Fix
	// it here, on this one line, rather than in each importer.
	full.MetadataProvider = "openlibrary"
	// The source hands over a monitored flag but no monitor mode, so take the
	// install-wide default rather than the column default "all" (#1666).
	db.ApplyAuthorMonitorDefaults(ctx, settings, full)

	if cerr := authors.Create(ctx, full); cerr != nil {
		if isAuthorCreateConflict(cerr) {
			if existing, _ := authors.GetByAnyForeignID(ctx, full.ForeignID); existing != nil {
				res.Skipped++
				return nil
			}
		}
		res.fail(name, cerr.Error())
		return nil
	}
	res.Added++
	res.AddedNames = append(res.AddedNames, full.Name)
	return full
}
