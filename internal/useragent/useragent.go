// Package useragent produces the canonical User-Agent string Bindery sends
// on every outbound HTTP request.
//
// The format follows the convention used by Sonarr/Radarr/Lidarr/Prowlarr:
//
//	bindery/<version> (<os>)
//
// e.g. "bindery/1.11.1 (linux)" or "bindery/dev (darwin)".
//
// Lowercase "bindery" is deliberate. At least one indexer (nzbfinder.ws)
// runs a Cloudflare WAF rule that case-sensitively rejects any User-Agent
// containing the substring "Bindery" with HTTP 403. Keeping a single
// lowercase identity across every external client means all of Bindery's
// outbound traffic shares one reputation signal — easy to whitelist and
// easy to debug.
package useragent

import (
	"runtime"
	"strings"
	"sync/atomic"
)

var current atomic.Pointer[string]

func init() {
	s := Build("dev")
	current.Store(&s)
}

// Set installs the canonical User-Agent for this process. Call once from
// main() after the build version is known; concurrent Set calls are safe
// but the last write wins.
func Set(version string) {
	s := Build(version)
	current.Store(&s)
}

// Get returns the canonical User-Agent. Safe for concurrent use; cheap
// enough to call on every request.
func Get() string {
	return *current.Load()
}

// Build constructs the canonical User-Agent without touching the singleton.
// Useful for clients that already accept a version parameter (e.g. abs,
// grimmory) and want to compute their UA up-front.
func Build(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "dev"
	}
	v = strings.TrimPrefix(v, "v")
	return "bindery/" + v + " (" + runtime.GOOS + ")"
}
