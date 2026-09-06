package indexer

import (
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

const maxTitleCandidates = 2

// TitleCandidate is one normalized title an indexer search may use. ManualOnly
// candidates are exposed to interactive searches but are never queried by the
// scheduler, which prevents an original-language fallback from being grabbed
// automatically for a localized-only metadata profile.
type TitleCandidate struct {
	Title      string `json:"title"`
	Language   string `json:"language,omitempty"`
	ManualOnly bool   `json:"manualOnly,omitempty"`
}

// BuildTitleCandidates derives a small, ordered set of indexer query titles
// from metadata already persisted on a book and its editions. The first entry
// is always the automatic-search title. At most one fallback is added so a
// missing book cannot multiply the existing four-tier indexer cascade without
// bound.
//
// A localized/original display title such as "El imperio final / The Final
// Empire" is split only when it has exactly one spaced slash outside grouping
// punctuation and the book/profile carries non-English language evidence.
// Multi-title bundles and format tags therefore keep their full title.
func BuildTitleCandidates(book models.Book, editions []models.Edition, allowedLanguages []string) []TitleCandidate {
	bookLanguage := normalizeLanguageCode(book.Language)
	if bookLanguage == "" {
		bookLanguage = firstSpecificLanguage(allowedLanguages)
	}

	primaryTitle := cleanTitleCandidate(book.Title)
	var manualFallback string
	if left, right, ok := splitLocalizedOriginalTitle(book.Title, book.OriginalTitle, bookLanguage, allowedLanguages); ok {
		primaryTitle = left
		manualFallback = right
	}
	if primaryTitle == "" {
		primaryTitle = cleanTitleCandidate(book.OriginalTitle)
	}
	if primaryTitle == "" {
		for _, edition := range editions {
			if title := cleanTitleCandidate(edition.Title); title != "" {
				primaryTitle = title
				bookLanguage = normalizeLanguageCode(edition.Language)
				break
			}
		}
	}

	candidates := make([]TitleCandidate, 0, maxTitleCandidates)
	add := func(title, language string, manualOnly bool) bool {
		title = cleanTitleCandidate(title)
		if title == "" {
			return false
		}
		for _, existing := range candidates {
			if strings.EqualFold(existing.Title, title) {
				return false
			}
		}
		candidates = append(candidates, TitleCandidate{
			Title:      title,
			Language:   normalizeLanguageCode(language),
			ManualOnly: manualOnly,
		})
		return true
	}

	add(primaryTitle, bookLanguage, false)
	if len(candidates) == 0 {
		return candidates
	}

	// A split bilingual title is the strongest available source for its
	// original-language spelling and wins the single fallback slot.
	if manualFallback != "" {
		add(manualFallback, languageForTitle(manualFallback, editions), true)
		return candidates
	}

	// Prefer one same-language edition title. It is safe for automatic search
	// and often carries the exact wording used by release names.
	for _, edition := range orderedEditions(book, editions) {
		if len(candidates) >= maxTitleCandidates {
			break
		}
		language := normalizeLanguageCode(edition.Language)
		if languageAllowed(language, allowedLanguages) || (language != "" && language == bookLanguage) {
			add(edition.Title, language, false)
		}
	}

	// If no localized edition alias exists, preserve the original title as an
	// interactive-only escape hatch.
	if len(candidates) < maxTitleCandidates {
		add(book.OriginalTitle, languageForTitle(book.OriginalTitle, editions), true)
	}
	if len(candidates) < maxTitleCandidates {
		for _, edition := range orderedEditions(book, editions) {
			language := normalizeLanguageCode(edition.Language)
			if language != "" && !languageAllowed(language, allowedLanguages) {
				if add(edition.Title, language, true) {
					break
				}
			}
		}
	}

	return candidates
}

func effectiveTitleCandidates(c MatchCriteria) []TitleCandidate {
	candidates := c.TitleCandidates
	if len(candidates) == 0 {
		candidates = []TitleCandidate{{Title: cleanTitleCandidate(c.Title)}}
	}
	out := make([]TitleCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Title = cleanTitleCandidate(candidate.Title)
		if candidate.Title == "" || (candidate.ManualOnly && !c.AllowManualFallback) {
			continue
		}
		out = append(out, candidate)
		if len(out) == maxTitleCandidates {
			break
		}
	}
	return out
}

func splitLocalizedOriginalTitle(title, originalTitle, bookLanguage string, allowedLanguages []string) (string, string, bool) {
	if strings.Count(title, " / ") != 1 || separatorInsideGrouping(title, " / ") {
		return "", "", false
	}
	parts := strings.SplitN(title, " / ", 2)
	left, right := cleanTitleCandidate(parts[0]), cleanTitleCandidate(parts[1])
	if left == "" || right == "" {
		return "", "", false
	}

	original := cleanTitleCandidate(originalTitle)
	originalIsLeft := original != "" && strings.EqualFold(original, left)
	originalIsRight := original != "" && strings.EqualFold(original, right)
	hasOriginalEvidence := originalIsLeft || originalIsRight
	hasLocalizedLanguage := bookLanguage != "" && bookLanguage != "eng"
	if !hasLocalizedLanguage {
		for _, language := range allowedLanguages {
			language = normalizeLanguageCode(language)
			if language != "" && language != "eng" && language != "any" {
				hasLocalizedLanguage = true
				break
			}
		}
	}
	if !hasOriginalEvidence && !hasLocalizedLanguage {
		return "", "", false
	}
	// Metadata normally stores "localized / original", but orient the pair
	// from OriginalTitle when a provider returns it in the opposite order.
	if originalIsLeft && !originalIsRight {
		return right, left, true
	}
	return left, right, true
}

func separatorInsideGrouping(title, separator string) bool {
	idx := strings.Index(title, separator)
	if idx < 0 {
		return false
	}
	var round, square int
	for _, r := range title[:idx] {
		switch r {
		case '(':
			round++
		case ')':
			if round > 0 {
				round--
			}
		case '[':
			square++
		case ']':
			if square > 0 {
				square--
			}
		}
	}
	return round > 0 || square > 0
}

func cleanTitleCandidate(title string) string {
	title = newznab.NormalizeQueryTitle(title)
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return title
		}
	}
	return ""
}

func orderedEditions(book models.Book, editions []models.Edition) []models.Edition {
	out := make([]models.Edition, 0, len(editions))
	if book.SelectedEditionID != nil {
		for _, edition := range editions {
			if edition.ID == *book.SelectedEditionID {
				out = append(out, edition)
				break
			}
		}
	}
	for _, edition := range editions {
		if book.SelectedEditionID != nil && edition.ID == *book.SelectedEditionID {
			continue
		}
		if edition.Monitored {
			out = append(out, edition)
		}
	}
	for _, edition := range editions {
		if book.SelectedEditionID != nil && edition.ID == *book.SelectedEditionID {
			continue
		}
		if !edition.Monitored {
			out = append(out, edition)
		}
	}
	return out
}

func languageForTitle(title string, editions []models.Edition) string {
	title = cleanTitleCandidate(title)
	for _, edition := range editions {
		if strings.EqualFold(cleanTitleCandidate(edition.Title), title) {
			return edition.Language
		}
	}
	return ""
}

func languageAllowed(language string, allowed []string) bool {
	language = normalizeLanguageCode(language)
	if language == "" || len(allowed) == 0 {
		return false
	}
	for _, candidate := range allowed {
		candidate = normalizeLanguageCode(candidate)
		if candidate == "any" || candidate == language {
			return true
		}
	}
	return false
}

func firstSpecificLanguage(languages []string) string {
	for _, language := range languages {
		language = normalizeLanguageCode(language)
		if language != "" && language != "any" {
			return language
		}
	}
	return ""
}

// normalizeLanguageCode canonicalises a provider-reported language so it can be
// compared with a metadata profile's allowed list.
//
// It delegates to models.NormalizeLanguageCode, which is the single ISO 639
// source (#2463). The local table this used to consult held ten two-letter
// codes and returned anything else unchanged, so a language outside those ten
// failed to reconcile with itself: a profile allowing "swe" dropped a release
// reported as "sv", "ger" dropped "deu", "por" dropped "pt-BR", and "eng"
// dropped both "en-US" and "English". The shared function handles the region
// and script subtag strip, the ISO 639-2 T-to-B pairs and the language names.
func normalizeLanguageCode(language string) string {
	return models.NormalizeLanguageCode(language)
}
