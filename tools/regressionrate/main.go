// Command regressionrate measures how many fix commits repair a bug that a
// recent release introduced, as opposed to an old latent bug that a user
// finally hit.
//
// The raw ratio of fix to feat subjects mixes the two. This tool separates
// them from git history alone: for every fix commit it blames the source
// lines the fix deleted or rewrote, maps both the fix and the blamed commits
// to the first release tag that shipped them, and classifies the fix by how
// many releases old the code it repaired was.
//
//	go run ./tools/regressionrate -out docs/regression-rate.md
//
// It shells out to git and nothing else, so it runs against any clone with
// tags fetched. See docs/regression-rate.md for the method in plain words.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Class is the age bucket a fix lands in.
type Class int

const (
	// Unreleased means the bug was introduced and fixed inside one release
	// cycle, so no release ever shipped it.
	Unreleased Class = iota
	// Regression1 means the bug shipped in the release right before the fix.
	Regression1
	// Regression2 means the bug shipped two releases before the fix.
	Regression2
	// Latent means the bug is older than that.
	Latent
	numClasses
)

func (c Class) String() string {
	return [...]string{"unreleased", "regression_1", "regression_2", "latent"}[c]
}

// classify buckets a fix by the distance, in releases, between the release
// that shipped the fix and the release that first shipped the blamed code.
// A negative distance can only come from cherry pick mapping pulling the fix
// onto an earlier release branch than the blamed code reached through main,
// and means the two shipped together.
func classify(fixRel, introRel int) Class {
	switch d := fixRel - introRel; {
	case d <= 0:
		return Unreleased
	case d == 1:
		return Regression1
	case d == 2:
		return Regression2
	default:
		return Latent
	}
}

var semverTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// release is one version tag.
type release struct {
	name                string
	major, minor, patch int
}

func (r release) line() string { return fmt.Sprintf("v%d.%d.x", r.major, r.minor) }

// sortReleases parses vX.Y.Z tags, drops anything else, and sorts them in
// version order.
func sortReleases(tags []string) []release {
	var out []release
	for _, t := range tags {
		m := semverTag.FindStringSubmatch(strings.TrimSpace(t))
		if m == nil {
			continue
		}
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		c, _ := strconv.Atoi(m[3])
		out = append(out, release{name: m[0], major: a, minor: b, patch: c})
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.major != y.major {
			return x.major < y.major
		}
		if x.minor != y.minor {
			return x.minor < y.minor
		}
		return x.patch < y.patch
	})
	return out
}

// git runs git in a fixed directory.
type git struct{ dir string }

func (g git) out(args ...string) (string, error) {
	full := append([]string{"-C", g.dir, "-c", "core.quotepath=off"}, args...)
	cmd := exec.CommandContext(context.Background(), "git", full...) //nolint:gosec // fixed binary, argv built from git's own output
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

// releaseMap maps a commit to the index of the first release that contains
// it. Commits no tag contains map to len(releases), the release in progress.
type releaseMap struct {
	releases []release
	first    map[string]int
	// bySubject maps a subject carrying a PR number to the earliest release
	// any commit with that subject reached. Hotfix releases are cut from
	// release branches by cherry pick, so the copy on main reaches a tag
	// later than the copy users actually got.
	bySubject map[string]int
	subject   map[string]string
	// when is the committer time, used for the calendar age view.
	when map[string]int64
}

var prRef = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// buildReleaseMap walks tags in version order once. Each tag claims the
// commits it reaches that no earlier tag reaches, which is the same answer
// as git tag --contains for every commit at a fraction of the cost.
func buildReleaseMap(g git, releases []release) (*releaseMap, error) {
	rm := &releaseMap{releases: releases, first: map[string]int{}, bySubject: map[string]int{}, subject: map[string]string{}, when: map[string]int64{}}
	for i, r := range releases {
		args := []string{"log", "--format=%H%x00%ct%x00%s", r.name}
		for _, p := range releases[:i] {
			args = append(args, "^"+p.name)
		}
		out, err := g.out(args...)
		if err != nil {
			return nil, err
		}
		for _, ln := range strings.Split(out, "\n") {
			sha, subj, when, ok := splitLog(ln)
			if !ok {
				continue
			}
			if _, seen := rm.first[sha]; seen {
				continue
			}
			rm.first[sha] = i
			rm.when[sha] = when
			rm.subject[sha] = subj
			if prRef.MatchString(subj) {
				if old, ok := rm.bySubject[subj]; !ok || i < old {
					rm.bySubject[subj] = i
				}
			}
		}
	}
	return rm, nil
}

// rel returns the release index for a commit, preferring an earlier release
// branch copy of the same PR when one exists.
func (rm *releaseMap) rel(sha, subject string) int {
	r, ok := rm.first[sha]
	if !ok {
		r = len(rm.releases)
	}
	if subject == "" {
		subject = rm.subject[sha]
	}
	if s, ok := rm.bySubject[subject]; ok && s < r {
		r = s
	}
	return r
}

func (rm *releaseMap) name(i int) string {
	if i >= len(rm.releases) {
		return "next"
	}
	return rm.releases[i].name
}

func (rm *releaseMap) line(i int) string {
	if i >= len(rm.releases) {
		return "next (untagged)"
	}
	return rm.releases[i].line()
}

// splitLog parses one "%H%x00%ct%x00%s" line.
func splitLog(ln string) (sha, subject string, when int64, ok bool) {
	parts := strings.SplitN(ln, "\x00", 3)
	if len(parts) != 3 {
		return "", "", 0, false
	}
	when, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", "", 0, false
	}
	return parts[0], parts[2], when, true
}

var fixSubject = regexp.MustCompile(`^fix(\([^)]*\))?!?:`)

// isSource reports whether a path is non-test Go or TS/TSX source that the
// analysis should blame.
func isSource(p string) bool {
	if !strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") {
		return false
	}
	for _, bad := range []string{"_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx", ".d.ts", "_gen.go", ".gen.go", ".gen.ts", "zz_generated"} {
		if strings.HasSuffix(p, bad) || strings.Contains(p, bad) {
			return false
		}
	}
	for _, dir := range []string{"docs/", "changelog.d/", "vendor/", "vendored/", "node_modules/", "testdata/", "tests/", "web/e2e/", "web/src/test/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return false
		}
	}
	base := p[strings.LastIndex(p, "/")+1:]
	return !strings.HasPrefix(base, "test-") && base != "test-utils.tsx" && base != "test-setup.ts"
}

var areas = []string{"internal/importer", "internal/indexer", "internal/metadata", "internal/api", "internal/abs", "internal/calibre", "internal/downloader", "web/src"}

func areaOf(p string) string {
	for _, a := range areas {
		if strings.HasPrefix(p, a+"/") {
			return a
		}
	}
	return "other"
}

// fileChange is what one fix did to one pre-existing file.
type fileChange struct {
	path    string
	deleted [][2]int // start line and count in the parent
	inserts []int    // pure insertions go after this parent line
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+\d+(?:,\d+)? @@`)

// parseDiff reads git diff -U0 output and returns the old side of every
// hunk, keyed by the path in the parent. New files have no old side and are
// skipped; they can only ever be additive.
func parseDiff(diff string) []fileChange {
	var out []fileChange
	var cur *fileChange
	// File headers only appear between "diff --git" and the first hunk. A
	// deleted line whose text starts with "-- " also reads "--- ..." and
	// must not open a new file.
	inHeader := false
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		ln := sc.Text()
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			cur = nil
			inHeader = true
		case inHeader && ln == "--- /dev/null":
			cur = nil
		case inHeader && strings.HasPrefix(ln, "--- a/"):
			out = append(out, fileChange{path: strings.TrimPrefix(ln, "--- a/")})
			cur = &out[len(out)-1]
		case strings.HasPrefix(ln, "@@ "):
			inHeader = false
			if cur == nil {
				continue
			}
			m := hunkHeader.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			n := 1
			if m[2] != "" {
				n, _ = strconv.Atoi(m[2])
			}
			if n == 0 {
				cur.inserts = append(cur.inserts, start)
			} else {
				cur.deleted = append(cur.deleted, [2]int{start, n})
			}
		}
	}
	return out
}

// blamedLine is one parent line a fix touched, and who wrote it.
type blamedLine struct {
	sha     string
	content string
}

// parseBlame reads git blame --porcelain output.
func parseBlame(out string) []blamedLine {
	var res []blamedLine
	var sha string
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "\t") {
			res = append(res, blamedLine{sha: sha, content: ln[1:]})
			continue
		}
		f := strings.Fields(ln)
		if len(f) >= 3 && len(f[0]) == 40 && isHex(f[0]) {
			sha = f[0]
		}
	}
	return res
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// trivial reports lines whose authorship says nothing about the bug: blank
// lines, lone braces and brackets, and bare import paths. Rewriting a
// function drags these along and they would otherwise vote for whoever last
// reformatted the file.
func trivial(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	if strings.Trim(t, "{}()[];,") == "" {
		return true
	}
	if strings.HasPrefix(t, "//") && len(t) < 4 {
		return true
	}
	if strings.HasPrefix(t, `"`) && strings.HasSuffix(t, `"`) && !strings.Contains(t[1:len(t)-1], `"`) && !strings.Contains(t, " ") {
		return true // import path
	}
	return t == "import (" || t == "} else {" || t == "return" || t == "return nil" || t == "default:" || t == "})" || t == "});"
}

// lineAge is one blamed line reduced to what classification needs.
type lineAge struct {
	sha  string
	rel  int
	area string
}

// Kind says what evidence a fix was classified on.
type Kind int

const (
	// Deleting fixes rewrote or removed existing source lines.
	Deleting Kind = iota
	// Additive fixes only inserted lines; they are classified on the lines
	// around each insertion, which is weaker evidence.
	Additive
	// NoSource fixes touched no blameable source at all (tests, docs, CI,
	// config, SQL migrations, new files only).
	NoSource
	// Tooling fixes repair lint, formatting, tests or CI by their subject.
	// A lint failure a recent commit introduced is real rework but no user
	// ever sees it, and the hand check found these inflating regression_1.
	Tooling
)

// toolingSubject looks only at the scope and the first word of the
// description, so a user facing fix that mentions a linter in passing
// ("close the hole gosec found") still counts.
var toolingSubject = regexp.MustCompile(`(?i)^fix(\((lint|ci|test|tests|build|deps|e2e|vet)\)!?:|(\([^)]*\))?!?:\s*(gofmt|golangci|lint\b|gosec|errcheck|sqlclosecheck|staticcheck|flaky))`)

// fixResult is one classified fix.
type fixResult struct {
	sha, subject, pr string
	rel              int
	kind             Kind
	lines            []lineAge
	youngest         Class
	majority         Class
	area             string
	youngestIntro    string
	// majorityIntro wrote the most blamed lines within the majority class:
	// the single commit the majority rule holds responsible.
	majorityIntro string
	// ageDays is how old the youngest blamed code was, in calendar days,
	// when the fix was committed. Releases here run about one a day, so a
	// release count alone hides how recent "latent" can be.
	ageDays int
	// recentMajority is true when at least half the blamed lines come from
	// code that had shipped and was at most 30 days old.
	recentMajority bool
}

// summarize computes the youngest and majority classes and the dominant area
// from the blamed lines.
func summarize(f *fixResult) {
	if len(f.lines) == 0 {
		return
	}
	var weight [numClasses]int
	areaCount := map[string]int{}
	best := -1
	for _, l := range f.lines {
		c := classify(f.rel, l.rel)
		weight[c]++
		areaCount[l.area]++
		if l.rel > best {
			best = l.rel
			f.youngestIntro = l.sha
		}
	}
	f.youngest = classify(f.rel, best)
	// Ties go to the younger class, so a fix split evenly between new and
	// old code still reads as touching new code.
	f.majority = Unreleased
	for c := Class(1); c < numClasses; c++ {
		if weight[c] > weight[f.majority] {
			f.majority = c
		}
	}
	perIntro := map[string]int{}
	introRel := map[string]int{}
	for _, l := range f.lines {
		if classify(f.rel, l.rel) == f.majority {
			perIntro[l.sha]++
			introRel[l.sha] = l.rel
		}
	}
	most := 0
	for sha, n := range perIntro {
		if n > most || (n == most && (introRel[sha] > introRel[f.majorityIntro] || (introRel[sha] == introRel[f.majorityIntro] && sha < f.majorityIntro))) {
			most, f.majorityIntro = n, sha
		}
	}
	top := 0
	for _, a := range append(append([]string{}, areas...), "other") {
		if areaCount[a] > top {
			top, f.area = areaCount[a], a
		}
	}
}

type analyzer struct {
	g  git
	rm *releaseMap
}

func (a *analyzer) blame(parent, path string, ranges [][2]int) ([]blamedLine, error) {
	if len(ranges) == 0 {
		return nil, nil
	}
	args := []string{"blame", "-w", "-M", "-C", "--porcelain"}
	for _, r := range ranges {
		args = append(args, "-L", fmt.Sprintf("%d,+%d", r[0], r[1]))
	}
	args = append(args, parent, "--", path)
	out, err := a.g.out(args...)
	if err != nil {
		return nil, err
	}
	return parseBlame(out), nil
}

// contextRanges turns insertion points into the two lines either side of
// each, clipped to the file.
func contextRanges(inserts []int, fileLen int) [][2]int {
	var out [][2]int
	for _, at := range inserts {
		lo, hi := at-1, at+2
		if lo < 1 {
			lo = 1
		}
		if hi > fileLen {
			hi = fileLen
		}
		if hi >= lo {
			out = append(out, [2]int{lo, hi - lo + 1})
		}
	}
	return out
}

func (a *analyzer) analyze(sha, subject string) (*fixResult, error) {
	f := &fixResult{sha: sha, subject: subject, rel: a.rm.rel(sha, subject), kind: NoSource}
	if m := prRef.FindStringSubmatch(subject); m != nil {
		f.pr = m[1]
	}
	if toolingSubject.MatchString(subject) {
		f.kind = Tooling
		return f, nil
	}
	diff, err := a.g.out("diff", "-U0", "--no-color", "--no-ext-diff", "-M", sha+"^", sha, "--")
	if err != nil {
		return nil, err
	}
	var changes []fileChange
	for _, c := range parseDiff(diff) {
		if isSource(c.path) {
			changes = append(changes, c)
		}
	}
	keep := func(bl []blamedLine, path string) {
		for _, b := range bl {
			if trivial(b.content) {
				continue
			}
			f.lines = append(f.lines, lineAge{sha: b.sha, rel: a.rm.rel(b.sha, ""), area: areaOf(path)})
		}
	}
	for _, c := range changes {
		bl, err := a.blame(sha+"^", c.path, c.deleted)
		if err != nil {
			return nil, err
		}
		keep(bl, c.path)
	}
	if len(f.lines) > 0 {
		f.kind = Deleting
		summarize(f)
		f.ageDays = a.age(f)
		return f, nil
	}
	for _, c := range changes {
		if len(c.inserts) == 0 {
			continue
		}
		body, err := a.g.out("cat-file", "blob", sha+"^:"+c.path)
		if err != nil {
			return nil, err
		}
		n := strings.Count(body, "\n")
		if !strings.HasSuffix(body, "\n") && body != "" {
			n++
		}
		bl, err := a.blame(sha+"^", c.path, contextRanges(c.inserts, n))
		if err != nil {
			return nil, err
		}
		keep(bl, c.path)
	}
	if len(f.lines) > 0 {
		f.kind = Additive
		summarize(f)
		f.ageDays = a.age(f)
	}
	return f, nil
}

func (a *analyzer) age(f *fixResult) int {
	recent := 0
	for _, l := range f.lines {
		if l.rel < f.rel && a.rm.when[f.sha]-a.rm.when[l.sha] <= 30*86400 {
			recent++
		}
	}
	f.recentMajority = 2*recent >= len(f.lines)
	d := a.rm.when[f.sha] - a.rm.when[f.youngestIntro]
	if d < 0 {
		return 0
	}
	return int(d / 86400)
}

func main() {
	repo := flag.String("repo", ".", "path to a clone with tags fetched")
	ref := flag.String("ref", "HEAD", "branch to analyse")
	outPath := flag.String("out", "", "write the Markdown report here (default stdout)")
	dump := flag.String("dump", "", "also write one TSV row per fix here, for spot checks")
	workers := flag.Int("j", 2, "parallel git workers")
	notes := flag.String("notes", "", "Markdown file appended verbatim to the report (the hand check)")
	flag.Parse()
	if err := run(*repo, *ref, *outPath, *dump, *notes, *workers); err != nil {
		fmt.Fprintln(os.Stderr, "regressionrate:", err)
		os.Exit(1)
	}
}

func run(repo, ref, outPath, dump, notesPath string, workers int) error {
	g := git{dir: repo}
	tagOut, err := g.out("tag", "-l", "v*")
	if err != nil {
		return err
	}
	releases := sortReleases(strings.Split(tagOut, "\n"))
	if len(releases) == 0 {
		return fmt.Errorf("no vX.Y.Z tags in %s; fetch tags first", repo)
	}
	rm, err := buildReleaseMap(g, releases)
	if err != nil {
		return err
	}
	head, err := g.out("rev-parse", ref)
	if err != nil {
		return err
	}
	logOut, err := g.out("log", "--no-merges", "--format=%H%x00%ct%x00%s", ref)
	if err != nil {
		return err
	}
	var fixes, all [][2]string
	for _, ln := range strings.Split(logOut, "\n") {
		sha, subj, when, ok := splitLog(ln)
		if !ok {
			continue
		}
		all = append(all, [2]string{sha, subj})
		if _, ok := rm.subject[sha]; !ok {
			rm.subject[sha] = subj
			rm.when[sha] = when
		}
		if fixSubject.MatchString(subj) {
			fixes = append(fixes, [2]string{sha, subj})
		}
	}
	results, err := analyzeAll(&analyzer{g: g, rm: rm}, fixes, workers)
	if err != nil {
		return err
	}
	var notes string
	if notesPath != "" {
		b, err := os.ReadFile(notesPath)
		if err != nil {
			return err
		}
		notes = string(b)
	}
	var w io.Writer = os.Stdout
	if outPath != "" {
		fh, err := os.Create(outPath)
		if err != nil {
			return err
		}
		defer fh.Close()
		w = fh
	}
	span := 0
	first, err1 := g.out("log", "-1", "--format=%ct", releases[0].name)
	last, err2 := g.out("log", "-1", "--format=%ct", releases[len(releases)-1].name)
	if err1 == nil && err2 == nil {
		a, _ := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
		b, _ := strconv.ParseInt(strings.TrimSpace(last), 10, 64)
		span = int((b - a) / 86400)
	}
	rep := report{spanDays: span, rm: rm, fixes: results, head: strings.TrimSpace(head), date: time.Now().UTC().Format("2006-01-02"), commits: len(all), notes: notes}
	if err := rep.write(w); err != nil {
		return err
	}
	if dump != "" {
		return writeDump(dump, rm, results)
	}
	return nil
}

func analyzeAll(a *analyzer, fixes [][2]string, workers int) ([]*fixResult, error) {
	if workers < 1 {
		workers = 1
	}
	results := make([]*fixResult, len(fixes))
	errs := make([]error, len(fixes))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i], errs[i] = a.analyze(fixes[i][0], fixes[i][1])
			}
		}()
	}
	for i := range fixes {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("fix %s: %w", fixes[i][0][:9], err)
		}
	}
	return results, nil
}

func writeDump(path string, rm *releaseMap, results []*fixResult) error {
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	bw := bufio.NewWriter(fh)
	w := &mdWriter{w: bw}
	w.f("%s\n", "fix\tfix_tag\tkind\tyoungest\tmajority\tarea\tyoungest_intro\tintro_tag\tage_days\tlines\tsubject")
	for _, f := range results {
		kind := [...]string{"deleting", "additive", "no_source", "tooling"}[f.kind]
		intro, introTag := "", ""
		if f.youngestIntro != "" {
			intro, introTag = f.youngestIntro[:9], rm.name(rm.rel(f.youngestIntro, ""))
		}
		w.f("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n", f.sha[:9], rm.name(f.rel), kind, f.youngest, f.majority, f.area, intro, introTag, f.ageDays, len(f.lines), f.subject)
	}
	if w.err != nil {
		return w.err
	}
	return bw.Flush()
}
