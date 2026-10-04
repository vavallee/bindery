package newznab

import (
	"strings"
	"testing"
)

// Fake ids and keys, built at run time so no high entropy literal sits in the
// source for a secret scanner to flag.
var (
	fakeHex32  = strings.Repeat("0a1b", 8)  // 32 hex: an MD5 id or a passkey
	fakeHash40 = strings.Repeat("c12f", 10) // 40 hex: an info hash
	fakeB62    = strings.Repeat("aB3d", 8)  // 32 base62: a token shaped id
)

// A detail page is a link people click. Its path is the indexer's own id for
// the release, often hex (an MD5, an info hash), so only credential
// parameters and user:pass@ come off it; the path is kept.
func TestDetailURL_KeepsIDsInDetailPaths(t *testing.T) {
	for _, link := range []string{
		"https://annas-archive.org/md5/" + fakeHex32,
		"https://bt4g.com/magnet/" + fakeHash40,
		"https://therarbg.com/post-detail/" + fakeHex32[:16] + "/dune-messiah/",
		"https://nzbking.com/details:" + fakeHex32[:24] + "/",
		"https://1337x.to/torrent/12345/Brandon-Sanderson-Wind-and-Truth-Stormlight4-EPUB/",
		"https://tracker.example/torrents/" + fakeB62,
	} {
		item := rssItem{
			Comments:  link,
			Enclosure: rssEnclosure{URL: "https://tracker.example/download/1/" + fakeHex32 + "/x.torrent"},
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

// <link> is a download fallback in torznab feeds even when an enclosure is
// present: a proxy enclosure can sit next to the tracker's direct passkey
// link. With no comments link and no permalink GUID, detailURL falls back to
// <link>, so that fallback always gets the full path rule.
func TestDetailURL_LinkFallbackWithEnclosureRedactsPathSecret(t *testing.T) {
	item := rssItem{
		GUID:      rssGUID{IsPermaLink: "false", Value: "opaque"},
		Link:      "https://tracker.example/rss/download/12345/" + fakeHex32 + "/Dune.torrent",
		Enclosure: rssEnclosure{URL: "http://prowlarr:9696/3/download?apikey=K&link=abc"},
	}
	got := detailURL(item)
	if strings.Contains(got, fakeHex32) || !strings.HasSuffix(got, "/Dune.torrent") {
		t.Fatalf("detailURL = %q, want the path passkey redacted and the rest kept", got)
	}
}

// With no enclosure the item's link is the download link itself, and the
// detail URL falls back to it, so the path rule applies there.
func TestDetailURL_RedactsPathSecretWhenItIsTheDownloadLink(t *testing.T) {
	item := rssItem{
		GUID: rssGUID{IsPermaLink: "false", Value: "opaque"},
		Link: "https://tracker.example/rss/download/12345/" + fakeHex32 + "/Dune.torrent",
	}
	got := detailURL(item)
	if strings.Contains(got, fakeHex32) || !strings.HasSuffix(got, "/Dune.torrent") {
		t.Fatalf("detailURL = %q, want the path passkey redacted and the rest kept", got)
	}
}
