package auth

import (
	"strings"
	"testing"
)

func TestMultiUserWithoutTenancy(t *testing.T) {
	cases := []struct {
		name      string
		users     int
		enforce   bool
		wantWarn  bool
		rationale string
	}{
		{"single user, gate off", 1, false, false, "the default install; nothing to warn about"},
		{"no users yet", 0, false, false, "pre-setup boot, before the first admin exists"},
		{"second user, gate off", 2, false, true, "the case this exists for"},
		{"second user, gate on", 2, true, false, "scoping is on, so the accounts are already separated"},
		{"single user, gate on", 1, true, false, "harmless, and not worth a line in the log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetEnforceTenancyForTests(t, tc.enforce)
			if got := MultiUserWithoutTenancy(tc.users); got != tc.wantWarn {
				t.Errorf("MultiUserWithoutTenancy(%d) with enforce=%v = %v, want %v (%s)",
					tc.users, tc.enforce, got, tc.wantWarn, tc.rationale)
			}
			if got := WarnIfMultiUserWithoutTenancy(tc.users); got != tc.wantWarn {
				t.Errorf("WarnIfMultiUserWithoutTenancy(%d) = %v, want %v", tc.users, got, tc.wantWarn)
			}
		})
	}
}

// The message has to name the variable an operator would set, or it is a
// warning with no next step.
func TestMultiUserNoTenancyWarning_NamesTheEnvVar(t *testing.T) {
	if !strings.Contains(MultiUserNoTenancyWarning, EnforceTenancyEnv) {
		t.Errorf("warning must name %s; got %q", EnforceTenancyEnv, MultiUserNoTenancyWarning)
	}
}
