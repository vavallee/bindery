// Package useragent produces the canonical User-Agent string Bindery sends
// on every outbound HTTP request.
//
// The format follows the convention used by Sonarr/Radarr/Lidarr/Prowlarr,
// extended with a contact pointer (URL or email) so providers that require
// one — OpenLibrary's API policy at https://openlibrary.org/developers/api
// is the load-bearing example — accept the request:
//
//	bindery/<version> (<os>; <contact>)
//
// e.g. "bindery/1.15.0 (linux; https://github.com/vavallee/bindery)" or,
// with BINDERY_CONTACT set, "bindery/1.15.0 (linux; mailto:me@x.org)".
//
// Lowercase "bindery" is deliberate. At least one indexer (nzbfinder.ws)
// runs a Cloudflare WAF rule that case-sensitively rejects any User-Agent
// containing the substring "Bindery" with HTTP 403. Keeping a single
// lowercase identity across every external client means all of Bindery's
// outbound traffic shares one reputation signal — easy to whitelist and
// easy to debug.
//
// The trailing contact is OpenLibrary-mandated (#834): without it,
// OpenLibrary returns 403 on every search request, breaking name/title
// book additions for any user running with OpenLibrary as the primary
// metadata provider. OpenLibrary also grants a higher rate limit when the
// contact is present.
//
// BINDERY_CONTACT lets each operator advertise their own contact (#848).
// The shared project-URL default causes per-UA rate-limiting to track the
// whole fleet of Bindery installs as one client, which is exactly what
// OpenLibrary's policy is trying to prevent ("contact email or phone of
// your application"). A per-instance contact differentiates each install
// and is more policy-compliant. Acceptable forms:
//
//   - mailto:you@example.org  (recommended for OpenLibrary)
//   - https://your-bindery.example.org
//   - you@example.org         (bare email; "mailto:" prepended automatically)
//
// Bindery never connects to the contact value — it goes only into the
// User-Agent — so the address can be anything an upstream provider would
// use to reach the operator.
package useragent

import (
	"os"
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

// DefaultContactURL is the contact pointer used when BINDERY_CONTACT is
// unset. The project URL satisfies OpenLibrary's policy on its face but,
// being shared by every Bindery install, can trip per-UA rate-limiting on
// expensive endpoints like /search/authors.json (#848). Operators hitting
// that should set BINDERY_CONTACT to their own email or instance URL.
const DefaultContactURL = "https://github.com/vavallee/bindery"

// resolveContact reads BINDERY_CONTACT and normalises whatever the
// operator typed into a form that can be embedded in a User-Agent:
//   - "mailto:foo@x" or "https://..." passes through.
//   - "foo@x" becomes "mailto:foo@x".
//   - Anything else (whitespace stripped) passes through unchanged.
//
// When BINDERY_CONTACT is unset or empty, DefaultContactURL is returned.
func resolveContact() string {
	raw := strings.TrimSpace(os.Getenv("BINDERY_CONTACT"))
	if raw == "" {
		return DefaultContactURL
	}
	// Strip whitespace inside the value too — pasted email addresses with
	// trailing spaces are easy to typo and would break Header.Set.
	raw = strings.ReplaceAll(raw, " ", "")
	if strings.Contains(raw, "@") && !strings.HasPrefix(raw, "mailto:") &&
		!strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return "mailto:" + raw
	}
	return raw
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
	return "bindery/" + v + " (" + runtime.GOOS + "; " + resolveContact() + ")"
}
