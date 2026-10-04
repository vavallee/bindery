package notifier

import (
	"errors"
	"net/url"
)

// redactWebhookError scrubs the webhook URL a *url.Error carries (a failed
// send, a URL that would not parse, a refused redirect) down to its scheme and
// host. Most webhook URLs are themselves the credential, and not only in the
// shapes httpsec.RedactSecrets knows: a self-hosted ntfy topic, an Apprise
// key, a Home Assistant webhook id and a Teams connector all live in the path.
// The host is what diagnosing a failed send needs, so everything after it is
// dropped. The error chain stays intact for errors.Is/As.
func redactWebhookError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = webhookURLForLog(ue.URL)
	}
	return err
}

// webhookURLForLog returns raw reduced to scheme://host, with "/REDACTED" in
// place of any path, query or fragment.
func webhookURLForLog(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "REDACTED"
	}
	out := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		out += "/REDACTED"
	}
	return out
}
