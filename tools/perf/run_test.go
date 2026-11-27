package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunTinyEndToEnd builds the real binary with the profiling overlay, boots
// it and runs every scenario group once on a 60 book library. It is the test
// that fails when a route, a payload or a startup requirement the harness
// depends on changes, which is the only way a harness that is run a few times
// a year stays runnable. It takes about half a minute, most of it waiting for
// the binary's 15 second download check.
func TestRunTinyEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and boots the binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	data := t.TempDir()
	mode, err := modeByName("tiny")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(data, "out")
	var log strings.Builder
	err = Run(ctx, Config{Mode: mode, DataDir: data, OutDir: out, Seed: 1, IndexerLatency: time.Millisecond, ClientLatency: time.Millisecond},
		func(format string, a ...any) { log.WriteString(format + "\n") })
	if err != nil {
		t.Fatalf("tiny run failed: %v\n%s", err, log.String())
	}

	raw, err := os.ReadFile(filepath.Join(out, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var res Results
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures: %v", res.Failures)
	}
	find := func(group, name string) *Record {
		for _, r := range res.Records {
			if r.Group == group && r.Name == name {
				return r
			}
		}
		t.Fatalf("no record %s / %s", group, name)
		return nil
	}
	// Every read endpoint answered, with no error, on the real router.
	reads := 0
	for _, r := range res.Records {
		if r.Group != "read" {
			continue
		}
		reads++
		if len(r.Runs) != 1 || r.Runs[0].Requests == 0 || r.Runs[0].Errors != 0 {
			t.Errorf("read %q: %+v", r.Name, r.Runs)
		}
	}
	if reads != len(readEndpoints(&SeedManifest{Books: 60, Authors: 5, Series: 6, SearchTerms: []string{"x"}})) {
		t.Errorf("recorded %d read endpoints", reads)
	}
	if r := find("startup", "new install, every migration"); r.Metrics["ready ms"][0] <= 0 {
		t.Error("a new install has a startup time")
	}
	if r := find("sweep", "read mix"); r.Runs[0].Errors != 0 {
		t.Errorf("sweep errors: %+v", r.Runs[0])
	}
	if r := find("poll", "refresh status poll (2 s), idle"); r.Runs[0].Requests == 0 || r.Runs[0].Errors != 0 {
		t.Errorf("idle polling: %+v", r.Runs[0])
	}
	// One query per indexer when the first tier finds releases, more when
	// every tier comes back empty.
	hit, miss := find("search", "book search, releases found"), find("search", "book search, nothing found (every fallback query)")
	if hit.Runs[0].Errors != 0 || miss.Runs[0].Errors != 0 {
		t.Errorf("search errors: %+v %+v", hit.Runs[0], miss.Runs[0])
	}
	if h, m := hit.Metrics["outbound queries per search per indexer"][0], miss.Metrics["outbound queries per search per indexer"][0]; h != 1 || m <= h {
		t.Errorf("queries per search per indexer: found=%v nothing found=%v", h, m)
	}
	// The scan matched every seeded file and the import moved every download.
	if r := find("scan", "first scan, no file tracked"); r.Metrics["books matched"][0] != 60 {
		t.Errorf("first scan matched %v of 60", r.Metrics["books matched"])
	}
	if r := find("scan", "rescan, every file already tracked"); r.Metrics["books matched"][0] != 0 {
		t.Errorf("a rescan of a tracked library matched %v new books", r.Metrics["books matched"])
	}
	if r := find("import", "finished torrents imported"); r.Metrics["books imported"][0] != 60 || r.Metrics["imports per s"][0] <= 0 {
		t.Errorf("import: %v", r.Metrics)
	}
	if r := find("memory", "read scenarios"); r.Metrics["peak RSS MB"][0] <= 0 || r.Metrics["database MB"][0] <= 0 {
		t.Errorf("memory: %v", r.Metrics)
	}
	// CPU and heap profiles came out of the overlay's listener.
	cpu, heap := 0, 0
	for _, p := range res.Profiles {
		fi, err := os.Stat(filepath.Join(out, p.File))
		if err != nil || fi.Size() == 0 {
			t.Errorf("profile %s: %v", p.File, err)
		}
		if p.Kind == "cpu" {
			cpu++
		} else {
			heap++
		}
	}
	if cpu < 3 || cpu != heap {
		t.Errorf("profiles: %d cpu, %d heap", cpu, heap)
	}
	if res.BlockedOutbound != 0 {
		t.Logf("%d outbound requests were refused by the trap", res.BlockedOutbound)
	}
	md, err := os.ReadFile(filepath.Join(out, "summary.md"))
	if err != nil || !strings.Contains(string(md), "## Import through the download client") {
		t.Errorf("summary.md is incomplete: %v", err)
	}
	// The work directories are gone; only seeds, the binary and results stay.
	if entries, _ := os.ReadDir(filepath.Join(data, "work")); len(entries) != 0 {
		t.Errorf("work directory not cleaned: %v", entries)
	}

	// A second pass restricted to one group reuses the seeds and the binary.
	only := filepath.Join(data, "only")
	err = Run(ctx, Config{Mode: mode, DataDir: data, OutDir: only, Binary: res.Binary, Only: "^startup$", NoProfiles: true, Seed: 1}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(only, "results.json"))
	var second Results
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatal(err)
	}
	for _, r := range second.Records {
		if r.Group != "startup" && r.Group != "memory" {
			t.Errorf("-only startup also ran %s", r.Group)
		}
	}
}
