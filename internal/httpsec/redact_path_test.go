package httpsec

import (
	"strings"
	"testing"
)

// pathSecretCases are download links, GUIDs and detail links that carry a
// tracker passkey, RSS key or download token in the URL path rather than the
// query. secret is the value that must not survive; keep is text around it
// that must.
var pathSecretCases = []struct {
	name, in, secret string
	keep             []string
}{
	{
		name:   "hex passkey segment before the file name",
		in:     "https://tracker.example/download/123/{HEX32}/file.torrent",
		secret: "{HEX32}",
		keep:   []string{"https://tracker.example/download/123/", "/file.torrent"},
	},
	{
		name:   "RSS key as the last segment",
		in:     "https://tracker.example/rss/download/12345/{B62_24}",
		secret: "{B62_24}",
		keep:   []string{"https://tracker.example/rss/download/12345/"},
	},
	{
		name:   "UNIT3D dotted hex suffix",
		in:     "https://tracker.example/torrent/download/12345.{HEX32B}",
		secret: "{HEX32B}",
		keep:   []string{"https://tracker.example/torrent/download/12345."},
	},
	{
		name:   "UNIT3D dotted base62 RSS key",
		in:     "https://tracker.example/torrent/download/12345.{B62_32}",
		secret: "{B62_32}",
		keep:   []string{"/torrent/download/12345."},
	},
	{
		name:   "UNIT3D RSS feed",
		in:     "https://tracker.example/rss/7.{B62_32}",
		secret: "{B62_32}",
		keep:   []string{"/rss/7."},
	},
	{
		name:   "20 character hex RSS key between id and release name",
		in:     "https://www.tracker.example/rss/download/12345/{HEX20}/Book.Title.2020.EPUB.torrent",
		secret: "{HEX20}",
		keep:   []string{"/rss/download/12345/", "/Book.Title.2020.EPUB.torrent"},
	},
	{
		name:   "base64url download token with dash and underscore",
		in:     "https://tracker.example/tor/download.php/{B64URL}",
		secret: "{B64URL}",
		keep:   []string{"/tor/download.php/"},
	},
	{
		name:   "upper case hex with an extension",
		in:     "https://tracker.example/DL/{HEX32U}.torrent",
		secret: "{HEX32U}",
		keep:   []string{"/DL/", ".torrent"},
	},
	{
		name:   "info hash",
		in:     "https://tracker.example/torrent/{HASH40A}",
		secret: "{HASH40A}",
		keep:   []string{"/torrent/"},
	},
	{
		name:   "percent encoded passkey",
		in:     "https://tracker.example/dl/123/%30{HEX32TAIL}/f.torrent",
		secret: "{HEX32TAIL}",
		keep:   []string{"/dl/123/", "/f.torrent"},
	},
	{
		name:   "named path parameter with a short value",
		in:     "https://tracker.example/rss/passkey=hunter2/feed.xml",
		secret: "hunter2",
		keep:   []string{"/rss/passkey=", "/feed.xml"},
	},
	{
		name:   "matrix parameter",
		in:     "https://tracker.example/rss;torrent_pass=hunter2;cat=7",
		secret: "hunter2",
		keep:   []string{";cat=7"},
	},
	{
		name:   "token next to a Unicode file name",
		in:     "https://tracker.example/dl/{HEX32}/%E4%B8%AD%E6%96%87.torrent",
		secret: "{HEX32}",
		keep:   []string{"/dl/", "/%E4%B8%AD%E6%96%87.torrent"},
	},
	{
		name:   "query secrets and a path secret together",
		in:     "https://tracker.example/download/9/{HEX32}?passkey=QUERYSECRET&id=9",
		secret: "{HEX32}",
		keep:   []string{"/download/9/", "id=9"},
	},
}

// Ordinary path parts: ids, words, slugs, release and file names, Unicode and
// percent encoded names, newznab release GUIDs after /details/ and /getnzb/,
// and the paths of the API URLs Bindery talks to. None of these may change.
var pathKeepCases = []string{
	"https://tracker.example/download/12345/Book.Title.2020.EPUB.torrent",
	"https://tracker.example/download/12345.torrent",
	"https://tracker.example/torrents/1234567890123456789",
	"https://tracker.example/download/Some_Book_Title_2020_EPUB_x264v2.torrent",
	"https://tracker.example/download/Brandon.Sanderson.The.Way.of.Kings.2010.RETAIL.EPUB-GROUP.torrent",
	"https://tracker.example/download/internationalization/abc",
	"https://tracker.example/deadbeef12/file.torrent",
	"https://tracker.example/download/ThisIsAVeryLongCamelCaseName/x",
	"https://hardcover.app/books/the-name-of-the-wind-2007-anniversary-edition",
	"https://openlibrary.org/works/OL12345W.json",
	"https://www.googleapis.com/books/v1/volumes/zyTCAlFPjgYC",
	"https://tracker.example/download/%E4%B8%AD%E6%96%87%E6%9B%B8%E7%B1%8D%E5%90%8D%E7%A8%B1%E4%B8%80%E4%BA%8C.torrent",
	"https://tracker.example/download/中文書籍名稱一二三四五六七八九十.torrent",
	"https://tracker.example/download/Книга.Название.2020.epub.torrent",
	"https://tracker.example/download/%D0%9A%D0%BD%D0%B8%D0%B3%D0%B0.epub",
	"https://indexer.example/details/{HEX40}",
	"https://indexer.example/getnzb/{HEX40}.nzb",
	"http://jackett:9117/api/v2.0/indexers/all/results/torznab/api?t=search&q=dune",
	"http://jackett:9117/dl/mytracker/?path=Q2ZESjhQ&file=Dune",
	"http://prowlarr:9696/3/download?file=Lee+Child&link=abc",
	"https://tracker.example/details.php?id=4",
	// Release name slugs on public trackers and archives: words, years and
	// short tags, never four or more letter and digit alternations in a piece.
	"https://1337x.to/torrent/12345/Brandon-Sanderson-Wind-and-Truth-Stormlight4-EPUB/",
	"https://torrentgalaxy.to/torrent/12345/Dune-Messiah-1969-Audiobook-MP3-128kbps",
	"https://tracker.example/torrent/12345/Stormlight_Archive_Book1_WayOfKings_Retail2010",
	"https://tracker.example/torrent/12345/HarryPotter1PhilosophersStone",
	"https://archive.org/download/wayofkings0000sand_x1y2/wayofkings0000sand_x1y2.pdf",
	// The query is not the path: an unknown parameter keeps its value.
	"https://tracker.example/browse?hash={HEX32}",
}

// The tables name their fake tokens by marker; see fx.
func init() {
	for i := range pathSecretCases {
		c := &pathSecretCases[i]
		c.in, c.secret = fx(c.in), fx(c.secret)
		for j := range c.keep {
			c.keep[j] = fx(c.keep[j])
		}
	}
	for i := range pathKeepCases {
		pathKeepCases[i] = fx(pathKeepCases[i])
	}
}

func TestStripURLSecrets_PathSecrets(t *testing.T) {
	for _, tc := range pathSecretCases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripURLSecrets(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("StripURLSecrets(%q) = %q, path secret survived", tc.in, got)
			}
			for _, k := range tc.keep {
				if !strings.Contains(got, k) {
					t.Fatalf("StripURLSecrets(%q) = %q, want %q kept", tc.in, got, k)
				}
			}
			if again := StripURLSecrets(got); again != got {
				t.Fatalf("not idempotent: %q then %q", got, again)
			}
		})
	}
}

func TestRedactSecrets_PathSecrets(t *testing.T) {
	for _, tc := range pathSecretCases {
		t.Run(tc.name, func(t *testing.T) {
			in := `Get "` + tc.in + `": dial tcp 10.0.0.1:443: i/o timeout`
			got := RedactSecrets(in)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("RedactSecrets(%q) = %q, path secret survived", in, got)
			}
			if !strings.HasPrefix(got, `Get "https://tracker.example/`) && !strings.HasPrefix(got, `Get "https://www.tracker.example/`) {
				t.Fatalf("RedactSecrets damaged the URL start: %q", got)
			}
			if !strings.HasSuffix(got, `": dial tcp 10.0.0.1:443: i/o timeout`) {
				t.Fatalf("RedactSecrets damaged the text after the URL: %q", got)
			}
			for _, k := range tc.keep {
				if strings.HasPrefix(k, "https://") {
					continue
				}
				if !strings.Contains(got, k) {
					t.Fatalf("RedactSecrets(%q) = %q, want %q kept", in, got, k)
				}
			}
		})
	}
}

func TestStripURLSecrets_OrdinaryPathsUnchanged(t *testing.T) {
	for _, in := range pathKeepCases {
		if got := StripURLSecrets(in); got != in {
			t.Errorf("StripURLSecrets(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestRedactSecrets_OrdinaryPathsUnchanged(t *testing.T) {
	for _, in := range pathKeepCases {
		line := "fetch failed for " + in + " after 3 tries"
		if got := RedactSecrets(line); got != line {
			t.Errorf("RedactSecrets(%q) = %q, want it unchanged", line, got)
		}
	}
	// Text outside a URL is not a path: a bare hex value in a log line, such
	// as a newznab GUID or an info hash, stays readable.
	for _, in := range []string{
		"grabbed {HEX32} from indexer 3",
		"hash={HASH40A} state=seeding",
	} {
		in = fx(in)
		if got := RedactSecrets(in); got != in {
			t.Errorf("RedactSecrets(%q) = %q, want it unchanged", in, got)
		}
	}
}

// A GUID that is not a URL (newznab GUIDs are bare hex) is not a path and is
// left alone, so the registry key and the response GUID stay the same value.
func TestStripURLSecrets_BareGUIDUnchanged(t *testing.T) {
	for _, in := range []string{
		"{HEX40}",
		"{B62_32}",
		"guid-sec-1",
	} {
		in = fx(in)
		if got := StripURLSecrets(in); got != in {
			t.Errorf("StripURLSecrets(%q) = %q, want it unchanged", in, got)
		}
	}
}

// The registry and the web UI tell results apart by the redacted GUID. Two
// GUIDs that differ only in a redacted segment must still differ after
// redaction, or a grab would resolve to the wrong release.
func TestStripURLSecrets_PathRedactionKeepsGUIDsDistinct(t *testing.T) {
	a := StripURLSecrets(fx("https://tracker.example/torrent/{HASH40A}"))
	b := StripURLSecrets(fx("https://tracker.example/torrent/{HASH40B}"))
	if a == b {
		t.Fatalf("two different GUIDs redacted to the same value %q", a)
	}
	// The same secret redacts to the same placeholder, so the raw and the
	// redacted form of one GUID agree.
	raw := fx("https://tracker.example/rss/download/1/{B62_24}")
	if StripURLSecrets(raw) != StripURLSecrets(StripURLSecrets(raw)) {
		t.Fatal("raw and redacted forms of one GUID redact differently")
	}
}
