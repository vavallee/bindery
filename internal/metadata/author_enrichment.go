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
// worth logging: enrichment is best effort and never fails a refresh.
//
// Enrichment runs only on author profile refreshes, never inside GetAuthor:
// the staggered refresh, an add and the Refresh button, and Refresh all, bulk
// refresh and discovery for an author that is due, within an hourly budget
// (#3398). An import that looks up many authors at once sends the enrichers
// nothing.
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
	// Partial says a source failed while the answer was built, so it may lack
	// a field it would otherwise have. It is not stamped as an answer.
	Partial bool
}

// EnrichAuthor fills author's empty photo and bio from the author enrichers,
// in order, stopping once both are set. It writes only to author, which must
// be the caller's own copy, never the primary's cached record. answered is
// true when there are enrichers and every one asked gave a full answer, a
// miss included: the refresh stamps that, and asks again only once it ages.
func (a *aggregatorCore) EnrichAuthor(ctx context.Context, author *models.Author) (answered bool) {
	if a == nil || author == nil || len(a.authorEnrichers) == 0 {
		return false
	}
	answered = true
	for _, e := range a.authorEnrichers {
		if author.ImageURL != "" && author.Description != "" {
			return answered
		}
		profile, err := e.EnrichAuthor(ctx, author)
		if err != nil {
			slog.Debug("author enrichment failed", "enricher", e.Name(), "author", author.Name, "error", err)
			answered = false
			continue
		}
		if profile == nil {
			continue
		}
		if profile.Partial {
			answered = false
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
	return answered
}
