package main

import (
	"log/slog"

	"github.com/vavallee/bindery/internal/api"
)

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
