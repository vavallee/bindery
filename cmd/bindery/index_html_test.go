package main

import (
	"strings"
	"testing"
)

// fakeIndex mimics the relevant shape of Vite's built index.html: the entry
// module <script> and the stylesheet <link> live in <head> with relative
// "./assets/…" URLs. That relative form is exactly what makes the <base> tag's
// position load-bearing.
const fakeIndex = `<!doctype html><html lang="en"><head>` +
	`<meta charset="UTF-8">` +
	`<script type="module" crossorigin src="./assets/index-abc.js"></script>` +
	`<link rel="stylesheet" crossorigin href="./assets/index-abc.css">` +
	`</head><body><div id="root"></div></body></html>`

// TestInjectBaseHTML_BasePrecedesAssets is the regression guard for the
// white-page-on-reload bug: <base> must be injected BEFORE the first relative
// "./assets/…" reference. If it lands after them (e.g. before </head>), the
// browser resolves the bundle against the document path and every non-root SPA
// route 404s its JS/CSS on a hard reload.
func TestInjectBaseHTML_BasePrecedesAssets(t *testing.T) {
	out := injectBaseHTML(fakeIndex, "/bindery")

	baseIdx := strings.Index(out, "<base ")
	if baseIdx == -1 {
		t.Fatalf("no <base> tag injected:\n%s", out)
	}
	assetIdx := strings.Index(out, "./assets/")
	if assetIdx == -1 {
		t.Fatalf("fixture lost its ./assets/ references:\n%s", out)
	}
	if baseIdx > assetIdx {
		t.Fatalf("<base> (at %d) must precede first ./assets/ ref (at %d):\n%s", baseIdx, assetIdx, out)
	}
}

func TestInjectBaseHTML_HrefForSubpath(t *testing.T) {
	out := injectBaseHTML(fakeIndex, "/bindery")
	if !strings.Contains(out, `<base href="/bindery/">`) {
		t.Fatalf("expected <base href=\"/bindery/\">, got:\n%s", out)
	}
	if !strings.Contains(out, `<script src="/bindery/__bindery_base.js"></script>`) {
		t.Fatalf("expected bootstrap script with /bindery prefix, got:\n%s", out)
	}
}

func TestInjectBaseHTML_HrefForRoot(t *testing.T) {
	out := injectBaseHTML(fakeIndex, "")
	if !strings.Contains(out, `<base href="/">`) {
		t.Fatalf("expected <base href=\"/\"> for empty url base, got:\n%s", out)
	}
}
