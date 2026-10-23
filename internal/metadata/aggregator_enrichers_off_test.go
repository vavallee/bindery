package metadata

import "testing"

// TestProviderSetKeyDiffersWhenAnEnricherIsRemoved covers #1890: turning an
// enricher off installs a smaller set on the same primary, and a cache entry
// written under the larger set must not be served under the smaller one.
func TestProviderSetKeyDiffersWhenAnEnricherIsRemoved(t *testing.T) {
	primary := &tagProvider{name: "openlibrary"}
	hc := &tagProvider{name: "hardcover"}
	dnb := &tagProvider{name: "dnb"}
	agg := NewAggregator(primary, hc, dnb)
	full := agg.load().providerSetKey()

	if err := agg.Reconfigure(primary, hc); err != nil {
		t.Fatal(err)
	}
	withoutDNB := agg.load().providerSetKey()
	if err := agg.Reconfigure(primary); err != nil {
		t.Fatal(err)
	}
	bare := agg.load().providerSetKey()

	if full == withoutDNB || withoutDNB == bare || full == bare {
		t.Errorf("provider set keys collide: full=%q withoutDNB=%q bare=%q", full, withoutDNB, bare)
	}
	if err := agg.Reconfigure(primary, hc, dnb); err != nil {
		t.Fatal(err)
	}
	if got := agg.load().providerSetKey(); got != full {
		t.Errorf("restoring the set changed its key: %q vs %q", got, full)
	}
}
