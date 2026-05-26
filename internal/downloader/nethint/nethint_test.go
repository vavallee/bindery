package nethint_test

import (
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/nethint"
)

func TestForErr_Nil(t *testing.T) {
	if got := nethint.ForErr(nil); got != "" {
		t.Errorf("ForErr(nil) = %q, want empty", got)
	}
}

func TestForErr_DNSNotFound(t *testing.T) {
	err := &net.DNSError{Name: "no-such-container", IsNotFound: true}
	got := nethint.ForErr(err)
	if got == "" {
		t.Fatal("expected non-empty hint for DNS not-found error")
	}
	want := " (if using a container name, ensure both services are on the same Docker network)"
	if got != want {
		t.Errorf("ForErr(DNSNotFound) = %q, want %q", got, want)
	}
}

// TestForErr_DNSTemporary verifies that a temporary DNS error (not IsNotFound)
// does not produce the container-name hint — it may be a transient failure, not
// a missing record.
func TestForErr_DNSTemporary(t *testing.T) {
	err := &net.DNSError{Name: "sabnzbd", IsNotFound: false, IsTimeout: true}
	got := nethint.ForErr(err)
	// Timeout DNS errors hit the timeout branch, not the DNS branch.
	if got != "" && got != " (host is reachable but not responding — check firewall or proxy)" {
		t.Errorf("ForErr(DNS timeout) = %q, want empty or timeout hint", got)
	}
}

func TestForErr_ConnectionRefused(t *testing.T) {
	err := fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED)
	got := nethint.ForErr(err)
	want := " (connection refused — service may not be listening on that port, or a host firewall is rejecting traffic from the Docker subnet)"
	if got != want {
		t.Errorf("ForErr(ECONNREFUSED) = %q, want %q", got, want)
	}
}

func TestForErr_Timeout(t *testing.T) {
	err := &timeoutErr{}
	got := nethint.ForErr(err)
	want := " (host is reachable but not responding — check firewall or proxy)"
	if got != want {
		t.Errorf("ForErr(timeout) = %q, want %q", got, want)
	}
}

func TestForErr_GenericError(t *testing.T) {
	err := fmt.Errorf("HTTP 500: internal server error")
	got := nethint.ForErr(err)
	if got != "" {
		t.Errorf("ForErr(generic) = %q, want empty", got)
	}
}

// timeoutErr is a minimal net.Error that signals a timeout.
type timeoutErr struct{}

func (e *timeoutErr) Error() string   { return "i/o timeout" }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }
