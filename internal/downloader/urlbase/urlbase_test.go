package urlbase

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"/", ""},
		{"//", ""},
		{"qbit", "/qbit"},
		{"/qbit", "/qbit"},
		{"/qbit/", "/qbit"},
		{"qbit/", "/qbit"},
		{"  /qbit  ", "/qbit"},
		{"/nested/path", "/nested/path"},
		{"nested/path/", "/nested/path"},
		// Paste-a-full-URL form: keep only the path component.
		{"https://proxy.example.com/qbit", "/qbit"},
		{"http://h.example:8080/abc/", "/abc"},
		{"https://host.only/", ""},
		{"https://host.only", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
