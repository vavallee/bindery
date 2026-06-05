package newznab

import (
	"encoding/xml"
	"testing"
)

// FuzzParseResults runs the untrusted-indexer-response path over arbitrary
// bytes: the Newznab/Torznab error sniffer and the RSS→SearchResult decoder.
// Indexer responses are remote and attacker-influenceable (a hostile or buggy
// indexer), so neither parser may panic on any input. New() does not dial and
// signDownloadURL only builds strings, so the target is hermetic.
func FuzzParseResults(f *testing.F) {
	seeds := []string{
		"",
		`<?xml version="1.0"?><error code="100" description="Incorrect user credentials"/>`,
		`<rss><channel><item><title>A Book</title><enclosure url="http://x/y.nzb" length="123"/>` +
			`<newznab:attr name="size" value="999"/></item></channel></rss>`,
		`<rss><channel></channel></rss>`,
		`not xml at all`,
		`<rss><channel><item><enclosure length="notanumber"/></item></channel></rss>`,
		`<error`,
		"\x00\x01\x02",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	c := New("http://indexer.invalid", "apikey")
	f.Fuzz(func(t *testing.T, body []byte) {
		// 1) Error sniffer must never panic.
		_ = parseNewznabError(body)

		// 2) RSS decode + result mapping must never panic. A decode error is a
		// legitimate, handled outcome (non-RSS body); only the mapping of a
		// successfully-decoded feed is under test.
		var resp rssResponse
		if err := xml.Unmarshal(body, &resp); err != nil {
			return
		}
		_ = c.parseResults(resp.Channel.Items)
	})
}
