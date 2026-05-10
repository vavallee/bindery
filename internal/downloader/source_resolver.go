package downloader

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// resolveTorrentSource normalises an indexer-supplied URL to whatever the
// download client can actually consume.
//
// Public torrent indexers (1337x, TPB, Nyaa, RuTracker, etc.) when proxied
// through Prowlarr's `/{indexerId}/download?...` endpoint commonly respond
// with `HTTP 30x` and `Location: magnet:?xt=...`. BitTorrent clients
// (Transmission, qBittorrent, Deluge) use libcurl-style HTTP fetchers that
// follow HTTP-to-HTTP redirects but reject `magnet:` as a redirect target,
// failing the grab with errors like `Couldn't fetch torrent: Moved
// Permanently (301)`.
//
// Sonarr/Radarr handle this by pre-fetching the URL, following redirects,
// and extracting the magnet from the `Location` header before handing it
// to the download client. This function does the same: walk redirects up
// to a small depth, return the first magnet we encounter, otherwise return
// the original URL untouched (so torrent-file-bytes responses still work).
//
// All errors short-circuit to the original URL — failing soft means a
// transient probe failure can never make a grab worse than the unfixed
// upstream behaviour.
func resolveTorrentSource(ctx context.Context, sourceURL string) string {
	if !strings.HasPrefix(sourceURL, "http://") && !strings.HasPrefix(sourceURL, "https://") {
		return sourceURL
	}

	// Don't follow redirects automatically — we need to inspect the
	// Location header on each hop to spot a `magnet:` target.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}

	current := sourceURL
	for hops := 0; hops < 5; hops++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return sourceURL
		}
		resp, err := client.Do(req)
		if err != nil {
			return sourceURL
		}
		// Drain + close so the connection can be re-used. We never need
		// the body for redirect detection.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			// 2xx (torrent file bytes) or 4xx/5xx (broken). Either way
			// nothing to extract — the download client will handle the
			// status appropriately.
			return sourceURL
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			return sourceURL
		}
		if strings.HasPrefix(loc, "magnet:") {
			slog.Debug("downloader: resolved indexer URL to magnet",
				"original", sourceURL, "hops", hops+1)
			return loc
		}
		// HTTP→HTTP redirect — follow another hop.
		if !strings.HasPrefix(loc, "http://") && !strings.HasPrefix(loc, "https://") {
			// Some other URI scheme we don't recognise (e.g. ftp://);
			// safest to bail and let the download client try the
			// original URL.
			return sourceURL
		}
		current = loc
	}
	return sourceURL
}
