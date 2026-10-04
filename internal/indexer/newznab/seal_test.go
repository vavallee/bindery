package newznab

import (
	"errors"
	"strings"
	"testing"
)

// A search result's download URL goes to the client redacted and comes back
// on the grab. Whatever RedactDownloadURL sealed must open to exactly the URL
// the indexer produced, or Jackett and private tracker grabs break.
func TestRedactThenUnseal_RoundTripsEveryParamName(t *testing.T) {
	for _, name := range secretParamCases {
		if name == "apikey" {
			continue // dropped and re-signed, covered by TestRedactThenReSign_RoundTrips
		}
		t.Run(name, func(t *testing.T) {
			orig := "https://jackett.example:9117/dl/tracker/?" + name + "=S3CRET%2BVALUE&path=a%20b&file=Dune+Messiah"
			redacted := RedactDownloadURL(orig)
			if strings.Contains(redacted, "S3CRET") {
				t.Fatalf("value leaked: %q", redacted)
			}
			got, err := UnsealDownloadURL(redacted)
			if err != nil {
				t.Fatalf("unseal: %v", err)
			}
			if got != orig {
				t.Fatalf("round trip changed the URL:\n orig %q\n got  %q", orig, got)
			}
		})
	}
}

func TestRedactDownloadURL_IsStableAndIdempotent(t *testing.T) {
	orig := "https://tracker.example/download.php?id=9&passkey=S3CRET"
	a, b := RedactDownloadURL(orig), RedactDownloadURL(orig)
	if a != b {
		t.Fatalf("same URL redacted differently: %q vs %q", a, b)
	}
	if again := RedactDownloadURL(a); again != a {
		t.Fatalf("redacting a redacted URL changed it: %q -> %q", a, again)
	}
}

// A sealed value only opens for the host and parameter it was sealed for, so
// a client cannot move a tracker passkey onto a URL of its choosing.
func TestUnsealDownloadURL_BoundToHostAndName(t *testing.T) {
	redacted := RedactDownloadURL("https://tracker.example/download.php?id=9&passkey=S3CRET")
	_, sealed, ok := strings.Cut(redacted, "passkey=")
	if !ok {
		t.Fatalf("no sealed passkey in %q", redacted)
	}
	// Change one character mid-blob; the last character can carry only
	// padding bits, which the decoder ignores.
	mid := len(sealed) / 2
	flipped := "A"
	if sealed[mid] == 'A' {
		flipped = "B"
	}
	tampered := sealed[:mid] + flipped + sealed[mid+1:]
	for name, moved := range map[string]string{
		"other host": "https://attacker.example/download.php?id=9&passkey=" + sealed,
		"other port": "https://tracker.example:8443/download.php?id=9&passkey=" + sealed,
		"other name": "https://tracker.example/download.php?id=9&authkey=" + sealed,
		"tampered":   "https://tracker.example/download.php?id=9&passkey=" + tampered,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := UnsealDownloadURL(moved)
			if !errors.Is(err, ErrSealExpired) {
				t.Fatalf("want ErrSealExpired, got %q, %v", got, err)
			}
		})
	}
}

func TestUnsealDownloadURL_PlainURLUnchanged(t *testing.T) {
	for _, in := range []string{
		"",
		"https://idx.example.com/dl?id=abc&apikey=SECRET",
		"magnet:?xt=urn:btih:abc&dn=Dune",
	} {
		got, err := UnsealDownloadURL(in)
		if err != nil || got != in {
			t.Errorf("UnsealDownloadURL(%q) = %q, %v; want it unchanged", in, got, err)
		}
	}
}

// The indexer apikey is still dropped, not sealed, so HasAPIKey and the
// server-side re-sign keep working exactly as before.
func TestRedactDownloadURL_DropsIndexerAPIKey(t *testing.T) {
	got := RedactDownloadURL("https://prowlarr.example/1/download?apikey=SECRET&link=abc&file=Dune")
	if got != "https://prowlarr.example/1/download?link=abc&file=Dune" {
		t.Fatalf("got %q", got)
	}
	if HasAPIKey(got) {
		t.Fatal("redacted URL still reports an apikey")
	}
}
