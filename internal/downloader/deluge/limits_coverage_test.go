package deluge

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadLimited(t *testing.T) {
	got, err := readLimited(strings.NewReader("abc"), 3)
	if err != nil || string(got) != "abc" {
		t.Fatalf("at the cap: got (%q, %v), want the whole body", got, err)
	}
	if _, err := readLimited(strings.NewReader("abcd"), 3); err == nil || !strings.Contains(err.Error(), "exceeds 3 bytes") {
		t.Fatalf("over the cap: err = %v", err)
	}
	boom := errors.New("read failed")
	if _, err := readLimited(iotest.ErrReader(boom), 3); !errors.Is(err, boom) {
		t.Fatalf("reader error: err = %v, want %v", err, boom)
	}
}

func TestDaemonHostLabel(t *testing.T) {
	tests := []struct {
		h    daemonHost
		want string
	}{
		{daemonHost{id: "abc"}, "abc"},
		{daemonHost{id: "abc", addr: "10.0.0.1"}, "10.0.0.1"},
		{daemonHost{id: "abc", addr: "10.0.0.1", port: 58846}, "10.0.0.1:58846"},
	}
	for _, tt := range tests {
		if got := tt.h.label(); got != tt.want {
			t.Errorf("%+v.label() = %q, want %q", tt.h, got, tt.want)
		}
	}
}
