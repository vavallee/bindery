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
)

// validTransitions defines which state transitions are allowed.
var validTransitions = map[DownloadState][]DownloadState{
	StateGrabbed:       {StateDownloading, StateFailed},
	StateDownloading:   {StateCompleted, StateFailed},
	StateCompleted:     {StateImportPending, StateImportFailed},
	StateImportPending: {StateImporting, StateImportFailed},
	StateImporting:     {StateImported, StateImportFailed, StateImportBlocked},
	StateImported:      {},
	StateFailed:        {},
	StateImportFailed:  {StateImportPending},
	StateImportBlocked: {},
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
