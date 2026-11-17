package bookorbit

import "testing"

func TestBookURL(t *testing.T) {
	cases := []struct {
		name, public, server, id, want string
	}{
		{"public wins", "https://books.example.com", "http://bookorbit:3000", "7", "https://books.example.com/book/7"},
		{"empty public falls back to server", "", "http://bookorbit:3000", "7", "http://bookorbit:3000/book/7"},
		{"blank public falls back", "   ", "http://bookorbit:3000", "7", "http://bookorbit:3000/book/7"},
		{"trailing slashes", "https://books.example.com///", "", "7", "https://books.example.com/book/7"},
		{"path prefix kept", "https://example.com/bookorbit/", "", "7", "https://example.com/bookorbit/book/7"},
		{"server api suffix dropped", "", "http://bookorbit:3000/api/v1", "7", "http://bookorbit:3000/book/7"},
		{"query and fragment dropped", "https://b.example.com/?x=1#y", "", "7", "https://b.example.com/book/7"},
		{"javascript scheme", "javascript:alert(1)", "http://bookorbit:3000", "7", ""},
		{"data scheme", "data:text/html,hi", "", "7", ""},
		{"file scheme", "file:///etc/passwd", "", "7", ""},
		{"ftp scheme on server", "", "ftp://bookorbit", "7", ""},
		{"bad public does not fall back", "not a url", "http://bookorbit:3000", "7", ""},
		{"credentials refused", "https://u:p@books.example.com", "", "7", ""},
		{"no host", "https://", "", "7", ""},
		{"both empty", "", "", "7", ""},
		{"non numeric id", "https://b.example.com", "", "../x", ""},
		{"zero id", "https://b.example.com", "", "0", ""},
		{"empty id", "https://b.example.com", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BookURL(c.public, c.server, c.id); got != c.want {
				t.Errorf("BookURL(%q, %q, %q) = %q, want %q", c.public, c.server, c.id, got, c.want)
			}
		})
	}
}

func TestNormalizePublicURL(t *testing.T) {
	if got, err := NormalizePublicURL(""); err != nil || got != "" {
		t.Errorf("empty = %q, %v", got, err)
	}
	if got, err := NormalizePublicURL(" https://b.example.com/x// "); err != nil || got != "https://b.example.com/x" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "file:///x", "b.example.com", "//b.example.com"} {
		if _, err := NormalizePublicURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
