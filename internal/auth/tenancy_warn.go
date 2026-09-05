package auth

import "log/slog"

// MultiUserNoTenancyWarning is the operator-facing message emitted when an
// install has more than one user account and per-user resource scoping is off.
//
// EnforceTenancy defaults off, which is right for the single-user install that
// nearly every deployment is: with the gate closed, every CheckOwnership and
// ListScopeUserID short-circuits to allow. The person it catches out is the
// operator who adds a second account through Settings and assumes the two
// libraries are separate. Nothing in the UI says otherwise, so the boot log is
// where it gets said.
//
// This is a warning and not a gate on purpose. Several accounts sharing one
// library is a perfectly good household setup, and turning the gate on by
// default would change what every existing multi-user install shows.
const MultiUserNoTenancyWarning = "this install has more than one user account but per-user scoping is off: " +
	"every account can see and modify every other account's authors, books, queue and history. " +
	"Set " + EnforceTenancyEnv + "=1 to scope each user to their own library, or ignore this if the accounts are meant to share one."

// MultiUserWithoutTenancy reports whether userCount and the tenancy gate form
// the combination MultiUserNoTenancyWarning describes. Split out from the
// warning so a caller that wants to surface the same condition somewhere other
// than the log does not have to re-derive it.
func MultiUserWithoutTenancy(userCount int) bool {
	return userCount > 1 && !EnforceTenancy()
}

// WarnIfMultiUserWithoutTenancy logs MultiUserNoTenancyWarning when
// MultiUserWithoutTenancy holds, and reports whether it warned so callers can
// pass the same text on to an operator through another channel.
func WarnIfMultiUserWithoutTenancy(userCount int) bool {
	if !MultiUserWithoutTenancy(userCount) {
		return false
	}
	slog.Warn(MultiUserNoTenancyWarning, "users", userCount, "env", EnforceTenancyEnv)
	return true
}
