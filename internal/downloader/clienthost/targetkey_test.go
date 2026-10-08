package clienthost

import "testing"

func TestTargetKey(t *testing.T) {
	base := TargetKey("qbittorrent", "qb.lan", 8080, false, "/qbit")
	same := []struct {
		name, typ, host, urlBase string
	}{
		{"host case and type case", "QBittorrent", "QB.LAN", "/qbit"},
		{"legacy scheme and slash on host", "qbittorrent", "http://qb.lan/", "/qbit"},
		{"url base spelling", "qbittorrent", " qb.lan ", "qbit/"},
	}
	for _, c := range same {
		if got := TargetKey(c.typ, c.host, 8080, false, c.urlBase); got != base {
			t.Errorf("%s: %q != %q", c.name, got, base)
		}
	}
	if TargetKey("qbittorrent", "[::1]", 8080, false, "") != TargetKey("qbittorrent", "::1", 8080, false, "") {
		t.Error("bracketed and bare IPv6 literals should match")
	}
	differ := map[string]string{
		"port":     TargetKey("qbittorrent", "qb.lan", 9090, false, "/qbit"),
		"ssl":      TargetKey("qbittorrent", "qb.lan", 8080, true, "/qbit"),
		"url base": TargetKey("qbittorrent", "qb.lan", 8080, false, ""),
		"type":     TargetKey("deluge", "qb.lan", 8080, false, "/qbit"),
		"host":     TargetKey("qbittorrent", "other.lan", 8080, false, "/qbit"),
	}
	for name, got := range differ {
		if got == base {
			t.Errorf("a different %s should not match", name)
		}
	}
}
