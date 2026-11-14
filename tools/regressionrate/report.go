package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type report struct {
	rm      *releaseMap
	fixes   []*fixResult
	head    string
	date    string
	commits int
	notes   string
	// spanDays is the time from the first release to the last.
	spanDays int
}

// tally counts fixes per class under both rules.
type tally struct {
	total, noSource, additive, tooling int
	recentMaj                          int
	youngest, majority                 [numClasses]int
	addYoungest                        [numClasses]int
	// ageBucket counts deleting fixes to code that shipped, by the calendar
	// age of the youngest code touched.
	ageBucket [len(ageBuckets)]int
}

// ageBuckets are upper bounds in days, the last one open ended.
var ageBuckets = [...]int{7, 30, 90, 1 << 30}

// recent30 is shipped code at most 30 days old.
func (t *tally) recent30() int { return t.ageBucket[0] + t.ageBucket[1] }

func (t *tally) add(f *fixResult) {
	t.total++
	switch f.kind {
	case NoSource:
		t.noSource++
	case Tooling:
		t.tooling++
	case Additive:
		t.additive++
		t.addYoungest[f.youngest]++
	default:
		t.youngest[f.youngest]++
		t.majority[f.majority]++
		if f.recentMajority {
			t.recentMaj++
		}
		if f.youngest != Unreleased {
			for i, b := range ageBuckets {
				if f.ageDays <= b {
					t.ageBucket[i]++
					break
				}
			}
		}
	}
}

func pct(n, d int) string {
	if d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(d))
}

// share is the regression share: fixes to code that shipped in one of the
// two releases before the fix, over every fix with blameable deleted lines.
func share(c [numClasses]int) (int, int) {
	n := 0
	for _, v := range c {
		n += v
	}
	return c[Regression1] + c[Regression2], n
}

// mdWriter keeps the first write error so the report code can stay a flat
// list of lines.
type mdWriter struct {
	w   io.Writer
	err error
}

func (m *mdWriter) f(format string, a ...any) {
	if m.err == nil {
		_, m.err = fmt.Fprintf(m.w, format, a...)
	}
}

func (r *report) write(out io.Writer) error {
	w := &mdWriter{w: out}
	var all tally
	for _, f := range r.fixes {
		all.add(f)
	}
	p := w.f
	p("# Regression rate\n\n")
	p("Generated %s at main `%s` by `go run ./tools/regressionrate`. Do not edit the tables by hand; rerun the tool.\n\n", r.date, r.head[:12])
	p("%d non merge commits, %d of them `fix` commits, %d release tags from %s to %s.\n\n", r.commits, all.total, len(r.rm.releases), r.rm.releases[0].name, r.rm.releases[len(r.rm.releases)-1].name)

	p("## Method\n\n")
	p("Every non merge commit whose subject starts with `fix` (any scope) is a fix. For each one the tool takes the lines it deleted or rewrote in non test Go and TS/TSX source " +
		"(test files, docs, changelog fragments, vendored and generated code, e2e and test helpers are skipped) and runs `git blame -w -M -C` on the parent to find the commits that wrote those lines. " +
		"Blank lines, lone braces, bare import paths and similar boilerplate are dropped first, because they vote for whoever last reformatted a file rather than whoever wrote the bug.\n\n")
	p("Both the fix and each blamed commit are mapped to the first release tag that contains them, by walking the tags in version order once. " +
		"Hotfix releases are cut by cherry pick onto release branches, so a commit whose subject carries the same PR number as a commit in an earlier tag is credited to that earlier tag.\n\n")
	p("The distance in releases between the fix and the blamed code gives the class:\n\n")
	p("* **unreleased**: the code was written and fixed inside one release cycle. No user ever ran the bug.\n")
	p("* **regression_1**: the code first shipped in the release right before the fix.\n")
	p("* **regression_2**: two releases before.\n")
	p("* **latent**: older than that. An old bug someone finally hit.\n\n")
	p("**majority** classifies a fix by the class holding most of its blamed lines (ties go to the younger class) and is the headline. **youngest** classifies it by the newest code it touched at all; the hand check below found that rule far too eager, because one recently reformatted line in a large fix is enough to call it a regression. " +
		"The regression share is regression_1 plus regression_2 over all fixes with blameable deleted lines. Patch releases count as releases, so v1.43.0 to v1.43.1 is one step.\n\n")
	p("Fixes that only add lines (a missing check, a new branch) are **additive**. They are kept out of the headline numbers and classified separately on the two lines either side of each insertion, which is weaker evidence. " +
		"Fixes that touch no blameable source at all (tests, docs, CI, SQL migrations, new files only) are **no source**, and fixes whose subject says they repair lint, formatting, tests or CI are **tooling**: real rework, but nothing a user runs.\n\n")
	p("Releases here come every %.1f days on average (%d tags over %d days), so two releases is often under a week and \"latent\" includes code that shipped last week. "+
		"The calendar view classifies the same fixes by the age in days of the code they touched when the fix was committed, counting only code that had shipped.\n\n", float64(r.spanDays)/float64(max(1, len(r.rm.releases)-1)), len(r.rm.releases), r.spanDays)
	p("What it cannot see: a fix that deletes old code to repair a bug a recent change caused elsewhere reads as latent, a fix that rewrites a recently edited line to repair an old bug reads as a regression, and a follow up that extends a recent partial fix reads as a regression of that fix. The hand check below measures how often that happens.\n\n")

	p("## Headline\n\n")
	nm, d := share(all.majority)
	n, _ := share(all.youngest)
	cadence := float64(r.spanDays) / float64(max(1, len(r.rm.releases)-1))
	p("Two numbers, because the question has a narrow and a broad answer:\n\n")
	p("* **Narrow, release based: %s** (%d of %d fixes with deleted source lines) mostly rewrite code that first shipped in one of the two releases before the fix. "+
		"At this cadence two releases is about %.1f days, so this counts only bugs caught almost at once. "+
		"The hand check below bounds its precision (about half of the sampled labels hold up). Its recall is not measured: nobody checked how many real regressions the %d latent fixes hide.\n",
		pct(nm, d), nm, d, 2*cadence, all.majority[Latent])
	p("* **Broad, calendar based: %s** (%d of %d) mostly rewrite shipped code that was 30 days old or less when the fix landed. "+
		"This is the widest reading of \"a recent change caused it\". It is a bound, not an estimate: it also counts old bugs in recently edited code.\n\n",
		pct(all.recentMaj, d), all.recentMaj, d)
	p("Neither is the true rate of fixes repairing a recently shipped bug, and the history alone cannot pin that down further.\n\n")
	p("| rule | fixes | unreleased | regression_1 | regression_2 | latent | regression share | shipped regression share |\n|---|---:|---:|---:|---:|---:|---:|---:|\n")
	p("| majority | %d | %d | %d | %d | %d | **%s** | %s |\n", d, all.majority[0], all.majority[1], all.majority[2], all.majority[3], pct(nm, d), pct(nm, nm+all.majority[Latent]))
	p("| youngest | %d | %d | %d | %d | %d | %s | %s |\n", d, all.youngest[0], all.youngest[1], all.youngest[2], all.youngest[3], pct(n, d), pct(n, n+all.youngest[Latent]))
	na, da := share(all.addYoungest)
	p("| additive, youngest context | %d | %d | %d | %d | %d | %s | %s |\n\n", da, all.addYoungest[0], all.addYoungest[1], all.addYoungest[2], all.addYoungest[3], pct(na, da), pct(na, na+all.addYoungest[Latent]))
	p("Shipped regression share leaves out unreleased fixes: of the bugs that reached a release, how many came from one of the last two. Of %d fix commits, %d had blameable deleted lines, %d were additive, %d were tooling and %d touched no blameable source.\n\n", all.total, d, all.additive, all.tooling, all.noSource)
	p("### Calendar age of shipped code\n\n")
	p("By the youngest code touched, among fixes with deleted lines whose youngest code had shipped:\n\n")
	p("| up to 7 days | 8 to 30 days | 31 to 90 days | over 90 days |\n|---:|---:|---:|---:|\n")
	p("| %d | %d | %d | %d |\n\n", all.ageBucket[0], all.ageBucket[1], all.ageBucket[2], all.ageBucket[3])
	p("Fixes where at least half the blamed lines are shipped code 30 days old or less: **%d of %d (%s)**. By the youngest line instead: %d (%s). "+
		"This is the broad bound from the headline; it carries the same misreadings as the release based rules.\n\n",
		all.recentMaj, d, pct(all.recentMaj, d), all.recent30(), pct(all.recent30(), d))

	r.groupTable(w, "Trend", "lines", func(f *fixResult) string { return r.decade(f.rel) }, r.decadeOrder())
	r.groupTable(w, "By minor version line", "line", func(f *fixResult) string {
		if f.rel < len(r.rm.releases) && r.rm.releases[f.rel].major == 0 {
			return "v0.x"
		}
		return r.rm.line(f.rel)
	}, append([]string{"v0.x"}, r.lineOrder()...))
	r.groupTable(w, "By area", "area", func(f *fixResult) string {
		if f.area == "" {
			return "(no source)"
		}
		return f.area
	}, append(append([]string{}, areas...), "other", "(no source)"))
	r.topIntroducers(w, 15)
	if r.notes != "" {
		p("%s\n", strings.TrimRight(r.notes, "\n"))
	}
	return w.err
}

func (r *report) lineOrder() []string {
	var out []string
	seen := map[string]bool{}
	for _, rel := range r.rm.releases {
		if l := rel.line(); !seen[l] && rel.major > 0 {
			seen[l] = true
			out = append(out, l)
		}
	}
	return append(out, "next (untagged)")
}

// decade groups minor lines in tens, v1.0.x to v1.9.x and so on, so the
// trend reads without the noise of a handful of fixes per line.
func (r *report) decade(rel int) string {
	if rel >= len(r.rm.releases) {
		rel = len(r.rm.releases) - 1
	}
	v := r.rm.releases[rel]
	if v.major == 0 {
		return "v0.x"
	}
	lo := v.minor / 10 * 10
	return fmt.Sprintf("v%d.%d to v%d.%d", v.major, lo, v.major, lo+9)
}

func (r *report) decadeOrder() []string {
	var out []string
	seen := map[string]bool{}
	for i := range r.rm.releases {
		if k := r.decade(i); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func (r *report) groupTable(w *mdWriter, title, col string, key func(*fixResult) string, order []string) {
	groups := map[string]*tally{}
	for _, f := range r.fixes {
		k := key(f)
		if groups[k] == nil {
			groups[k] = &tally{}
		}
		groups[k].add(f)
	}
	w.f("## %s\n\nClass counts use the majority rule on fixes with deleted lines.", title)
	switch col {
	case "area":
		w.f(" A fix belongs to the area holding most of its blamed lines.")
	case "lines":
		w.f(" Untagged fixes on main count with the newest line.")
	}
	w.f("\n\n| %s | fix commits | tooling | no source | additive | unreleased | regression_1 | regression_2 | latent | regression share | youngest rule | recent 30 days |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n", col)
	for _, k := range order {
		t := groups[k]
		if t == nil {
			continue
		}
		nm, d := share(t.majority)
		n, _ := share(t.youngest)
		w.f("| %s | %d | %d | %d | %d | %d | %d | %d | %d | **%s** | %s | %s |\n", k, t.total, t.tooling, t.noSource, t.additive, t.majority[0], t.majority[1], t.majority[2], t.majority[3], pct(nm, d), pct(n, d), pct(t.recentMaj, d))
	}
	w.f("\n")
}

func (r *report) topIntroducers(w *mdWriter, limit int) {
	type intro struct {
		sha           string
		youngest, any int
		regress       []string
	}
	m := map[string]*intro{}
	get := func(sha string) *intro {
		if m[sha] == nil {
			m[sha] = &intro{sha: sha}
		}
		return m[sha]
	}
	for _, f := range r.fixes {
		if f.kind != Deleting {
			continue
		}
		seen := map[string]bool{}
		for _, l := range f.lines {
			if !seen[l.sha] {
				seen[l.sha] = true
				get(l.sha).any++
			}
		}
		in := get(f.majorityIntro)
		in.youngest++
		if f.majority == Regression1 || f.majority == Regression2 {
			in.regress = append(in.regress, f.sha)
		}
	}
	var list []*intro
	for _, in := range m {
		if in.youngest > 0 {
			list = append(list, in)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.youngest != b.youngest {
			return a.youngest > b.youngest
		}
		if a.any != b.any {
			return a.any > b.any
		}
		return a.sha < b.sha
	})
	byShipped := append([]*intro{}, list...)
	if len(list) > limit {
		list = list[:limit]
	}
	w.f("## Changes that caused the most rework\n\n")
	w.f("Ranked by the number of later fixes the majority rule holds this commit responsible for: within the fix's majority class, it wrote the most blamed lines. \"any line\" counts every fix that rewrote at least one of its lines; \"shipped\" counts the fixes classified regression_1 or regression_2. The big early commits lead because they wrote most of the code that still exists; the next table is the one about regressions.\n\n")
	w.f("| commit | PR | tag | fixes | any line | shipped | subject |\n|---|---|---|---:|---:|---:|---|\n")
	for _, in := range list {
		subj := r.rm.subject[in.sha]
		pr := ""
		if mm := prRef.FindStringSubmatch(subj); mm != nil {
			pr = "#" + mm[1]
		}
		subj = cleanSubject(subj)
		w.f("| `%s` | %s | %s | %d | %d | %d | %s |\n", in.sha[:9], pr, r.rm.name(r.rm.rel(in.sha, "")), in.youngest, in.any, len(in.regress), subj)
	}
	w.f("\n")

	// The table above is dominated by the big early commits that wrote most
	// of the code. This one asks which changes shipped bugs that needed a
	// fix within two releases.
	sort.SliceStable(byShipped, func(i, j int) bool { return len(byShipped[i].regress) > len(byShipped[j].regress) })
	w.f("### Most shipped regressions\n\nCommits ranked by fixes the majority rule classified regression_1 or regression_2 against them. Commits with one such fix are left out.\n\n")
	w.f("| commit | PR | tag | shipped regressions | fixed by | subject |\n|---|---|---|---:|---|---|\n")
	single := 0
	for _, in := range byShipped {
		if len(in.regress) == 1 {
			single++
		}
	}
	for i, in := range byShipped {
		if i >= limit || len(in.regress) < 2 {
			break
		}
		var by []string
		for _, f := range in.regress {
			by = append(by, "`"+f[:9]+"`")
		}
		subj := cleanSubject(r.rm.subject[in.sha])
		pr := ""
		if mm := prRef.FindStringSubmatch(subj); mm != nil {
			pr = "#" + mm[1]
		}
		w.f("| `%s` | %s | %s | %d | %s | %s |\n", in.sha[:9], pr, r.rm.name(r.rm.rel(in.sha, "")), len(in.regress), strings.Join(by, " "), subj)
	}
	w.f("\n%d other commits have exactly one shipped regression against them.\n\n", single)
}

// cleanSubject makes a commit subject safe inside a Markdown table and keeps
// the docs free of the long dashes a few early subjects carry.
func cleanSubject(s string) string {
	return strings.NewReplacer("|", "\\|", "\u2014", "-", "\u2013", "-").Replace(s)
}
