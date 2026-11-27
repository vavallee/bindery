package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// fetchProfile downloads one profile from the overlay's listener into file.
func fetchProfile(ctx context.Context, addr, query, file string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/debug/pprof/"+query, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pprof %s: status %d", query, resp.StatusCode)
	}
	out, err := os.Create(file)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// profileWhile records a CPU profile of inst for dur while work runs, then a
// heap profile, and adds both to the results.
func (r *runner) profileWhile(ctx context.Context, inst *Instance, scenario string, dur time.Duration, work func(context.Context)) {
	dir := filepath.Join(r.cfg.OutDir, "profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.fail("profile %s: %v", scenario, err)
		return
	}
	slug := strings.NewReplacer(" ", "-", ",", "", "(", "", ")", "").Replace(strings.ToLower(scenario))
	cpu := filepath.Join(dir, slug+".cpu.pb.gz")
	errc := make(chan error, 1)
	go func() {
		errc <- fetchProfile(ctx, inst.PprofAddr, fmt.Sprintf("profile?seconds=%d", max(int(dur.Seconds()), 1)), cpu)
	}()
	workCtx, cancel := context.WithTimeout(ctx, dur)
	work(workCtx)
	cancel()
	if err := <-errc; err != nil {
		r.fail("profile %s: cpu: %v", scenario, err)
	} else {
		r.res.Profiles = append(r.res.Profiles, ProfileRef{Scenario: scenario, Kind: "cpu", File: filepath.Join("profiles", filepath.Base(cpu)), Top: r.pprofTop(ctx, cpu)})
	}
	heap := filepath.Join(dir, slug+".heap.pb.gz")
	if err := fetchProfile(ctx, inst.PprofAddr, "heap", heap); err != nil {
		r.fail("profile %s: heap: %v", scenario, err)
		return
	}
	r.res.Profiles = append(r.res.Profiles, ProfileRef{Scenario: scenario, Kind: "heap", File: filepath.Join("profiles", filepath.Base(heap)), Top: r.pprofTop(ctx, heap)})
}

// pprofTop writes the text form of a profile next to it, so the hottest
// functions can be read without opening the profile. Best effort: a missing
// Go toolchain only loses the text file.
func (r *runner) pprofTop(ctx context.Context, profile string) string {
	out, err := exec.CommandContext(ctx, "go", "tool", "pprof", "-top", "-nodecount=40", r.cfg.Binary, profile).Output() //nolint:gosec // G204: fixed arguments, files this run wrote
	if err != nil {
		return ""
	}
	txt := strings.TrimSuffix(profile, ".pb.gz") + ".top.txt"
	if os.WriteFile(txt, out, 0o644) != nil {
		return ""
	}
	return filepath.Join("profiles", filepath.Base(txt))
}

// slowestReads returns the n read endpoints with the highest median p95 at
// one library size, from the records this run already holds.
func (r *runner) slowestReads(size, n int) []string {
	type row struct {
		name string
		p95  float64
	}
	var rows []row
	for _, rec := range r.res.Records {
		if rec.Group == "read" && rec.Books == size && len(rec.Runs) > 0 {
			rows = append(rows, row{rec.Name, median(column(rec.Runs, func(s Stats) float64 { return s.P95 }))})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].p95 > rows[j].p95 })
	out := make([]string, 0, n)
	for i := 0; i < len(rows) && i < n; i++ {
		out = append(out, rows[i].name)
	}
	return out
}

// profile records CPU and heap profiles for the scenarios that cost the most:
// the read mix and the slowest read endpoints on the largest library, a first
// library scan, and a search that falls through every fallback query.
func (r *runner) profile(ctx context.Context) error {
	mode := r.cfg.Mode
	size := mode.ReadSizes[len(mode.ReadSizes)-1]
	m, err := r.seed(ctx, size, false)
	if err != nil {
		return err
	}
	inst, c, err := r.boot(ctx, m, InstanceOpts{Pprof: true})
	if err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	inst.WaitQuiet(ctx, 60*time.Second)
	eps := readEndpoints(m)
	r.profileWhile(ctx, inst, fmt.Sprintf("read mix %d books 16 clients", size), mode.ProfileDur, func(ctx context.Context) {
		RunLoad(ctx, c, 16, mode.ProfileDur, r.cfg.Seed, mixOf(eps))
	})
	for _, name := range r.slowestReads(size, 3) {
		for _, e := range eps {
			if e.name == name {
				r.profileWhile(ctx, inst, fmt.Sprintf("%s %d books", name, size), mode.ProfileDur, func(ctx context.Context) {
					RunLoad(ctx, c, 4, mode.ProfileDur, r.cfg.Seed, e.path)
				})
			}
		}
	}
	inst.Stop()

	scanSize := mode.ScanSizes[len(mode.ScanSizes)-1]
	um, err := r.seed(ctx, scanSize, true)
	if err != nil {
		return err
	}
	inst, c, err = r.boot(ctx, um, InstanceOpts{Pprof: true})
	if err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	inst.WaitQuiet(ctx, 60*time.Second)
	r.profileWhile(ctx, inst, fmt.Sprintf("first library scan %d files", um.TreeFiles), mode.ProfileDur, func(ctx context.Context) {
		done, _, err := startScan(ctx, c)
		if err != nil {
			r.fail("profile: start scan: %v", err)
			return
		}
		for !done() && ctx.Err() == nil {
			time.Sleep(50 * time.Millisecond)
		}
	})
	inst.Stop()

	sm, err := r.seed(ctx, mode.SearchSize, false)
	if err != nil {
		return err
	}
	n := mode.Indexers[len(mode.Indexers)-1]
	fake, err := NewFakeIndexers(n, r.cfg.IndexerLatency)
	if err != nil {
		return err
	}
	defer fake.Close()
	fake.SetMiss(true)
	inst, c, err = r.boot(ctx, sm, InstanceOpts{Pprof: true})
	if err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	defer inst.Stop()
	inst.WaitQuiet(ctx, 60*time.Second)
	if _, err := createIndexers(ctx, c, fake, n); err != nil {
		return err
	}
	r.profileWhile(ctx, inst, fmt.Sprintf("book search nothing found %d indexers", n), mode.ProfileDur, func(ctx context.Context) {
		// A different book each time, so no search is answered from the per
		// indexer query cache.
		for id := sm.Books; ctx.Err() == nil; id-- {
			if id < 1 {
				id = sm.Books
			}
			_, _, _ = c.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/book/%d/search", id), nil)
		}
	})
	return nil
}
