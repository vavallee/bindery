package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/bnf"
	"github.com/vavallee/bindery/internal/metadata/googlebooks"
	"github.com/vavallee/bindery/internal/metadata/nb"
	"github.com/vavallee/bindery/internal/metadata/wikidata"
)

// metadataProviders owns every metadata client for the life of the process
// and turns the current settings into the aggregator's provider set.
//
// The clients are built once and reused by every rebuild, never recreated:
// Hardcover keeps its daily quota and rejected token holds inside the
// client, OpenLibrary, DNB, NB and Google Books each hold a refusal gate,
// and BnF's pacer is process wide. A rebuild that made new clients would
// reset those holds, or let a second client run beside a held one. The cache
// keys also identify providers by address (metadata.providerSetKey), which
// is only stable because the instances are.
//
// Google Books is the one client whose configuration changes; its key is set
// live with SetAPIKey rather than by building a new client.
type metadataProviders struct {
	settings    *db.SettingsRepo
	openLibrary metadata.Provider
	dnb         metadata.Provider
	hardcover   metadata.Provider
	nb          metadata.Provider
	bnf         metadata.Provider
	googleBooks *googlebooks.Client
	// wikidata is the opt in author enricher (#3287). Building it sends
	// nothing; it is only asked once SettingMetadataAuthorWikidata is on.
	wikidata *wikidata.Client

	// mu serialises apply. Each apply reads the settings and installs what it
	// read; without it two saves racing could install the older read last.
	mu sync.Mutex
}

func newMetadataProviders(settings *db.SettingsRepo, openLibrary, dnbClient, hardcover metadata.Provider) *metadataProviders {
	return &metadataProviders{
		settings:    settings,
		openLibrary: openLibrary,
		dnb:         dnbClient,
		hardcover:   hardcover,
		// NB and BnF are opt in primaries and never enrichers: an install
		// that did not choose one sends it no traffic, because neither
		// publishes rate limits for a fleet of independent installs. Building
		// the client sends nothing; only wiring it as primary does.
		nb:          nb.New(),
		bnf:         bnf.New(),
		googleBooks: googlebooks.New(""),
		wikidata:    wikidata.New(),
	}
}

// metadataProviderSet is one resolved configuration: the primary's name (after
// resolveMetadataPrimaryProvider's fallbacks), the primary, and the enrichers
// in the order the aggregator asks them.
type metadataProviderSet struct {
	primaryName string
	primary     metadata.Provider
	enrichers   []metadata.Provider
	// authorEnrichers fill an author's empty photo and bio (#3287).
	authorEnrichers []metadata.AuthorEnricher
	// googleBooksKeyed and hardcoverToken are for the log line only.
	googleBooksKeyed bool
	hardcoverToken   bool
}

// build reads the settings and returns the provider set they select. It is
// what boot runs and what every reconfiguration runs, so the two cannot
// drift apart. It also hands the Google Books key to the shared client.
func (m *metadataProviders) build(ctx context.Context) metadataProviderSet {
	configuredPrimary := ""
	if s, _ := m.settings.Get(ctx, api.SettingMetadataPrimaryProvider); s != nil {
		configuredPrimary = s.Value
	}
	hcToken := api.GetHardcoverAPIToken(ctx, m.settings) != ""
	gbKey := googleBooksAPIKey(ctx, m.settings)
	m.googleBooks.SetAPIKey(gbKey)
	set := m.assemble(resolveMetadataPrimaryProvider(configuredPrimary, hcToken), gbKey != "")
	set.hardcoverToken = hcToken
	if s, _ := m.settings.Get(ctx, api.SettingMetadataAuthorWikidata); s != nil && strings.EqualFold(strings.TrimSpace(s.Value), "true") {
		set.authorEnrichers = append(set.authorEnrichers, m.wikidata)
	}
	return set
}

// assemble wires the clients for a resolved primary name. The primary decides
// what an author's catalogue looks like; every other provider is added as an
// enricher so metadata is still cross-checked.
//
//   - "dnb" is the recommended choice for German/Austrian/Swiss catalogues,
//     where OpenLibrary coverage is too thin for German-language books.
//   - "nb" is the same for Norwegian ones: original titles where
//     OpenLibrary has the English translation.
//   - "bnf" is the same for French editions, translations included: the
//     French title an indexer names a release after (#3144).
//   - "hardcover" trades breadth for a cleaner, editorially curated
//     catalogue: no translation editions masquerading as separate works, no
//     omnibus bundles, no non-book merchandise rows (#2040).
func (m *metadataProviders) assemble(primaryName string, googleBooksKeyed bool) metadataProviderSet {
	set := metadataProviderSet{
		primaryName:      primaryName,
		primary:          m.primaryFor(primaryName),
		googleBooksKeyed: googleBooksKeyed,
	}
	if googleBooksKeyed {
		set.enrichers = append(set.enrichers, m.googleBooks)
	}
	if primaryName != "hardcover" {
		// The enricher is registered whether or not a token is set: the token
		// is read live per request through WithTokenSource, so configuring one
		// later takes effect without a restart (#2075).
		set.enrichers = append(set.enrichers, m.hardcover)
	}
	if primaryName != "openlibrary" {
		set.enrichers = append(set.enrichers, m.openLibrary)
	}
	if primaryName != "dnb" {
		set.enrichers = append(set.enrichers, m.dnb)
	}
	return set
}

// primaryFor is the provider wired as primary for name, a value
// resolveMetadataPrimaryProvider returned. Primary and enricher share one
// instance for the providers that can be either.
func (m *metadataProviders) primaryFor(name string) metadata.Provider {
	switch name {
	case "dnb":
		return m.dnb
	case "nb":
		return m.nb
	case "bnf":
		// BnF publishes no rate limits; the client paces itself at one
		// request a second (internal/metadata/bnf).
		return m.bnf
	case "hardcover":
		return m.hardcover
	default:
		return m.openLibrary
	}
}

// apply rebuilds the provider set from the current settings and installs it
// on agg. On error the aggregator keeps the set it had.
func (m *metadataProviders) apply(ctx context.Context, agg *metadata.Aggregator) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.build(ctx)
	if err := agg.Reconfigure(set.primary, set.enrichers...); err != nil {
		return err
	}
	agg.SetAuthorEnrichers(set.authorEnrichers...)
	set.log("metadata providers reconfigured")
	return nil
}

// log reports the set in one line, plus the Hardcover note #2075 asked for:
// the enricher is wired without a token but every call short circuits.
func (s metadataProviderSet) log(msg string) {
	names := make([]string, 0, len(s.enrichers))
	for _, p := range s.enrichers {
		names = append(names, p.Name())
	}
	authorNames := make([]string, 0, len(s.authorEnrichers))
	for _, e := range s.authorEnrichers {
		authorNames = append(authorNames, e.Name())
	}
	slog.Info(msg, "primary", s.primaryName, "enrichers", names, "author_enrichers", authorNames, "googlebooks_keyed", s.googleBooksKeyed)
	if s.primaryName != "hardcover" && !s.hardcoverToken {
		slog.Info("hardcover enrichment idle: no api token configured")
	}
}

// metadataProviderSettingKeys are the settings whose change alters the
// provider set: the primary itself, the Google Books key under both
// spellings, and the Hardcover token, because a stored hardcover primary
// with no token falls back to OpenLibrary and comes back when one is saved.
var metadataProviderSettingKeys = map[string]bool{
	api.SettingMetadataPrimaryProvider: true,
	api.SettingGoogleBooksAPIKey:       true,
	api.LegacySettingGoogleBooksAPIKey: true,
	api.SettingHardcoverAPIToken:       true,
	api.SettingMetadataAuthorWikidata:  true,
}

// metadataProvidersReconfigurer adapts metadataProviders to the settings
// handler's hook: a save of one of metadataProviderSettingKeys rebuilds the
// provider set immediately instead of at the next restart (#1771). It
// reports handled=false for every other key.
func metadataProvidersReconfigurer(m *metadataProviders, agg *metadata.Aggregator) api.SettingsChangeHook {
	return func(ctx context.Context, key string) (bool, error) {
		if !metadataProviderSettingKeys[key] {
			return false, nil
		}
		// The save already succeeded; a rebuild failure leaves the old set
		// serving and the next restart applies the setting.
		if err := m.apply(context.WithoutCancel(ctx), agg); err != nil {
			slog.Warn("metadata providers not reconfigured; the previous providers stay in use until restart",
				"setting", key, "error", err)
			return true, err
		}
		return true, nil
	}
}

// metadataPrimaryProviderDefault is the provider used when
// metadata.primary_provider is empty, unknown, or unusable.
const metadataPrimaryProviderDefault = "openlibrary"

// resolveMetadataPrimaryProvider maps the stored metadata.primary_provider
// value onto the provider that will actually be wired as primary.
//
// Two things can force a fallback to OpenLibrary:
//
//   - An unknown value. The settings API validates writes, but a value can
//     also arrive from a hand-edited database or a downgrade, and a primary
//     provider we can't construct would leave metadata dead.
//   - "hardcover" with no API token. Hardcover authenticates every GraphQL
//     query, search included, so a tokenless Hardcover primary fails on every
//     author and book lookup. The settings API refuses to store that
//     combination, but the token can still be removed out-of-band, so the
//     boot path degrades loudly instead of silently breaking metadata.
func resolveMetadataPrimaryProvider(configured string, hardcoverTokenConfigured bool) string {
	if configured == "" {
		return metadataPrimaryProviderDefault
	}
	if !api.IsMetadataPrimaryProviderValid(configured) {
		slog.Warn("unknown metadata.primary_provider — falling back to openlibrary",
			"configured", configured)
		return metadataPrimaryProviderDefault
	}
	if configured == "hardcover" && !hardcoverTokenConfigured {
		slog.Warn("metadata.primary_provider is hardcover but no Hardcover API token is configured — falling back to openlibrary (Hardcover authenticates every query, including search)")
		return metadataPrimaryProviderDefault
	}
	return configured
}
