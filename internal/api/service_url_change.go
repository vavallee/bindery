package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"

	"github.com/vavallee/bindery/internal/db"
)

// canonicalServiceURL reduces a service base URL to the form used to decide
// whether an edit moved it to a different server: lower case scheme and host,
// default port dropped, trailing slashes dropped, query and fragment ignored.
// A trailing dot on the host is kept: a bare name can resolve through a DNS
// search domain to a different machine than the absolute name, so the two
// count as different servers.
// An unparseable value falls back to its trimmed text.
func canonicalServiceURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/")
}

// sameServiceURL reports whether two base URLs name the same server, so a
// no-op edit (trailing slash, host case, explicit default port) does not count
// as moving the integration.
func sameServiceURL(a, b string) bool {
	return canonicalServiceURL(a) == canonicalServiceURL(b)
}

// rawStringSupplied reports whether the raw request body carries key as a
// non-empty string, i.e. the caller sent a replacement rather than leaving the
// field blank to mean "keep the saved value".
func rawStringSupplied(raw map[string]json.RawMessage, key string) bool {
	value, ok := raw[key]
	if !ok {
		return false
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return false
	}
	return strings.TrimSpace(s) != ""
}

// logCredentialsCleared records that saved credentials were dropped because
// the integration's address moved. It names the fields, never their values.
func logCredentialsCleared(kind string, id int64, fields ...string) {
	if len(fields) == 0 {
		return
	}
	slog.Info("cleared saved credentials because the address changed", "integration", kind, "id", id, "cleared", fields)
}

// credentialsBoundToURL lists, for each service URL setting the generic
// settings route can write, the stored credentials that were issued for that
// server. Changing the URL through the generic route cannot carry a
// replacement credential, so they are cleared rather than sent to the new
// address.
var credentialsBoundToURL = map[string][]string{
	SettingABSBaseURL:       {SettingABSAPIKey},
	SettingBookOrbitBaseURL: {SettingBookOrbitAPIKey},
	SettingGrimmoryBaseURL:  {SettingGrimmoryAPIKey, SettingGrimmoryPassword},
	SettingCalibrePluginURL: {SettingCalibrePluginAPIKey},
}

// clearCredentialsOnURLChange clears the credentials bound to a service URL
// setting when newValue names a different server than the stored value.
// An unset URL counts as the empty string, so setting a URL where none was, or
// deleting the URL, is a change whenever a credential is still stored. It
// returns the settings keys it cleared (never their values) and logs them.
func clearCredentialsOnURLChange(ctx context.Context, settings *db.SettingsRepo, key, newValue string) ([]string, error) {
	creds, ok := credentialsBoundToURL[key]
	if !ok {
		return nil, nil
	}
	oldValue := ""
	if old, _ := settings.Get(ctx, key); old != nil {
		oldValue = old.Value
	}
	if sameServiceURL(oldValue, newValue) {
		return nil, nil
	}
	var cleared []string
	for _, ck := range creds {
		if cur, _ := settings.Get(ctx, ck); cur != nil && cur.Value != "" {
			if err := settings.Set(ctx, ck, ""); err != nil {
				return cleared, err
			}
			cleared = append(cleared, ck)
		}
	}
	if len(cleared) > 0 {
		slog.Info("settings: cleared saved credentials because the service URL changed", "urlKey", key, "cleared", cleared)
	}
	return cleared, nil
}
