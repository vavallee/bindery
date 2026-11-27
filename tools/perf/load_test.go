package main

import (
	"context"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPercentileIsNearestRank(t *testing.T) {
	var lat []time.Duration
	for i := 1; i <= 100; i++ {
		lat = append(lat, time.Duration(i)*time.Millisecond)
	}
	for p, want := range map[float64]time.Duration{50: 50 * time.Millisecond, 95: 95 * time.Millisecond, 99: 99 * time.Millisecond, 100: 100 * time.Millisecond, 0: time.Millisecond} {
		if got := percentile(lat, p); got != want {
			t.Errorf("p%v = %s, want %s", p, got, want)
		}
	}
	if percentile(nil, 50) != 0 {
		t.Error("no samples has no percentile")
	}
}

func TestSummarize(t *testing.T) {
	s := summarize([]time.Duration{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}, 1, 300, "boom", 2*time.Second)
	if s.Requests != 3 || s.Errors != 1 || s.P50 != 20 || s.P95 != 30 || s.Max != 30 || s.Mean != 20 || s.RPS != 1.5 || s.FirstErr != "boom" || s.Bytes != 300 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	if empty := summarize(nil, 0, 0, "", time.Second); empty.Requests != 0 || empty.P95 != 0 || empty.RPS != 0 {
		t.Fatalf("an empty run summarises to zeros: %+v", empty)
	}
}

func TestRunLoadDrivesTheServerAndCountsErrors(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/bad") {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := NewAPIClient(srv.URL, "k")

	good := RunLoad(context.Background(), c, 3, 150*time.Millisecond, 1, func(*rand.Rand) string { return "/good" })
	if good.Requests < 3 || good.Errors != 0 || good.Bytes != int64(2*good.Requests) || good.P50 <= 0 {
		t.Fatalf("unexpected run: %+v", good)
	}
	if int64(good.Requests) > hits.Load() {
		t.Fatalf("recorded %d requests, server saw %d", good.Requests, hits.Load())
	}
	bad := RunLoad(context.Background(), c, 1, 100*time.Millisecond, 1, func(*rand.Rand) string { return "/bad" })
	if bad.Errors != bad.Requests || !strings.Contains(bad.FirstErr, "status 500") {
		t.Fatalf("a 500 is an error: %+v", bad)
	}
	if _, _, err := NewAPIClient(srv.URL, "wrong").Do(context.Background(), http.MethodPost, "/good", []byte(`{}`)); err == nil {
		t.Fatal("a 401 is an error")
	}
	if _, _, err := NewAPIClient("http://127.0.0.1:1", "k").Do(context.Background(), http.MethodGet, "/", nil); err == nil {
		t.Fatal("a refused connection is an error")
	}
}

func TestRunPollKeepsOnlyActiveSamples(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	c := NewAPIClient(srv.URL, "k")
	series := []PollSeries{{Name: "fast", Path: "/a", Every: 20 * time.Millisecond}, {Name: "slow", Path: "/b", Every: 10 * time.Second}}

	all := RunPoll(context.Background(), c, 2, 300*time.Millisecond, 1, series, nil)
	if all["fast"].Requests < 8 {
		t.Fatalf("two tabs polling every 20 ms for 300 ms made %d requests", all["fast"].Requests)
	}
	if all["slow"].Requests > 2 {
		t.Fatalf("a 10 s poll fires at most once per tab in 300 ms, got %d", all["slow"].Requests)
	}
	none := RunPoll(context.Background(), c, 2, 100*time.Millisecond, 1, series, func() bool { return false })
	if none["fast"].Requests != 0 {
		t.Fatalf("samples taken while inactive must be dropped, kept %d", none["fast"].Requests)
	}
}

func TestMixDrawsByWeight(t *testing.T) {
	mix := mixOf([]endpoint{{"a", 9, fixed("/a")}, {"b", 1, fixed("/b")}})
	rng := rand.New(rand.NewPCG(1, 1))
	counts := map[string]int{}
	for range 2000 {
		counts[mix(rng)]++
	}
	if counts["/a"] < 1700 || counts["/b"] < 100 || counts["/a"]+counts["/b"] != 2000 {
		t.Fatalf("weights 9 to 1 drew %v", counts)
	}
}
