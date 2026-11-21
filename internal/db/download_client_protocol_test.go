package db

import "testing"

func TestClientProtocol_Soulseek(t *testing.T) {
	for typ, want := range map[string]string{
		"slskd": "soulseek", "qbittorrent": "torrent", "rtorrent": "torrent",
		"sabnzbd": "usenet", "nzbget": "usenet",
	} {
		if got := ClientProtocol(typ); got != want {
			t.Errorf("ClientProtocol(%q) = %q, want %q", typ, got, want)
		}
	}
}
