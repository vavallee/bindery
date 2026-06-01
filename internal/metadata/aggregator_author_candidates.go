package metadata

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// SearchAuthorCandidates searches every configured metadata provider for an
// author name. It is intentionally broader than SearchAuthors, which preserves
// the primary-provider-only behavior used by normal add/search flows.
func (a *Aggregator) SearchAuthorCandidates(ctx context.Context, query string) ([]models.Author, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	var (
		out  []models.Author
		errs []error
		seen = make(map[string]struct{})
	)
	for _, provider := range a.providers() {
		if provider == nil {
			continue
		}
		authors, err := provider.SearchAuthors(ctx, query)
		if err != nil {
			if errors.Is(err, ErrProviderNotConfigured) {
				continue
			}
			errs = append(errs, err)
			slog.Warn("author candidate provider search failed", "provider", provider.Name(), "query", query, "error", err)
			continue
		}
		for _, author := range authors {
			author.ForeignID = strings.TrimSpace(author.ForeignID)
			author.Name = strings.TrimSpace(author.Name)
			if author.ForeignID == "" || author.Name == "" {
				continue
			}
			if strings.TrimSpace(author.MetadataProvider) == "" {
				author.MetadataProvider = normalizedProviderName(provider.Name())
			}
			key := strings.ToLower(author.ForeignID)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, author)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, nil
}
