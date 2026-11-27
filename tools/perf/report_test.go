package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpreadAndNum(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		want string
	}{
		{nil, ""},
		{[]float64{12.34}, "12.3"},
		{[]float64{3, 1, 2}, "2 (1 to 3)"},
		{[]float64{1.5, 2.5}, "2 (1.50 to 2.50)"},
		{[]float64{250.4, 250.4, 250.4}, "250"},
	} {
		if got := spread(tc.in); got != tc.want {
			t.Errorf("spread(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if median(nil) != 0 || median([]float64{4, 1, 3, 2}) != 2.5 || median([]float64{9, 1, 5}) != 5 {
		t.Error("median is wrong")
	}
}

func TestProcParsers(t *testing.T) {
	status := "Name:\tbindery\nVmHWM:\t  204800 kB\nVmRSS:\t  102400 kB\n"
	if procStatusKB(status, "VmHWM:") != 204800 || procStatusKB(status, "VmRSS:") != 102400 || procStatusKB(status, "VmSwap:") != 0 {
		t.Error("procStatusKB misread /proc/pid/status")
	}
	stat := "4242 (bindery perf) S 1 4242 4242 0 -1 4194304 100 0 0 0 37 5 0 0 20 0 9 0 100 1000 200"
	if got := parseStatTicks(stat); got != 42 {
		t.Errorf("utime plus stime = %d, want 42 (a command name with a space must not shift the fields)", got)
	}
	if parseStatTicks("garbage") != -1 || parseStatTicks("1 (x) S 1") != -1 {
		t.Error("a short stat line has no CPU time")
	}
	if procField("model name\t: Example CPU\nflags : x", "model name") != "Example CPU" || procField("", "model name") != "" {
		t.Error("procField misread /proc/cpuinfo")
	}
	h := readHostFacts()
	if h.Cores < 1 || h.GoVersion == "" || h.LoadStart == "" {
		t.Errorf("host facts incomplete: %+v", h)
	}
}

func TestMarkdownAndWrite(t *testing.T) {
	run := func(p50, p95 float64) Stats { return Stats{Requests: 10, P50: p50, P95: p95, P99: p95, RPS: 5} }
	r := &Results{
		Mode: "quick", Commit: "abc1234", Repetitions: 3, StartedAt: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC), WallSeconds: 125,
		Host:  HostFacts{CPUModel: "Example CPU", Cores: 6, RAMMB: 12000, LoadStart: "0.1 0.1 0.1", LoadEnd: "2 1 0.5"},
		Seeds: []SeedManifest{{Books: 1000, Authors: 50}, {Books: 1000, Spec: SeedSpec{Untracked: true}}},
		Records: []*Record{
			{Group: "read", Name: "books list, first page", Books: 1000, Runs: []Stats{run(3, 5), run(4, 9), run(2, 4)}},
			{Group: "poll", Name: "queue poll (5 s), idle", Books: 1000, Params: "20 tabs", Runs: []Stats{run(2, 4)}},
			{Group: "poll", Name: "queue poll (5 s), scan writing", Books: 1000, Params: "20 tabs", Runs: []Stats{run(3, 40)}},
			{Group: "poll", Name: "books page (10 s), idle", Books: 1000, Params: "20 tabs", Runs: []Stats{{}}},
			{Group: "poll", Name: "books page (10 s), scan writing", Books: 1000, Params: "20 tabs", Runs: []Stats{run(1, 1)}},
			{Group: "poll", Name: "refresh status poll (2 s), idle", Books: 1000, Params: "20 tabs", Runs: []Stats{run(1, 1)}},
			{Group: "scan", Name: "first scan, no file tracked", Books: 1000, Params: "1000 files", Metrics: map[string][]float64{"wall s": {1.2, 1.4, 1.3}}},
		},
		Profiles: []ProfileRef{{Scenario: "read mix", Kind: "cpu", File: "profiles/read-mix.cpu.pb.gz"}},
		Skipped:  []string{"metadata: no fake"},
		Failures: []string{"import 1000: stalled"},
	}
	md := r.Markdown()
	for _, want := range []string{
		"# Bindery performance run: quick",
		"wall time 2m5s",
		"| CPU | Example CPU |",
		"| 1000 | untracked |",
		"## Read endpoints, one client",
		"| books list, first page | 1000 |  | 30 | 3 (2 to 4) | 5 (4 to 9) |",
		"## UI polling, idle against a library scan writing (#2147)",
		"| queue poll (5 s) | 1000 | 20 tabs | 2 | 3 | 4 | 40 | 10 |",
		"| books page (10 s) | 1000 | 20 tabs | 0 | 1 | 0 | 1 | n/a |",
		"| first scan, no file tracked | 1000 | 1000 files | wall s | 1.30 (1.20 to 1.40) |",
		"`profiles/read-mix.cpu.pb.gz`",
		"## Skipped\n\n- metadata: no fake",
		"## Failures\n\n- import 1000: stalled",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("summary is missing %q\n%s", want, md)
		}
	}
	if strings.Contains(md, "refresh status poll (2 s) | 1000 | 20 tabs | 1 |") {
		t.Error("a request with no writing half has no comparison row")
	}
	if strings.ContainsAny(md, "–—") {
		t.Error("the summary must not contain dashes")
	}

	dir := filepath.Join(t.TempDir(), "out")
	if err := r.Write(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back Results
	if err := json.Unmarshal(raw, &back); err != nil || back.Mode != "quick" || len(back.Records) != 7 || back.Records[0].Runs[1].P95 != 9 {
		t.Fatalf("results.json does not round trip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.md")); err != nil {
		t.Fatal(err)
	}
}
