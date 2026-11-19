package grimmory

import (
	"strings"
	"testing"
)

// TestNormalizeBaseURL_SchemelessHostNamesTheRealProblem is #2056 for
// Grimmory. The normalizer was copied from the ABS client before that fix, so
// "grimmory:6060" was rejected with a complaint about the scheme, and a user
// who then dropped the port reached port 80 instead.
func TestNormalizeBaseURL_SchemelessHostNamesTheRealProblem(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"grimmory:6060", "grimmory", "grimmory/sub"} {
		_, err := NormalizeBaseURL(raw)
		if err == nil {
			t.Fatalf("NormalizeBaseURL(%q) = nil error, want a missing-scheme error", raw)
		}
		if msg := err.Error(); !strings.Contains(msg, "missing a scheme") || !strings.Contains(msg, "http://"+raw) {
			t.Errorf("NormalizeBaseURL(%q) error = %q, want it to say the scheme is missing and name %q", raw, msg, "http://"+raw)
		}
		if msg := err.Error(); !strings.Contains(msg, "Grimmory") {
			t.Errorf("NormalizeBaseURL(%q) error = %q, want it to name Grimmory", raw, msg)
		}
	}
}
