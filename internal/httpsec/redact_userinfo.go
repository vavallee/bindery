package httpsec

import (
	"net/url"
	"regexp"
	"strings"
)

// Feeds and download links sometimes carry basic credentials in the URL
// itself, https://user:password@host/... Net/http sends them as an
// Authorization header, so the userinfo is a credential wherever the URL is
// shown. A token is often passed as the user name alone, so the whole
// userinfo is replaced, not only the password.
//
// The placeholder is the same keyed hash as a path secret (pathPlaceholder):
// REDACTED-<12 hex>, so two GUIDs that differ only in their credentials
// still differ once redacted, and a userinfo that is already a placeholder
// is left alone.

// redactUserinfo replaces u's userinfo with its placeholder and reports
// whether it changed anything.
func redactUserinfo(u *url.URL) bool {
	if u.User == nil {
		return false
	}
	if _, hasPassword := u.User.Password(); !hasPassword && strings.HasPrefix(u.User.Username(), "REDACTED") {
		return false
	}
	u.User = url.User(pathPlaceholder(u.User.String()))
	return true
}

// userinfoInTextRE finds the userinfo of each absolute URL in free text: the
// part of the authority before its last @. It cannot cross a slash, so an @
// in a path or query is never taken for one.
var userinfoInTextRE = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.\-]*://)([^/\s"'<>?#]+)@`)

// redactUserinfoInText applies redactUserinfo's rule to every URL in s.
func redactUserinfoInText(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return userinfoInTextRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := userinfoInTextRE.FindStringSubmatch(m)
		info := sub[2]
		if !strings.Contains(info, ":") && strings.HasPrefix(info, "REDACTED") {
			return m
		}
		return sub[1] + pathPlaceholder(info) + "@"
	})
}
