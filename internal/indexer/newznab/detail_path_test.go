package newznab

import (
	"strings"
	"testing"
)

// A detail page is a link people click. Its path is the indexer's own id for
// the release, often hex (an MD5, an info hash), so only credential
// parameters and user:pass@ come off it; the path is kept.
func TestDetailURL_KeepsIDsInDetailPaths(t *testing.T) {
	for _, link := range []string{
		"https://annas-archive.org/md5/0a1b2c3d4e5f60718293a4b5c6d7e8f9",
		"https://bt4g.com/magnet/c12fe1c06bba254a9dc9f519b335aa7c1367a88a",
		"https://therarbg.com/post-detail/0a1b2c3d4e5f6071/dune-messiah/",
		"https://nzbking.com/details:0a1b2c3d4e5f60718293a4b5/",
		"https://1337x.to/torrent/12345/Brandon-Sanderson-Wind-and-Truth-Stormlight4-EPUB/",
		"https://tracker.example/torrents/aB3dE5fG7hJ9kL2mN4pQ6rS8tU0vW1xY",
	} {
		item := rssItem{
			Comments:  link,
			Enclosure: rssEnclosure{URL: "https://tracker.example/download/1/0a1b2c3d4e5f60718293a4b5c6d7e8f9/x.torrent"},
		}
		if got := detailURL(item); got != link {
			t.Errorf("detailURL(comments %q) = %q, want it unchanged", link, got)
		}
	}
	item := rssItem{
		Comments:  "https://bob:SECRETPW@tracker.example/details.php?id=4&passkey=SECRETPK",
		Enclosure: rssEnclosure{URL: "https://tracker.example/download.php?id=4"},
	}
	if got := detailURL(item); strings.Contains(got, "SECRETP") || !strings.Contains(got, "details.php?id=4") {
		t.Errorf("detailURL = %q, want credentials gone and the page kept", got)
	}
}

// With no enclosure the item's link is the download link itself, and the
// detail URL falls back to it, so the path rule applies there.
func TestDetailURL_RedactsPathSecretWhenItIsTheDownloadLink(t *testing.T) {
	item := rssItem{
		GUID: rssGUID{IsPermaLink: "false", Value: "opaque"},
		Link: "https://tracker.example/rss/download/12345/0a1b2c3d4e5f60718293a4b5c6d7e8f9/Dune.torrent",
	}
	got := detailURL(item)
	if strings.Contains(got, "0a1b2c3d4e5f60718293a4b5c6d7e8f9") || !strings.HasSuffix(got, "/Dune.torrent") {
		t.Fatalf("detailURL = %q, want the path passkey redacted and the rest kept", got)
	}
}
