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
	// once the external tool places it; searchWanted skips books that have a
	// download in this state so the release is not re-grabbed forever while the
	// hand-off is outstanding (issue #706 finding 3).
	StateImportExternal DownloadState = "importExternal"
	// StateImportHeld marks a completed drop-folder download whose format is
	// being held back under pair gating (#942): a media_type=both book only
	// hands off to the paired-reader tool (Storyteller) once BOTH the ebook and
	// audiobook are present, so the first-arriving format parks here — files
	// untouched in the download dir, its on-disk location recorded in
	// import_path — until the sibling completes and releases both together, or
	// the pair-gating timeout drops it alone. Like StateImportExternal it is
	// deliberately NON-terminal and is NOT swept by RecoverInterruptedImports;
	// searchWanted skips books with a download in this state so the release is
	// not re-grabbed while the hold is outstanding.
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
