package models

import (
	"fmt"
	"slices"
)

// DownloadState is the typed state of a download record.
type DownloadState string

const (
	StateGrabbed       DownloadState = "grabbed"
	StateDownloading   DownloadState = "downloading"
	StateCompleted     DownloadState = "completed"
	StateImportPending DownloadState = "importPending"
	StateImporting     DownloadState = "importing"
	StateImported      DownloadState = "imported"
	StateFailed        DownloadState = "failed"
	StateImportFailed  DownloadState = "importFailed"
	StateImportBlocked DownloadState = "importBlocked"
	// StateImportExternal marks a download handed off to an external import
	// tool (import.mode=external). It is deliberately NON-terminal: the file
	// has not yet been reconciled into the library, so the download is not
	// "imported". The book stays Wanted so ScanLibrary can reconcile the file
	// once the external tool places it; searchWanted skips the FORMAT this
	// download is working toward so the release is not re-grabbed forever while
	// the hand-off is outstanding (issue #706 finding 3). The book's other
	// format, if it wants one, is still searched (#2365).
	StateImportExternal DownloadState = "importExternal"
	// StateImportHeld marks a completed drop-folder download whose format is
	// being held back under pair gating (#942): a media_type=both book only
	// hands off to the paired-reader tool (Storyteller) once BOTH the ebook and
	// audiobook are present, so the first-arriving format parks here — files
	// untouched in the download dir, its on-disk location recorded in
	// import_path — until the sibling completes and releases both together, or
	// the pair-gating timeout drops it alone. Like StateImportExternal it is
	// deliberately NON-terminal and is NOT swept by RecoverInterruptedImports;
	// searchWanted skips the held FORMAT so the release is not re-grabbed while
	// the hold is outstanding. It must not skip the whole book: the sibling the
	// hold is waiting for can only arrive via a search, so blocking both
	// formats made the gate wait for something it had just prevented (#2365).
	StateImportHeld DownloadState = "importHeld"
)

// validTransitions defines which state transitions are allowed.
// StateGrabbed includes StateCompleted for the 409 duplicate-add case: when a
// torrent is re-grabbed and qBittorrent already holds it at 100%, Bindery
// skips the downloading phase and goes straight to import (#769).
var validTransitions = map[DownloadState][]DownloadState{
	StateGrabbed:       {StateDownloading, StateCompleted, StateFailed},
	StateDownloading:   {StateCompleted, StateFailed},
	StateCompleted:     {StateImportPending, StateImportFailed},
	StateImportPending: {StateImporting, StateImportFailed, StateImportExternal, StateImportHeld},
	StateImporting:     {StateImported, StateImportFailed, StateImportBlocked},
	StateImported:      {},
	StateFailed:        {},
	StateImportFailed:  {StateImportPending, StateImportBlocked, StateImporting},
	// Terminal to the automatic pollers, but a manual match / retry can recover
	// it: back into the import flow directly (StateImportPending), or back to
	// StateImportFailed so the scanner re-polls with a fresh retry budget (#1589).
	StateImportBlocked: {StateImportPending, StateImportFailed},
	// External hand-off is non-terminal. It can only be retired by a manual
	// retry (which routes through StateImportPending) — there is no automatic
	// path out, by design: ScanLibrary reconciles the file independently.
	StateImportExternal: {StateImportPending},
	// A held format (#942) releases into the external hand-off once its sibling
	// arrives or the pair-gating timeout fires (StateImportExternal), fails there
	// if the placement can't complete (StateImportFailed) or the destination is
	// invalid (StateImportBlocked), or is recovered by a manual retry
	// (StateImportPending), mirroring StateImportExternal.
	StateImportHeld: {StateImportExternal, StateImportFailed, StateImportBlocked, StateImportPending},
}

// IsDeadForRegrab reports whether a download in state s is a finished attempt
// with no automatic path out, so a fresh grab of the same release may reuse
// its row.
//
// StateFailed is the download side failure: the release was never fetched, or
// the client gave up on it. StateImportBlocked is the import side one (#1955):
// the retry budget is spent, no poller will revisit the row, and it would
// otherwise pin the release's GUID forever.
//
// StateImportFailed deliberately does NOT qualify: the scanner is still
// working through its retry budget on that row and a re-grab would race it.
// Neither do the non terminal hand off states (StateImportExternal,
// StateImportHeld): the files are still on their way into the library.
//
// The state alone never makes an imported row re-grabbable. The one imported
// row that is, an import whose book has since been deleted, depends on more
// than the state and is decided by Download.IsOrphanedImport (#2289).
//
// Callers: api.regrabbableState, Download.BlocksRegrab, and the SQL in
// db.DownloadRepo.RetryFailed and RetryDeadForAutoGrab. Keep them in
// agreement.
func (s DownloadState) IsDeadForRegrab() bool {
	return s == StateFailed || s == StateImportBlocked
}

// IsDeadForAutoRegrab is IsDeadForRegrab for the scheduler's automatic grab:
// only a failed download, where nothing was fetched or the client gave up on
// what it had. StateImportBlocked is deliberately excluded, for the reasons
// Download.BlocksAutoRegrab gives.
//
// db.DownloadRepo.RetryDeadForAutoGrab claims exactly the states this accepts;
// TestRetryDeadForAutoGrabMatchesThePredicate fails if the two diverge.
func (s DownloadState) IsDeadForAutoRegrab() bool {
	return s == StateFailed
}

// AllStates returns every download state, derived from the transition table so
// a new state cannot be added without appearing here. Order is stable so tests
// that enumerate states report the same way twice.
func AllStates() []DownloadState {
	out := make([]DownloadState, 0, len(validTransitions))
	for s := range validTransitions {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// CanTransitionTo reports whether a transition from s to next is valid.
func (s DownloadState) CanTransitionTo(next DownloadState) bool {
	return slices.Contains(validTransitions[s], next)
}

// ErrInvalidTransition is returned when an illegal state transition is attempted.
type ErrInvalidTransition struct {
	From DownloadState
	To   DownloadState
}

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid download state transition: %s → %s", e.From, e.To)
}
