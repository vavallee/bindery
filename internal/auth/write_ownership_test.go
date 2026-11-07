package auth

import (
	"context"
	"testing"
)

// CheckWriteOwnership refuses a row with no owner to a signed in user who is
// not an admin, under tenancy only; every other answer matches CheckOwnership.
func TestCheckWriteOwnership(t *testing.T) {
	user := WithUserRole(WithUserID(context.Background(), 7), "user")
	noRole := WithUserID(context.Background(), 7)
	admin := WithUserRole(WithUserID(context.Background(), 1), "admin")
	anon := context.Background()

	for _, tc := range []struct {
		name    string
		tenancy bool
		ctx     context.Context
		owner   int64
		read    bool
		write   bool
	}{
		{"user, own row", true, user, 7, true, true},
		{"user, other user's row", true, user, 8, false, false},
		{"user, row with no owner", true, user, 0, true, false},
		{"user without role, row with no owner", true, noRole, 0, true, false},
		{"admin, row with no owner", true, admin, 0, true, true},
		{"admin, other user's row", true, admin, 8, true, true},
		{"no identity, row with no owner", true, anon, 0, true, true},
		{"tenancy off, user, row with no owner", false, user, 0, true, true},
		{"tenancy off, user, other user's row", false, user, 8, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetEnforceTenancyForTests(t, tc.tenancy)
			if got := CheckOwnership(tc.ctx, tc.owner); got != tc.read {
				t.Errorf("CheckOwnership = %v, want %v", got, tc.read)
			}
			if got := CheckWriteOwnership(tc.ctx, tc.owner); got != tc.write {
				t.Errorf("CheckWriteOwnership = %v, want %v", got, tc.write)
			}
		})
	}
}
