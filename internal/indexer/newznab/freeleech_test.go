package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// freeleechRSS is a torznab feed carrying the downloadvolumefactor attribute
// Jackett emits for private trackers: one freeleech item (0), one normal (1),
// one with the attribute absent, and one with mixed-case spelling.
const freeleechRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <item>
      <title>Freeleech Book</title>
      <guid>guid-free</guid>
      <enclosure url="http://tracker/free.torrent" length="100" type="application/x-bittorrent"/>
      <torznab:attr name="downloadvolumefactor" value="0"/>
      <torznab:attr name="uploadvolumefactor" value="1"/>
    </item>
    <item>
      <title>Normal Book</title>
      <guid>guid-normal</guid>
      <enclosure url="http://tracker/normal.torrent" length="200" type="application/x-bittorrent"/>
      <torznab:attr name="downloadvolumefactor" value="1"/>
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
      <torznab:attr name="downloadVolumeFactor" value="0.5"/>
    </item>
  </channel>
</rss>`

// TestParseResults_DownloadVolumeFactor covers the torznab ratio-cost
// attribute the per-indexer freeleech-only policy keys off. Absent means nil
// ("unreported") rather than 0, so the policy can fail closed instead of
// assuming a release is free.
func TestParseResults_DownloadVolumeFactor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(freeleechRSS))
	}))
	defer srv.Close()

	results, err := testNew(srv.URL, "testkey").Search(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}

	byTitle := map[string]*float64{}
	for _, r := range results {
		byTitle[r.Title] = r.DownloadVolumeFactor
	}

	if got := byTitle["Freeleech Book"]; got == nil || *got != 0 {
		t.Errorf("freeleech item: downloadVolumeFactor = %v, want 0", derefOrNil(got))
	}
	if got := byTitle["Normal Book"]; got == nil || *got != 1 {
		t.Errorf("normal item: downloadVolumeFactor = %v, want 1", derefOrNil(got))
	}
	if got := byTitle["Unreported Book"]; got != nil {
		t.Errorf("absent attribute must stay nil (unreported), got %v", *got)
	}
	// Attribute names are matched case-insensitively: the torznab spec is
	// lowercase but trackers and proxies vary.
	if got := byTitle["Mixed Case Book"]; got == nil || *got != 0.5 {
		t.Errorf("mixed-case attribute: downloadVolumeFactor = %v, want 0.5", derefOrNil(got))
	}
}

func derefOrNil(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}
