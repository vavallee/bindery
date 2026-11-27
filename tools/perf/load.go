package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Stats is one measured run of one scenario. Latencies are milliseconds.
type Stats struct {
	Requests  int     `json:"requests"`
	Errors    int     `json:"errors"`
	Seconds   float64 `json:"seconds"`
	RPS       float64 `json:"rps"`
	P50       float64 `json:"p50Ms"`
	P95       float64 `json:"p95Ms"`
	P99       float64 `json:"p99Ms"`
	Mean      float64 `json:"meanMs"`
	Max       float64 `json:"maxMs"`
	Bytes     int64   `json:"bytes"`
	FirstErr  string  `json:"firstError,omitempty"`
	latencies []time.Duration
}

// recorder collects samples from several goroutines.
type recorder struct {
	mu       sync.Mutex
	lat      []time.Duration
	errs     int
	bytes    int64
	firstErr string
}

func (r *recorder) add(d time.Duration, n int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lat = append(r.lat, d)
	r.bytes += n
	if err != nil {
		r.errs++
		if r.firstErr == "" {
			r.firstErr = err.Error()
		}
	}
}

func (r *recorder) stats(elapsed time.Duration) Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return summarize(r.lat, r.errs, r.bytes, r.firstErr, elapsed)
}

// percentile is the nearest rank percentile of a sorted slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}

func ms(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*1000) / 1000 }

func summarize(lat []time.Duration, errs int, bytes int64, firstErr string, elapsed time.Duration) Stats {
	sorted := slices.Clone(lat)
	slices.Sort(sorted)
	s := Stats{Requests: len(sorted), Errors: errs, Seconds: elapsed.Seconds(), Bytes: bytes, FirstErr: firstErr, latencies: sorted}
	if len(sorted) == 0 {
		return s
	}
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	s.P50, s.P95, s.P99 = ms(percentile(sorted, 50)), ms(percentile(sorted, 95)), ms(percentile(sorted, 99))
	s.Mean, s.Max = ms(sum/time.Duration(len(sorted))), ms(sorted[len(sorted)-1])
	if elapsed > 0 {
		s.RPS = math.Round(float64(len(sorted))/elapsed.Seconds()*100) / 100
	}
	return s
}

// APIClient talks to one Bindery instance with its API key, the way
// `make smoke` does.
type APIClient struct {
	Base string
	Key  string
	HTTP *http.Client
}

// NewAPIClient returns a client whose connection pool is large enough for the
// widest concurrency sweep.
func NewAPIClient(base, key string) *APIClient {
	tr := &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 512, IdleConnTimeout: 30 * time.Second}
	return &APIClient{Base: base, Key: key, HTTP: &http.Client{Transport: tr, Timeout: 120 * time.Second}}
}

// Do sends one request and reads the whole response. A status of 400 or above
// is an error.
func (c *APIClient) Do(ctx context.Context, method, path string, body []byte) ([]byte, time.Duration, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Api-Key", c.Key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, time.Since(start), err
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	elapsed := time.Since(start)
	if err != nil {
		return data, elapsed, err
	}
	if resp.StatusCode >= 400 {
		return data, elapsed, fmt.Errorf("%s %s: status %d: %.120s", method, path, resp.StatusCode, data)
	}
	return data, elapsed, nil
}

// PathFunc picks the next request path for one worker.
type PathFunc func(rng *rand.Rand) string

// RunLoad drives clients closed loop workers for dur: each sends a request,
// waits for the whole response and sends the next.
func RunLoad(ctx context.Context, c *APIClient, clients int, dur time.Duration, seed uint64, next PathFunc) Stats {
	rec := &recorder{}
	ctx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()
	start := time.Now()
	var wg sync.WaitGroup
	for w := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, uint64(w)+1)) //nolint:gosec // G404: request order, not a secret
			for ctx.Err() == nil {
				data, d, err := c.Do(ctx, http.MethodGet, next(rng), nil)
				if ctx.Err() != nil {
					// The deadline cut this request short; it is not a sample.
					return
				}
				rec.add(d, int64(len(data)), err)
			}
		}()
	}
	wg.Wait()
	return rec.stats(time.Since(start))
}

// PollSeries is one request a browser tab repeats on a timer.
type PollSeries struct {
	Name  string
	Path  string
	Every time.Duration
}

// RunPoll simulates tabs browser tabs for dur. Each tab repeats every series
// on its own timer, starting at a random offset so the tabs do not fire
// together. A sample is kept only when active is nil or reports true as the
// request starts.
func RunPoll(ctx context.Context, c *APIClient, tabs int, dur time.Duration, seed uint64, series []PollSeries, active func() bool) map[string]Stats {
	recs := make([]*recorder, len(series))
	for i := range recs {
		recs[i] = &recorder{}
	}
	ctx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()
	start := time.Now()
	var wg sync.WaitGroup
	for tab := range tabs {
		for si, s := range series {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(seed, uint64(tab*len(series)+si)+1)) //nolint:gosec // G404: timer phase, not a secret
				timer := time.NewTimer(time.Duration(rng.Float64() * float64(s.Every)))
				defer timer.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-timer.C:
					}
					keep := active == nil || active()
					data, d, err := c.Do(ctx, http.MethodGet, s.Path, nil)
					if ctx.Err() != nil {
						return
					}
					if keep {
						recs[si].add(d, int64(len(data)), err)
					}
					timer.Reset(s.Every)
				}
			}()
		}
	}
	wg.Wait()
	out := make(map[string]Stats, len(series))
	for i, s := range series {
		out[s.Name] = recs[i].stats(time.Since(start))
	}
	return out
}
