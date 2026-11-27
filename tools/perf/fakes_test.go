package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestFakeIndexersAnswerCountAndSwitchToMiss(t *testing.T) {
	f, err := NewFakeIndexers(2, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	start := time.Now()
	_, caps := get(t, f.URL(0)+"/api?t=caps")
	if !strings.Contains(caps, "<caps>") || time.Since(start) < 20*time.Millisecond {
		t.Fatalf("caps must answer after the configured delay, got %q in %s", caps, time.Since(start))
	}
	if q := f.Queries(2); q[0] != 0 || q[1] != 0 {
		t.Fatalf("a capability request is not a search: %v", q)
	}

	_, hit := get(t, f.URL(1)+"/api?t=book&author=Ada+Abbott&title=Salt+%26+Stone")
	if n := strings.Count(hit, "<item>"); n != 30 {
		t.Fatalf("a hit returns 30 releases, got %d", n)
	}
	if !strings.Contains(hit, "Ada Abbott - Salt &amp; Stone RETAIL EPUB-GRP") {
		t.Fatalf("releases are named after the query and escaped: %.300s", hit)
	}
	_, free := get(t, f.URL(1)+"/api?t=search&q=salt+stone")
	if !strings.Contains(free, "<title>salt stone (2021) [EPUB]</title>") {
		t.Fatalf("a free text search names releases after q: %.300s", free)
	}

	f.SetMiss(true)
	_, miss := get(t, f.URL(1)+"/api?t=search&q=anything")
	if strings.Contains(miss, "<item>") || !strings.Contains(miss, "<channel>") {
		t.Fatalf("a miss is an empty feed, got %.200s", miss)
	}
	if q := f.Queries(2); q[0] != 0 || q[1] != 3 {
		t.Fatalf("queries per indexer = %v, want [0 3]", q)
	}
}

func TestFakeQbittorrentReportsFinishedTorrents(t *testing.T) {
	f, err := NewFakeQbittorrent(0, "/downloads", "books")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", f.Port)

	resp, err := http.Post(base+"/api/v2/auth/login", "application/x-www-form-urlencoded", strings.NewReader("username=a&password=b"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(resp.Cookies()) != 1 || resp.Cookies()[0].Name != "SID" {
		t.Fatal("login must set the SID cookie the real client keeps")
	}
	if _, first := f.Stats(); !first.IsZero() {
		t.Fatal("no torrent list has been asked for yet")
	}

	f.SetTorrents([]FakeTorrent{{Hash: "ABC", Name: "A - B", ContentPath: "/downloads/A - B", File: "A - B/A - B.epub", Size: 5000}})
	var list []map[string]any
	_, body := get(t, base+"/api/v2/torrents/info?category=books")
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 1 {
		t.Fatalf("torrent list: %v %s", err, body)
	}
	if list[0]["progress"] != 1.0 || list[0]["amount_left"] != 0.0 || list[0]["content_path"] != "/downloads/A - B" {
		t.Fatalf("a torrent is reported finished with its content path: %v", list[0])
	}
	if _, other := get(t, base+"/api/v2/torrents/info?category=films"); strings.TrimSpace(other) != "[]" {
		t.Fatalf("another category holds nothing, got %s", other)
	}
	if _, all := get(t, base+"/api/v2/torrents/info"); !strings.Contains(all, `"hash":"ABC"`) {
		t.Fatalf("the unfiltered list holds every torrent, got %s", all)
	}

	_, files := get(t, base+"/api/v2/torrents/files?hash=abc")
	if !strings.Contains(files, `"name":"A - B/A - B.epub"`) {
		t.Fatalf("file list is matched without regard to case: %s", files)
	}
	if code, _ := get(t, base+"/api/v2/torrents/files?hash=nope"); code != http.StatusNotFound {
		t.Fatalf("unknown hash = %d, want 404", code)
	}
	for _, p := range []string{"/api/v2/app/version", "/api/v2/app/webapiVersion", "/api/v2/app/preferences", "/api/v2/torrents/categories", "/api/v2/torrents/delete"} {
		if code, _ := get(t, base+p); code != http.StatusOK {
			t.Fatalf("%s = %d", p, code)
		}
	}
	requests, first := f.Stats()
	if requests != 11 || first.IsZero() {
		t.Fatalf("requests=%d first list=%v", requests, first)
	}
}

func TestOutboundTrapRefusesAndCounts(t *testing.T) {
	trap, err := NewOutboundTrap()
	if err != nil {
		t.Fatal(err)
	}
	defer trap.Close()
	code, _ := get(t, fmt.Sprintf("http://127.0.0.1:%d/anything", trap.Port))
	if code != http.StatusBadGateway || trap.Blocked() != 1 {
		t.Fatalf("status=%d blocked=%d, want 502 and 1", code, trap.Blocked())
	}
}
