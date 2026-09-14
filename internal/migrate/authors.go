package migrate

import (
	"context"
	"errors"
	"fmt"
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

// boundProvider is the metadata_provider to store for an author about to be
// created. It is read off the author's foreign ID, the same classifier the
// aggregator routes catalogue fetches by, so the stored label and the stored
// id cannot disagree. The importers used to stamp "openlibrary" on whatever
// record the search returned, which left dnb: and hc: authors claiming to be
// OpenLibrary's (#2332).
func boundProvider(a *models.Author) string {
	return models.AuthorProviderFromForeignID(a.ForeignID)
}

// refusedBindReason is the per row message for a match the #2271 guard
// refused: the primary provider did not answer, so the match came from a
// fallback by default rather than on the merits, and writing it would have
// made that fallback the author's provider for good.
func refusedBindReason(o metadata.SearchOutcome, foreignID string) string {
	return fmt.Sprintf("primary metadata provider %s did not answer, so the %s match was not used; run the import again once it responds",
		o.Primary, models.AuthorProviderFromForeignID(foreignID))
}

// resolveAndCreateAuthor is the author resolution step every bulk importer
// runs: search the metadata providers for a name, take the top match, skip it
// if the library already has that foreign id, refuse it if it only won because
// the primary provider failed, fetch the full record, stamp the monitor
// defaults and the provider the record belongs to, and create it. It records
// its own outcome on res, so the caller only has to handle the created author.
//
// The CSV and Readarr importers carried identical copies of this block until
// #2366, which is why #2332 is fixed here once rather than in each.
//
// source is the importer's name, used only in log lines.
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
	// Search every provider. Top match wins, subject to the guard below.
	matches, outcome, err := agg.SearchAuthorsWithOutcome(ctx, name)
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

	// Skip if already present. Nothing is written, so this needs no guard.
	if existing, _ := authors.GetByAnyForeignID(ctx, top.ForeignID); existing != nil {
		res.Skipped++
		return nil
	}

	// The search only fails outright when every provider does, so with the
	// primary timed out and a fallback answering it reports success and hands
	// back the fallback's record. Creating the author from it would bind them
	// to that provider permanently, and a later lookup by the primary's key
	// would miss the row and mint a duplicate (#2117, #2271, #2332).
	if !outcome.SafeToBind(top.ForeignID) {
		slog.Warn(source+" import: refusing to bind author to a fallback provider",
			"name", name, "primary", outcome.Primary, "failed", outcome.FailureSummary(),
			"wouldHaveLinked", top.ForeignID)
		res.fail(name, refusedBindReason(outcome, top.ForeignID))
		return nil
	}

	// Fetch full metadata (description, image). Soft-fail if it errors: the
	// search hit is already a usable author record.
	full, ferr := agg.GetAuthor(ctx, top.ForeignID)
	if ferr != nil || full == nil {
		full = &top
	}
	full.Monitored = monitored
	full.MetadataProvider = boundProvider(full)
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
