// Package useragent produces the canonical User-Agent string Bindery sends
// on every outbound HTTP request.
//
// The format follows the convention used by Sonarr/Radarr/Lidarr/Prowlarr,
// extended with the project URL as the contact pointer required by
// OpenLibrary's API policy (https://openlibrary.org/developers/api):
//
//	bindery/<version> (<os>; https://github.com/vavallee/bindery)
//
// e.g. "bindery/1.14.2 (linux; https://github.com/vavallee/bindery)".
//
// Lowercase "bindery" is deliberate. At least one indexer (nzbfinder.ws)
// runs a Cloudflare WAF rule that case-sensitively rejects any User-Agent
// containing the substring "Bindery" with HTTP 403. Keeping a single
// lowercase identity across every external client means all of Bindery's
// outbound traffic shares one reputation signal — easy to whitelist and
// easy to debug.
//
// The trailing URL is OpenLibrary-mandated (#834): without it, OpenLibrary
// returns 403 on every search request, breaking name/title book additions
// for any user running with OpenLibrary as the primary metadata provider.
// OpenLibrary also grants a higher rate limit when the contact is present.
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

// ContactURL is the contact pointer Bindery advertises in its User-Agent so
// OpenLibrary (and any other provider that requires it) can reach the
// project. Exported so tests can assert it is present in the UA.
const ContactURL = "https://github.com/vavallee/bindery"

// Build constructs the canonical User-Agent without touching the singleton.
// Useful for clients that already accept a version parameter (e.g. abs,
// grimmory) and want to compute their UA up-front.
func Build(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "dev"
	}
	v = strings.TrimPrefix(v, "v")
	return "bindery/" + v + " (" + runtime.GOOS + "; " + ContactURL + ")"
}
