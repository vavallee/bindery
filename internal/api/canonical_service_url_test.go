package api

import "testing"

func TestCanonicalServiceURL(t *testing.T) {
	same := [][2]string{
		{"http://host:8080", "http://host:8080/"},
		{"http://host:8080/", "http://host:8080///"},
		{"http://HOST:8080", "http://host:8080"},
		{"HTTP://host:8080", "http://host:8080"},
		{"http://host", "http://host:80"},
		{"https://host", "https://host:443/"},
		{"http://host", "http://host."},
		{"http://[::1]:8080", "http://[::1]:8080/"},
		{"http://[2001:DB8::1]", "http://[2001:db8::1]:80"},
		{"http://host/abs", "http://host/abs/"},
		{" http://host ", "http://host"},
		{"http://host", "http://host?x=1#frag"},
	}
	for _, c := range same {
		if !sameServiceURL(c[0], c[1]) {
			t.Errorf("%q and %q should be the same server (%q vs %q)", c[0], c[1], canonicalServiceURL(c[0]), canonicalServiceURL(c[1]))
		}
	}
	different := [][2]string{
		{"http://host", "https://host"},
		{"http://host", "http://host:8080"},
		{"https://host", "https://host:80"},
		{"http://host", "http://other"},
		{"http://host/abs", "http://host/other"},
		{"http://host/abs", "http://host"},
		{"http://[::1]:8080", "http://[::2]:8080"},
		{"http://host", ""},
		{"", "http://host"},
	}
	for _, c := range different {
		if sameServiceURL(c[0], c[1]) {
			t.Errorf("%q and %q should be different servers", c[0], c[1])
		}
	}
	if !sameServiceURL("", "  ") {
		t.Error("two empty values should match")
	}
}
