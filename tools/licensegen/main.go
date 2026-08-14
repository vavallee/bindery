// Command licensegen regenerates THIRD_PARTY_LICENSES.md.
//
// The Go side is scoped to the packages that are actually linked into
// ./cmd/bindery — the union across every GOOS we release binaries for — so
// test-only and tooling dependencies (testify, the linters, go-licenses
// itself) are excluded while anything that reaches the shipped binary on any
// platform is included. License classification comes from `go-licenses report`
// (google/licenseclassifier); NOTICE discovery and the module grouping are
// done here because go-licenses does not model either.
//
// The npm side covers production dependencies only: devDependencies are build
// and test tooling and do not end up in the Vite bundle that gets embedded
// into the binary via internal/webui.
//
// Usage:
//
//	go run ./tools/licensegen            # write THIRD_PARTY_LICENSES.md
//	go run ./tools/licensegen -check     # exit 1 if the file is out of date
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// releaseGOOS is every GOOS goreleaser and the Dockerfile build for. Taking the
// union of dependency graphs across all three keeps platform-specific imports
// (for example the Windows-only branches of modernc.org/libc) in the report.
var releaseGOOS = []string{"linux", "darwin", "windows"}

// mainModule is skipped in the report — Bindery's own license is LICENSE.
const mainModule = "github.com/vavallee/bindery"

// outputFile is written relative to the repository root.
const outputFile = "THIRD_PARTY_LICENSES.md"

// licenseOverride records a module whose license the classifier cannot
// identify with sufficient confidence. Each entry names the file that was read
// by hand and the reason the override exists; nothing here is a guess.
type licenseOverride struct {
	name   string // SPDX identifier
	file   string // path relative to the module root
	url    string
	reason string
}

var overrides = map[string]licenseOverride{
	// The classifier reports "Unknown": the file is a verbatim Go-project
	// BSD-3-Clause with "the mathutil Authors" substituted for "Google Inc.",
	// which drops below the 0.9 confidence threshold.
	"modernc.org/mathutil": {
		name:   "BSD-3-Clause",
		file:   "LICENSE",
		url:    "https://gitlab.com/cznic/mathutil/-/blob/v1.7.1/LICENSE",
		reason: "classifier returns Unknown; LICENSE is BSD-3-Clause with a non-standard copyright holder line",
	},
	// go-licenses latches onto LICENSE-3RD-PARTY.md (the musl/SQLite notices
	// libc vendors) and reports MIT. The module's own license is the BSD-3-Clause
	// in LICENSE; both texts are reproduced below.
	"modernc.org/libc": {
		name:   "BSD-3-Clause",
		file:   "LICENSE",
		url:    "https://gitlab.com/cznic/libc/-/blob/v1.74.4/LICENSE",
		reason: "go-licenses selects the vendored third-party notice file; the module's own license is LICENSE",
	},
}

type goModule struct {
	Path    string
	Version string
	Dir     string
	Main    bool
}

type goListPackage struct {
	ImportPath string
	Standard   bool
	Module     *goModule
}

// licenseFile is one license or notice document reproduced in the output.
type licenseFile struct {
	rel  string // path relative to the module root
	text string
}

// module is one third-party Go module in the report.
type module struct {
	path     string
	version  string
	dir      string
	spdx     string
	url      string
	override string // non-empty when an override supplied the classification
	licenses []licenseFile
	notices  []licenseFile
}

// npmPackage is one production npm dependency in the report.
type npmPackage struct {
	name     string
	version  string
	spdx     string
	licenses []licenseFile
}

func main() {
	check := flag.Bool("check", false, "verify the committed file is up to date instead of writing it")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}

	mods, err := collectGoModules(root)
	if err != nil {
		fatal(fmt.Errorf("go modules: %w", err))
	}
	pkgs, err := collectNPMPackages(root)
	if err != nil {
		fatal(fmt.Errorf("npm packages: %w", err))
	}

	out := render(mods, pkgs)
	dest := filepath.Join(root, outputFile)

	if *check {
		have, readErr := os.ReadFile(dest) //nolint:gosec // path is derived from the repo root
		if readErr != nil {
			fatal(fmt.Errorf("read %s: %w", outputFile, readErr))
		}
		if !bytes.Equal(have, []byte(out)) {
			fmt.Fprintf(os.Stderr, "%s is out of date — run `make licenses` and commit the result\n", outputFile)
			os.Exit(1)
		}
		fmt.Printf("%s is up to date\n", outputFile)
		return
	}

	if err := os.WriteFile(dest, []byte(out), 0o644); err != nil { //nolint:gosec // attribution file is world-readable by design
		fatal(err)
	}
	fmt.Printf("wrote %s (%d Go modules, %d npm packages)\n", outputFile, len(mods), len(pkgs))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "licensegen:", err)
	os.Exit(1)
}

// command builds a subprocess. Every invocation here is a fixed argv against a
// developer's own toolchain, so a plain background context is enough; the
// helper exists so the tool never reaches for the context-less constructor.
func command(name string, args ...string) *exec.Cmd {
	return exec.CommandContext(context.Background(), name, args...) //nolint:gosec // fixed argv, no user input
}

func repoRoot() (string, error) {
	out, err := command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("locate module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// collectGoModules resolves every module linked into ./cmd/bindery on any
// release GOOS, then attaches license and notice files to each.
func collectGoModules(root string) ([]module, error) {
	byPath := map[string]*module{}
	pkgToModule := map[string]string{}

	for _, goos := range releaseGOOS {
		list, err := goListDeps(root, goos)
		if err != nil {
			return nil, err
		}
		for _, p := range list {
			if p.Standard || p.Module == nil || p.Module.Main || p.Module.Path == mainModule {
				continue
			}
			pkgToModule[p.ImportPath] = p.Module.Path
			if _, ok := byPath[p.Module.Path]; !ok {
				byPath[p.Module.Path] = &module{
					path:    p.Module.Path,
					version: p.Module.Version,
					dir:     p.Module.Dir,
				}
			}
		}
	}

	classified, err := classify(root, pkgToModule)
	if err != nil {
		return nil, err
	}

	mods := make([]module, 0, len(byPath))
	for path, m := range byPath {
		if c, ok := classified[path]; ok {
			m.spdx, m.url = c.spdx, c.url
			for _, rel := range c.files {
				text, readErr := readModuleFile(m.dir, rel)
				if readErr != nil {
					return nil, readErr
				}
				m.licenses = append(m.licenses, licenseFile{rel: rel, text: text})
			}
		}
		if o, ok := overrides[path]; ok {
			m.spdx, m.url, m.override = o.name, o.url, o.reason
			text, readErr := readModuleFile(m.dir, o.file)
			if readErr != nil {
				return nil, readErr
			}
			if !hasFile(m.licenses, o.file) {
				m.licenses = append([]licenseFile{{rel: o.file, text: text}}, m.licenses...)
			}
		}
		if m.spdx == "" {
			return nil, fmt.Errorf("no license identified for %s — add an override in tools/licensegen", path)
		}
		notices, noticeErr := findNotices(m.dir)
		if noticeErr != nil {
			return nil, noticeErr
		}
		m.notices = notices
		sortFiles(m.licenses)
		mods = append(mods, *m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].path < mods[j].path })
	return mods, nil
}

func hasFile(files []licenseFile, rel string) bool {
	for _, f := range files {
		if f.rel == rel {
			return true
		}
	}
	return false
}

func sortFiles(files []licenseFile) {
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
}

func goListDeps(root, goos string) ([]goListPackage, error) {
	cmd := command("go", "list", "-deps", "-json", "./cmd/bindery")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list (GOOS=%s): %w", goos, err)
	}
	var pkgs []goListPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p goListPackage
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

type classification struct {
	spdx  string
	url   string
	files []string // module-relative license file paths
}

// classify runs `go-licenses report` once per release GOOS and folds the
// per-package results up to their modules. A module keeps every distinct
// license file the tool found (go-jose, for example, has a BSD-3-Clause
// sub-package inside an Apache-2.0 module) and reports the most restrictive
// classification of the set.
func classify(root string, pkgToModule map[string]string) (map[string]classification, error) {
	tmpl, err := os.CreateTemp("", "licensegen-*.tpl")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmpl.Name()) }()
	const body = `{{range .}}{{.Name}}` + "\x1f" + `{{.LicenseName}}` + "\x1f" + `{{.LicenseURL}}` + "\x1f" + `{{.LicensePath}}` + "\x1e" + `{{end}}`
	if _, err := tmpl.WriteString(body); err != nil {
		return nil, err
	}
	if err := tmpl.Close(); err != nil {
		return nil, err
	}

	result := map[string]classification{}
	for _, goos := range releaseGOOS {
		cmd := command("go-licenses", "report", "./cmd/bindery", "--template", tmpl.Name())
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go-licenses report (GOOS=%s): %w — is go-licenses v1.6.0 on PATH?", goos, err)
		}
		for _, rec := range strings.Split(string(out), "\x1e") {
			fields := strings.Split(strings.TrimSpace(rec), "\x1f")
			if len(fields) != 4 || fields[0] == "" {
				continue
			}
			pkg, spdx, url, path := fields[0], fields[1], fields[2], fields[3]
			modPath, ok := resolveModule(pkg, pkgToModule)
			if !ok || spdx == "Unknown" || path == "" {
				continue
			}
			c := result[modPath]
			if c.spdx == "" || rank(spdx) > rank(c.spdx) {
				c.spdx, c.url = spdx, url
			}
			rel := moduleRelative(path, modPath)
			if !contains(c.files, rel) {
				c.files = append(c.files, rel)
			}
			result[modPath] = c
		}
	}
	return result, nil
}

// resolveModule maps a go-licenses "library" name to a module. go-licenses
// names a library after the directory holding its license file, which is often
// the module root (golang.org/x/text) rather than any package that go list
// reported, so an exact package lookup is tried first and a longest-module-path
// prefix match second.
func resolveModule(name string, pkgToModule map[string]string) (string, bool) {
	if mod, ok := pkgToModule[name]; ok {
		return mod, true
	}
	best := ""
	for _, mod := range pkgToModule {
		if name == mod || strings.HasPrefix(name, mod+"/") {
			if len(mod) > len(best) {
				best = mod
			}
		}
	}
	return best, best != ""
}

// rank orders licenses by how much they constrain redistribution, so a module
// that mixes licenses is labelled with the one that carries the most
// obligations rather than whichever package the tool happened to emit last.
func rank(spdx string) int {
	switch {
	case strings.HasPrefix(spdx, "GPL"), strings.HasPrefix(spdx, "AGPL"), strings.HasPrefix(spdx, "LGPL"):
		return 3
	case strings.HasPrefix(spdx, "MPL"):
		return 2
	case strings.HasPrefix(spdx, "Apache"):
		return 1
	default:
		return 0
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// moduleRelative turns an absolute license path in the module cache into a
// path relative to the module root.
func moduleRelative(abs, modPath string) string {
	// Module cache dirs end in <module path>@<version>; everything after that
	// is the module-relative path.
	slash := filepath.ToSlash(abs)
	base := filepath.Base(modPath)
	if idx := strings.LastIndex(slash, "/"+base+"@"); idx >= 0 {
		rest := slash[idx+1:]
		if cut := strings.Index(rest, "/"); cut >= 0 {
			return rest[cut+1:]
		}
	}
	return filepath.Base(slash)
}

func readModuleFile(dir, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))) //nolint:gosec // paths come from the module cache
	if err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	return normalize(string(b)), nil
}

// findNotices returns any NOTICE file at the module root. Apache-2.0 §4(d)
// only obliges us to reproduce a NOTICE the dependency actually ships, so this
// discovers them rather than assuming them.
func findNotices(dir string) ([]licenseFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read module dir %s: %w", dir, err)
	}
	var found []licenseFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(strings.ToUpper(e.Name()), "NOTICE") {
			continue
		}
		text, readErr := readModuleFile(dir, e.Name())
		if readErr != nil {
			return nil, readErr
		}
		found = append(found, licenseFile{rel: e.Name(), text: text})
	}
	sortFiles(found)
	return found, nil
}

// fenceFor returns a code fence long enough to contain text verbatim, so a
// license that itself contains a Markdown fence cannot break the document.
func fenceFor(text string) string {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return fence
}

func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, "\n \t")
}

// ── npm ───────────────────────────────────────────────────────────────────

type npmNode struct {
	Version      string             `json:"version"`
	Resolved     string             `json:"resolved"`
	Dependencies map[string]npmNode `json:"dependencies"`
}

type npmTree struct {
	Dependencies map[string]npmNode `json:"dependencies"`
}

// collectNPMPackages flattens the production dependency tree reported by
// `npm ls --omit=dev --all`. devDependencies are deliberately excluded: they
// are build and test tooling and none of their code is emitted into the Vite
// bundle that internal/webui embeds.
func collectNPMPackages(root string) ([]npmPackage, error) {
	web := filepath.Join(root, "web")
	if _, err := os.Stat(filepath.Join(web, "node_modules")); err != nil {
		return nil, fmt.Errorf("web/node_modules is missing — run `npm ci --prefix web` first")
	}
	cmd := command("npm", "ls", "--omit=dev", "--all", "--json")
	cmd.Dir = web
	out, err := cmd.Output() // npm exits non-zero on peer-dep warnings; the JSON is still valid
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("npm ls: %w", err)
	}
	var tree npmTree
	if err := json.Unmarshal(out, &tree); err != nil {
		return nil, fmt.Errorf("decode npm ls output: %w", err)
	}

	flat := map[string]string{}
	var walk func(map[string]npmNode)
	walk = func(deps map[string]npmNode) {
		for name, node := range deps {
			flat[name] = node.Version
			walk(node.Dependencies)
		}
	}
	walk(tree.Dependencies)

	names := make([]string, 0, len(flat))
	for n := range flat {
		names = append(names, n)
	}
	sort.Strings(names)

	pkgs := make([]npmPackage, 0, len(names))
	for _, name := range names {
		dir := filepath.Join(web, "node_modules", filepath.FromSlash(name))
		spdx, err := npmLicenseID(dir)
		if err != nil {
			return nil, err
		}
		files, err := npmLicenseFiles(dir)
		if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, npmPackage{name: name, version: flat[name], spdx: spdx, licenses: files})
	}
	return pkgs, nil
}

func npmLicenseID(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json")) //nolint:gosec // path is under web/node_modules
	if err != nil {
		return "", fmt.Errorf("read package.json in %s: %w", dir, err)
	}
	var meta struct {
		License  any `json:"license"`
		Licenses []struct {
			Type string `json:"type"`
		} `json:"licenses"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return "", fmt.Errorf("decode package.json in %s: %w", dir, err)
	}
	switch v := meta.License.(type) {
	case string:
		if v != "" {
			return v, nil
		}
	case map[string]any:
		if t, ok := v["type"].(string); ok && t != "" {
			return t, nil
		}
	}
	if len(meta.Licenses) > 0 && meta.Licenses[0].Type != "" {
		return meta.Licenses[0].Type, nil
	}
	return "", fmt.Errorf("no license field in %s/package.json", dir)
}

func npmLicenseFiles(dir string) ([]licenseFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var found []licenseFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		upper := strings.ToUpper(e.Name())
		if !strings.HasPrefix(upper, "LICENSE") && !strings.HasPrefix(upper, "LICENCE") && !strings.HasPrefix(upper, "NOTICE") {
			continue
		}
		text, readErr := readModuleFile(dir, e.Name())
		if readErr != nil {
			return nil, readErr
		}
		found = append(found, licenseFile{rel: e.Name(), text: text})
	}
	sortFiles(found)
	return found, nil
}

// ── rendering ─────────────────────────────────────────────────────────────

func render(mods []module, pkgs []npmPackage) string {
	var b strings.Builder
	writeHeader(&b, mods, pkgs)
	writeGoTable(&b, mods)
	writeNPMTable(&b, pkgs)
	writeNotices(&b, mods)
	writeTexts(&b, mods, pkgs)
	return b.String()
}

func writeHeader(b *strings.Builder, mods []module, pkgs []npmPackage) {
	fmt.Fprintf(b, `# Third-party licenses

Bindery is MIT licensed (see [LICENSE](LICENSE)). The released binaries
statically link the Go modules listed below, and the web UI bundle embedded in
those binaries includes the npm packages listed below. Their licenses and, where
the project ships one, their NOTICE files are reproduced here so the attribution
travels with every binary, archive and container image.

**Do not edit this file by hand.** It is generated by
`+"`tools/licensegen`"+` and verified in CI:

    make licenses         # regenerate
    make licenses-check   # fail if the committed copy is stale

## Scope

- **Go:** the modules linked into `+"`./cmd/bindery`"+`, taken as the union of the
  dependency graphs for %s — the platforms we release binaries for.
  Test-only and tooling dependencies (for example `+"`stretchr/testify`"+`,
  linters, `+"`go-licenses`"+` itself) are not linked into the binary and are not
  listed. License identification is `+"`go-licenses report`"+`
  (google/licenseclassifier); the handful of modules the classifier cannot
  identify are covered by reviewed overrides recorded in
  `+"`tools/licensegen/main.go`"+`.
- **npm:** production dependencies of `+"`web/package.json`"+` and their transitive
  production dependencies — the code that ends up in the Vite bundle embedded by
  `+"`internal/webui`"+`. devDependencies (Vite, ESLint, Vitest, Tailwind's
  compiler) are build and test tooling; their own code is not shipped, and
  Tailwind's generated utility CSS — which is shipped — is MIT licensed like
  Tailwind itself.

Where the two rules disagree we err towards listing too much rather than too
little: a package that is in the production tree but whose code never reaches
the bundle (`+"`typescript`"+`, pulled in as a runtime dependency of `+"`i18next`"+` for
its type definitions) is listed anyway, and modules that only build on one of
the three release platforms are listed for all of them. Omitting something that
does ship is the failure that matters.

%d Go modules, %d npm packages.

`, strings.Join(releaseGOOS, ", "), len(mods), len(pkgs))
}

func writeGoTable(b *strings.Builder, mods []module) {
	fmt.Fprintf(b, "## Go modules\n\n| Module | Version | License | NOTICE |\n| --- | --- | --- | --- |\n")
	for _, m := range mods {
		notice := "—"
		if len(m.notices) > 0 {
			notice = "yes"
		}
		link := m.path
		if m.url != "" && m.url != "Unknown" {
			link = fmt.Sprintf("[%s](%s)", m.path, m.url)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", link, m.version, m.spdx, notice)
	}
	b.WriteString("\n### Notes on specific modules\n\n")
	var noted bool
	for _, m := range mods {
		switch {
		case rank(m.spdx) == 3:
			fmt.Fprintf(b, "- **%s** is %s. Copyleft: the binaries that link it are subject to its terms.\n", m.path, m.spdx)
			noted = true
		case strings.HasPrefix(m.spdx, "MPL"):
			fmt.Fprintf(b, "- **%s** is %s. Used unmodified; its source is available at %s.\n", m.path, m.spdx, m.url)
			noted = true
		}
		if m.override != "" {
			fmt.Fprintf(b, "- **%s** — license set by override: %s.\n", m.path, m.override)
			noted = true
		}
	}
	if !noted {
		b.WriteString("- None: every module is a permissive license identified directly from its license file.\n")
	}
	b.WriteString("\n")
}

func writeNPMTable(b *strings.Builder, pkgs []npmPackage) {
	b.WriteString("## npm packages (embedded web bundle)\n\n| Package | Version | License |\n| --- | --- | --- |\n")
	for _, p := range pkgs {
		fmt.Fprintf(b, "| %s | %s | %s |\n", p.name, p.version, p.spdx)
	}
	b.WriteString("\n")
}

func writeNotices(b *strings.Builder, mods []module) {
	b.WriteString("## NOTICE files\n\n")
	b.WriteString("Apache-2.0 §4(d) requires reproducing the NOTICE file of any dependency that\nships one. These are the modules that do:\n\n")
	var any bool
	for _, m := range mods {
		for _, n := range m.notices {
			any = true
			fence := fenceFor(n.text)
			fmt.Fprintf(b, "### %s — %s\n\n%s\n%s\n%s\n\n", m.path, n.rel, fence, n.text, fence)
		}
	}
	if !any {
		b.WriteString("None of the linked modules ship a NOTICE file.\n\n")
	}
}

// writeTexts reproduces every distinct license text once, listing the modules
// and packages it covers. Deduplicating keeps the file navigable — the Go
// BSD-3-Clause text alone would otherwise repeat a dozen times.
func writeTexts(b *strings.Builder, mods []module, pkgs []npmPackage) {
	type group struct {
		spdx    string
		text    string
		holders []string
	}
	groups := map[string]*group{}
	order := []string{}
	add := func(spdx, owner string, f licenseFile) {
		sum := sha256.Sum256([]byte(f.text))
		key := hex.EncodeToString(sum[:])
		g, ok := groups[key]
		if !ok {
			g = &group{spdx: spdx, text: f.text}
			groups[key] = g
			order = append(order, key)
		}
		entry := owner + " (" + f.rel + ")"
		if !contains(g.holders, entry) {
			g.holders = append(g.holders, entry)
		}
	}
	for _, m := range mods {
		for _, f := range m.licenses {
			add(m.spdx, m.path+"@"+m.version, f)
		}
	}
	for _, p := range pkgs {
		for _, f := range p.licenses {
			add(p.spdx, "npm:"+p.name+"@"+p.version, f)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		a, c := groups[order[i]], groups[order[j]]
		if a.spdx != c.spdx {
			return a.spdx < c.spdx
		}
		return a.holders[0] < c.holders[0]
	})

	b.WriteString("## License texts\n\n")
	b.WriteString("Each distinct license text appears once, with the dependencies it covers.\n\n")
	for i, key := range order {
		g := groups[key]
		sort.Strings(g.holders)
		fmt.Fprintf(b, "### %d. %s — %s\n\n", i+1, g.spdx, g.holders[0])
		if len(g.holders) > 1 {
			b.WriteString("<details>\n<summary>Applies to " + fmt.Sprint(len(g.holders)) + " dependencies</summary>\n\n")
			for _, h := range g.holders {
				fmt.Fprintf(b, "- %s\n", h)
			}
			b.WriteString("\n</details>\n\n")
		}
		fence := fenceFor(g.text)
		fmt.Fprintf(b, "%s\n%s\n%s\n\n", fence, g.text, fence)
	}
}
