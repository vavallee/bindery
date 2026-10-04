package httpsec

import (
	"strings"
	"testing"
)

// Credentials embedded as user:pass@host. The whole userinfo is replaced, so
// neither the password nor a token passed as the user name survives.
var userinfoCases = []struct {
	name, in string
	secrets  []string
	keep     string
}{
	{"user and password", "https://bob:Hunter2Pw@tracker.example/dl/1.torrent", []string{"Hunter2Pw", "bob:"}, "tracker.example/dl/1.torrent"},
	{"token as user name", "https://ghp_TokenValue@feeds.example/rss?id=3", []string{"ghp_TokenValue"}, "feeds.example/rss?id=3"},
	{"empty password", "http://alice:@10.0.0.5:9117/dl/x", []string{"alice"}, "10.0.0.5:9117/dl/x"},
	{"escaped password", "https://bob:p%40ss%3Aword@tracker.example/x", []string{"p%40ss", "word"}, "tracker.example/x"},
}

func TestStripURLSecrets_Userinfo(t *testing.T) {
	for _, tc := range userinfoCases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripURLSecrets(tc.in)
			for _, s := range tc.secrets {
				if strings.Contains(got, s) {
					t.Fatalf("StripURLSecrets(%q) = %q, %q survived", tc.in, got, s)
				}
			}
			if !strings.Contains(got, tc.keep) {
				t.Fatalf("StripURLSecrets(%q) = %q, want %q kept", tc.in, got, tc.keep)
			}
			if again := StripURLSecrets(got); again != got {
				t.Fatalf("not idempotent: %q then %q", got, again)
			}
		})
	}
}

func TestRedactSecrets_Userinfo(t *testing.T) {
	for _, tc := range userinfoCases {
		t.Run(tc.name, func(t *testing.T) {
			in := `Get "` + tc.in + `": EOF`
			got := RedactSecrets(in)
			for _, s := range tc.secrets {
				if strings.Contains(got, s) {
					t.Fatalf("RedactSecrets(%q) = %q, %q survived", in, got, s)
				}
			}
			if !strings.Contains(got, tc.keep) || !strings.HasSuffix(got, `": EOF`) {
				t.Fatalf("RedactSecrets(%q) = %q, want %q and the tail kept", in, got, tc.keep)
			}
			if again := RedactSecrets(got); again != got {
				t.Fatalf("not idempotent: %q then %q", got, again)
			}
		})
	}
}

// An @ outside a URL's authority is not userinfo.
func TestUserinfo_AtOutsideAuthorityUnchanged(t *testing.T) {
	for _, in := range []string{
		"mail bob@example.com about it",
		"https://tracker.example/users/@bob/feed",
		"https://tracker.example/search?q=a@b",
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("RedactSecrets(%q) = %q, want it unchanged", in, got)
		}
		if strings.Contains(in, "://") {
			if got := StripURLSecrets(in); got != in {
				t.Errorf("StripURLSecrets(%q) = %q, want it unchanged", in, got)
			}
		}
	}
}
