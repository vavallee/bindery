package abs

import (
	"strings"
	"testing"
)

// TestNormalizeBaseURL_SchemelessHostNamesTheRealProblem covers #2056: a user
// typing the natural docker-compose form got an error about the scheme for
// input whose only visible novelty was the port, concluded the port had been
// refused, retried without it, and reached port 80.
//
// The assertion is on what the message tells them, not just that it failed:
// the old message failed too, it just pointed at the wrong thing.
func TestNormalizeBaseURL_SchemelessHostNamesTheRealProblem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"host and port", "audiobookshelf:13378"},
		{"bare host", "audiobookshelf"},
		{"host with path", "audiobookshelf/abs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NormalizeBaseURL(tc.raw)
			if err == nil {
				t.Fatalf("NormalizeBaseURL(%q) = nil error, want a scheme complaint", tc.raw)
			}
			msg := err.Error()
			if !strings.Contains(msg, "missing a scheme") {
				t.Errorf("error = %q, want it to say the scheme is missing", msg)
			}
			if !strings.Contains(msg, "http://"+tc.raw) {
				t.Errorf("error = %q, want it to name the corrected value %q", msg, "http://"+tc.raw)
			}
		})
	}
}

// TestNormalizeBaseURL_UnsupportedSchemeStillRejectedAsScheme guards the other
// side of the split: a real scheme that Bindery does not speak must keep the
// original message. Telling someone who typed ftp:// that they "left off the
// scheme" would be a worse error than the one #2056 replaced.
func TestNormalizeBaseURL_UnsupportedSchemeStillRejectedAsScheme(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"ftp://audiobookshelf", "ws://audiobookshelf:13378"} {
		_, err := NormalizeBaseURL(raw)
		if err == nil {
			t.Fatalf("NormalizeBaseURL(%q) = nil error, want rejection", raw)
		}
		if !strings.Contains(err.Error(), "must use http or https") {
			t.Errorf("NormalizeBaseURL(%q) error = %q, want the unsupported-scheme message", raw, err.Error())
		}
		if strings.Contains(err.Error(), "missing a scheme") {
			t.Errorf("NormalizeBaseURL(%q) error = %q, but a scheme was present", raw, err.Error())
		}
	}
}

// TestNormalizeBaseURL_PortIsPreserved is the fact the reporter never got to
// find out: ports were always supported, the input boundary just never let
// them prove it.
func TestNormalizeBaseURL_PortIsPreserved(t *testing.T) {
	t.Parallel()
	got, err := NormalizeBaseURL("http://audiobookshelf:13378")
	if err != nil {
		t.Fatalf("NormalizeBaseURL: %v", err)
	}
	if got != "http://audiobookshelf:13378" {
		t.Errorf("got %q, want the port preserved", got)
	}
}
