package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		fix, intro int
		want       Class
	}{
		{5, 5, Unreleased},
		{4, 5, Unreleased}, // cherry pick pulled the fix earlier than the code reached main
		{5, 4, Regression1},
		{5, 3, Regression2},
		{5, 2, Latent},
		{5, 0, Latent},
	}
	for _, c := range cases {
		if got := classify(c.fix, c.intro); got != c.want {
			t.Errorf("classify(%d, %d) = %s, want %s", c.fix, c.intro, got, c.want)
		}
	}
}

func TestSortReleases(t *testing.T) {
	got := sortReleases([]string{"v1.10.0", "v1.2.1", "v1.2.0", "v0.9.0", "v1.10.0-rc1", "latest", "v1.9.12", ""})
	var names []string
	for _, r := range got {
		names = append(names, r.name)
	}
	want := []string{"v0.9.0", "v1.2.0", "v1.2.1", "v1.9.12", "v1.10.0"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	if got[2].line() != "v1.2.x" {
		t.Fatalf("line = %q", got[2].line())
	}
}

func TestParseDiff(t *testing.T) {
	diff := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -3,2 +3,1 @@ func x() {
-old
-old
+new
@@ -10 +9 @@
-x
+y
@@ -20,0 +20,2 @@
+added
+added
diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1 @@
+package x
`
	got := parseDiff(diff)
	want := []fileChange{{path: "a.go", deleted: [][2]int{{3, 2}, {10, 1}}, inserts: []int{20}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseDiffDeletedDashLine(t *testing.T) {
	// A deleted SQL comment line "-- note" shows as "--- note" in the diff.
	diff := `diff --git a/q.go b/q.go
--- a/q.go
+++ b/q.go
@@ -4,2 +4,0 @@
--- note
-x
@@ -9 +7 @@
-y
+z
`
	got := parseDiff(diff)
	want := []fileChange{{path: "q.go", deleted: [][2]int{{4, 2}, {9, 1}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTrivial(t *testing.T) {
	for _, s := range []string{"", "   ", "}", "\t})", ");", "import (", `	"strings"`, "} else {", "return nil", "//"} {
		if !trivial(s) {
			t.Errorf("trivial(%q) = false", s)
		}
	}
	for _, s := range []string{"return err", `x := "a b"`, "if rsrc == rdst {", "// explains the guard", `fmt.Println("x")`} {
		if trivial(s) {
			t.Errorf("trivial(%q) = true", s)
		}
	}
	// Trivial lines do not vote: two recent braces cannot outvote one old
	// line of logic.
	f := &fixResult{rel: 10}
	for _, b := range []blamedLine{{sha: "new", content: "}"}, {sha: "new", content: ""}, {sha: "old", content: "return oldA()"}} {
		if !trivial(b.content) {
			rel := 1
			if b.sha == "new" {
				rel = 9
			}
			f.lines = append(f.lines, lineAge{sha: b.sha, rel: rel})
		}
	}
	summarize(f)
	if f.majority != Latent {
		t.Fatalf("majority = %s, want latent", f.majority)
	}
}

func TestToolingSubject(t *testing.T) {
	tooling := []string{"fix(lint): resolve errcheck", "fix: gofmt internal/calibre/syncer.go", "fix(test): flaky timeout", "fix(ci)!: pin action", "fix: lint failures in telemetry", "fix(api): gosec G104 in handler"}
	user := []string{"fix(api): close the SSRF hole gosec found (#1)", "fix: restore series Fill; use WithoutCancel (G118)", "fix(importer): stop the linter in the name parser"}
	for _, s := range tooling {
		if !toolingSubject.MatchString(s) {
			t.Errorf("%q should be tooling", s)
		}
	}
	for _, s := range user {
		if toolingSubject.MatchString(s) {
			t.Errorf("%q should not be tooling", s)
		}
	}
}

func TestIsSourceAndArea(t *testing.T) {
	yes := []string{"internal/api/books.go", "web/src/pages/Book.tsx", "cmd/bindery/main.go"}
	no := []string{"internal/api/books_test.go", "web/src/App.test.tsx", "docs/x.go", "README.md", "web/package-lock.json", "tools/licensegen/vendored/x.go", "web/src/vite-env.d.ts", "tests/smoke/main.go"}
	for _, p := range yes {
		if !isSource(p) {
			t.Errorf("isSource(%q) = false", p)
		}
	}
	for _, p := range no {
		if isSource(p) {
			t.Errorf("isSource(%q) = true", p)
		}
	}
	if a := areaOf("internal/indexer/newznab/x.go"); a != "internal/indexer" {
		t.Errorf("area = %q", a)
	}
	if a := areaOf("internal/db/x.go"); a != "other" {
		t.Errorf("area = %q", a)
	}
}

func TestContextRanges(t *testing.T) {
	got := contextRanges([]int{0, 5, 9}, 10)
	want := [][2]int{{1, 2}, {4, 4}, {8, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestEndToEnd builds a small repository with five releases and checks that
// every kind of fix lands in the right bucket, including a hotfix that was
// cherry picked onto a release branch.
func TestEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name string, lines ...string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) string {
		t.Helper()
		run("add", "-A")
		run("commit", "-q", "-m", msg)
		return run("rev-parse", "HEAD")
	}

	run("init", "-q")
	// v1.0.0: old code in two areas.
	write("internal/api/a.go", "package api", "", "func A() int {", "\treturn oldA()", "}", "", "var keep = 1")
	write("web/src/b.ts", "export const b = 1;", "export const c = 2;", "export const d = 3;")
	commit("feat: initial")
	run("tag", "v1.0.0")
	write("README.md", "x")
	commit("docs: readme")
	run("tag", "v1.1.0")
	write("README.md", "y")
	commit("docs: more")
	run("tag", "v1.2.0")
	// v1.3.0 introduces a new line.
	write("internal/api/a.go", "package api", "", "func A() int {", "\treturn oldA()", "}", "", "var keep = 1", "var recent = brokenThing()")
	introRecent := commit("feat(api): add recent (#10)")
	run("tag", "v1.3.0")

	// v1.4.0: a regression fix, a latent fix, an unreleased bug and its fix,
	// an additive fix, and a test only fix.
	write("internal/api/a.go", "package api", "", "func A() int {", "\treturn oldA()", "}", "", "var keep = 1", "var recent = fixedThing()")
	regFix := commit("fix(api): repair recent (#11)")
	write("web/src/b.ts", "export const b = 1;", "export const c = 20;", "export const d = 3;")
	latentFix := commit("fix(web): c was wrong (#12)")
	write("web/src/b.ts", "export const b = 1;", "export const c = 20;", "export const d = 3;", "export const e = bug();")
	commit("feat(web): add e (#13)")
	write("web/src/b.ts", "export const b = 1;", "export const c = 20;", "export const d = 3;", "export const e = ok();")
	unrelFix := commit("fix(web): e before release (#14)")
	write("internal/api/a.go", "package api", "", "func A() int {", "\tguard()", "\treturn oldA()", "}", "", "var keep = 1", "var recent = fixedThing()")
	addFix := commit("fix: add a guard (#15)")
	write("internal/api/a_test.go", "package api")
	testFix := commit("fix: cover a with a test (#16)")
	write("internal/api/a.go", "package api", "", "func A() int {", "\tguard()", "\treturn oldA()", "}", "", "var keep = 1", "var recent = fixedThing() ")
	lintFix := commit("fix(lint): trailing space (#18)")
	run("tag", "v1.4.0")

	// A hotfix lands on main after v1.4.0, and is cherry picked to a release
	// branch tagged v1.4.1. It must be credited to v1.4.1, not v1.5.0.
	// The docs commit first gives the main copy a different parent, so the
	// cherry pick is a distinct commit as it is in real history.
	write("README.md", "between")
	commit("docs: between")
	write("internal/api/a.go", "package api", "", "func A() int {", "\tguard()", "\treturn newA()", "}", "", "var keep = 1", "var recent = fixedThing() ")
	hotfix := commit("fix(api): hotfix A (#17)")
	run("checkout", "-q", "-b", "release", "v1.4.0")
	run("cherry-pick", hotfix)
	run("tag", "v1.4.1")
	run("checkout", "-q", "main")
	write("README.md", "z")
	commit("docs: after")
	run("tag", "v1.5.0")

	g := git{dir: dir}
	tagOut, _ := g.out("tag", "-l", "v*")
	rels := sortReleases(strings.Split(tagOut, "\n"))
	rm, err := buildReleaseMap(g, rels)
	if err != nil {
		t.Fatal(err)
	}
	if got := rm.name(rm.rel(introRecent, "")); got != "v1.3.0" {
		t.Fatalf("introRecent first tag = %s", got)
	}
	if got := rm.name(rm.rel(hotfix, "fix(api): hotfix A (#17)")); got != "v1.4.1" {
		t.Fatalf("hotfix credited to %s, want v1.4.1 via its release branch copy", got)
	}

	a := &analyzer{g: g, rm: rm}
	var analyzed []*fixResult
	check := func(sha, subj string, kind Kind, youngest Class, area string) {
		t.Helper()
		f, err := a.analyze(sha, subj)
		if err != nil {
			t.Fatal(err)
		}
		analyzed = append(analyzed, f)
		if f.kind != kind {
			t.Fatalf("%s: kind %d, want %d", subj, f.kind, kind)
		}
		if kind == NoSource || kind == Tooling {
			return
		}
		if f.youngest != youngest || f.area != area {
			t.Fatalf("%s: got %s in %s, want %s in %s", subj, f.youngest, f.area, youngest, area)
		}
	}
	check(regFix, "fix(api): repair recent (#11)", Deleting, Regression1, "internal/api")
	check(latentFix, "fix(web): c was wrong (#12)", Deleting, Latent, "web/src")
	check(unrelFix, "fix(web): e before release (#14)", Deleting, Unreleased, "web/src")
	check(addFix, "fix: add a guard (#15)", Additive, Latent, "internal/api")
	check(testFix, "fix: cover a with a test (#16)", NoSource, 0, "")
	check(lintFix, "fix(lint): trailing space (#18)", Tooling, 0, "")
	// The hotfix rewrites v1.0.0 code and ships in v1.4.1.
	check(hotfix, "fix(api): hotfix A (#17)", Deleting, Latent, "internal/api")
	if analyzed[len(analyzed)-1].rel != 5 {
		t.Fatalf("hotfix analyzed at release %d, want 5 (v1.4.1)", analyzed[len(analyzed)-1].rel)
	}

	// The report renders and counts the five fixes with source evidence.
	var buf strings.Builder
	rep := report{rm: rm, fixes: analyzed, head: strings.Repeat("a", 40), date: "2026-01-01", commits: 12}
	if err := rep.write(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"| majority | 4 | 1 | 1 | 0 | 2 | **25%** |",
		"1 were additive, 1 were tooling and 1 touched no blameable source",
		"| v1.4.x |",
		"| `" + introRecent[:9] + "` | #10 | v1.3.0 | 1 |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
}

func TestSummarizeMajority(t *testing.T) {
	f := &fixResult{rel: 10, lines: []lineAge{
		{sha: "new", rel: 9, area: "web/src"},
		{sha: "old", rel: 1, area: "internal/api"},
		{sha: "old", rel: 1, area: "internal/api"},
	}}
	summarize(f)
	if f.youngest != Regression1 || f.majority != Latent || f.youngestIntro != "new" || f.majorityIntro != "old" || f.area != "internal/api" {
		t.Fatalf("got youngest %s majority %s intro %s area %s", f.youngest, f.majority, f.youngestIntro, f.area)
	}
	tie := &fixResult{rel: 10, lines: []lineAge{{sha: "a", rel: 9}, {sha: "b", rel: 1}}}
	summarize(tie)
	if tie.majority != Regression1 {
		t.Fatalf("tie should go to the younger class, got %s", tie.majority)
	}
}
