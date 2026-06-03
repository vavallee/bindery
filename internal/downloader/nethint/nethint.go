// Package nethint classifies network errors and returns a short contextual
// hint string to append to "could not reach …" messages shown to users.
// The hints target the most common failure modes in Docker deployments.
package nethint

import (
	"errors"
	"net"
	"syscall"
)

// ForErr inspects err and returns a hint string (with a leading space) that
// can be appended directly to an error message. Returns an empty string when
// the error does not match any known class.
//
// Classification priority (first match wins):
//  1. net.DNSError with IsNotFound==true → Docker DNS hint
//  2. syscall.ECONNREFUSED (or wrapped) → port-not-open hint
//  3. net.Error with Timeout()==true → firewall/proxy hint
func ForErr(err error) string {
	if err == nil {
		return ""
	}

	// 1. DNS lookup failure — most likely a container name that isn't on the
	//    same Docker network as Bindery.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return " (if using a container name, ensure both services are on the same Docker network)"
	}

	// 2. Connection refused — TCP got rejected (RST). Common causes, in
	//    rough order of how often they bite:
	//    a) Wrong port, or the service is bound to a different interface than
	//       the one in the URL — e.g. listening on 127.0.0.1 only while the
	//       URL points at a LAN IP. Under `network_mode: host` this is the
	//       usual culprit: another container reaching the same service works
	//       because it uses localhost, while this URL uses the LAN address.
	//    b) Service genuinely isn't running / not listening on that port.
	//    c) Bindery runs on a Docker bridge network (not host), the target is
	//       on the bare-metal host or another subnet, and a host firewall is
	//       REJECTing traffic from the bridge subnet (REJECT sends RST →
	//       ECONNREFUSED; DROP would surface as a timeout instead).
	//    Note: don't assert a Docker subnet — host-networked deployments have
	//    none, and blaming a firewall there sends users down the wrong path.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return " (connection refused — check the port, and that the service is listening on the interface in the URL (a service bound to 127.0.0.1 will refuse a LAN-IP URL); on a Docker bridge network a host firewall may also be rejecting the traffic)"
	}

	// 3. Timeout — host is reachable but not responding (firewall, proxy, etc.).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return " (host is reachable but not responding — check firewall or proxy)"
	}

	return ""
}
