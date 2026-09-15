package filterengine

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// vetoFixtures maps every DefaultSignals() member's ID to a Candidate/Context
// pair guaranteed to make it fire. TestRegistryIsVetoOnlyAtV1 uses these to
// check the actual EMITTED observation, not just the Weight() accessor — see
// that test's doc for why the accessor alone isn't a sufficient guard. A
// signal added to DefaultSignals() with no matching entry here fails that
// test explicitly (missing fixture), rather than silently skipping the
// check, so this table has to grow with the registry.
func vetoFixtures() map[string]struct {
	c   Candidate
	ctx *Context
} {
	pages50 := 50
	return map[string]struct {
		c   Candidate
		ctx *Context
	}{
		"mediatype.strictMismatch": {
			c:   Candidate{Book: &models.Book{MediaType: models.MediaTypeAudiobook}},
			ctx: &Context{StrictMediaType: true, MediaTypeDefault: models.MediaTypeEbook},
		},
		"junk.titleEmptyOrAuthorName": {
			c:   Candidate{Book: &models.Book{Title: ""}},
			ctx: &Context{},
		},
		"junk.providerFlaggedNoise": {
			c: Candidate{Book: &models.Book{Observations: []models.FilterObservation{
				{Signal: models.SignalProviderOpenLibraryNoise, Reason: "test fixture"},
			}}},
			ctx: &Context{},
		},
		"language.notAllowed": {
			c:   Candidate{Book: &models.Book{Language: "fre"}},
			ctx: &Context{AllowedLanguages: []string{"eng"}},
		},
		"structure.partBookTitle": {
			c:   Candidate{Book: &models.Book{Title: "The Foo Trilogy Boxed Set"}},
			ctx: &Context{SkipPartBooks: true},
		},
		"catalog.missingReleaseDate": {
			c:   Candidate{Book: &models.Book{ReleaseDate: nil}},
			ctx: &Context{SkipMissingDate: true},
		},
		"catalog.missingISBN": {
			c:   Candidate{Book: &models.Book{}, Editions: nil},
			ctx: &Context{SkipMissingISBN: true},
		},
		"catalog.belowMinPages": {
			c:   Candidate{Book: &models.Book{}, Editions: []models.Edition{{NumPages: &pages50}}},
			ctx: &Context{MinPages: 200},
		},
	}
}

// TestRegistryIsVetoOnlyAtV1 guards the parity default. Migration 086 ships
// keep_threshold = exclude_threshold = 0 for every existing metadata
// profile, which only reproduces the pre-#2235 boolean chain's keep/exclude
// decision while every registered signal is exclude-direction at exactly
// -vetoWeight. Registering a graded (any other magnitude) or keep-direction
// (positive weight) signal in DefaultSignals without also revisiting the
// shipped thresholds is a silent behavior change for every existing user's
// profile, so it fails here instead of shipping quietly.
//
// Checks the signal's Weight() accessor AND the Weight/Confidence on the
// observation it actually emits when triggered (via vetoFixtures + the same
// fires() helper the per-signal tests use) — a signal could in principle
// report a veto-looking Weight() while its Observe method emits an
// observation carrying a different (graded) Weight value, decoupled from
// what the accessor claims. The accessor alone doesn't catch that; this
// does.
func TestRegistryIsVetoOnlyAtV1(t *testing.T) {
	fixtures := vetoFixtures()
	for _, s := range DefaultSignals() {
		if w := s.Weight(); w != -vetoWeight {
			t.Errorf("signal %q has weight %v, want exactly %v (v1 registry must be veto-only) — "+
				"if you're intentionally adding a graded or keep-direction signal, migration 086's "+
				"keep_threshold/exclude_threshold defaults must move with it, not stay implicit",
				s.ID(), w, -vetoWeight)
		}
		fixture, ok := fixtures[s.ID()]
		if !ok {
			t.Errorf("signal %q has no entry in vetoFixtures — add one so its emitted observation's "+
				"Weight/Confidence, not just Weight(), gets checked", s.ID())
			continue
		}
		if !fires(t, s, fixture.c, fixture.ctx) {
			t.Errorf("signal %q's vetoFixtures entry did not actually fire — fixture is stale or wrong", s.ID())
		}
	}
}

func TestNewRegistry_RejectsDuplicateID(t *testing.T) {
	_, err := NewRegistry(NewLanguageSignal(), NewLanguageSignal())
	if err == nil {
		t.Fatal("want an error registering two signals with the same ID, got nil")
	}
}

func TestNewRegistry_RejectsEmptyID(t *testing.T) {
	_, err := NewRegistry(&fakeSignal{id: ""})
	if err == nil {
		t.Fatal("want an error registering a signal with an empty ID, got nil")
	}
}

func TestMustRegistry_PanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want MustRegistry to panic on a duplicate ID")
		}
	}()
	MustRegistry(NewLanguageSignal(), NewLanguageSignal())
}

// TestRegistry_Decide pins that Registry.Decide is exactly the batched-pass
// equivalent its doc claims: running every registered signal against a
// candidate produces the identical Result the package-level Decide would,
// given the same signal list.
func TestRegistry_Decide(t *testing.T) {
	r := MustRegistry(NewLanguageSignal())
	c := Candidate{Book: &models.Book{Language: "fre"}}
	ctx := &Context{AllowedLanguages: []string{"eng"}}

	got := r.Decide(c, ctx)
	want := Decide(c, ctx, NewLanguageSignal())

	if got.Band != want.Band {
		t.Errorf("Registry.Decide Band = %v, want %v", got.Band, want.Band)
	}
	if got.Band != BandExclude {
		t.Fatalf("Registry.Decide Band = %v, want BandExclude for a disallowed language", got.Band)
	}
	if len(got.Observations) != 1 || got.Observations[0].Signal != "language.notAllowed" {
		t.Errorf("Registry.Decide Observations = %+v, want one language.notAllowed observation", got.Observations)
	}
}

func TestDefaultRegistry_MatchesDefaultSignalsOrder(t *testing.T) {
	r := DefaultRegistry()
	signals := r.Signals()
	want := []string{
		"mediatype.strictMismatch",
		"junk.titleEmptyOrAuthorName",
		"junk.providerFlaggedNoise",
		"language.notAllowed",
		"structure.partBookTitle",
		"catalog.missingReleaseDate",
		"catalog.missingISBN",
		"catalog.belowMinPages",
	}
	if len(signals) != len(want) {
		t.Fatalf("registry has %d signals, want %d", len(signals), len(want))
	}
	for i, id := range want {
		if signals[i].ID() != id {
			t.Errorf("signal at index %d = %q, want %q (registry order must match the pre-#2235 check order)", i, signals[i].ID(), id)
		}
	}
}

type fakeSignal struct{ id string }

func (f *fakeSignal) ID() string      { return f.id }
func (f *fakeSignal) Weight() float64 { return -vetoWeight }
func (f *fakeSignal) Observe(Candidate, *Context) []models.FilterObservation {
	return nil
}
