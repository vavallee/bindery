package metadata

import (
	"context"
	"log/slog"

	"github.com/vavallee/bindery/internal/models"
)

// AuthorEnricher fills an author's photo or bio from a source other than the
// author's primary provider (#3287). The primary decides who the author is;
// an enricher only supplies what the primary left empty, the way the book
// enrichers do for covers and descriptions.
//
// EnrichAuthor gets the author the primary returned and answers with whatever
// profile fields it found, each with the credit its licence asks for. It
// returns (nil, nil) when it has nothing, and an error only for a failure
// worth logging: enrichment is best effort and never fails the author lookup.
type AuthorEnricher interface {
	Name() string
	EnrichAuthor(ctx context.Context, author *models.Author) (*AuthorProfile, error)
}

// AuthorProfile is what an AuthorEnricher found. A field left empty means the
// enricher has nothing for it.
type AuthorProfile struct {
	Description       string
	DescriptionSource *models.ProfileSource
	ImageURL          string
	ImageSource       *models.ProfileSource
}

// enrichAuthor fills author's empty photo and bio from the author enrichers,
// in order, stopping once both are set. It writes only to author, which must
// be the caller's own copy: GetAuthor hands it a clone, so the primary's
// cached record never carries an enriched value.
func (a *aggregatorCore) enrichAuthor(ctx context.Context, author *models.Author) {
	if author == nil {
		return
	}
	for _, e := range a.authorEnrichers {
		if author.ImageURL != "" && author.Description != "" {
			return
		}
		profile, err := e.EnrichAuthor(ctx, author)
		if err != nil {
			slog.Debug("author enrichment failed", "enricher", e.Name(), "author", author.Name, "error", err)
			continue
		}
		if profile == nil {
			continue
		}
		if author.ImageURL == "" && profile.ImageURL != "" {
			author.ImageURL = profile.ImageURL
			author.ImageSource = profile.ImageSource
		}
		if author.Description == "" && profile.Description != "" {
			author.Description = profile.Description
			author.DescriptionSource = profile.DescriptionSource
		}
	}
}
