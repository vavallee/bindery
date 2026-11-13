package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// swarmRSS is a torznab feed carrying the seeders and peers attributes Jackett
// and Prowlarr emit: a seeded item, a dead one (a reported 0), one with the
// attributes absent, one with mixed-case names, and one with values that are
// not counts.
const swarmRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <item>
      <title>Seeded Book</title>
      <guid>guid-seeded</guid>
      <enclosure url="http://tracker/seeded.torrent" length="100" type="application/x-bittorrent"/>
      <torznab:attr name="grabs" value="40"/>
      <torznab:attr name="seeders" value="15"/>
      <torznab:attr name="peers" value="18"/>
    </item>
    <item>
      <title>Dead Book</title>
      <guid>guid-dead</guid>
      <enclosure url="http://tracker/dead.torrent" length="200" type="application/x-bittorrent"/>
      <torznab:attr name="grabs" value="900"/>
      <torznab:attr name="seeders" value="0"/>
      <torznab:attr name="peers" value="0"/>
    </item>
    <item>
      <title>Unreported Book</title>
      <guid>guid-unreported</guid>
      <enclosure url="http://tracker/unreported.torrent" length="300" type="application/x-bittorrent"/>
    </item>
    <item>
      <title>Mixed Case Book</title>
      <guid>guid-mixed</guid>
      <enclosure url="http://tracker/mixed.torrent" length="400" type="application/x-bittorrent"/>
      <torznab:attr name="Seeders" value=" 7 "/>
      <torznab:attr name="PEERS" value="9"/>
    </item>
    <item>
      <title>Malformed Book</title>
      <guid>guid-malformed</guid>
      <enclosure url="http://tracker/malformed.torrent" length="500" type="application/x-bittorrent"/>
      <torznab:attr name="seeders" value="many"/>
      <torznab:attr name="peers" value="-1"/>
    </item>
  </channel>
</rss>`

// TestParseResults_SeedersAndPeers covers the torznab swarm attributes shown
// on interactive search rows (#3819). A reported 0 must stay distinguishable
// from an unreported count, since one is a dead torrent and the other is an
// indexer that does not say.
func TestParseResults_SeedersAndPeers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(swarmRSS))
	}))
	defer srv.Close()

	results, err := testNew(srv.URL, "testkey").Search(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	byTitle := map[string]SearchResult{}
	for _, r := range results {
		byTitle[r.Title] = r
	}

	cases := []struct {
		title          string
		seeders, peers any
	}{
		{"Seeded Book", 15, 18},
		{"Dead Book", 0, 0},
		{"Unreported Book", nil, nil},
		// Attribute names are matched case-insensitively and values are
		// trimmed, like the other torznab attributes.
		{"Mixed Case Book", 7, 9},
		// A value that is not a count is unreported, not 0.
		{"Malformed Book", nil, nil},
	}
	for _, tc := range cases {
		r := byTitle[tc.title]
		if got := intOrNil(r.Seeders); got != tc.seeders {
			t.Errorf("%s: seeders = %v, want %v", tc.title, got, tc.seeders)
		}
		if got := intOrNil(r.Peers); got != tc.peers {
			t.Errorf("%s: peers = %v, want %v", tc.title, got, tc.peers)
		}
	}

	// Grabs is still parsed alongside, and a dead torrent's high lifetime
	// count is exactly the case seeders exist to expose.
	if got := byTitle["Dead Book"].Grabs; got != 900 {
		t.Errorf("Dead Book: grabs = %d, want 900", got)
	}
}

func intOrNil(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}
