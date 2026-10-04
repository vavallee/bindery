package newznab

import (
	"strings"
	"testing"
)

// Mirrors secretParamCases in internal/httpsec/redact_params_test.go.
var secretParamCases = []string{
	"apikey", "api_key", "jackett_apikey", "key", "token", "access_token",
	"auth", "authkey", "passkey", "torrent_pass", "rsskey", "pass",
	"password", "secret", "sig", "signature", "tp",
	"PassKey", "JACKETT_APIKEY", "Torrent_Pass",
}

// Search, queue and pending responses reach non-admin users, so no credential
// a download URL carries may survive RedactDownloadURL.
func TestRedactDownloadURL_EveryParamName(t *testing.T) {
	for _, name := range secretParamCases {
		t.Run(name, func(t *testing.T) {
			in := "https://jackett.example:9117/dl/tracker/?" + name + "=S3CRETVALUE&path=abc&file=Dune"
			got := RedactDownloadURL(in)
			if strings.Contains(got, "S3CRETVALUE") {
				t.Fatalf("value of %s leaked: %q", name, got)
			}
			if !strings.Contains(got, "path=abc") || !strings.Contains(got, "file=Dune") {
				t.Fatalf("non secret params were damaged: %q", got)
			}
		})
	}
}

func TestRedactAPIKey_EveryParamName(t *testing.T) {
	for _, name := range secretParamCases {
		t.Run(name, func(t *testing.T) {
			got := redactAPIKey("https://indexer.example/api?t=search&" + name + "=S3CRETVALUE&q=Dune")
			if strings.Contains(got, "S3CRETVALUE") {
				t.Fatalf("value of %s leaked: %q", name, got)
			}
			if !strings.Contains(got, "q=Dune") {
				t.Fatalf("non secret params were damaged: %q", got)
			}
		})
	}
}
