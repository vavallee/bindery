package deluge

import (
	"testing"
)

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
