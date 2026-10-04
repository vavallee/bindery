package notifier

import (
	"errors"
	"net/url"
	"strings"

	"github.com/vavallee/bindery/internal/httpsec"
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

// redactWebhookValidation scrubs a validator error for target. The validator
// wraps the parse error with fmt.Errorf, which formats the message at once, so
// rewriting the *url.Error afterwards would not reach the text. The message is
// rebuilt with target replaced by its host only form; the chain is kept.
func redactWebhookValidation(err error, target string) error {
	msg := err.Error()
	scrubbed := msg
	if target != "" {
		// The validator may already have run the URL through
		// httpsec.RedactSecrets, which leaves path secrets in place.
		for _, form := range []string{target, httpsec.RedactSecrets(target)} {
			scrubbed = strings.ReplaceAll(scrubbed, form, webhookURLForLog(target))
		}
	}
	if scrubbed == msg {
		return redactWebhookError(err)
	}
	return &scrubbedError{msg: scrubbed, err: err}
}

type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

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
