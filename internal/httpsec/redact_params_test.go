package httpsec

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// secretParamCases are the query parameters that carry a credential in URLs
// Bindery logs, stores or returns: newznab and Jackett keys, tracker passkeys,
// and generic token and signature names. The newznab tests keep a copy, so a
// name added here belongs there too.
var secretParamCases = []string{
	"apikey", "api_key", "jackett_apikey", "key", "token", "access_token",
	"auth", "authkey", "passkey", "torrent_pass", "rsskey", "pass",
	"password", "secret", "sig", "signature", "tp",
	// The name is matched case insensitively.
	"PassKey", "JACKETT_APIKEY", "Torrent_Pass",
}

func TestRedactSecrets_EveryParamName(t *testing.T) {
	for _, name := range secretParamCases {
		t.Run(name, func(t *testing.T) {
			in := fmt.Sprintf(`Get "https://tracker.example/dl/42?id=7&%s=S3CRETVALUE&file=x": dial tcp: i/o timeout`, name)
			got := RedactSecrets(in)
			if strings.Contains(got, "S3CRETVALUE") {
				t.Fatalf("value of %s leaked: %q", name, got)
			}
			if !strings.Contains(got, `?id=7&`) || !strings.Contains(got, `&file=x": dial tcp`) {
				t.Fatalf("non secret params were damaged: %q", got)
			}
		})
	}
}

// A secret as the last parameter must not swallow the closing quote Go puts
// around the URL in a *url.Error string.
func TestRedactSecrets_StopsAtClosingQuote(t *testing.T) {
	in := `Get "https://tracker.example/dl?passkey=S3CRETVALUE": EOF`
	want := `Get "https://tracker.example/dl?passkey=REDACTED": EOF`
	if got := RedactSecrets(in); got != want {
		t.Fatalf("RedactSecrets(%q) = %q, want %q", in, got, want)
	}
}

func TestRedactURLError_EveryParamName(t *testing.T) {
	for _, name := range secretParamCases {
		t.Run(name, func(t *testing.T) {
			ue := &url.Error{Op: "Get", URL: "https://tracker.example/dl?" + name + "=S3CRETVALUE", Err: sentinelNetErr{}}
			err := fmt.Errorf("fetch torrent from indexer: %w", RedactURLError(ue))
			if strings.Contains(err.Error(), "S3CRETVALUE") {
				t.Fatalf("value of %s leaked: %v", name, err)
			}
		})
	}
}

// Names are matched exactly: a parameter that merely contains a secret name
// is a normal parameter and must come through untouched.
func TestRedactSecrets_DoesNotMatchSubstrings(t *testing.T) {
	for _, in := range []string{
		"https://x.example/a?monkey=VISIBLE",
		"https://x.example/a?keyword=VISIBLE&passage=VISIBLE",
		"https://x.example/a?tokens=VISIBLE&author=VISIBLE",
		"https://x.example/a?apikeys=VISIBLE&signatures=VISIBLE",
		"https://x.example/a?t=search&q=VISIBLE&cat=7020",
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("RedactSecrets(%q) = %q, want it unchanged", in, got)
		}
	}
}

// Webhook services that put the credential in the URL path rather than the
// query string.
func TestRedactSecrets_PathTokens(t *testing.T) {
	cases := []struct {
		name, in, keep string
	}{
		{"discord", `Post "https://discord.com/api/webhooks/123456789012345678/S3CRETVALUE-abc_DEF": dial tcp: i/o timeout`, `discord.com/api/webhooks/123456789012345678/`},
		{"discordapp", `https://discordapp.com/api/webhooks/123/S3CRETVALUE?wait=true`, "?wait=true"},
		{"discord versioned", `https://discord.com/api/v10/webhooks/123/S3CRETVALUE/slack`, "/slack"},
		{"telegram", `Post "https://api.telegram.org/bot123456:S3CRETVALUE-x_y/sendMessage": EOF`, `/sendMessage": EOF`},
		{"slack", `https://hooks.slack.com/services/T000/B000/S3CRETVALUE`, "hooks.slack.com/services/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.in)
			if strings.Contains(got, "S3CRETVALUE") {
				t.Fatalf("path token leaked: %q", got)
			}
			if !strings.Contains(got, tc.keep) {
				t.Fatalf("redaction removed more than the token, want %q kept: %q", tc.keep, got)
			}
		})
	}
}

// Shapes the log export sees outside the notifier's own error scrubbing.
func TestRedactSecrets_MoreWebhookAndIndexerShapes(t *testing.T) {
	for _, in := range []string{
		`Post "https://ha.example/api/webhook/S3CRETVALUE": EOF`,
		`https://contoso.webhook.office.com/webhookb2/1111-2222@3333-4444/IncomingWebhook/S3CRETVALUE/5555-6666`,
		`https://outlook.office.com/webhook/1111@2222/IncomingWebhook/S3CRETVALUE/3333`,
		`https://ntfy.sh/S3CRETVALUE`,
		`http://apprise:8000/notify/S3CRETVALUE`,
		`https://indexer.example/getnzb/abc.nzb&i=42&r=S3CRETVALUE`,
		`https://indexer.example/getnzb?id=abc&r=S3CRETVALUE`,
	} {
		if got := RedactSecrets(in); strings.Contains(got, "S3CRETVALUE") {
			t.Errorf("RedactSecrets(%q) = %q, secret survived", in, got)
		}
	}
	if in := "https://tracker.example/browse?r=1&page=2"; RedactSecrets(in) != in {
		t.Errorf("r= outside a getnzb link must be left alone: %q", RedactSecrets(in))
	}
}
