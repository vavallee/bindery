package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// shareLimitsStub records the form of the last setShareLimits call, enforcing
// the required parameters the way qBittorrent 5.2 does.
func shareLimitsStub(t *testing.T) (*Client, *url.Values) {
	t.Helper()
	got := &url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/setShareLimits":
			_ = r.ParseForm()
			if !requireShareLimitParams(w, r) {
				return
			}
			*got = r.PostForm
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(srv.URL, "u", "p")
	c.loggedIn = true
	return c, got
}

func ptrInt(v int) *int           { return &v }
func ptrFloat(v float64) *float64 { return &v }

// TestSetShareLimitsDetailed_SeedTimes covers #2206: both seed time limits are
// posted in minutes, which is the unit qBittorrent's setShareLimits takes (the
// torrent compares seedingTimeLimit * 60 against its seconds counters), and an
// unset ratio stays at -2 so qBittorrent keeps its own ratio rule.
func TestSetShareLimitsDetailed_SeedTimes(t *testing.T) {
	c, got := shareLimitsStub(t)
	err := c.SetShareLimitsDetailed(context.Background(), "abc", ShareLimits{
		SeedingTimeMinutes:         ptrInt(4320),
		InactiveSeedingTimeMinutes: ptrInt(60),
	})
	if err != nil {
		t.Fatalf("SetShareLimitsDetailed: %v", err)
	}
	want := map[string]string{
		"hashes":                   "abc",
		"ratioLimit":               "-2",
		"seedingTimeLimit":         "4320",
		"inactiveSeedingTimeLimit": "60",
		"shareLimitAction":         "Default",
		"shareLimitsMode":          "Default",
	}
	for k, v := range want {
		if g := got.Get(k); g != v {
			t.Errorf("%s = %q, want %q", k, g, v)
		}
	}
}

// TestSetShareLimitsDetailed_AllThree: ratio and both times together, each in
// its own field.
func TestSetShareLimitsDetailed_AllThree(t *testing.T) {
	c, got := shareLimitsStub(t)
	err := c.SetShareLimitsDetailed(context.Background(), "abc", ShareLimits{
		Ratio:                      ptrFloat(1.5),
		SeedingTimeMinutes:         ptrInt(10080),
		InactiveSeedingTimeMinutes: ptrInt(1440),
	})
	if err != nil {
		t.Fatalf("SetShareLimitsDetailed: %v", err)
	}
	if g := got.Get("ratioLimit"); g != "1.5" {
		t.Errorf("ratioLimit = %q, want 1.5", g)
	}
	if g := got.Get("seedingTimeLimit"); g != "10080" {
		t.Errorf("seedingTimeLimit = %q, want 10080", g)
	}
	if g := got.Get("inactiveSeedingTimeLimit"); g != "1440" {
		t.Errorf("inactiveSeedingTimeLimit = %q, want 1440", g)
	}
}

// TestSetShareLimitsDetailed_RatioOnlyKeepsGlobalTimes: the existing ratio
// path is unchanged, both time fields stay at -2 (use the global rule).
func TestSetShareLimitsDetailed_RatioOnlyKeepsGlobalTimes(t *testing.T) {
	c, got := shareLimitsStub(t)
	if err := c.SetShareLimitsDetailed(context.Background(), "abc", ShareLimits{Ratio: ptrFloat(-1)}); err != nil {
		t.Fatalf("SetShareLimitsDetailed: %v", err)
	}
	if g := got.Get("ratioLimit"); g != "-1" {
		t.Errorf("ratioLimit = %q, want -1", g)
	}
	for _, k := range []string{"seedingTimeLimit", "inactiveSeedingTimeLimit"} {
		if g := got.Get(k); g != "-2" {
			t.Errorf("%s = %q, want -2", k, g)
		}
	}
}

func TestShareLimits_IsZero(t *testing.T) {
	if !(ShareLimits{}).IsZero() {
		t.Error("empty ShareLimits must report IsZero")
	}
	for _, l := range []ShareLimits{
		{Ratio: ptrFloat(1)},
		{SeedingTimeMinutes: ptrInt(1)},
		{InactiveSeedingTimeMinutes: ptrInt(1)},
	} {
		if l.IsZero() {
			t.Errorf("%+v must not report IsZero", l)
		}
	}
}
