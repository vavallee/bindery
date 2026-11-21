package api

import (
	"context"
	"log/slog"

	"github.com/vavallee/bindery/internal/db"
)

// The global autoGrab.enabled switch stops every dispatch of an indexer
// search, including the ones a user starts by hand: the author page "Search
// wanted" button and the Wanted page bulk Search both fan out through
// scheduler.SearchAndGrabBook, and that is where the switch is enforced
// (#2256). Until #2669 the bulk handlers had already written {"ok":true} by
// the time the fan-out reached the gate, so the button flashed, the response
// said success, and the only trace was one INFO line in the server log.
//
// This file is the honest-refusal half of the fix: the bulk handlers ask the
// gate before they queue anything, and a refusal is reported per ID with a
// machine readable code the web UI turns into a message that names the
// setting. Nothing here dispatches a search or relaxes the switch, so the
// chokepoint invariant is untouched: the gate refuses strictly earlier than
// the scheduler would, and never the other way round.
//
// Deliberately not fixed here: making a manual search actually query the
// indexers with the switch off. That is a real behaviour change (the bulk
// paths have no results UI for releases nobody is allowed to grab) and is
// tracked separately; see the pull request for the shape it would take.

// autoGrabDisabledCode is the stable, machine readable reason code returned in
// a bulk result when a user initiated search is refused because the global
// auto grab switch is off. The web UI matches on this rather than on the
// message text, so the wording can be translated and reworded freely.
const autoGrabDisabledCode = "auto_grab_disabled"

// autoGrabDisabledMessage is the fallback English text for API clients that do
// not localise (curl, scripts, the *arr style integrations). It has to stand
// on its own, so it says both what did not happen and where to change it.
const autoGrabDisabledMessage = "automatic grabbing is disabled, so no search was run. Turn on Settings > Metadata Profiles > Enable automatic grabbing, or search this book on its own page to pick a release by hand."

// refuseSearchWhenAutoGrabDisabled reports whether a user initiated bulk
// search must be refused outright. It reads the same key, with the same fail
// open default, as every other caller (autoGrabEnabled), so a handler can
// never refuse a search the scheduler would have run.
//
// Deliberately silent: the log line is emitted by logSearchRefusal after the
// per ID ownership filter has run, so ids the caller does not own produce no
// line. Logging the requested id count here instead would let a caller post a
// list of arbitrary ids and write WARN lines about resources that are not
// theirs, which is log noise whose volume the caller gets to choose. Nothing
// leaks back either way; this just keeps the log about work the caller could
// actually have done.
func refuseSearchWhenAutoGrabDisabled(ctx context.Context, settings *db.SettingsRepo) bool {
	return !autoGrabEnabled(ctx, settings)
}

// logSearchRefusal emits one line for a request whose search was refused,
// counting only the ids that survived the ownership filter. One line per
// request, not one per book: the point is to explain a refusal a user is
// looking at, not to reproduce the scheduler's per book INFO line. A request
// that refused nothing the caller owns logs nothing.
func logSearchRefusal(path string, refused int) {
	if refused <= 0 {
		return
	}
	slog.Warn("manual search refused: automatic grabbing is disabled globally",
		"path", path, "refused", refused, "setting", "autoGrab.enabled", "issue", 2669)
}

// autoGrabDisabledResult is the per ID entry a refused search reports.
func autoGrabDisabledResult() bulkItemResult {
	return bulkItemResult{Code: autoGrabDisabledCode, Error: autoGrabDisabledMessage}
}
