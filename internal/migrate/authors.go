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

// primaryDownReason is the per row message when the primary provider did not
// answer and nothing another provider returned could be used. It is not a
// verdict on the row, which may well match once the primary is back, so it
// says to retry rather than to fix anything.
func primaryDownReason(o metadata.SearchOutcome) string {
	var daily *metadata.DailyQuotaError
	if errors.As(o.FirstErr, &daily) {
		return daily.Error()
	}
	return fmt.Sprintf("primary metadata provider %s did not answer, run the import again once it responds", o.Primary)
}

// noMatchReason is the per row message when every provider that answered had
// nothing. It names them rather than assuming OpenLibrary, which a Hardcover
// or DNB primary may never have asked.
func noMatchReason(o metadata.SearchOutcome) string {
	if asked := o.AnsweredSummary(); asked != "" {
		return "no match on " + asked
	}
	return "no metadata match"
}

// firstLinkableAuthor returns the first match carrying a foreign ID, or nil.
// A record without one cannot become a provider link: Google Books author
// results never have one, and one used to reach authors.Create with an empty
// foreign_id labelled openlibrary. resolveGoodreadsByTitleAuthor applies the
// same rule to book results.
//
// The top match is taken as it stands, as it always was. A later match is
// only taken in its place when it names the same person as the top match or
// as the name searched for: the aggregator folds same name records together
// before ranking, so any linkable record still below a name only one is a
// different author. Importing "Andy Weir" with only Google Books knowing that
// name and OpenLibrary answering "Andrew Weir" would otherwise create Andrew
// Weir and count it as added.
func firstLinkableAuthor(name string, matches []models.Author) *models.Author {
	if len(matches) == 0 {
		return nil
	}
	if strings.TrimSpace(matches[0].ForeignID) != "" {
		return &matches[0]
	}
	want := map[string]bool{
		metadata.CanonicalAuthorKey(name):            true,
		metadata.CanonicalAuthorKey(matches[0].Name): true,
	}
	delete(want, "")
	for i := range matches[1:] {
		m := &matches[i+1]
		if strings.TrimSpace(m.ForeignID) != "" && want[metadata.CanonicalAuthorKey(m.Name)] {
			return m
		}
	}
	return nil
}

// resolveAndCreateAuthor is the author resolution step every bulk importer
// runs: search the metadata providers for a name, take the first match that
// carries a foreign id, skip it if the library already has that id, refuse it
// if it only won because the primary provider failed, fetch the full record,
// stamp the monitor defaults and the provider the record belongs to, and
// create it. It records its own outcome on res, so the caller only has to
// handle the created author.
//
// The CSV and Readarr importers carried identical copies of this block until
// #2366, which is why #2332 is fixed here once rather than in each.
//
// source is the importer's name, used only in log lines. outage is the run's
// primary outage streak: once it trips, the name fails at once rather than
// waiting out another primary timeout it could not bind after anyway (#2613).
// Returns nil when the name was skipped or failed; res already carries why.
func resolveAndCreateAuthor(
	ctx context.Context,
	source, name string,
	monitored bool,
	authors *db.AuthorRepo,
	settings *db.SettingsRepo,
	agg *metadata.Aggregator,
	outage *primaryOutage,
	res *Result,
) *models.Author {
	if outage.down() {
		res.fail(name, outage.reason())
		return nil
	}

	// Search every provider. The first match that carries a foreign id wins,
	// subject to the guard below.
	matches, outcome, err := agg.SearchAuthorsWithOutcome(ctx, name)
	outage.observe(source, outcome)
	if err != nil {
		slog.Warn(source+" import: search failed", "name", name, "error", err)
		res.fail(name, "metadata lookup failed: "+err.Error())
		return nil
	}
	match := firstLinkableAuthor(name, matches)
	if match == nil {
		switch {
		case outcome.PrimaryFailed:
			// Whatever came back may be missing the primary's record, so
			// this is not a verdict on the name.
			res.fail(name, primaryDownReason(outcome))
		case len(matches) > 0:
			res.fail(name, "no linkable match on "+outcome.AnsweredSummary()+": name only results carry no provider id")
		default:
			res.fail(name, noMatchReason(outcome))
		}
		return nil
	}
	top := *match

	// Skip if already present. Nothing is written, so this needs no guard.
	if existing, _ := authors.GetByAnyForeignID(ctx, top.ForeignID); existing != nil {
		res.Skipped++
		// A daily hold can interrupt the initial catalogue after the author was
		// committed. A rerun must queue it again, without repopulating a catalogue
		// the user deliberately emptied (the marker survives book deletion).
		populated, err := authors.CataloguePopulatedAt(ctx, existing.ID)
		if err != nil {
			res.fail(name, err.Error())
			return nil
		}
		if populated == nil {
			return existing
		}
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
