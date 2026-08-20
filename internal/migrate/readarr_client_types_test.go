package migrate

import "testing"

// TestDownloadClientTypeFor covers #1983: every Readarr download client that
// was not qBittorrent imported as SABnzbd, so a Transmission or NZBGet install
// arrived with the right host and port and the wrong type, and every grab
// against it failed with nothing in the migration result to say the type had
// been guessed.
//
// The implementation strings are Readarr's own class names.
func TestDownloadClientTypeFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		impl string
		want string
	}{
		{"QBittorrent", "qbittorrent"},
		{"Transmission", "transmission"},
		{"Deluge", "deluge"},
		{"RTorrent", "rtorrent"},
		{"Nzbget", "nzbget"},
		{"Sabnzbd", "sabnzbd"},
		// Case and surrounding whitespace must not matter: the value comes
		// straight out of Readarr's database.
		{"  qBittorrent  ", "qbittorrent"},
		{"TRANSMISSION", "transmission"},
	}
	for _, tc := range cases {
		t.Run(tc.impl, func(t *testing.T) {
			t.Parallel()
			got, ok := downloadClientTypeFor(tc.impl)
			if !ok {
				t.Fatalf("downloadClientTypeFor(%q) reported unsupported, want %q", tc.impl, tc.want)
			}
			if got != tc.want {
				t.Errorf("downloadClientTypeFor(%q) = %q, want %q", tc.impl, got, tc.want)
			}
		})
	}
}

// TestDownloadClientTypeFor_UnsupportedIsReportedNotGuessed is the half that
// actually fixes the bug. Readarr ships clients Bindery does not implement, and
// the old code turned every one of them into a SABnzbd row that looked
// successful and could never work. They must be reported instead.
func TestDownloadClientTypeFor_UnsupportedIsReportedNotGuessed(t *testing.T) {
	t.Parallel()
	unsupported := []string{
		"NzbVortex",
		"TorrentBlackhole",
		"UsenetBlackhole",
		"TorrentDownloadStation",
		"UsenetDownloadStation",
		"Flood",
		"Hadouken",
		"Aria2",
		"Pneumatic",
		"",
		"   ",
	}
	for _, impl := range unsupported {
		got, ok := downloadClientTypeFor(impl)
		if ok {
			t.Errorf("downloadClientTypeFor(%q) = %q, want it reported as unsupported", impl, got)
		}
	}
}

// TestDownloadClientTypeFor_NeedlesDoNotShadowEachOther pins the assumption the
// substring matching rests on. "TorrentBlackhole" must not resolve through the
// "rtorrent" needle, and the two usenet needles must not collide, since both
// contain "nzb".
func TestDownloadClientTypeFor_NeedlesDoNotShadowEachOther(t *testing.T) {
	t.Parallel()
	if got, ok := downloadClientTypeFor("TorrentBlackhole"); ok {
		t.Errorf("TorrentBlackhole resolved to %q, want unsupported: a bare torrent needle would shadow rtorrent", got)
	}
	if got, _ := downloadClientTypeFor("Sabnzbd"); got != "sabnzbd" {
		t.Errorf("Sabnzbd = %q, want sabnzbd", got)
	}
	if got, _ := downloadClientTypeFor("Nzbget"); got != "nzbget" {
		t.Errorf("Nzbget = %q, want nzbget", got)
	}
}
