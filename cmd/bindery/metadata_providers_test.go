package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/bnf"
	"github.com/vavallee/bindery/internal/metadata/dnb"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
)

func metadataProvidersFixture(t *testing.T) (*metadataProviders, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSettingsRepo(database)
	return newMetadataProviders(repo, openlibrary.New(), dnb.New(), hardcover.New()), repo
}

// providerConfig is what decides the provider set.
type providerConfig struct {
	primary        string // stored metadata.primary_provider, "" for unset
	hardcoverToken string
	googleBooksKey string
}

func storeProviderConfig(t *testing.T, repo *db.SettingsRepo, cfg providerConfig) {
	t.Helper()
	ctx := context.Background()
	// Written straight to the repo, past the settings API's validation, so
	// the out of band "hardcover primary, no token" state is reachable.
	for key, value := range map[string]string{
		api.SettingMetadataPrimaryProvider: cfg.primary,
		api.SettingHardcoverAPIToken:       cfg.hardcoverToken,
		api.SettingGoogleBooksAPIKey:       cfg.googleBooksKey,
	} {
		if value == "" {
			if err := repo.Delete(ctx, key); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := repo.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
}

// freshBootNames is what a restart with cfg stored would serve: a new
// process, new clients, the boot path in main.
func freshBootNames(t *testing.T, cfg providerConfig) []string {
	t.Helper()
	m, repo := metadataProvidersFixture(t)
	storeProviderConfig(t, repo, cfg)
	set := m.build(context.Background())
	return metadata.NewAggregator(set.primary, set.enrichers...).ProviderNames()
}

// providerSetCases pins the provider order the boot code in main.go wired
// before #1771 (Google Books if keyed, then Hardcover, OpenLibrary and DNB,
// each unless it is the primary). It is written out rather than derived so a
// change to assemble cannot quietly move the expectation with it.
var providerSetCases = []struct {
	name string
	cfg  providerConfig
	want []string
}{
	{"unset", providerConfig{}, []string{"openlibrary", "hardcover", "dnb"}},
	{"openlibrary", providerConfig{primary: "openlibrary"}, []string{"openlibrary", "hardcover", "dnb"}},
	{"openlibrary keyed", providerConfig{primary: "openlibrary", googleBooksKey: "k"}, []string{"openlibrary", "googlebooks", "hardcover", "dnb"}},
	{"dnb", providerConfig{primary: "dnb"}, []string{"dnb", "hardcover", "openlibrary"}},
	{"dnb keyed", providerConfig{primary: "dnb", googleBooksKey: "k"}, []string{"dnb", "googlebooks", "hardcover", "openlibrary"}},
	{"nb", providerConfig{primary: "nb"}, []string{"nb", "hardcover", "openlibrary", "dnb"}},
	{"bnf", providerConfig{primary: "bnf"}, []string{"bnf", "hardcover", "openlibrary", "dnb"}},
	{"bnf keyed", providerConfig{primary: "bnf", googleBooksKey: "k"}, []string{"bnf", "googlebooks", "hardcover", "openlibrary", "dnb"}},
	{"hardcover with token", providerConfig{primary: "hardcover", hardcoverToken: "tok"}, []string{"hardcover", "openlibrary", "dnb"}},
	{"hardcover keyed", providerConfig{primary: "hardcover", hardcoverToken: "tok", googleBooksKey: "k"}, []string{"hardcover", "googlebooks", "openlibrary", "dnb"}},
	{"hardcover without token falls back", providerConfig{primary: "hardcover"}, []string{"openlibrary", "hardcover", "dnb"}},
	{"unknown value falls back", providerConfig{primary: "goodreads"}, []string{"openlibrary", "hardcover", "dnb"}},
	{"enricher only provider falls back", providerConfig{primary: "googlebooks", googleBooksKey: "k"}, []string{"openlibrary", "googlebooks", "hardcover", "dnb"}},
}

// TestMetadataProviderSetBootAndRebuildAgree is the parity check #1771 asks
// for: for every primary choice, the set a fresh boot wires and the set a
// live rebuild installs on a running aggregator have the same providers in
// the same order, and both match the pre #1771 boot order.
func TestMetadataProviderSetBootAndRebuildAgree(t *testing.T) {
	for _, tc := range providerSetCases {
		t.Run(tc.name, func(t *testing.T) {
			boot := freshBootNames(t, tc.cfg)
			if !reflect.DeepEqual(boot, tc.want) {
				t.Errorf("boot set = %v, want %v", boot, tc.want)
			}

			// A process that booted on the defaults, then had the setting
			// changed under it.
			m, repo := metadataProvidersFixture(t)
			set := m.build(context.Background())
			agg := metadata.NewAggregator(set.primary, set.enrichers...)
			storeProviderConfig(t, repo, tc.cfg)
			if err := m.apply(context.Background(), agg); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := agg.ProviderNames(); !reflect.DeepEqual(got, boot) {
				t.Errorf("rebuilt set = %v, fresh boot = %v", got, boot)
			}
			if got, want := agg.PrimaryProviderName(), tc.want[0]; got != want {
				t.Errorf("PrimaryProviderName = %q, want %q", got, want)
			}
		})
	}
}

// TestMetadataProviderSwitchSequenceMatchesFreshBoot walks the switches the
// maintainer hit on dev (#1771) and checks each step against a restart with
// the same settings: openlibrary, then bnf, then hardcover with no token
// (which must fall back exactly as boot does), then dnb.
func TestMetadataProviderSwitchSequenceMatchesFreshBoot(t *testing.T) {
	m, repo := metadataProvidersFixture(t)
	set := m.build(context.Background())
	agg := metadata.NewAggregator(set.primary, set.enrichers...)

	for _, cfg := range []providerConfig{
		{primary: "openlibrary"},
		{primary: "bnf"},
		{primary: "hardcover"},
		{primary: "dnb"},
	} {
		storeProviderConfig(t, repo, cfg)
		if err := m.apply(context.Background(), agg); err != nil {
			t.Fatalf("apply %+v: %v", cfg, err)
		}
		if got, want := agg.ProviderNames(), freshBootNames(t, cfg); !reflect.DeepEqual(got, want) {
			t.Errorf("after switching to %q: set = %v, fresh boot = %v", cfg.primary, got, want)
		}
	}
	if got := agg.PrimaryProviderName(); got != "dnb" {
		t.Errorf("final primary = %q, want dnb", got)
	}
}

// TestMetadataProviderClientsAreReused guards the stateful clients: every
// rebuild must hand back the same instances, or a reconfiguration would reset
// Hardcover's quota and token holds, the refusal gates, and the cache keys
// that identify providers by address.
func TestMetadataProviderClientsAreReused(t *testing.T) {
	m, repo := metadataProvidersFixture(t)
	instances := map[string]metadata.Provider{}
	for _, cfg := range []providerConfig{
		{primary: "openlibrary", googleBooksKey: "a"},
		{primary: "bnf", googleBooksKey: "b"},
		{primary: "nb", googleBooksKey: "c"},
		{primary: "dnb", googleBooksKey: "d"},
		{primary: "hardcover", hardcoverToken: "tok", googleBooksKey: "e"},
		{primary: "bnf", googleBooksKey: "f"},
		{primary: "nb", googleBooksKey: "g"},
	} {
		storeProviderConfig(t, repo, cfg)
		set := m.build(context.Background())
		for _, p := range append([]metadata.Provider{set.primary}, set.enrichers...) {
			if prev, ok := instances[p.Name()]; ok && prev != p {
				t.Errorf("primary %q: %s is a new client instance", cfg.primary, p.Name())
			}
			instances[p.Name()] = p
		}
	}
	if _, ok := instances["bnf"].(*bnf.Client); !ok {
		t.Error("bnf is not wired to the BnF client")
	}
	if len(instances) != 6 {
		t.Errorf("saw providers %v, want all six", reflect.ValueOf(instances).MapKeys())
	}
}

// TestMetadataProviderPrimaryFor covers the wiring after resolution: each
// accepted value selects that provider, so a value the settings accept never
// silently serves OpenLibrary instead, and DNB as primary is the enricher's
// instance rather than a second client.
func TestMetadataProviderPrimaryFor(t *testing.T) {
	m, _ := metadataProvidersFixture(t)
	for _, name := range []string{"openlibrary", "dnb", "nb", "bnf", "hardcover"} {
		if got := m.primaryFor(name); got == nil || got.Name() != name {
			t.Errorf("primaryFor(%q) = %v, want the %s provider", name, got, name)
		}
	}
	if m.primaryFor("dnb") != m.dnb {
		t.Error("dnb primary is a second DNB client, want the enricher's instance")
	}
}

// TestGoogleBooksKeyChangeAppliesLive covers the key half of #1771: saving a
// key adds the keyed Google Books enricher to the running aggregator, and
// clearing it removes the enricher again, which is how an unkeyed install
// has always behaved (no Google Books traffic at all).
func TestGoogleBooksKeyChangeAppliesLive(t *testing.T) {
	m, repo := metadataProvidersFixture(t)
	set := m.build(context.Background())
	agg := metadata.NewAggregator(set.primary, set.enrichers...)
	has := func() bool {
		for _, name := range agg.ProviderNames() {
			if name == "googlebooks" {
				return true
			}
		}
		return false
	}
	if has() || m.googleBooks.APIKeyConfigured() {
		t.Fatalf("unkeyed boot wired Google Books: %v", agg.ProviderNames())
	}

	storeProviderConfig(t, repo, providerConfig{googleBooksKey: "AIzaKEY"})
	if err := m.apply(context.Background(), agg); err != nil {
		t.Fatal(err)
	}
	if !has() || !m.googleBooks.APIKeyConfigured() {
		t.Fatalf("after saving a key: providers %v, keyed %v", agg.ProviderNames(), m.googleBooks.APIKeyConfigured())
	}

	storeProviderConfig(t, repo, providerConfig{})
	if err := m.apply(context.Background(), agg); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Errorf("after clearing the key Google Books is still wired: %v", agg.ProviderNames())
	}
}

// TestMetadataProviderHookThroughSettingsHandler runs the real wiring end to
// end: a PUT through the settings handler with main's hook installed moves the
// live aggregator's primary, and says so in the response.
func TestMetadataProviderHookThroughSettingsHandler(t *testing.T) {
	m, repo := metadataProvidersFixture(t)
	set := m.build(context.Background())
	agg := metadata.NewAggregator(set.primary, set.enrichers...)
	h := api.NewSettingsHandler(repo).WithChangeHook(metadataProvidersReconfigurer(m, agg))

	put := func(key, value string) map[string]any {
		t.Helper()
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("key", key)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+key, strings.NewReader(`{"value":"`+value+`"}`))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		h.Set(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s=%s: status %d: %s", key, value, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	if body := put(api.SettingMetadataPrimaryProvider, "bnf"); body["appliedLive"] != true {
		t.Errorf("appliedLive = %v, want true", body["appliedLive"])
	}
	if got := agg.PrimaryProviderName(); got != "bnf" {
		t.Errorf("primary after PUT = %q, want bnf", got)
	}
	if body := put(api.SettingGoogleBooksAPIKey, "AIzaKEY"); body["appliedLive"] != true || body["value"] != "" {
		t.Errorf("Google Books key PUT answered %v, want appliedLive true and no value", body)
	}
	if got := agg.ProviderNames(); !reflect.DeepEqual(got, []string{"bnf", "googlebooks", "hardcover", "openlibrary", "dnb"}) {
		t.Errorf("providers after saving the Google Books key = %v", got)
	}
	if body := put("autoGrab.enabled", "true"); body["appliedLive"] != nil {
		t.Errorf("unrelated key reported appliedLive %v", body["appliedLive"])
	}
}

// TestResolveMetadataPrimaryProvider covers the boot-time provider selection:
// openlibrary stays the default for every unset / unusable value, dnb and
// hardcover are honoured when selected, and a tokenless hardcover selection
// degrades to openlibrary instead of leaving every lookup unauthenticated.
func TestResolveMetadataPrimaryProvider(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		hasToken   bool
		want       string
	}{
		{"unset defaults to openlibrary", "", false, "openlibrary"},
		{"unset defaults to openlibrary even with a token", "", true, "openlibrary"},
		{"explicit openlibrary", "openlibrary", false, "openlibrary"},
		{"explicit dnb", "dnb", false, "dnb"},
		{"explicit nb", "nb", false, "nb"},
		{"explicit bnf", "bnf", false, "bnf"},
		{"hardcover with a token", "hardcover", true, "hardcover"},
		{"hardcover without a token falls back", "hardcover", false, "openlibrary"},
		{"unknown value falls back", "goodreads", true, "openlibrary"},
		{"enricher-only provider falls back", "googlebooks", true, "openlibrary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMetadataPrimaryProvider(tc.configured, tc.hasToken); got != tc.want {
				t.Errorf("resolveMetadataPrimaryProvider(%q, %v) = %q, want %q",
					tc.configured, tc.hasToken, got, tc.want)
			}
		})
	}
}

// TestWikidataAuthorEnricherIsOptInAndAppliesLive covers #3287: the Wikidata
// author enricher is off until its setting is saved as true, a save wires it
// into the running aggregator without a restart, and clearing it removes it.
// Saving the setting must also be one of the keys that triggers a rebuild.
func TestWikidataAuthorEnricherIsOptInAndAppliesLive(t *testing.T) {
	m, repo := metadataProvidersFixture(t)
	ctx := context.Background()
	set := m.build(ctx)
	agg := metadata.NewAggregator(set.primary, set.enrichers...)
	agg.SetAuthorEnrichers(set.authorEnrichers...)
	if names := agg.AuthorEnricherNames(); len(names) != 0 {
		t.Fatalf("a default boot wired author enrichers %v; Wikidata must be opt in", names)
	}
	if !metadataProviderSettingKeys[api.SettingMetadataAuthorWikidata] {
		t.Fatal("saving the Wikidata setting does not trigger a provider rebuild")
	}

	if err := repo.Set(ctx, api.SettingMetadataAuthorWikidata, "true"); err != nil {
		t.Fatal(err)
	}
	if err := m.apply(ctx, agg); err != nil {
		t.Fatal(err)
	}
	if names := agg.AuthorEnricherNames(); len(names) != 1 || names[0] != "wikidata" {
		t.Fatalf("after switching it on: author enrichers %v, want [wikidata]", names)
	}

	if err := repo.Set(ctx, api.SettingMetadataAuthorWikidata, "false"); err != nil {
		t.Fatal(err)
	}
	if err := m.apply(ctx, agg); err != nil {
		t.Fatal(err)
	}
	if names := agg.AuthorEnricherNames(); len(names) != 0 {
		t.Errorf("after switching it off: author enrichers %v, want none", names)
	}
}
