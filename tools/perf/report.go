package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

// HostFacts describes the machine a run was taken on. Numbers from two runs
// are only comparable when these match and the load averages are low.
type HostFacts struct {
	CPUModel  string  `json:"cpuModel"`
	Cores     int     `json:"cores"`
	RAMMB     float64 `json:"ramMb"`
	Kernel    string  `json:"kernel"`
	GoVersion string  `json:"goVersion"`
	LoadStart string  `json:"loadAverageStart"`
	LoadEnd   string  `json:"loadAverageEnd"`
}

// Record is one scenario at one library size and one set of parameters. Runs
// holds one latency summary per repetition; Metrics holds the values that are
// not request latencies (wall time, queries per search, memory), also one per
// repetition.
type Record struct {
	Group   string               `json:"group"`
	Name    string               `json:"name"`
	Books   int                  `json:"books"`
	Params  string               `json:"params,omitempty"`
	Runs    []Stats              `json:"runs,omitempty"`
	Metrics map[string][]float64 `json:"metrics,omitempty"`
}

// ProfileRef names one profile file written by the profile pass.
type ProfileRef struct {
	Scenario string `json:"scenario"`
	Kind     string `json:"kind"`
	File     string `json:"file"`
	Top      string `json:"top,omitempty"`
}

// Results is everything one run measured. It is written as results.json.
type Results struct {
	Mode             string         `json:"mode"`
	Commit           string         `json:"commit"`
	Binary           string         `json:"binary"`
	StartedAt        time.Time      `json:"startedAt"`
	FinishedAt       time.Time      `json:"finishedAt"`
	WallSeconds      float64        `json:"wallSeconds"`
	Host             HostFacts      `json:"host"`
	IndexerLatencyMs float64        `json:"indexerLatencyMs"`
	ClientLatencyMs  float64        `json:"clientLatencyMs"`
	Repetitions      int            `json:"repetitions"`
	Seeds            []SeedManifest `json:"seeds"`
	Records          []*Record      `json:"records"`
	Profiles         []ProfileRef   `json:"profiles,omitempty"`
	BlockedOutbound  int64          `json:"blockedOutboundRequests"`
	Skipped          []string       `json:"skipped,omitempty"`
	Failures         []string       `json:"failures,omitempty"`
}

func readHostFacts() HostFacts {
	h := HostFacts{Cores: runtime.NumCPU(), GoVersion: runtime.Version(), LoadStart: loadAverage()}
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		h.CPUModel = procField(string(raw), "model name")
	}
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		h.RAMMB = procStatusKB(string(raw), "MemTotal:") / 1024
	}
	if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		h.Kernel = strings.TrimSpace(string(raw))
	}
	return h
}

func procField(text, key string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func loadAverage() string {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "unavailable"
	}
	f := strings.Fields(string(raw))
	if len(f) < 3 {
		return "unavailable"
	}
	return strings.Join(f[:3], " ")
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output() //nolint:noctx // one short local command
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// spread formats the median of vs with its range: "12.3 (11 to 14.1)". On a
// shared machine the range is the honest part of the number.
func spread(vs []float64) string {
	if len(vs) == 0 {
		return ""
	}
	s := slices.Clone(vs)
	slices.Sort(s)
	med := s[len(s)/2]
	if len(s)%2 == 0 {
		med = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	if len(s) == 1 || s[0] == s[len(s)-1] {
		return num(med)
	}
	return fmt.Sprintf("%s (%s to %s)", num(med), num(s[0]), num(s[len(s)-1]))
}

func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := slices.Clone(vs)
	slices.Sort(s)
	if len(s)%2 == 0 {
		return (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return s[len(s)/2]
}

func num(v float64) string {
	switch {
	case v >= 100 || v == float64(int64(v)):
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func column(runs []Stats, pick func(Stats) float64) []float64 {
	out := make([]float64, len(runs))
	for i, r := range runs {
		out[i] = pick(r)
	}
	return out
}

var groupTitles = map[string]string{
	"startup": "Startup and migrations",
	"read":    "Read endpoints, one client",
	"mix":     "Read mix",
	"sweep":   "Concurrency sweep on the read mix",
	"poll":    "UI polling, idle against a library scan writing (#2147)",
	"search":  "Indexer search fan-out (#3455)",
	"scan":    "Library scan",
	"import":  "Import through the download client",
	"memory":  "Memory and database size",
}

var groupOrder = []string{"startup", "read", "mix", "sweep", "poll", "search", "scan", "import", "memory"}

// Markdown renders the summary. Every latency cell is the median over the
// repetitions followed by the smallest and largest repetition.
func (r *Results) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Bindery performance run: %s\n\n", r.Mode)
	fmt.Fprintf(&b, "Commit `%s`, started %s, wall time %s. Each scenario ran %d times; cells show the median with the smallest and largest run in brackets. Latencies are milliseconds.\n\n",
		r.Commit, r.StartedAt.UTC().Format("2006-01-02 15:04 UTC"), (time.Duration(r.WallSeconds) * time.Second).String(), r.Repetitions)
	b.WriteString("## Host\n\n| Fact | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| CPU | %s |\n| Cores | %d |\n| RAM | %.0f MB |\n| Kernel | %s |\n| Go | %s |\n| Load average at start | %s |\n| Load average at end | %s |\n",
		r.Host.CPUModel, r.Host.Cores, r.Host.RAMMB, r.Host.Kernel, r.Host.GoVersion, r.Host.LoadStart, r.Host.LoadEnd)
	fmt.Fprintf(&b, "| Fake indexer latency | %s ms |\n| Fake download client latency | %s ms |\n| Outbound requests refused | %d |\n\n",
		num(r.IndexerLatencyMs), num(r.ClientLatencyMs), r.BlockedOutbound)

	if len(r.Seeds) > 0 {
		b.WriteString("## Seeded libraries\n\n| Books | Variant | Authors | Editions | Series | Book files | History | Downloads | Queue | Files on disk |\n|---|---|---|---|---|---|---|---|---|---|\n")
		for _, s := range r.Seeds {
			variant := "tracked"
			if s.Spec.Untracked {
				variant = "untracked"
			}
			fmt.Fprintf(&b, "| %d | %s | %d | %d | %d | %d | %d | %d | %d | %d |\n", s.Books, variant, s.Authors, s.Editions, s.Series, s.BookFiles, s.History, s.Downloads, s.Queue, s.TreeFiles)
		}
		b.WriteString("\n")
	}

	for _, g := range groupOrder {
		var lat, met []*Record
		for _, rec := range r.Records {
			if rec.Group != g {
				continue
			}
			if len(rec.Runs) > 0 {
				lat = append(lat, rec)
			}
			if len(rec.Metrics) > 0 {
				met = append(met, rec)
			}
		}
		if len(lat)+len(met) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", groupTitles[g])
		if len(lat) > 0 {
			b.WriteString("| Scenario | Books | Parameters | Requests | p50 | p95 | p99 | Requests per second | Errors |\n|---|---|---|---|---|---|---|---|---|\n")
			for _, rec := range lat {
				reqs, errs := 0, 0
				for _, run := range rec.Runs {
					reqs += run.Requests
					errs += run.Errors
				}
				fmt.Fprintf(&b, "| %s | %d | %s | %d | %s | %s | %s | %s | %d |\n", rec.Name, rec.Books, rec.Params, reqs,
					spread(column(rec.Runs, func(s Stats) float64 { return s.P50 })),
					spread(column(rec.Runs, func(s Stats) float64 { return s.P95 })),
					spread(column(rec.Runs, func(s Stats) float64 { return s.P99 })),
					spread(column(rec.Runs, func(s Stats) float64 { return s.RPS })), errs)
			}
			b.WriteString("\n")
		}
		if len(met) > 0 {
			b.WriteString("| Scenario | Books | Parameters | Metric | Value |\n|---|---|---|---|---|\n")
			for _, rec := range met {
				keys := make([]string, 0, len(rec.Metrics))
				for k := range rec.Metrics {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", rec.Name, rec.Books, rec.Params, k, spread(rec.Metrics[k]))
				}
			}
			b.WriteString("\n")
		}
		if g == "poll" {
			b.WriteString(r.pollComparison())
		}
	}

	if len(r.Profiles) > 0 {
		b.WriteString("## Profiles\n\nOpen one with `go tool pprof -http=: <binary> <file>`. The binary is the one named in results.json.\n\n| Scenario | Kind | File |\n|---|---|---|\n")
		for _, p := range r.Profiles {
			fmt.Fprintf(&b, "| %s | %s | `%s` |\n", p.Scenario, p.Kind, p.File)
		}
		b.WriteString("\n")
	}
	for _, sec := range []struct {
		title string
		items []string
	}{{"Skipped", r.Skipped}, {"Failures", r.Failures}} {
		if len(sec.items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", sec.title)
		for _, s := range sec.items {
			fmt.Fprintf(&b, "- %s\n", s)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// pollComparison lines each polled request up against itself: the same tabs,
// first on an idle instance and then while the scan writes.
func (r *Results) pollComparison() string {
	type key struct {
		name, params string
		books        int
	}
	idle, busy := map[key]*Record{}, map[key]*Record{}
	var order []key
	for _, rec := range r.Records {
		if rec.Group != "poll" || len(rec.Runs) == 0 {
			continue
		}
		name, state, ok := strings.Cut(rec.Name, ", ")
		if !ok {
			continue
		}
		k := key{name, rec.Params, rec.Books}
		if state == "idle" {
			idle[k] = rec
			order = append(order, k)
		} else {
			busy[k] = rec
		}
	}
	if len(order) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Same request, idle against writing. A ratio near 1 means the reads did not wait for the writer.\n\n| Request | Books | Parameters | p50 idle | p50 writing | p95 idle | p95 writing | p95 ratio |\n|---|---|---|---|---|---|---|---|\n")
	for _, k := range order {
		i, w := idle[k], busy[k]
		if w == nil {
			continue
		}
		i50, w50 := median(column(i.Runs, func(s Stats) float64 { return s.P50 })), median(column(w.Runs, func(s Stats) float64 { return s.P50 }))
		i95, w95 := median(column(i.Runs, func(s Stats) float64 { return s.P95 })), median(column(w.Runs, func(s Stats) float64 { return s.P95 }))
		ratio := "n/a"
		if i95 > 0 {
			ratio = num(w95 / i95)
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s |\n", k.name, k.books, k.params, num(i50), num(w50), num(i95), num(w95), ratio)
	}
	b.WriteString("\n")
	return b.String()
}

// Write stores results.json and summary.md in dir.
func (r *Results) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(r.Markdown()), 0o644)
}
