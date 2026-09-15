package filterengine

// DefaultSignals returns the v1 shipped signal set, in the exact order the
// pre-#2235 boolean chain checked them: media type, junk title, language,
// part book, missing date, missing ISBN, min pages — with ProviderNoiseSignal
// inserted right after JunkTitleSignal, since it replays a claim
// (models.SignalProviderOpenLibraryNoise) that pre-#2235 was decided
// unilaterally by the OpenLibrary client itself, before the work ever
// reached this chain at all, and routes to the same SkippedJunk counter a
// junk title already uses. See Registry's doc for why order matters beyond
// readability — it's what makes strongest-observation counter attribution
// order-equivalent to first-drop-wins for the golden parity test.
//
// Every signal here is exclude-only, veto-weight, per the package doc's
// keep_threshold=exclude_threshold=0 parity argument. Registering a graded
// or keep-direction signal here without revisiting that default is exactly
// what TestRegistryIsVetoOnlyAtV1 exists to catch.
func DefaultSignals() []Signal {
	return []Signal{
		NewMediaTypeSignal(),
		NewJunkTitleSignal(),
		NewProviderNoiseSignal(),
		NewLanguageSignal(),
		NewPartBookSignal(),
		NewMissingDateSignal(),
		NewMissingISBNSignal(),
		NewMinPagesSignal(),
	}
}

// DefaultRegistry builds the Registry for DefaultSignals().
func DefaultRegistry() *Registry {
	return MustRegistry(DefaultSignals()...)
}
