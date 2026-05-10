package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveTorrentSource_PassesThroughMagnet(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:abcdef1234567890abcdef1234567890abcdef12&dn=test"
	got := resolveTorrentSource(context.Background(), magnet)
	if got != magnet {
		t.Fatalf("magnet input mutated: got %q want %q", got, magnet)
	}
}

func TestResolveTorrentSource_PassesThroughNonHTTP(t *testing.T) {
	const ftp = "ftp://example.com/file.torrent"
	got := resolveTorrentSource(context.Background(), ftp)
	if got != ftp {
		t.Fatalf("non-HTTP input mutated: got %q want %q", got, ftp)
	}
}

func TestResolveTorrentSource_PassesThroughTorrentBytes(t *testing.T) {
	// Indexer responds with the .torrent file bytes inline (200 OK) — Bindery
	// must NOT mangle the URL, the download client should handle it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("d8:announce..."))
	}))
	defer srv.Close()

	got := resolveTorrentSource(context.Background(), srv.URL+"/foo.torrent")
	if got != srv.URL+"/foo.torrent" {
		t.Fatalf("200-OK url should be unchanged: got %q", got)
	}
}

func TestResolveTorrentSource_ExtractsMagnetFrom301(t *testing.T) {
	// The bug this whole helper exists to fix: Prowlarr-proxied URL
	// 301-redirects to a magnet: target, BitTorrent clients can't follow.
	const wantMagnet = "magnet:?xt=urn:btih:1234567890abcdef1234567890abcdef12345678&dn=The+Phoenix+Project"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, wantMagnet, http.StatusMovedPermanently)
	}))
	defer srv.Close()

	got := resolveTorrentSource(context.Background(), srv.URL+"/dl?id=42")
	if got != wantMagnet {
		t.Fatalf("expected magnet extracted from 301 Location, got %q", got)
	}
}

func TestResolveTorrentSource_ExtractsMagnetFrom302(t *testing.T) {
	const wantMagnet = "magnet:?xt=urn:btih:fedcba0987654321fedcba0987654321fedcba09"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, wantMagnet, http.StatusFound)
	}))
	defer srv.Close()

	got := resolveTorrentSource(context.Background(), srv.URL+"/dl")
	if got != wantMagnet {
		t.Fatalf("expected magnet extracted from 302 Location, got %q", got)
	}
}

func TestResolveTorrentSource_FollowsHTTPChainToMagnet(t *testing.T) {
	const wantMagnet = "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// Final hop: 301 → magnet
	finalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, wantMagnet, http.StatusMovedPermanently)
	}))
	defer finalSrv.Close()
	// First hop: 301 → finalSrv
	firstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, finalSrv.URL+"/step2", http.StatusMovedPermanently)
	}))
	defer firstSrv.Close()

	got := resolveTorrentSource(context.Background(), firstSrv.URL+"/step1")
	if got != wantMagnet {
		t.Fatalf("expected magnet through 2-hop chain, got %q", got)
	}
}

func TestResolveTorrentSource_BoundedRedirectLoop(t *testing.T) {
	// Server that infinitely redirects to itself; we must not spin forever.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/loop", http.StatusMovedPermanently)
	}))
	defer srv.Close()

	got := resolveTorrentSource(context.Background(), srv.URL+"/loop")
	// After the hop budget is exhausted we fall back to the original URL.
	if !strings.HasPrefix(got, srv.URL) {
		t.Fatalf("infinite-redirect should fall back to original URL, got %q", got)
	}
}

func TestResolveTorrentSource_RedirectToFTPBailsOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://example.com/file.torrent", http.StatusMovedPermanently)
	}))
	defer srv.Close()

	got := resolveTorrentSource(context.Background(), srv.URL+"/dl")
	if got != srv.URL+"/dl" {
		t.Fatalf("non-magnet/non-HTTP redirect should fall back to original URL, got %q", got)
	}
}
