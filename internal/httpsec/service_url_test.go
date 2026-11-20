package httpsec

import (
	"strings"
	"testing"
)

// TestNormalizeServiceBaseURL_SchemelessHostNamesTheRealProblem covers #2056: a user
// typing the natural docker-compose form got an error about the scheme for
// input whose only visible novelty was the port, concluded the port had been
// refused, retried without it, and reached port 80.
//
// The assertion is on what the message tells them, not just that it failed:
// the old message failed too, it just pointed at the wrong thing.
func TestNormalizeServiceBaseURL_SchemelessHostNamesTheRealProblem(t *testing.T) {
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
			_, err := NormalizeServiceBaseURL(tc.raw, "Audiobookshelf")
			if err == nil {
				t.Fatalf("NormalizeServiceBaseURL(%q) = nil error, want a scheme complaint", tc.raw)
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

// TestNormalizeServiceBaseURL_UnsupportedSchemeStillRejectedAsScheme guards the other
// side of the split: a real scheme that Bindery does not speak must keep the
// original message. Telling someone who typed ftp:// that they "left off the
// scheme" would be a worse error than the one #2056 replaced.
func TestNormalizeServiceBaseURL_UnsupportedSchemeStillRejectedAsScheme(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"ftp://audiobookshelf", "ws://audiobookshelf:13378"} {
		_, err := NormalizeServiceBaseURL(raw, "Audiobookshelf")
		if err == nil {
			t.Fatalf("NormalizeServiceBaseURL(%q) = nil error, want rejection", raw)
		}
		if !strings.Contains(err.Error(), "must use http or https") {
			t.Errorf("NormalizeServiceBaseURL(%q) error = %q, want the unsupported-scheme message", raw, err.Error())
		}
		if strings.Contains(err.Error(), "missing a scheme") {
			t.Errorf("NormalizeServiceBaseURL(%q) error = %q, but a scheme was present", raw, err.Error())
		}
	}
}

// TestNormalizeServiceBaseURL_PortIsPreserved is the fact the reporter never got to
// find out: ports were always supported, the input boundary just never let
// them prove it.
func TestNormalizeServiceBaseURL_PortIsPreserved(t *testing.T) {
	t.Parallel()
	got, err := NormalizeServiceBaseURL("http://audiobookshelf:13378", "Audiobookshelf")
	if err != nil {
		t.Fatalf("NormalizeServiceBaseURL: %v", err)
	}
	if got != "http://audiobookshelf:13378" {
		t.Errorf("got %q, want the port preserved", got)
	}
}

func TestNormalizeAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "empty string ok",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace trimmed",
			input: "  mykey  ",
			want:  "mykey",
		},
		{
			name:    "control character 0x01",
			input:   "key\x01bad",
			wantErr: true,
		},
		{
			name:    "control character 0x7f",
			input:   "key\x7fbad",
			wantErr: true,
		},
		{
			name:  "clean key ok",
			input: "abc123-XYZ_valid.key",
			want:  "abc123-XYZ_valid.key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAPIKey(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("NormalizeAPIKey(%q): want error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeAPIKey(%q): unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeAPIKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsAllDigits(t *testing.T) {
	for in, want := range map[string]bool{"": false, "123": true, "12a": false, "-1": false} {
		if got := isAllDigits(in); got != want {
			t.Errorf("isAllDigits(%q) = %v, want %v", in, got, want)
		}
	}
}
