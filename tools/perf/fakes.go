package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// listenLoopback starts handler on a free loopback port and returns the
// server and its port. Every fake binds 127.0.0.1 only.
func listenLoopback(handler http.Handler) (*http.Server, int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // a listener for the life of the run, closed through the server
	if err != nil {
		return nil, 0, err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	return srv, l.Addr().(*net.TCPAddr).Port, nil
}

// FakeIndexers is a set of Torznab servers, one port each, that answer every
// search after a fixed delay. In hit mode a search returns releases named
// after the query; in miss mode it returns an empty feed, which is what sends
// Bindery down its fallback query variants.
type FakeIndexers struct {
	latency time.Duration
	miss    atomic.Bool
	servers []*http.Server
	ports   []int
	queries []atomic.Int64
}

// NewFakeIndexers starts n indexers.
func NewFakeIndexers(n int, latency time.Duration) (*FakeIndexers, error) {
	f := &FakeIndexers{latency: latency, queries: make([]atomic.Int64, n)}
	for i := range n {
		srv, port, err := listenLoopback(f.handler(i))
		if err != nil {
			f.Close()
			return nil, err
		}
		f.servers = append(f.servers, srv)
		f.ports = append(f.ports, port)
	}
	return f, nil
}

// URL is the base URL of indexer i.
func (f *FakeIndexers) URL(i int) string { return fmt.Sprintf("http://127.0.0.1:%d", f.ports[i]) }

// SetMiss switches every indexer between empty feeds and populated ones.
func (f *FakeIndexers) SetMiss(miss bool) { f.miss.Store(miss) }

// Queries returns the search requests each of the first n indexers has
// served. Capability requests are not counted.
func (f *FakeIndexers) Queries(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = f.queries[i].Load()
	}
	return out
}

// Close stops every indexer.
func (f *FakeIndexers) Close() {
	for _, s := range f.servers {
		_ = s.Close()
	}
}

const capsXML = `<?xml version="1.0" encoding="UTF-8"?><caps><server title="perf"/><limits max="100" default="100"/><searching><search available="yes" supportedParams="q"/><book-search available="yes" supportedParams="q,author,title"/></searching><categories><category id="7000" name="Books"><subcat id="7020" name="EBook"/></category><category id="3000" name="Audio"><subcat id="3030" name="Audiobook"/></category></categories></caps>`

func (f *FakeIndexers) handler(i int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(f.latency)
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		if q.Get("t") == "caps" {
			_, _ = w.Write([]byte(capsXML))
			return
		}
		n := f.queries[i].Add(1)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>perf</title>`)
		if !f.miss.Load() {
			name := q.Get("q")
			if q.Get("title") != "" {
				name = strings.TrimPrefix(q.Get("author")+" - "+q.Get("title"), " - ")
			}
			writeReleases(&b, name, i, n, r.Host)
		}
		b.WriteString(`</channel></rss>`)
		_, _ = w.Write([]byte(b.String()))
	})
}

// releaseShapes are the names a search returns for one query: retail and
// scene style ebooks, an audiobook, a box set and unrelated titles, so the
// relevance and ranking stages have every kind of release to judge.
var releaseShapes = []string{
	"%s (2021) [EPUB]", "%s RETAIL EPUB-GRP", "%s [AZW3] (v5.0)", "%s (epub, mobi)", "%s 2021 Retail eBook-BitBook",
	"%s Unabridged M4B 64kbps", "%s - Complete Collection 1-7 (epub)", "%s [ENG] [PDF]", "%s REPOST", "%s (German Edition) epub",
	"Unrelated Title About Gardening (epub)", "%s Summary and Analysis", "%s Book 2 (epub)", "%s MP3 Audiobook", "A Different Author - %s (epub)",
}

func writeReleases(b *strings.Builder, name string, indexer int, query int64, host string) {
	for j := range 30 {
		title := releaseShapes[j%len(releaseShapes)]
		if strings.Contains(title, "%s") {
			title = fmt.Sprintf(title, name)
		}
		if j >= len(releaseShapes) {
			title += fmt.Sprintf(" x%d", j)
		}
		cat := 7020
		if strings.Contains(title, "M4B") || strings.Contains(title, "MP3") {
			cat = 3030
		}
		guid := fmt.Sprintf("perf-%d-%d-%d", indexer, query, j)
		fmt.Fprintf(b, `<item><title>%s</title><guid isPermaLink="false">%s</guid><link>http://%s/dl/%s.torrent</link><pubDate>Mon, 02 Mar 2026 10:00:00 +0000</pubDate><size>%d</size><enclosure url="http://%s/dl/%s.torrent" length="%d" type="application/x-bittorrent"/><torznab:attr name="category" value="%d"/><torznab:attr name="seeders" value="%d"/><torznab:attr name="peers" value="%d"/></item>`,
			xmlEscape(title), guid, xmlEscape(host), guid, (j+1)<<20, xmlEscape(host), guid, (j+1)<<20, cat, 3+j, 5+j)
	}
}

// FakeTorrent is one finished torrent the fake download client reports.
type FakeTorrent struct {
	Hash, Name, ContentPath, File string
	Size                          int64
	// BookID and Audio describe the book the torrent is for. The fake does not
	// report them; the import preparation reads them.
	BookID int64
	Audio  bool
}

// FakeQbittorrent answers the parts of the qBittorrent WebUI API the import
// poll uses: login, the torrent list and the per torrent file list. Every
// torrent it holds is reported as finished and seeding.
type FakeQbittorrent struct {
	latency  time.Duration
	savePath string
	category string
	srv      *http.Server
	Port     int

	mu        sync.Mutex
	torrents  []FakeTorrent
	byHash    map[string]int
	requests  int64
	firstList time.Time
}

// NewFakeQbittorrent starts the fake.
func NewFakeQbittorrent(latency time.Duration, savePath, category string) (*FakeQbittorrent, error) {
	f := &FakeQbittorrent{latency: latency, savePath: savePath, category: category, byHash: map[string]int{}}
	srv, port, err := listenLoopback(http.HandlerFunc(f.serve))
	if err != nil {
		return nil, err
	}
	f.srv, f.Port = srv, port
	return f, nil
}

// SetTorrents replaces the torrent list.
func (f *FakeQbittorrent) SetTorrents(ts []FakeTorrent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.torrents = ts
	f.byHash = make(map[string]int, len(ts))
	for i, t := range ts {
		f.byHash[strings.ToLower(t.Hash)] = i
	}
	f.firstList = time.Time{}
}

// Stats returns the number of requests served and when the torrent list was
// first asked for since the last SetTorrents.
func (f *FakeQbittorrent) Stats() (requests int64, firstList time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.firstList
}

// Close stops the fake.
func (f *FakeQbittorrent) Close() { _ = f.srv.Close() }

func (f *FakeQbittorrent) serve(w http.ResponseWriter, r *http.Request) {
	time.Sleep(f.latency)
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	switch r.URL.Path {
	case "/api/v2/auth/login":
		// Not marked Secure: the fake speaks plain HTTP on loopback, and a
		// client would not send a Secure cookie back over it.
		w.Header().Set("Set-Cookie", "SID=perf-session; Path=/; HttpOnly")
		_, _ = w.Write([]byte("Ok."))
	case "/api/v2/app/version":
		_, _ = w.Write([]byte("v5.0.0"))
	case "/api/v2/app/webapiVersion":
		_, _ = w.Write([]byte("2.11.2"))
	case "/api/v2/torrents/info":
		f.mu.Lock()
		if f.firstList.IsZero() && len(f.torrents) > 0 {
			f.firstList = time.Now()
		}
		cat := r.URL.Query().Get("category")
		out := make([]map[string]any, 0, len(f.torrents))
		if cat == "" || cat == f.category {
			for _, t := range f.torrents {
				out = append(out, map[string]any{
					"hash": t.Hash, "name": t.Name, "size": t.Size, "amount_left": 0, "progress": 1.0,
					"state": "stalledUP", "category": f.category, "save_path": f.savePath, "content_path": t.ContentPath,
					"eta": 0, "added_on": 1700000000, "dlspeed": 0, "tags": "",
				})
			}
		}
		f.mu.Unlock()
		writeFakeJSON(w, out)
	case "/api/v2/torrents/files":
		f.mu.Lock()
		i, ok := f.byHash[strings.ToLower(r.URL.Query().Get("hash"))]
		var files []map[string]any
		if ok {
			t := f.torrents[i]
			files = []map[string]any{{"name": t.File, "size": t.Size, "progress": 1.0, "index": 0}}
		}
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeFakeJSON(w, files)
	case "/api/v2/app/preferences":
		writeFakeJSON(w, map[string]any{"save_path": f.savePath})
	case "/api/v2/torrents/categories":
		writeFakeJSON(w, map[string]any{f.category: map[string]any{"name": f.category, "savePath": f.savePath}})
	default:
		_, _ = w.Write([]byte("Ok."))
	}
}

func writeFakeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// OutboundTrap is an HTTP proxy that refuses everything. The binary is pointed
// at it with BINDERY_OUTBOUND_PROXY, so a metadata provider or cover fetch
// that would have left the machine is refused here and counted instead. The
// fake indexers and the fake download client are on loopback and bypass it.
type OutboundTrap struct {
	srv   *http.Server
	Port  int
	count atomic.Int64
}

// NewOutboundTrap starts the trap.
func NewOutboundTrap() (*OutboundTrap, error) {
	t := &OutboundTrap{}
	srv, port, err := listenLoopback(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.count.Add(1)
		http.Error(w, "outbound requests are blocked during a performance run", http.StatusBadGateway)
	}))
	if err != nil {
		return nil, err
	}
	t.srv, t.Port = srv, port
	return t, nil
}

// Blocked is the number of outbound requests refused so far.
func (t *OutboundTrap) Blocked() int64 { return t.count.Load() }

// Close stops the trap.
func (t *OutboundTrap) Close() { _ = t.srv.Close() }
