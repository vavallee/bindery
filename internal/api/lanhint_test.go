package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// timeoutErr satisfies net.Error with Timeout()=true, mirroring what
// http.Client produces when its overall Timeout fires.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "Client.Timeout exceeded while awaiting headers" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestLanTimeoutHint covers #1474: only a timeout against a LAN-shaped host
// earns the VPN-killswitch hint; every other combination keeps the original
// message untouched.
func TestLanTimeoutHint(t *testing.T) {
	timeout := error(timeoutErr{})
	wrappedDeadline := fmt.Errorf("Get \"http://abs:13378/ping\": %w", context.DeadlineExceeded)
	refused := errors.New("connection refused")

	cases := []struct {
		name     string
		url      string
		err      error
		wantHint bool
	}{
		{"timeout + RFC1918 IP", "http://192.168.1.4:13378", timeout, true},
		{"timeout + loopback", "http://127.0.0.1:8080", timeout, true},
		{"timeout + bare docker hostname", "http://audiobookshelf:13378", timeout, true},
		{"timeout + .local suffix", "http://abs.local:13378", timeout, true},
		{"wrapped deadline + bare hostname", "http://abs:13378", wrappedDeadline, true},
		{"timeout + public host", "https://api.audiobookshelf.org", timeout, false},
		{"timeout + public IP", "http://8.8.8.8", timeout, false},
		{"non-timeout + LAN IP", "http://192.168.1.4:13378", refused, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lanTimeoutHint(c.url, c.err)
			if !strings.HasPrefix(got, c.err.Error()) {
				t.Errorf("message must start with the original error, got %q", got)
			}
			hinted := strings.Contains(got, "FIREWALL_OUTBOUND_SUBNETS")
			if hinted != c.wantHint {
				t.Errorf("hint=%v, want %v (got %q)", hinted, c.wantHint, got)
			}
		})
	}
}
