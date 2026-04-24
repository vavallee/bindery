package recommender

import (
	"math"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// Scoring weights.
const (
	weightGenre     = 0.30
	weightAuthor    = 0.25
	weightSeries    = 0.20
	weightCommunity = 0.10
	weightRecency   = 0.15

	bonusSeries    = 0.15
	bonusAuthorNew = 0.10
)

// Score computes a 0.0–1.0 relevance score for a candidate against the user's
// profile. Higher is better.
func Score(c models.RecommendationCandidate, p *UserProfile) float64 {
	genre := genreScore(c, p)
	author := authorScore(c, p)
	series := seriesScore(c, p)
	community := communityScore(c)
	recency := recencyScore(c, p.LibraryMedianYear)

	score := genre*weightGenre +
		author*weightAuthor +
		series*weightSeries +
		community*weightCommunity +
		recency*weightRecency

	// Bonus for next-in-sequence series book.
	if c.RecType == models.RecTypeSeries {
		score += bonusSeries
	}
	// Bonus for new work from monitored author.
	if c.RecType == models.RecTypeAuthorNew && c.AuthorID != nil && p.MonitoredAuthors[*c.AuthorID] {
		score += bonusAuthorNew
	}

	return clamp(score)
}

// genreScore computes cosine similarity between the candidate's genre set
// (binary vector) and the user's TF-IDF genre weights.
func genreScore(c models.RecommendationCandidate, p *UserProfile) float64 {
	if len(p.GenreWeights) == 0 || len(c.Genres) == 0 {
		return 0
	}

	// Build candidate binary vector in the same genre space.
	var dotProduct, candMag float64
	for _, g := range c.Genres {
		g = strings.ToLower(strings.TrimSpace(g))
		if g == "" {
			continue
		}
		candMag += 1.0 // binary: 1^2 = 1
		if w, ok := p.GenreWeights[g]; ok {
			dotProduct += w // w * 1
		}
	}

	if candMag == 0 {
		return 0
	}

	// Profile magnitude.
	var profileMag float64
	for _, w := range p.GenreWeights {
		profileMag += w * w
	}
	profileMag = math.Sqrt(profileMag)
	candMag = math.Sqrt(candMag)

	if profileMag == 0 {
		return 0
	}

	return dotProduct / (profileMag * candMag)
}

// authorScore returns an affinity score based on how many books the user has
// from this author and whether the author is monitored.
func authorScore(c models.RecommendationCandidate, p *UserProfile) float64 {
	if c.AuthorID == nil {
		return 0
	}
	aid := *c.AuthorID
	if p.MonitoredAuthors[aid] {
		return 1.0
	}
	count := p.AuthorBookCounts[aid]
	if count >= 3 {
		return 0.7
	}
	if count >= 1 {
		return 0.4
	}
	return 0
}

// seriesScore rewards candidates that continue or fill gaps in a series the
// user has started.
func seriesScore(c models.RecommendationCandidate, p *UserProfile) float64 {
	if c.SeriesID == nil {
		return 0
	}
	state, ok := p.SeriesState[*c.SeriesID]
	if !ok {
		return 0
	}

	pos := parsePosition(c.SeriesPos)
	if pos <= 0 {
		return 0
	}

	// Next-in-sequence: one position after the user's max.
	if pos == state.MaxPosition+1 {
		return 1.0
	}

	// Fills a gap in the user's collection.
	for _, missing := range state.MissingPositions {
		if pos == missing {
			return 0.5
		}
	}

	return 0
}

// communityScore blends rating quality with rating quantity using a log scale.
func communityScore(c models.RecommendationCandidate) float64 {
	ratingNorm := math.Min(1.0, c.Rating/5.0)
	countNorm := math.Log10(1+float64(c.RatingsCount)) / math.Log10(1001)
	return ratingNorm * countNorm
}

// recencyScore applies linear decay relative to the user's library median
// publication year. A candidate published in the same year as the median
// scores 1.0; one published 30+ years before the median scores 0.1.
// Returns 0.5 if no release date is available or medianYear is 0.
func recencyScore(c models.RecommendationCandidate, medianYear int) float64 {
	if c.ReleaseDate == nil || medianYear == 0 {
		return 0.5
	}
	yearsBeforeMedian := float64(medianYear - c.ReleaseDate.Year())
	if yearsBeforeMedian < 0 {
		yearsBeforeMedian = 0
	}
	if yearsBeforeMedian >= 30 {
		return 0.1
	}
	// Linear from 1.0 (at or after median) to 0.1 (30 years before median).
	return 1.0 - (0.9/30.0)*yearsBeforeMedian
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
