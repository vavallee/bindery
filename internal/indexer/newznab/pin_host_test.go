package newznab

import "testing"

// DownloadHostMatchesIndexer decides which indexers a release could have come
// from for the purpose of a download client pin. It fails closed: a spurious
// match is a refused grab, a spurious miss would put a private tracker's
// release on the public client.
func TestDownloadHostMatchesIndexer(t *testing.T) {
	cases := []struct {
		name, release, indexer string
		want                   bool
	}{
		{"same host", "http://tracker.example/dl/1", "http://tracker.example/api", true},
		{"case differs", "http://Tracker.EXAMPLE/dl/1", "http://tracker.example/api", true},
		{"trailing dot", "http://tracker.example./dl/1", "http://tracker.example/api", true},
		{"trailing dot on the indexer", "http://tracker.example/dl/1", "http://tracker.example./api", true},
		{"explicit port against none (the Prowlarr layout)", "http://prowlarr/dl/1", "http://prowlarr:9696/1/api", true},
		{"none against explicit port", "http://prowlarr:9696/dl/1", "http://prowlarr/1/api", true},
		{"default https port against none", "https://tracker.example:443/dl/1", "https://tracker.example/api", true},
		{"default http port against none", "http://tracker.example:80/dl/1", "http://tracker.example/api", true},
		{"different explicit ports", "http://tracker.example:8080/dl/1", "http://tracker.example:9090/api", true},
		{"ipv4 with ports", "http://10.0.0.5:9117/dl/1", "http://10.0.0.5:9696/api", true},
		{"ipv6", "http://[::1]:9117/dl/1", "http://[::1]/api", true},
		{"different host", "http://other.example/dl/1", "http://tracker.example/api", false},
		{"www is a different host, a documented limit", "http://www.tracker.example/dl/1", "http://tracker.example/api", false},
		{"subdomain is a different host, a documented limit", "http://dl.tracker.example/dl/1", "http://tracker.example/api", false},
		{"empty release url", "", "http://tracker.example/api", false},
		{"unparseable release url", "http://[bad", "http://tracker.example/api", false},
		{"indexer url with no host", "http://tracker.example/dl/1", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DownloadHostMatchesIndexer(tc.release, tc.indexer); got != tc.want {
				t.Errorf("DownloadHostMatchesIndexer(%q, %q) = %v, want %v", tc.release, tc.indexer, got, tc.want)
			}
		})
	}
}
