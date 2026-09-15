package models

import "time"

// UnknownLanguageBehavior controls what happens when a metadata source
// returns no language for a book while the profile has a non-empty
// allowedLanguages list. See #232.
const (
	UnknownLanguagePass = "pass"
	UnknownLanguageFail = "fail"
)

type MetadataProfile struct {
	ID                      int64  `json:"id"`
	Name                    string `json:"name"`
	MinPopularity           int    `json:"minPopularity"`
	MinPages                int    `json:"minPages"`
	SkipMissingDate         bool   `json:"skipMissingDate"`
	SkipMissingISBN         bool   `json:"skipMissingIsbn"`
	SkipPartBooks           bool   `json:"skipPartBooks"`
	AllowedLanguages        string `json:"allowedLanguages"`
	UnknownLanguageBehavior string `json:"unknownLanguageBehavior"`
	// KeepThreshold and ExcludeThreshold are internal/metadata/filterengine's
	// banding thresholds (migration 086, #2235). Both default to 0, which —
	// with every v1 signal at veto weight and Context.Prior hardcoded to 0 —
	// reproduces the pre-#2235 boolean filter chain's keep/exclude decision
	// exactly. At v1 the API layer (internal/api/metadata_profiles.go)
	// rejects any value where either field is nonzero, not merely where they
	// disagree: a nonzero EQUAL pair (e.g. both 50) still bands every clean
	// candidate as EXCLUDE, since a clean candidate's score is always
	// exactly 0. See validateScoreThresholds's doc for the full reasoning —
	// this was a real bug in an earlier, looser version of that check.
	KeepThreshold    float64 `json:"keepThreshold"`
	ExcludeThreshold float64 `json:"excludeThreshold"`
	// ClusterFilterPreset selects #2235 Phase 2's ClusterEditionCountSignal
	// tuning (migration 087) — a closed set of server-owned presets
	// (internal/metadata/filterengine.ClusterFilterPreset), never raw
	// threshold numbers. "off" (the default for every existing profile) is
	// byte-identical to this field not existing at all: no cluster signal is
	// constructed, and the KeepThreshold/ExcludeThreshold columns above stay
	// exactly the locked 0/0 pair validateScoreThresholds enforces. A non-off
	// preset supplies its own shared threshold at the fetchAuthorBooks call
	// site instead of reading it from those two columns — see
	// filterengine.ThresholdForPreset's doc for why it has to be one shared
	// value, not an independent exclude-side move.
	ClusterFilterPreset string    `json:"clusterFilterPreset"`
	CreatedAt           time.Time `json:"createdAt"`
	// OwnerUserID is the per-user ownership column added in migration 025.
	// Zero means "no recorded owner" (legacy pre-backfill rows); auth's
	// CheckOwnership treats that as visible to every authenticated caller.
	OwnerUserID int64 `json:"-"`
}
