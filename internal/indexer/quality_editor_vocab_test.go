package indexer

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// qualityTabPath points at the quality profile editor relative to this
// package directory (go test always runs with the package dir as cwd).
const qualityTabPath = "../../web/src/pages/settings/QualityTab.tsx"

// extractTSXStringList pulls the string literals out of a
// `const NAME = ['a', 'b', ...] as const` declaration in the editor source.
// It fails the test loudly when the declaration cannot be found, so a rename
// or refactor of the TSX constants breaks here instead of silently passing.
func extractTSXStringList(t *testing.T, src, name string) []string {
	t.Helper()
	re := regexp.MustCompile(`const ` + name + `\s*=\s*\[([^\]]*)\]`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("could not find `const %s = [...]` in %s; if the constant was renamed or moved, update this drift guard to follow it", name, qualityTabPath)
	}
	var out []string
	for _, q := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	if len(out) == 0 {
		t.Fatalf("`const %s` in %s parsed to an empty list; the editor would offer no formats", name, qualityTabPath)
	}
	return out
}

// TestQualityEditorFormatVocabulary is the drift guard for #1700: the quality
// profile editor keeps a hand-written mirror of formatTokens, and when the
// mirror fell behind (8 formats offered, 17 recognised) the missing nine
// could never be allowed once #1693 made the allow-list authoritative.
//
// It asserts that EBOOK_FORMATS and AUDIOBOOK_FORMATS in QualityTab.tsx,
// taken together, are exactly formatTokens, and that each token sits in the
// list matching MediaTypeForFormat. Membership is compared as sets: the order
// within each TSX list is a UI concern (best first, the order the chips are
// offered in) that this test does not pin. Adding a token to formatTokens
// without giving it a checkbox, or adding a checkbox for a token release
// parsing can never emit, fails here.
func TestQualityEditorFormatVocabulary(t *testing.T) {
	raw, err := os.ReadFile(qualityTabPath)
	if err != nil {
		t.Fatalf("reading %s: %v (this guard needs the full repo checkout)", qualityTabPath, err)
	}
	src := string(raw)

	ebook := extractTSXStringList(t, src, "EBOOK_FORMATS")
	audio := extractTSXStringList(t, src, "AUDIOBOOK_FORMATS")

	seen := map[string]string{}
	for _, l := range []struct {
		name    string
		formats []string
	}{{"EBOOK_FORMATS", ebook}, {"AUDIOBOOK_FORMATS", audio}} {
		for _, f := range l.formats {
			if prev, dup := seen[f]; dup {
				t.Errorf("%q appears in both %s and %s in %s", f, prev, l.name, qualityTabPath)
				continue
			}
			seen[f] = l.name
		}
	}

	var wantEbook, wantAudio []string
	for _, tok := range formatTokens {
		switch MediaTypeForFormat(tok) {
		case models.MediaTypeEbook:
			wantEbook = append(wantEbook, tok)
		case models.MediaTypeAudiobook:
			wantAudio = append(wantAudio, tok)
		default:
			t.Errorf("formatTokens entry %q maps to no media type; TestMediaTypeForFormat should have caught this", tok)
		}
	}

	assertSameSet := func(name string, got, want []string) {
		t.Helper()
		for _, w := range want {
			if !slices.Contains(got, w) {
				t.Errorf("release parsing emits %q but %s in %s has no entry for it, so it can never be allowed (#1700); add it to the editor list", w, name, qualityTabPath)
			}
		}
		for _, g := range got {
			if !slices.Contains(want, g) {
				t.Errorf("%s in %s offers %q, which release parsing never emits (formatTokens in internal/indexer/release.go) or which belongs in the other list per MediaTypeForFormat", name, qualityTabPath, g)
			}
		}
	}
	assertSameSet("EBOOK_FORMATS", ebook, wantEbook)
	assertSameSet("AUDIOBOOK_FORMATS", audio, wantAudio)

	// The new-profile seed must be a subset of the ebook vocabulary; a typo
	// there would create profile items no release can ever match.
	for _, f := range extractTSXStringList(t, src, "DEFAULT_EBOOK_ITEMS") {
		if !slices.Contains(wantEbook, f) {
			t.Errorf("DEFAULT_EBOOK_ITEMS in %s seeds %q, which is not an ebook format release parsing can emit", qualityTabPath, f)
		}
	}
}
