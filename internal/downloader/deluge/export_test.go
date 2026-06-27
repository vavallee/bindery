package deluge

import (
	"net/http"
)

// SetHTTPTransport replaces the underlying http.Client transport for testing.
func (c *Client) SetHTTPTransport(rt http.RoundTripper) {
	c.http.Transport = rt
}

// SetValidateTorrentURL injects a custom URL validator for tests, bypassing
// the default SSRF check so httptest.Server loopback addresses are accepted.
// It also drops the dial-time SSRF guard on the fetch transport, which would
// otherwise reject the loopback test server at connect time.
func (c *Client) SetValidateTorrentURL(fn func(string) error) {
	c.validateTorrentURL = fn
	c.fetchTransport = nil
}
