package dirperm

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		mode uint32
	}{
		{"empty falls back to default", "", 0o750},
		{"whitespace falls back to default", "  ", 0o750},
		{"whitelisted with leading zero", "0755", 0o755},
		{"whitelisted without leading zero", "755", 0o755},
		{"historical default", "0750", 0o750},
		{"group-write mode stripped by umask falls back", "0770", 0o750},
		{"world-readable group-write falls back", "0775", 0o750},
		{"owner-only stays valid", "0700", 0o700},
		{"open mode stripped by umask falls back", "0777", 0o750},
		{"unlisted octal falls back", "0774", 0o750},
		{"invalid text falls back", "abc", 0o750},
		{"decimal digits fall back", "099", 0o750},
		{"go-style 0o prefix falls back", "0o755", 0o750},
		{"negative falls back", "-1", 0o750},
		{"not a directory mode falls back", "0644", 0o750},
		{"zero falls back", "0", 0o750},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parse(tc.raw); uint32(got) != tc.mode {
				t.Fatalf("parse(%q) = %#o, want %#o", tc.raw, got, tc.mode)
			}
		})
	}
}

func TestLibrary(t *testing.T) {
	tests := []struct {
		name  string
		value string
		mode  uint32
	}{
		{"unset falls back to default", "", 0o750},
		{"whitelisted mode honoured", "0755", 0o755},
		{"whitelisted mode without leading zero", "750", 0o750},
		{"group-write mode stripped by umask falls back", "0775", 0o750},
		{"unrecognised value falls back", "0o755", 0o750},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BINDERY_DIR_MODE", tc.value)
			if got := Library(); uint32(got) != tc.mode {
				t.Fatalf("Library() with BINDERY_DIR_MODE=%q = %#o, want %#o", tc.value, got, tc.mode)
			}
		})
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty is valid (use default)", "", true},
		{"whitelisted value", "0755", true},
		{"whitelisted without leading zero", "755", true},
		{"whitespace only", "  ", true},
		{"unrecognised value", "0o755", false},
		{"unlisted octal", "0774", false},
		{"junk", "abc", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Valid(tc.raw); got != tc.want {
				t.Fatalf("Valid(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
