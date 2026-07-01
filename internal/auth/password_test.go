package auth

import (
	"strings"
	"testing"
)

// TestDummyPasswordHashEqualizesTiming asserts the dummy hash is a real
// argon2id hash with the CURRENT parameters, so verifying against it costs the
// same as verifying a genuine user's hash. If the argon parameters ever change
// without the dummy following, the username-not-found path would run a
// cheaper/absent KDF and reintroduce the login timing side-channel.
func TestDummyPasswordHashEqualizesTiming(t *testing.T) {
	dummy := DummyPasswordHash()

	// Structurally valid and rejected for attacker-supplied passwords, so the
	// missing-user branch always runs the KDF and fails. (The handler's u == nil
	// guard means even a match here still returns 401, so this is belt-and-braces.)
	for _, pw := range []string{"", "password", "admin", "hunter2"} {
		if VerifyPassword(pw, dummy) {
			t.Fatalf("dummy hash unexpectedly matched password %q", pw)
		}
	}

	// Its parameters must match what HashPassword produces today, otherwise the
	// KDF cost differs from a real verify and login timing diverges again.
	real, err := HashPassword("whatever")
	if err != nil {
		t.Fatal(err)
	}
	if d, r := paramField(t, dummy), paramField(t, real); d != r {
		t.Fatalf("dummy params %q != real params %q", d, r)
	}
}

// paramField returns the "m=..,t=..,p=.." segment (4th field) of a PHC hash.
func paramField(t *testing.T, phc string) string {
	t.Helper()
	parts := strings.Split(phc, "$")
	if len(parts) != 6 {
		t.Fatalf("malformed PHC hash: %q", phc)
	}
	return parts[3]
}
