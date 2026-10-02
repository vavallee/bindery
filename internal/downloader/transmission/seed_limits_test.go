package transmission

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// rpcCapture records the arguments of every RPC the stub receives, keyed by
// method, so a test can assert both what went on torrent-add and what went on
// the follow-up torrent-set.
type rpcCapture struct {
	mu    sync.Mutex
	calls map[string][]map[string]any
}

func (r *rpcCapture) last(method string) (map[string]any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.calls[method]
	if len(c) == 0 {
		return nil, false
	}
	return c[len(c)-1], true
}

// captureRPC spins up an RPC stub that answers torrent-add with a torrent that
// carries a hash and records every call. setResult is the result string the
// stub returns for torrent-set, so a test can make that call fail.
func captureRPC(t *testing.T, setResult string) (*Client, *rpcCapture) {
	t.Helper()
	rc := &rpcCapture{calls: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = newJSONDecoder(r.Body).Decode(&req)
		rc.mu.Lock()
		rc.calls[req.Method] = append(rc.calls[req.Method], req.Arguments)
		rc.mu.Unlock()
		switch req.Method {
		case "torrent-add":
			_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-added":{"id":1,"name":"B","hashString":"ABCDEF0123"}}}`))
		case "torrent-set":
			_, _ = fmt.Fprintf(w, `{"result":%q,"arguments":{}}`, setResult)
		default:
			_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return newTestClient(srv.URL, "u", "p"), rc
}

func intPtr(v int) *int { return &v }

// TestAddTorrent_SeedLimitsNotOnTorrentAdd pins why the limits moved to a
// separate call. Transmission's torrent-add reads none of seedRatioLimit,
// seedRatioMode, seedIdleLimit or seedIdleMode; only torrent-set does (compare
// torrentAdd and torrentSet in libtransmission/rpcimpl.cc). Sending them on
// torrent-add was silently dropped, so the per indexer ratio never applied.
func TestAddTorrent_SeedLimitsNotOnTorrentAdd(t *testing.T) {
	c, rc := captureRPC(t, "success")
	if _, err := c.AddTorrentWithLimits(context.Background(), "magnet:?xt=urn:btih:abc", "",
		SeedLimits{Ratio: floatPtr(2.5), IdleMinutes: intPtr(90)}); err != nil {
		t.Fatalf("AddTorrentWithLimits: %v", err)
	}
	add, ok := rc.last("torrent-add")
	if !ok {
		t.Fatal("torrent-add was not called")
	}
	for _, k := range []string{"seedRatioLimit", "seedRatioMode", "seedIdleLimit", "seedIdleMode"} {
		if _, ok := add[k]; ok {
			t.Errorf("torrent-add carried %s, which Transmission ignores there", k)
		}
	}
}

// TestAddTorrent_SeedRatio_Positive: a positive override sets a per-torrent
// ratio with seedRatioMode=1 (single, ignore the global rule) on torrent-set,
// addressed by the info hash torrent-add returned.
func TestAddTorrent_SeedRatio_Positive(t *testing.T) {
	c, rc := captureRPC(t, "success")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "", floatPtr(2.5)); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	set, ok := rc.last("torrent-set")
	if !ok {
		t.Fatal("torrent-set was not called for a ratio override")
	}
	if ids, _ := set["ids"].([]any); len(ids) != 1 || ids[0] != "abcdef0123" {
		t.Errorf("torrent-set ids = %v, want [abcdef0123]", set["ids"])
	}
	if got := set["seedRatioLimit"]; got != 2.5 {
		t.Errorf("seedRatioLimit = %v, want 2.5", got)
	}
	// JSON numbers decode to float64.
	if got := set["seedRatioMode"]; got != float64(seedRatioModeSingle) {
		t.Errorf("seedRatioMode = %v, want %d", got, seedRatioModeSingle)
	}
	if _, ok := set["seedIdleMode"]; ok {
		t.Error("seedIdleMode must be omitted when no inactive limit is set")
	}
}

// TestAddTorrent_SeedRatio_Unlimited: the -1 sentinel must not be sent as a
// negative seedRatioLimit (the RPC rejects it); it becomes seedRatioMode=2.
func TestAddTorrent_SeedRatio_Unlimited(t *testing.T) {
	c, rc := captureRPC(t, "success")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "", floatPtr(-1)); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	set, ok := rc.last("torrent-set")
	if !ok {
		t.Fatal("torrent-set was not called for the unlimited sentinel")
	}
	if _, ok := set["seedRatioLimit"]; ok {
		t.Errorf("seedRatioLimit must be omitted for the unlimited sentinel, got %v", set["seedRatioLimit"])
	}
	if got := set["seedRatioMode"]; got != float64(seedRatioModeUnlimited) {
		t.Errorf("seedRatioMode = %v, want %d", got, seedRatioModeUnlimited)
	}
}

// TestAddTorrent_SeedRatio_Unset: no override means no torrent-set at all, so
// the torrent keeps Transmission's global rules.
func TestAddTorrent_SeedRatio_Unset(t *testing.T) {
	c, rc := captureRPC(t, "success")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "", nil); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if _, ok := rc.last("torrent-set"); ok {
		t.Error("torrent-set must not be called when no override is set")
	}
}

// TestAddTorrent_SeedIdleLimit: an inactive seed time (#2206) becomes
// seedIdleLimit in minutes with seedIdleMode=1 (single, ignore the global
// rule), and the ratio is left alone when it has no override.
func TestAddTorrent_SeedIdleLimit(t *testing.T) {
	c, rc := captureRPC(t, "success")
	if _, err := c.AddTorrentWithLimits(context.Background(), "magnet:?xt=urn:btih:abc", "",
		SeedLimits{IdleMinutes: intPtr(90)}); err != nil {
		t.Fatalf("AddTorrentWithLimits: %v", err)
	}
	set, ok := rc.last("torrent-set")
	if !ok {
		t.Fatal("torrent-set was not called for an inactive limit")
	}
	if got := set["seedIdleLimit"]; got != float64(90) {
		t.Errorf("seedIdleLimit = %v, want 90", got)
	}
	if got := set["seedIdleMode"]; got != float64(seedIdleModeSingle) {
		t.Errorf("seedIdleMode = %v, want %d", got, seedIdleModeSingle)
	}
	for _, k := range []string{"seedRatioLimit", "seedRatioMode"} {
		if _, ok := set[k]; ok {
			t.Errorf("%s must be omitted when no ratio override is set", k)
		}
	}
}

// TestAddTorrent_SeedIdleLimit_Clamped: Transmission stores the idle limit as
// a uint16 (tr_torrentSetIdleLimit), so a larger value would wrap around to a
// short limit. It is clamped to the largest value the daemon can hold.
func TestAddTorrent_SeedIdleLimit_Clamped(t *testing.T) {
	c, rc := captureRPC(t, "success")
	if _, err := c.AddTorrentWithLimits(context.Background(), "magnet:?xt=urn:btih:abc", "",
		SeedLimits{IdleMinutes: intPtr(70000)}); err != nil {
		t.Fatalf("AddTorrentWithLimits: %v", err)
	}
	set, _ := rc.last("torrent-set")
	if got := set["seedIdleLimit"]; got != float64(maxSeedIdleMinutes) {
		t.Errorf("seedIdleLimit = %v, want %d", got, maxSeedIdleMinutes)
	}
}

// TestAddTorrent_SeedLimitsErrorNonFatal: the torrent is already added when
// torrent-set fails, so the grab must still succeed and report the torrent.
func TestAddTorrent_SeedLimitsErrorNonFatal(t *testing.T) {
	c, _ := captureRPC(t, "invalid argument")
	added, err := c.AddTorrentWithLimits(context.Background(), "magnet:?xt=urn:btih:abc", "",
		SeedLimits{Ratio: floatPtr(1), IdleMinutes: intPtr(10)})
	if err != nil {
		t.Fatalf("AddTorrentWithLimits must succeed despite a torrent-set failure: %v", err)
	}
	if added.ID != 1 {
		t.Errorf("added.ID = %d, want 1", added.ID)
	}
}
