package newznab

import "testing"

func TestRedactDownloadURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"strips apikey", "https://idx.example.com/dl?id=abc&apikey=SECRET", "https://idx.example.com/dl?id=abc"},
		{"no apikey untouched", "https://idx.example.com/dl?id=abc", "https://idx.example.com/dl?id=abc"},
		{"only apikey", "https://idx.example.com/dl?apikey=SECRET", "https://idx.example.com/dl"},
		{"empty", "", ""},
		{"soulseek address is not echoed", "slskd://eyJ1IjoicGVlciJ9", "slskd://result"},
	}
	for _, tt := range tests {
		if got := RedactDownloadURL(tt.in); got != tt.want {
			t.Errorf("%s: RedactDownloadURL(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestSignDownloadURLFor(t *testing.T) {
	const indexerURL = "https://idx.example.com/api"

	// Same-host URL with no key gets the indexer apikey.
	got := SignDownloadURLFor("https://idx.example.com/dl?id=abc", indexerURL, "SECRET")
	if got != "https://idx.example.com/dl?apikey=SECRET&id=abc" {
		t.Errorf("same-host sign: got %q", got)
	}

	// A URL that already carries an apikey is left alone (idempotent — covers the
	// scheduler/retry paths that hand back an already-signed URL).
	already := "https://idx.example.com/dl?apikey=OTHER&id=abc"
	if got := SignDownloadURLFor(already, indexerURL, "SECRET"); got != already {
		t.Errorf("already-signed should be unchanged: got %q", got)
	}

	// A URL pointing at a different host (a direct-from-uploader link) never gets
	// the indexer key appended.
	foreign := "https://cdn.other.net/file.nzb?id=abc"
	if got := SignDownloadURLFor(foreign, indexerURL, "SECRET"); got != foreign {
		t.Errorf("foreign host must not be signed: got %q", got)
	}

	// Empty apikey is a no-op.
	if got := SignDownloadURLFor("https://idx.example.com/dl?id=abc", indexerURL, ""); got != "https://idx.example.com/dl?id=abc" {
		t.Errorf("empty apikey should be a no-op: got %q", got)
	}
}

// The client redacts a signed URL for the response, and the grab handler
// restores exactly the original signed URL — the round trip a real grab makes.
func TestRedactThenReSign_RoundTrips(t *testing.T) {
	const indexerURL = "https://idx.example.com/api"
	signed := signDownloadURL("https://idx.example.com/dl?id=abc", "idx.example.com", "SECRET")
	if signed == "" {
		t.Fatal("precondition: signed URL empty")
	}
	redacted := RedactDownloadURL(signed)
	if redacted == signed {
		t.Fatal("redaction did not remove the apikey")
	}
	if resigned := SignDownloadURLFor(redacted, indexerURL, "SECRET"); resigned != signed {
		t.Errorf("round trip mismatch: signed=%q resigned=%q", signed, resigned)
	}
}
