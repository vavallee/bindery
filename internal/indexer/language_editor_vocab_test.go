package indexer

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// metadataTabPath points at the metadata profile editor relative to this
// package directory (go test always runs with the package dir as cwd).
const metadataTabPath = "../../web/src/pages/settings/MetadataTab.tsx"

// TestMetadataEditorLanguageVocabulary is the drift guard for #2273. The
// release-name language filter is a negative signal: a release tagged with a
// language outside the profile's allowed set is dropped. So every code
// releaseLanguageTags can emit has to be selectable in the editor, or the
// filter can drop a language the operator has no way to allow — nine codes
// (kor, ara, swe, nor, dan, pol, cze, tur, hin) were in exactly that state
// before this test existed.
//
// The reverse direction is checked too: a code offered in the editor that no
// marker can produce is a checkbox that changes nothing, which is the same
// shape of dead setting as #1723.
func TestMetadataEditorLanguageVocabulary(t *testing.T) {
	raw, err := os.ReadFile(metadataTabPath)
	if err != nil {
		t.Fatalf("reading %s: %v (this guard needs the full repo checkout)", metadataTabPath, err)
	}

	block := regexp.MustCompile(`const KNOWN_LANGUAGES[^=]*=\s*\[([\s\S]*?)\n\]`).FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatalf("could not find `const KNOWN_LANGUAGES = [...]` in %s; if it was renamed or moved, update this drift guard to follow it", metadataTabPath)
	}
	var offered []string
	for _, m := range regexp.MustCompile(`code:\s*'([^']+)'`).FindAllStringSubmatch(block[1], -1) {
		offered = append(offered, m[1])
	}
	if len(offered) == 0 {
		t.Fatalf("`const KNOWN_LANGUAGES` in %s parsed to an empty list; the editor would offer no languages", metadataTabPath)
	}

	seen := map[string]bool{}
	for _, code := range offered {
		if seen[code] {
			t.Errorf("KNOWN_LANGUAGES in %s lists %q twice", metadataTabPath, code)
		}
		seen[code] = true
	}

	emitted := map[string]bool{}
	for _, code := range releaseLanguageTags {
		emitted[code] = true
	}
	for code := range emitted {
		if !slices.Contains(offered, code) {
			t.Errorf("releaseLanguageTags can tag a release %q and FilterByAllowedLanguages will drop it, but KNOWN_LANGUAGES in %s does not offer %q, so no profile can allow it (#2273)", code, metadataTabPath, code)
		}
	}
	for _, code := range offered {
		if !emitted[code] {
			t.Errorf("KNOWN_LANGUAGES in %s offers %q but no marker in releaseLanguageTags produces it, so allowing or disallowing it changes nothing", metadataTabPath, code)
		}
	}

	// FilterByAllowedLanguages reduces each profile entry through
	// models.NormalizeLanguageCode before comparing it to a release's tags, so
	// every code the editor offers has to survive that reduction unchanged. A
	// canonicaliser that rewrote "ger" to "deu" would leave the checkbox
	// checked and the filter matching nothing.
	for _, code := range offered {
		if got := models.NormalizeLanguageCode(code); got != code {
			t.Errorf("KNOWN_LANGUAGES offers %q but models.NormalizeLanguageCode rewrites it to %q, so ticking that box would filter on a code no release tag produces", code, got)
		}
	}
}
