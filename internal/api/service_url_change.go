package api

import (
	"context"
	"net/url"
	"strings"

	"github.com/vavallee/bindery/internal/db"
)

// canonicalServiceURL reduces a service base URL to the form used to decide
// whether an edit moved it to a different server: lower case scheme and host,
// default port dropped, trailing slashes dropped, query and fragment ignored.
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
func clearCredentialsOnURLChange(ctx context.Context, settings *db.SettingsRepo, key, newValue string) error {
	creds, ok := credentialsBoundToURL[key]
	if !ok {
		return nil
	}
	old, _ := settings.Get(ctx, key)
	if old == nil || sameServiceURL(old.Value, newValue) {
		return nil
	}
	for _, ck := range creds {
		if cur, _ := settings.Get(ctx, ck); cur != nil && cur.Value != "" {
			if err := settings.Set(ctx, ck, ""); err != nil {
				return err
			}
		}
	}
	return nil
}
