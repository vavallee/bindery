package filterengine

import (
	"encoding/csv"
	"os"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

type datasetWork struct {
	authorName string
	title      string
	workKind   string
}

func loadWorksSample(t *testing.T) []datasetWork {
	t.Helper()
	f, err := os.Open("testdata/works_sample.csv")
	if err != nil {
		t.Fatalf("open testdata/works_sample.csv: %v", err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("parse testdata/works_sample.csv: %v", err)
	}
	if len(records) < 2 {
		t.Fatal("testdata/works_sample.csv has no data rows")
	}
	header := records[0]
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[h] = i
	}
	works := make([]datasetWork, 0, len(records)-1)
	for _, row := range records[1:] {
		works = append(works, datasetWork{
			authorName: row[col["author_name"]],
			title:      row[col["title"]],
			workKind:   row[col["work_kind"]],
		})
	}
	return works
}

// TestWorksSample_CoreTitlesNeverFalsePositive is the regression pinned
// against fiction-author-dataset's hand-verified ground truth (#2235,
// testdata/README.md): JunkTitleSignal and PartBookSignal, run at the
// shipped 0/0 default, must never band a real ("core") title as EXCLUDE.
// Measured against the full 1,422-row dataset (not just this 80-row sample)
// this holds at 804/804 — 100% — for both signals; this test pins the same
// property on the checked-in sample so it runs in CI without a network
// fetch.
func TestWorksSample_CoreTitlesNeverFalsePositive(t *testing.T) {
	works := loadWorksSample(t)
	junk := NewJunkTitleSignal()
	partBook := NewPartBookSignal()
	fCtx := &Context{SkipPartBooks: true}

	coreCount := 0
	for _, w := range works {
		if w.workKind != "core" {
			continue
		}
		coreCount++
		fCtx.NormalizedAuthor = strings.ToLower(strings.TrimSpace(w.authorName))
		result := Decide(Candidate{Book: &models.Book{Title: w.title}}, fCtx, junk, partBook)
		if result.Band == BandExclude {
			t.Errorf("core title %q (author %q) was EXCLUDED: %+v", w.title, w.authorName, result.Observations)
		}
	}
	if coreCount == 0 {
		t.Fatal("fixture sanity check failed: no core-kind rows found in testdata/works_sample.csv")
	}
}

// TestWorksSample_PartBookSignalOnlyCatchesTitleShapedBundles documents,
// rather than asserts a target for, PartBookSignal's real recall on the
// dataset's compilation/posthumous-compilation rows: measured against the
// full dataset, it's 53/284 (18.7%) — PartBookSignal only catches bundles
// whose TITLE says so ("Boxed Set", "Omnibus", "Books 1-3"); most
// compilations in this ground truth carry no such marker and are only
// distinguishable by cluster-level edition_count (see cluster.go's doc for
// the measurement that motivates that future signal). This test asserts
// the recall is neither ~0% (the signal does something) nor anywhere near
// 100% (a naive reader of "18.7%" might otherwise assume this needs fixing
// before it can be trusted — it doesn't; it's precisely why clustering
// exists as a separate stage) against this sample.
func TestWorksSample_PartBookSignalOnlyCatchesTitleShapedBundles(t *testing.T) {
	works := loadWorksSample(t)
	partBook := NewPartBookSignal()
	fCtx := &Context{SkipPartBooks: true}

	var total, caught int
	for _, w := range works {
		if w.workKind != "compilation" && w.workKind != "posthumous-compilation" {
			continue
		}
		total++
		result := Decide(Candidate{Book: &models.Book{Title: w.title}}, fCtx, partBook)
		if result.Band == BandExclude {
			caught++
		}
	}
	if total == 0 {
		t.Fatal("fixture sanity check failed: no compilation-kind rows found in testdata/works_sample.csv")
	}
	// Not a tight assertion by design: this documents an order of magnitude
	// (title-shape detection catches a real minority, not all and not none),
	// not a specific percentage that would make this test brittle against
	// an unrelated future change to the bundle-title keyword list.
	if caught == 0 {
		t.Errorf("PartBookSignal caught 0/%d compilations in the sample — expected it to catch at least the ones with an obvious title marker", total)
	}
	if caught == total {
		t.Errorf("PartBookSignal caught all %d/%d compilations in the sample — expected title-shape detection to miss most of them (that's what motivates the cluster-level signal)", caught, total)
	}
}

// TestLanguageSignal_EmptyLanguageIsNotForeign is the named regression case
// from issue #2235 itself: "Summer Stars: Second Nature / One Summer", an
// obviously real English-language Roberts bind-up, was dropped by the
// pre-#2235 boolean chain because its language field arrived empty and the
// profile had unknown_language_behavior=fail. This case isn't in
// fiction-author-dataset (it came from a live sync run, not the ground-truth
// dataset — see testdata/README.md for why the dataset can't carry it), so
// it's a synthetic fixture built to match the original report exactly.
//
// LanguageSignal wraps models.IsLanguageAllowed verbatim (see
// signals_language.go), so this is really pinning that function's
// documented "empty language honors unknown_language_behavior, defaulting
// to pass" contract specifically against the title that motivated #2235 —
// not testing new logic, testing that nothing about the #2235 port
// regressed it.
func TestLanguageSignal_EmptyLanguageIsNotForeign(t *testing.T) {
	book := &models.Book{Title: "Summer Stars: Second Nature / One Summer", Language: ""}
	s := NewLanguageSignal()

	// unknown_language_behavior=pass (the safe default, #232): must KEEP.
	passCtx := &Context{AllowedLanguages: []string{"eng"}, UnknownLangFail: false}
	if result := Decide(Candidate{Book: book}, passCtx, s); result.Band == BandExclude {
		t.Errorf("with unknown_language_behavior=pass, an empty-language real book must not be EXCLUDED: %+v", result.Observations)
	}

	// unknown_language_behavior=fail: this IS the #2235-reported failure
	// mode. Asserting EXCLUDE here (not KEEP) is deliberate — the fix isn't
	// "empty language always passes", it's "the fail setting means what it
	// says, and the actual fix is giving the language a real value via
	// FillMissingAuthorWorkLanguages/applyAuthorMajorityLanguageFallback
	// before it ever reaches this signal" (both already exist on main,
	// unrelated to #2235 — see the Opus architecture plan's finding that the
	// original 60.9% figure predates applyAuthorMajorityLanguageFallback).
	failCtx := &Context{AllowedLanguages: []string{"eng"}, UnknownLangFail: true}
	if result := Decide(Candidate{Book: book}, failCtx, s); result.Band != BandExclude {
		t.Errorf("with unknown_language_behavior=fail, an unresolved empty-language book is still excluded by design — this asserts that design choice didn't silently change, got %+v", result.Observations)
	}
}
