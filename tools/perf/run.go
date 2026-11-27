package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Mode sizes a run. quick proves every scenario in a few minutes on the
// smallest library; full is the measurement; tiny exists for the harness's own
// test.
type Mode struct {
	Name string
	// ReadSizes are the library sizes the read scenarios run against.
	ReadSizes []int
	Reps      int
	// EndpointDur is how long each read endpoint is driven by one client.
	EndpointDur time.Duration
	MixDur      time.Duration
	MixClients  int
	SweepDur    time.Duration
	Sweep       []int
	// PollSize is the library the polling comparison runs against, PollTabs
	// the simulated browser tabs, PollDur the length of each of its two halves.
	PollSize int
	PollTabs []int
	PollDur  time.Duration
	// SearchSize is the library the search fan-out runs against, Indexers the
	// indexer counts, Searches the searches per repetition.
	SearchSize  int
	Indexers    []int
	Searches    int
	ScanSizes   []int
	ImportSizes []int
	// ImportCap ends an import repetition that has not finished.
	ImportCap  time.Duration
	ProfileDur time.Duration
}

func modeByName(name string) (Mode, error) {
	switch name {
	case "tiny":
		return Mode{Name: name, ReadSizes: []int{60}, Reps: 1, EndpointDur: 100 * time.Millisecond, MixDur: 200 * time.Millisecond, MixClients: 2,
			SweepDur: 150 * time.Millisecond, Sweep: []int{1, 4}, PollSize: 60, PollTabs: []int{4}, PollDur: 1500 * time.Millisecond,
			SearchSize: 60, Indexers: []int{1, 3}, Searches: 2, ScanSizes: []int{60}, ImportSizes: []int{60}, ImportCap: 2 * time.Minute, ProfileDur: time.Second}, nil
	case "quick":
		return Mode{Name: name, ReadSizes: []int{1000}, Reps: 3, EndpointDur: 600 * time.Millisecond, MixDur: 2 * time.Second, MixClients: 4,
			SweepDur: time.Second, Sweep: []int{1, 4, 16, 64}, PollSize: 1000, PollTabs: []int{20}, PollDur: 5 * time.Second,
			SearchSize: 1000, Indexers: []int{1, 5, 20}, Searches: 3, ScanSizes: []int{1000}, ImportSizes: []int{1000}, ImportCap: 3 * time.Minute, ProfileDur: 3 * time.Second}, nil
	case "full":
		return Mode{Name: name, ReadSizes: []int{1000, 10000, 40000}, Reps: 3, EndpointDur: 3 * time.Second, MixDur: 10 * time.Second, MixClients: 4,
			SweepDur: 8 * time.Second, Sweep: []int{1, 4, 16, 64}, PollSize: 10000, PollTabs: []int{1, 10, 50}, PollDur: 30 * time.Second,
			SearchSize: 10000, Indexers: []int{1, 5, 20}, Searches: 10, ScanSizes: []int{1000, 10000}, ImportSizes: []int{1000, 10000}, ImportCap: 8 * time.Minute, ProfileDur: 15 * time.Second}, nil
	}
	return Mode{}, fmt.Errorf("unknown mode %q (want quick, full or tiny)", name)
}

// Config is one invocation of `perf run`.
type Config struct {
	Mode           Mode
	DataDir        string
	Binary         string
	OutDir         string
	Only           string
	IndexerLatency time.Duration
	ClientLatency  time.Duration
	Seed           uint64
	NoProfiles     bool
	// LogLevel is passed to the binary. Anything below info changes what is
	// measured, because every log line is also written to the database.
	LogLevel string
}

type runner struct {
	cfg   Config
	logf  func(string, ...any)
	res   *Results
	trap  *OutboundTrap
	only  *regexp.Regexp
	seeds map[string]*SeedManifest
	boots int
}

// Run executes every scenario group the configuration selects and writes the
// results directory. A scenario that fails is recorded and the run carries on;
// the error returned at the end names how many did.
func Run(ctx context.Context, cfg Config, logf func(string, ...any)) error {
	r := &runner{cfg: cfg, logf: logf, seeds: map[string]*SeedManifest{}}
	if cfg.Only != "" {
		re, err := regexp.Compile(cfg.Only)
		if err != nil {
			return fmt.Errorf("-only: %w", err)
		}
		r.only = re
	}
	started := time.Now()
	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join(cfg.DataDir, "results", started.Format("20060102-150405")+"-"+cfg.Mode.Name)
		r.cfg.OutDir = cfg.OutDir
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}
	if cfg.Binary == "" {
		r.cfg.Binary = filepath.Join(cfg.DataDir, "bin", "bindery-perf")
		logf("building %s", r.cfg.Binary)
		if err := BuildBinary(ctx, r.cfg.Binary); err != nil {
			return err
		}
	}
	trap, err := NewOutboundTrap()
	if err != nil {
		return err
	}
	defer trap.Close()
	r.trap = trap
	r.res = &Results{Mode: cfg.Mode.Name, Commit: gitCommit(), Binary: r.cfg.Binary, StartedAt: started, Host: readHostFacts(),
		IndexerLatencyMs: ms(cfg.IndexerLatency), ClientLatencyMs: ms(cfg.ClientLatency), Repetitions: cfg.Mode.Reps}

	groups := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"startup", r.startup}, {"read", r.read}, {"poll", r.poll}, {"search", r.search},
		{"scan", r.scan}, {"import", r.importFiles}, {"profile", r.profile},
	}
	for _, g := range groups {
		if ctx.Err() != nil {
			break
		}
		if r.only != nil && !r.only.MatchString(g.name) {
			continue
		}
		if g.name == "profile" && cfg.NoProfiles {
			r.res.Skipped = append(r.res.Skipped, "profile: disabled with -no-profiles")
			continue
		}
		t := time.Now()
		logf("== %s", g.name)
		if err := g.fn(ctx); err != nil {
			r.fail("%s: %v", g.name, err)
		}
		logf("== %s done in %s", g.name, time.Since(t).Round(time.Millisecond))
	}
	r.res.Skipped = append(r.res.Skipped,
		"author refresh and metadata lookups: the provider base URLs are constants in internal/metadata, so the binary cannot be pointed at a fake metadata server without a code change. Outbound requests are refused by a local proxy instead.",
		"migration replay on a populated database: the seeder writes the current schema, so only a new install runs every migration. Startup on a seeded database measures the migration check and the startup sweeps.",
	)
	r.res.FinishedAt = time.Now()
	r.res.WallSeconds = time.Since(started).Seconds()
	r.res.Host.LoadEnd = loadAverage()
	r.res.BlockedOutbound = trap.Blocked()
	names := make([]string, 0, len(r.seeds))
	for k := range r.seeds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		r.res.Seeds = append(r.res.Seeds, *r.seeds[k])
	}
	if err := r.res.Write(cfg.OutDir); err != nil {
		return err
	}
	logf("results in %s (summary.md, results.json), wall time %s", cfg.OutDir, time.Since(started).Round(time.Second))
	if len(r.res.Failures) > 0 {
		return fmt.Errorf("%d scenario failure(s), first: %s", len(r.res.Failures), r.res.Failures[0])
	}
	return ctx.Err()
}

func (r *runner) fail(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.logf("FAIL %s", msg)
	r.res.Failures = append(r.res.Failures, msg)
}

func (r *runner) record(group, name string, books int, params string) *Record {
	for _, rec := range r.res.Records {
		if rec.Group == group && rec.Name == name && rec.Books == books && rec.Params == params {
			return rec
		}
	}
	rec := &Record{Group: group, Name: name, Books: books, Params: params}
	r.res.Records = append(r.res.Records, rec)
	return rec
}

func (rec *Record) metric(name string, v float64) {
	if rec.Metrics == nil {
		rec.Metrics = map[string][]float64{}
	}
	rec.Metrics[name] = append(rec.Metrics[name], v)
}

func (r *runner) seed(ctx context.Context, books int, untracked bool) (*SeedManifest, error) {
	dir := seedDir(r.cfg.DataDir, books, untracked)
	if m, ok := r.seeds[dir]; ok {
		return m, nil
	}
	lib := libraryDir(r.cfg.DataDir, books, untracked)
	m, err := Seed(ctx, dir, SeedSpec{Books: books, Seed: r.cfg.Seed, Untracked: untracked, LibraryDir: lib}, r.logf)
	if err != nil {
		return nil, err
	}
	r.seeds[dir] = m
	return m, nil
}

// boot starts an instance on a copy of the seed (or a new install when m is
// nil) and waits for its startup work to settle.
func (r *runner) boot(ctx context.Context, m *SeedManifest, o InstanceOpts) (*Instance, *APIClient, error) {
	r.boots++
	o.Binary = r.cfg.Binary
	o.WorkDir = filepath.Join(r.cfg.DataDir, "work", fmt.Sprintf("run-%d", r.boots))
	o.ProxyURL = fmt.Sprintf("http://127.0.0.1:%d", r.trap.Port)
	o.LogLevel = r.cfg.LogLevel
	o.LogCopy = filepath.Join(r.cfg.OutDir, "logs", fmt.Sprintf("run-%d.log", r.boots))
	if m != nil {
		o.SeedDB = m.DBPath
		if o.LibraryDir == "" {
			o.LibraryDir = m.Spec.LibraryDir
		}
	}
	if o.LibraryDir == "" {
		o.LibraryDir = filepath.Join(o.WorkDir, "library")
	}
	if o.DownloadDir == "" {
		o.DownloadDir = filepath.Join(o.WorkDir, "downloads")
	}
	inst, err := StartInstance(ctx, o)
	if err != nil {
		return nil, nil, err
	}
	return inst, NewAPIClient(inst.Base, apiKey), nil
}

// finish records what the instance cost in memory and disk, then stops it.
func (r *runner) finish(inst *Instance, label string, books int) {
	rss, peak := inst.Memory()
	rec := r.record("memory", label, books, "")
	rec.metric("peak RSS MB", peak)
	rec.metric("RSS MB at end", rss)
	rec.metric("database MB", inst.DBSizeMB())
	inst.Stop()
}

func (r *runner) startup(ctx context.Context) error {
	for rep := range r.cfg.Mode.Reps {
		inst, _, err := r.boot(ctx, nil, InstanceOpts{})
		if err != nil {
			return fmt.Errorf("new install: %w", err)
		}
		rec := r.record("startup", "new install, every migration", 0, "")
		rec.metric("ready ms", ms(inst.Boot))
		rec.metric("settle s", inst.WaitQuiet(ctx, 30*time.Second).Seconds())
		if rep == r.cfg.Mode.Reps-1 {
			r.finish(inst, "new install", 0)
		} else {
			inst.Stop()
		}
	}
	for _, size := range r.cfg.Mode.ReadSizes {
		m, err := r.seed(ctx, size, false)
		if err != nil {
			return err
		}
		for range r.cfg.Mode.Reps {
			inst, _, err := r.boot(ctx, m, InstanceOpts{})
			if err != nil {
				return fmt.Errorf("startup %d: %w", size, err)
			}
			rec := r.record("startup", "seeded library", size, "")
			rec.metric("ready ms", ms(inst.Boot))
			rec.metric("settle s", inst.WaitQuiet(ctx, 60*time.Second).Seconds())
			inst.Stop()
		}
		r.logf("startup %d books: ready ms %s", size, spread(r.record("startup", "seeded library", size, "").Metrics["ready ms"]))
	}
	return nil
}

// endpoint is one read the web UI or a reading app makes.
type endpoint struct {
	name   string
	weight int
	path   PathFunc
}

func fixed(p string) PathFunc { return func(*rand.Rand) string { return p } }

// readEndpoints is the read surface of the UI pages and the OPDS catalogue,
// with ids, offsets and search terms drawn from the seed.
func readEndpoints(m *SeedManifest) []endpoint {
	id := func(n int) func(*rand.Rand) int { return func(r *rand.Rand) int { return 1 + r.IntN(max(n, 1)) } }
	author, book, series := id(m.Authors), id(m.Books), id(m.Series)
	term := func(r *rand.Rand) string { return url.QueryEscape(m.SearchTerms[r.IntN(len(m.SearchTerms))]) }
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	calendar := fmt.Sprintf("/api/v1/book?releaseFrom=%s&releaseBefore=%s&limit=500", monthStart.Format("2006-01-02"), monthStart.AddDate(0, 1, 0).Format("2006-01-02"))
	return []endpoint{
		{"authors list", 6, fixed("/api/v1/author?limit=50&offset=0")},
		{"authors list, search", 2, func(r *rand.Rand) string { return "/api/v1/author?limit=50&search=" + term(r) }},
		{"authors list, 500 per page", 1, fixed("/api/v1/author?limit=500&offset=0")},
		{"author detail", 6, func(r *rand.Rand) string { return fmt.Sprintf("/api/v1/author/%d", author(r)) }},
		{"author detail, books of author", 6, func(r *rand.Rand) string { return fmt.Sprintf("/api/v1/book?authorId=%d&limit=500", author(r)) }},
		{"books list, first page", 8, fixed("/api/v1/book?limit=50&offset=0&sort=title-az")},
		{"books list, deep page", 4, func(r *rand.Rand) string {
			return fmt.Sprintf("/api/v1/book?limit=50&offset=%d&sort=title-az", r.IntN(max(m.Books-50, 1)))
		}},
		{"books list, newest first", 3, fixed("/api/v1/book?limit=50&offset=0&sort=date-new")},
		{"books list, by author, deep page", 2, func(r *rand.Rand) string {
			return fmt.Sprintf("/api/v1/book?limit=50&offset=%d&sort=author-az", r.IntN(max(m.Books-50, 1)))
		}},
		{"books list, status filter", 3, fixed("/api/v1/book?limit=50&status=wanted&sort=title-az")},
		{"books list, media and language filter", 2, fixed("/api/v1/book?limit=50&mediaType=audiobook&language=eng&sort=title-az")},
		{"books list, search", 4, func(r *rand.Rand) string { return "/api/v1/book?limit=50&sort=title-az&search=" + term(r) }},
		{"book detail", 8, func(r *rand.Rand) string { return fmt.Sprintf("/api/v1/book/%d", book(r)) }},
		{"series list", 2, fixed("/api/v1/series")},
		{"series list, paged", 3, fixed("/api/v1/series?limit=50&offset=0")},
		{"series detail", 3, func(r *rand.Rand) string { return fmt.Sprintf("/api/v1/series/%d", series(r)) }},
		{"wanted", 4, fixed("/api/v1/wanted/missing?limit=50&offset=0&sort=title-az")},
		{"calendar month", 2, fixed(calendar)},
		{"history, first page", 3, fixed("/api/v1/history?limit=50&offset=0")},
		{"history, event type filter", 2, fixed("/api/v1/history?limit=50&offset=0&eventType=bookImported")},
		{"queue", 10, fixed("/api/v1/queue")},
		{"library search", 4, func(r *rand.Rand) string { return "/api/v1/search/library?q=" + term(r) }},
		{"OPDS root", 2, fixed("/opds/")},
		{"OPDS authors", 2, fixed("/opds/authors")},
		{"OPDS one author", 2, func(r *rand.Rand) string { return fmt.Sprintf("/opds/authors/%d", author(r)) }},
		{"OPDS series", 1, fixed("/opds/series")},
		{"OPDS one series", 1, func(r *rand.Rand) string { return fmt.Sprintf("/opds/series/%d", series(r)) }},
		{"OPDS recent", 2, fixed("/opds/recent")},
		{"OPDS search", 1, func(r *rand.Rand) string { return "/opds/search?q=" + term(r) }},
	}
}

// mixOf returns a PathFunc that draws endpoints in proportion to their weight.
func mixOf(eps []endpoint) PathFunc {
	total := 0
	for _, e := range eps {
		total += e.weight
	}
	return func(r *rand.Rand) string {
		n := r.IntN(total)
		for _, e := range eps {
			if n < e.weight {
				return e.path(r)
			}
			n -= e.weight
		}
		return eps[0].path(r)
	}
}

func (r *runner) read(ctx context.Context) error {
	mode := r.cfg.Mode
	for _, size := range mode.ReadSizes {
		m, err := r.seed(ctx, size, false)
		if err != nil {
			return err
		}
		inst, c, err := r.boot(ctx, m, InstanceOpts{})
		if err != nil {
			return fmt.Errorf("read %d: %w", size, err)
		}
		inst.WaitQuiet(ctx, 60*time.Second)
		eps := readEndpoints(m)
		// One request each before measuring: it fails loudly on a route that
		// has moved, and the first repetition does not pay for cold caches
		// the others do not.
		warm := rand.New(rand.NewPCG(r.cfg.Seed, 99)) //nolint:gosec // G404: request order, not a secret
		for _, e := range eps {
			if _, _, err := c.Do(ctx, http.MethodGet, e.path(warm), nil); err != nil {
				r.fail("read %d: %s: %v", size, e.name, err)
			}
		}
		for rep := range mode.Reps {
			for i, e := range eps {
				st := RunLoad(ctx, c, 1, mode.EndpointDur, r.cfg.Seed+uint64(rep*1000+i), e.path)
				rec := r.record("read", e.name, size, "")
				rec.Runs = append(rec.Runs, st)
			}
		}
		mix := mixOf(eps)
		for rep := range mode.Reps {
			rec := r.record("mix", "read mix", size, fmt.Sprintf("%d clients", mode.MixClients))
			rec.Runs = append(rec.Runs, RunLoad(ctx, c, mode.MixClients, mode.MixDur, r.cfg.Seed+uint64(7000+rep), mix))
		}
		for rep := range mode.Reps {
			for _, n := range mode.Sweep {
				rec := r.record("sweep", "read mix", size, fmt.Sprintf("%d clients", n))
				rec.Runs = append(rec.Runs, RunLoad(ctx, c, n, mode.SweepDur, r.cfg.Seed+uint64(8000+rep*100+n), mix)) //nolint:gosec // G115: small positive loop counters
			}
		}
		r.logf("read %d books: mix p95 ms %s", size, spread(column(r.record("mix", "read mix", size, fmt.Sprintf("%d clients", mode.MixClients)).Runs, func(s Stats) float64 { return s.P95 })))
		r.finish(inst, "read scenarios", size)
	}
	return nil
}

// pollSeries is what an open browser tab asks for on its own: the queue every
// five seconds and the author refresh status every two (the intervals in
// web/src), plus one page of books every ten seconds for a user paging through
// the library.
var pollSeries = []PollSeries{
	{Name: "queue poll (5 s)", Path: "/api/v1/queue", Every: 5 * time.Second},
	{Name: "refresh status poll (2 s)", Path: "/api/v1/authors/refresh-all/status", Every: 2 * time.Second},
	{Name: "books page (10 s)", Path: "/api/v1/book?limit=50&offset=0&sort=title-az", Every: 10 * time.Second},
}

// startScan asks for a library scan and returns a function reporting whether
// it has finished. The status route answers 404 until the first scan of an
// instance has stored its result, so a 200 is the finish line.
func startScan(ctx context.Context, c *APIClient) (done func() bool, wait func(time.Duration) (map[string]any, error), err error) {
	if _, _, err := c.Do(ctx, http.MethodPost, "/api/v1/library/scan", nil); err != nil {
		return nil, nil, err
	}
	finished := make(chan struct{})
	var body map[string]any
	var pollErr error
	go func() {
		defer close(finished)
		for ctx.Err() == nil {
			data, _, err := c.Do(ctx, http.MethodGet, "/api/v1/library/scan/status", nil)
			if err == nil {
				var b map[string]any
				if json.Unmarshal(data, &b) == nil && b["running"] != true {
					body = b
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		pollErr = ctx.Err()
	}()
	done = func() bool {
		select {
		case <-finished:
			return true
		default:
			return false
		}
	}
	wait = func(limit time.Duration) (map[string]any, error) {
		select {
		case <-finished:
			return body, pollErr
		case <-time.After(limit):
			return nil, fmt.Errorf("library scan not finished after %s", limit)
		}
	}
	return done, wait, nil
}

// total reads the total of a paged list.
func total(ctx context.Context, c *APIClient, path string) (int, error) {
	data, _, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, err
	}
	var page struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return 0, err
	}
	return page.Total, nil
}

func (r *runner) poll(ctx context.Context) error {
	mode := r.cfg.Mode
	m, err := r.seed(ctx, mode.PollSize, true)
	if err != nil {
		return err
	}
	for _, tabs := range mode.PollTabs {
		params := fmt.Sprintf("%d tabs", tabs)
		for rep := range mode.Reps {
			inst, c, err := r.boot(ctx, m, InstanceOpts{})
			if err != nil {
				return fmt.Errorf("poll: %w", err)
			}
			inst.WaitQuiet(ctx, 60*time.Second)
			seed := r.cfg.Seed + uint64(rep*10+tabs) //nolint:gosec // G115: small positive loop counters
			idle := RunPoll(ctx, c, tabs, mode.PollDur, seed, pollSeries, nil)
			done, wait, err := startScan(ctx, c)
			if err != nil {
				inst.Stop()
				return fmt.Errorf("poll: start scan: %w", err)
			}
			scanStart := time.Now()
			// The second half ends with the scan or after PollDur, whichever
			// is first, and keeps only requests that started while the scan
			// was still writing.
			pollCtx, cancel := context.WithCancel(ctx)
			go func() {
				for !done() && pollCtx.Err() == nil {
					time.Sleep(50 * time.Millisecond)
				}
				cancel()
			}()
			busy := RunPoll(pollCtx, c, tabs, mode.PollDur, seed, pollSeries, func() bool { return !done() })
			cancel()
			overlap := time.Since(scanStart)
			if _, err := wait(15 * time.Minute); err != nil {
				r.fail("poll: %v", err)
			}
			for _, s := range pollSeries {
				ri := r.record("poll", s.Name+", idle", mode.PollSize, params)
				ri.Runs = append(ri.Runs, idle[s.Name])
				rb := r.record("poll", s.Name+", scan writing", mode.PollSize, params)
				rb.Runs = append(rb.Runs, busy[s.Name])
			}
			rec := r.record("poll", "scan under polling", mode.PollSize, params)
			rec.metric("seconds polled while the scan wrote", overlap.Seconds())
			rec.metric("scan wall s", time.Since(scanStart).Seconds())
			if rep == mode.Reps-1 {
				r.finish(inst, "polling under a scan, "+params, mode.PollSize)
			} else {
				inst.Stop()
			}
		}
	}
	return nil
}

func (r *runner) search(ctx context.Context) error {
	mode := r.cfg.Mode
	m, err := r.seed(ctx, mode.SearchSize, false)
	if err != nil {
		return err
	}
	maxIdx := 0
	for _, n := range mode.Indexers {
		maxIdx = max(maxIdx, n)
	}
	fake, err := NewFakeIndexers(maxIdx, r.cfg.IndexerLatency)
	if err != nil {
		return err
	}
	defer fake.Close()
	inst, c, err := r.boot(ctx, m, InstanceOpts{})
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	defer r.finish(inst, "search fan-out", mode.SearchSize)
	inst.WaitQuiet(ctx, 60*time.Second)
	next := 0
	for _, n := range mode.Indexers {
		ids, err := createIndexers(ctx, c, fake, n)
		if err != nil {
			return err
		}
		params := fmt.Sprintf("%d indexers", n)
		for _, miss := range []bool{false, true} {
			name := "book search, releases found"
			if miss {
				name = "book search, nothing found (every fallback query)"
			}
			fake.SetMiss(miss)
			for range mode.Reps {
				st, perSearch := r.searchRun(ctx, c, fake, m, n, &next)
				rec := r.record("search", name, mode.SearchSize, params)
				rec.Runs = append(rec.Runs, st)
				rec.metric("outbound queries per search", perSearch)
				rec.metric("outbound queries per search per indexer", perSearch/float64(n))
			}
		}
		for _, id := range ids {
			if _, _, err := c.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/indexer/%d", id), nil); err != nil {
				return fmt.Errorf("delete indexer: %w", err)
			}
		}
		r.logf("search %d indexers: queries per search, nothing found: %s", n,
			spread(r.record("search", "book search, nothing found (every fallback query)", mode.SearchSize, params).Metrics["outbound queries per search"]))
	}
	return nil
}

func createIndexers(ctx context.Context, c *APIClient, fake *FakeIndexers, n int) ([]int64, error) {
	ids := make([]int64, 0, n)
	for i := range n {
		body, err := json.Marshal(map[string]any{
			"name": fmt.Sprintf("perf-indexer-%d", i+1), "type": "torznab", "url": fake.URL(i), "apiKey": "perf",
			"categories": []int{7000, 7020, 3030}, "priority": 25, "enabled": true, "supportsSearch": true,
		})
		if err != nil {
			return nil, err
		}
		data, _, err := c.Do(ctx, http.MethodPost, "/api/v1/indexer", body)
		if err != nil {
			return nil, fmt.Errorf("create indexer: %w", err)
		}
		var created struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(data, &created); err != nil || created.ID == 0 {
			return nil, fmt.Errorf("create indexer: unexpected response %.200s", data)
		}
		ids = append(ids, created.ID)
	}
	return ids, nil
}

// searchRun runs the mode's number of interactive searches one after another,
// each for the next book id so the per indexer query cache never answers, and
// returns the latencies and the mean number of requests the indexers received
// per search.
func (r *runner) searchRun(ctx context.Context, c *APIClient, fake *FakeIndexers, m *SeedManifest, indexers int, next *int) (Stats, float64) {
	rec := &recorder{}
	sum := func() (t int64) {
		for _, q := range fake.Queries(indexers) {
			t += q
		}
		return t
	}
	before, start := sum(), time.Now()
	for range r.cfg.Mode.Searches {
		id := 1 + *next%m.Books
		*next++
		data, d, err := c.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/book/%d/search", id), nil)
		rec.add(d, int64(len(data)), err)
	}
	return rec.stats(time.Since(start)), float64(sum()-before) / float64(r.cfg.Mode.Searches)
}

func (r *runner) scan(ctx context.Context) error {
	for _, size := range r.cfg.Mode.ScanSizes {
		for _, untracked := range []bool{true, false} {
			m, err := r.seed(ctx, size, untracked)
			if err != nil {
				return err
			}
			name := "rescan, every file already tracked"
			if untracked {
				name = "first scan, no file tracked"
			}
			for rep := range r.cfg.Mode.Reps {
				inst, c, err := r.boot(ctx, m, InstanceOpts{})
				if err != nil {
					return fmt.Errorf("scan %d: %w", size, err)
				}
				inst.WaitQuiet(ctx, 60*time.Second)
				wall, matched, rows, err := r.scanOnce(ctx, c)
				if err != nil {
					r.fail("scan %d: %v", size, err)
					inst.Stop()
					continue
				}
				rec := r.record("scan", name, size, fmt.Sprintf("%d files", m.TreeFiles))
				rec.metric("wall s", wall.Seconds())
				rec.metric("files per s", float64(m.TreeFiles)/wall.Seconds())
				rec.metric("books matched", float64(matched))
				rec.metric("rows written per s (books and history, a lower bound)", float64(rows)/wall.Seconds())
				if rep == r.cfg.Mode.Reps-1 {
					r.finish(inst, name, size)
				} else {
					inst.Stop()
				}
			}
			r.logf("scan %d books, %s: wall s %s", size, name, spread(r.record("scan", name, size, fmt.Sprintf("%d files", m.TreeFiles)).Metrics["wall s"]))
		}
	}
	return nil
}

const (
	importedTotalPath = "/api/v1/book?status=imported&limit=1"
	historyTotalPath  = "/api/v1/history?limit=1"
)

// scanOnce runs one library scan to the end and reports how long it took, how
// many books it turned from wanted to imported and how many book and history
// rows that wrote.
func (r *runner) scanOnce(ctx context.Context, c *APIClient) (wall time.Duration, matched, rows int, err error) {
	books0, err := total(ctx, c, importedTotalPath)
	if err != nil {
		return 0, 0, 0, err
	}
	hist0, err := total(ctx, c, historyTotalPath)
	if err != nil {
		return 0, 0, 0, err
	}
	start := time.Now()
	_, wait, err := startScan(ctx, c)
	if err != nil {
		return 0, 0, 0, err
	}
	if _, err := wait(20 * time.Minute); err != nil {
		return 0, 0, 0, err
	}
	wall = time.Since(start)
	books1, err := total(ctx, c, importedTotalPath)
	if err != nil {
		return 0, 0, 0, err
	}
	hist1, err := total(ctx, c, historyTotalPath)
	if err != nil {
		return 0, 0, 0, err
	}
	return wall, books1 - books0, (books1 - books0) + (hist1 - hist0), nil
}

var errNoProgress = errors.New("no import progress for 90 s")

func (r *runner) importFiles(ctx context.Context) error {
	for _, size := range r.cfg.Mode.ImportSizes {
		m, err := r.seed(ctx, size, true)
		if err != nil {
			return err
		}
		params := fmt.Sprintf("%d downloads", size)
		for rep := range r.cfg.Mode.Reps {
			if err := r.importOnce(ctx, m, size, params, rep == r.cfg.Mode.Reps-1); err != nil {
				r.fail("import %d: %v", size, err)
			}
		}
		r.logf("import %d downloads: wall s %s", size, spread(r.record("import", "finished torrents imported", size, params).Metrics["wall s"]))
	}
	return nil
}

func (r *runner) importOnce(ctx context.Context, m *SeedManifest, size int, params string, last bool) error {
	stage := filepath.Join(r.cfg.DataDir, "work", "import-downloads")
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	fake, err := NewFakeQbittorrent(r.cfg.ClientLatency, stage, importCategory)
	if err != nil {
		return err
	}
	defer fake.Close()
	torrents, err := stageDownloads(m.Spec, stage)
	if err != nil {
		return err
	}
	fake.SetTorrents(torrents)
	inst, c, err := r.boot(ctx, m, InstanceOpts{
		// An empty library of its own: the import writes into it.
		LibraryDir:  filepath.Join(r.cfg.DataDir, "work", "import-library"),
		DownloadDir: stage,
		Prepare:     func(dbPath string) error { return prepareImport(ctx, dbPath, fake.Port, torrents) },
	})
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Join(r.cfg.DataDir, "work", "import-library")) }()
	booted := time.Now()
	imported, lastChange, deadline := 0, time.Now(), time.Now().Add(r.cfg.Mode.ImportCap)
	var stop error
	for imported < len(torrents) && stop == nil {
		time.Sleep(200 * time.Millisecond)
		n, err := total(ctx, c, importedTotalPath)
		switch {
		case err != nil:
			stop = err
		case n != imported:
			imported, lastChange = n, time.Now()
		case time.Since(lastChange) > 90*time.Second:
			stop = errNoProgress
		case time.Now().After(deadline):
			stop = fmt.Errorf("stopped at the %s cap", r.cfg.Mode.ImportCap)
		}
	}
	end := lastChange
	requests, first := fake.Stats()
	rec := r.record("import", "finished torrents imported", size, params)
	if !first.IsZero() && imported > 0 {
		wall := end.Sub(first)
		rec.metric("wall s", wall.Seconds())
		rec.metric("imports per s", float64(imported)/wall.Seconds())
		rec.metric("wait for the first download check s", first.Sub(booted).Seconds())
	}
	rec.metric("books imported", float64(imported))
	rec.metric("download client requests", float64(requests))
	if last {
		r.finish(inst, "import", size)
	} else {
		inst.Stop()
	}
	if stop != nil {
		return fmt.Errorf("%d of %d imported: %w", imported, len(torrents), stop)
	}
	return nil
}
