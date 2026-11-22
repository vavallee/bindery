package api

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// ResolveDefaultMediaType is the exported wrapper around resolveDefaultMediaType
// so out-of-package wiring (e.g. the bulk "refresh" callback in main.go) can
// resolve the same default media type the Refresh endpoint uses.
func (h *AuthorHandler) ResolveDefaultMediaType(ctx context.Context) string {
	return h.resolveDefaultMediaType(ctx)
}

// resolveDefaultMediaType reads the global default.media_type setting and
// falls back to ebook when unset so fresh installs keep the historical
// behaviour. An invalid stored value — should never happen because writes
// are validated — also falls back to ebook.
func (h *AuthorHandler) resolveDefaultMediaType(ctx context.Context) string {
	return defaultMediaTypeSetting(ctx, h.settings)
}

// defaultMediaTypeSetting reads default.media_type from settings: ebook when
// the repository is absent or the value is unset or unknown.
func defaultMediaTypeSetting(ctx context.Context, settings *db.SettingsRepo) string {
	if settings == nil {
		return models.MediaTypeEbook
	}
	s, _ := settings.Get(ctx, SettingDefaultMediaType)
	if s == nil || s.Value == "" {
		return models.MediaTypeEbook
	}
	switch s.Value {
	case models.MediaTypeEbook, models.MediaTypeAudiobook, models.MediaTypeBoth:
		return s.Value
	default:
		return models.MediaTypeEbook
	}
}

// resolveDefaultMediaTypeStrict reports whether the strict media-type policy
// is enabled (#1575). When on, catalogue books whose media type falls entirely
// outside default.media_type are skipped at add/refresh time instead of being
// created as un-grabbable rows, and "both" works are narrowed to the default.
// Defaults to off so existing installs keep their historical mixed catalogue;
// a "both" default is unaffected because there is nothing to narrow to.
func (h *AuthorHandler) resolveDefaultMediaTypeStrict(ctx context.Context) bool {
	if h.settings == nil {
		return false
	}
	s, _ := h.settings.Get(ctx, SettingDefaultMediaTypeStrict)
	if s == nil {
		return false
	}
	return s.Value == "true"
}

// isAutoGrabEnabled reads the autoGrab.enabled setting. Defaults to true when
// the key is absent so existing installs keep working without any migration.
func (h *AuthorHandler) isAutoGrabEnabled(ctx context.Context) bool {
	return autoGrabEnabled(ctx, h.settings)
}

// autoGrabEnabled reads the global autoGrab.enabled kill switch from a
// settings repository. Defaults to true when the repository is absent or the
// key is unset, so existing installs keep working without any migration.
//
// Package-level rather than a method so every handler that can dispatch a
// grab reads the same switch: the series fill fan-out shipped without one and
// grabbed a whole series with the switch off (#2242).
//
// Since #2256 the authoritative check is inside
// scheduler.Scheduler.SearchAndGrabBook, so the handler-side uses of this are
// early exits that skip fan-out work and goroutines. They must agree with the
// scheduler's reading of the same key, never contradict it: no handler may
// grab when this returns false.
func autoGrabEnabled(ctx context.Context, settings *db.SettingsRepo) bool {
	if settings == nil {
		return true
	}
	s, _ := settings.Get(ctx, "autoGrab.enabled")
	if s == nil {
		return true
	}
	return s.Value != "false"
}

func shouldMonitorBookForAuthor(author *models.Author, book models.Book, latestKeys map[string]struct{}, today time.Time) bool {
	if author == nil || !author.Monitored {
		return false
	}
	switch author.MonitorMode {
	case models.AuthorMonitorModeFuture:
		if book.ReleaseDate == nil {
			return false
		}
		return dateOnly(book.ReleaseDate.UTC()).After(today)
	case models.AuthorMonitorModeLatest:
		_, ok := latestKeys[indexer.NormalizeTitleForDedup(book.Title)]
		return ok
	case models.AuthorMonitorModeNone:
		return false
	case models.AuthorMonitorModeSeries:
		// Newly-discovered books default to unmonitored under series mode:
		// the series-membership join is established by the series sync
		// later, so we don't yet know which series this book belongs to.
		// A subsequent ApplyMonitorModeToExisting (manual or scheduled
		// refresh) flips the flag on once the join row exists.
		return false
	case models.AuthorMonitorModeAll, "":
		return true
	default:
		return true
	}
}

type latestMonitorCandidate struct {
	key         string
	title       string
	releaseDate time.Time
}

func latestBookMonitorKeys(books []models.Book, count int, include func(models.Book) bool) map[string]struct{} {
	if count <= 0 {
		count = models.DefaultAuthorMonitorLatestCount
	}
	byKey := make(map[string]latestMonitorCandidate)
	for _, book := range books {
		if book.ReleaseDate == nil {
			continue
		}
		if include != nil && !include(book) {
			continue
		}
		key := indexer.NormalizeTitleForDedup(book.Title)
		if key == "" {
			continue
		}
		candidate := latestMonitorCandidate{
			key:         key,
			title:       book.Title,
			releaseDate: dateOnly(book.ReleaseDate.UTC()),
		}
		if existing, ok := byKey[key]; ok && !candidate.releaseDate.After(existing.releaseDate) {
			continue
		}
		byKey[key] = candidate
	}
	candidates := make([]latestMonitorCandidate, 0, len(byKey))
	for _, candidate := range byKey {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].releaseDate.Equal(candidates[j].releaseDate) {
			return candidates[i].releaseDate.After(candidates[j].releaseDate)
		}
		return strings.ToLower(candidates[i].title) < strings.ToLower(candidates[j].title)
	})
	if len(candidates) > count {
		candidates = candidates[:count]
	}
	keys := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		keys[candidate.key] = struct{}{}
	}
	return keys
}

func isAuthorWorkMonitorCandidate(book models.Book, normalizedAuthor string, allowedLangs []string, unknownFail bool) bool {
	normalizedTitle := strings.ToLower(strings.TrimSpace(book.Title))
	if normalizedTitle == "" || normalizedTitle == normalizedAuthor {
		return false
	}
	return models.IsLanguageAllowed(book.Language, allowedLangs, unknownFail)
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (h *AuthorHandler) resolveAllowedLanguages(ctx context.Context, author *models.Author) ([]string, bool) {
	res, err := db.ResolveMetadataProfile(ctx, h.profiles, author)
	if err != nil {
		return []string{"eng"}, false
	}
	p := res.Profile
	return models.ParseAllowedLanguages(p.AllowedLanguages), p.UnknownLanguageBehavior == models.UnknownLanguageFail
}

// authorMajorityLanguageMinSample is the minimum number of already-resolved
// works required before applying the majority fallback at all — one or two
// data points aren't enough to confidently label the rest of a batch, no
// matter how uniform they happen to be.
const authorMajorityLanguageMinSample = 3

// authorMajorityLanguageThreshold is how dominant a single language must be
// among a batch's resolved works before it's applied to unresolved ones.
// High enough that a genuinely mixed-language author (translations,
// multiple original languages) isn't force-tagged into one language.
const authorMajorityLanguageThreshold = 0.9

// applyAuthorMajorityLanguageFallback assigns each still-blank-Language book
// in books the majority language already resolved among the rest of the
// batch, when that majority clears authorMajorityLanguageThreshold over at
// least authorMajorityLanguageMinSample resolved works. Mutates books in
// place. A no-op when too few works have a resolved language yet, or when
// no single language dominates strongly enough to trust.
//
// A blank work whose title is written in a different script from the
// majority's titles is left blank (#3091): "Рожби на съзнанието" in an English
// author's catalogue is a translation, and stamping "eng" on it let it past
// even unknown_language_behavior = fail. It keeps an unknown language, so the
// profile's unknown-language rule decides it. So is a work its provider has
// already proved is in a language the profile does not allow: the filter
// rejects it on that evidence anyway, and the skipped sample should not show
// it as English. Indeterminate evidence still gets the fallback, as it did
// before: it means the provider found no edition in any language (#2754).
func applyAuthorMajorityLanguageFallback(books []models.Book, evidence map[string]metadata.AuthorWorkLanguageEvidence) {
	counts := make(map[string]int, 4)
	resolved := 0
	for _, b := range books {
		if b.Language == "" {
			continue
		}
		counts[b.Language]++
		resolved++
	}
	if resolved < authorMajorityLanguageMinSample {
		return
	}
	var majorityLang string
	var majorityCount int
	for lang, count := range counts {
		if count > majorityCount {
			majorityLang, majorityCount = lang, count
		}
	}
	if float64(majorityCount)/float64(resolved) < authorMajorityLanguageThreshold {
		return
	}
	majorityScript := majorityTitleScript(books, majorityLang)
	for i := range books {
		if books[i].Language != "" {
			continue
		}
		if found, ok := evidence[strings.TrimSpace(books[i].ForeignID)]; ok && found.State == metadata.AuthorWorkLanguageNotAllowed {
			continue
		}
		if script := titleScript(books[i].Title); script != "" && majorityScript != "" && script != majorityScript {
			continue
		}
		books[i].Language = majorityLang
	}
}

// adoptMergedHardcoverLanguages stores Hardcover's language on a work merged
// with a Hardcover book when Hardcover's evidence found an edition in an
// allowed language (#3792). The evidence was asked for under the hc: id
// (metadata.withMergedHardcoverAliases) and already decides the language
// filter; without this the row kept OpenLibrary's edition sample, so King's
// novel passed an English-only profile but was stored, shown and sent to
// Calibre as Portuguese.
//
// Only merged rows: a Hardcover work's own evidence stays filter-only, as
// authorWorkPassesLanguageFilter describes. An indeterminate or not-allowed
// answer changes nothing, and a not-allowed work is filtered out anyway. A
// row whose own language is already one the profile allows keeps it: the
// evidence names just one allowed edition, which for a profile allowing
// English and Spanish can be the Spanish one of an English book.
func adoptMergedHardcoverLanguages(books []models.Book, evidence map[string]metadata.AuthorWorkLanguageEvidence, allowed []string) {
	for i := range books {
		id := strings.TrimSpace(books[i].ForeignID)
		if id == "" || strings.HasPrefix(id, "hc:") || !strings.HasPrefix(strings.TrimSpace(books[i].HardcoverForeignID), "hc:") {
			continue
		}
		found, ok := evidence[id]
		if !ok || found.State != metadata.AuthorWorkLanguageAllowed || strings.TrimSpace(found.Language) == "" {
			continue
		}
		if current := strings.TrimSpace(books[i].Language); current != "" && models.IsLanguageAllowed(current, allowed, true) {
			continue
		}
		books[i].Language = found.Language
	}
}

// authorWorkPassesLanguageFilter applies provider-supplied edition evidence
// when it is definitive and otherwise falls through to the work's scalar
// language. Evidence is filter-only: it never rewrites the preferred/display
// language, and an indeterminate result cannot discard edition sampling or the
// author-majority fallback that already resolved the scalar.
func authorWorkPassesLanguageFilter(book *models.Book, allowed []string, unknownFail bool, evidence map[string]metadata.AuthorWorkLanguageEvidence) (bool, bool) {
	if len(allowed) == 0 {
		return true, false
	}
	if resolved, ok := evidence[strings.TrimSpace(book.ForeignID)]; ok {
		switch resolved.State {
		case metadata.AuthorWorkLanguageAllowed:
			return true, false
		case metadata.AuthorWorkLanguageNotAllowed:
			return false, false
		}
	}
	if strings.TrimSpace(book.Language) == "" {
		return !unknownFail, true
	}
	return models.IsLanguageAllowed(book.Language, allowed, unknownFail), false
}

// resolveSkipMissingDate returns the author's effective metadata profile's
// SkipMissingDate setting. Defaults to false (matches the setting's prior
// no-op behavior) on any lookup failure, so an unresolvable profile never
// turns into unexpected catalogue loss.
func (h *AuthorHandler) resolveSkipMissingDate(ctx context.Context, author *models.Author) bool {
	res, err := db.ResolveMetadataProfile(ctx, h.profiles, author)
	if err != nil {
		return false
	}
	p := res.Profile
	return p.SkipMissingDate
}

// resolveEditionFilters returns the author's effective metadata profile's
// MinPages floor and SkipMissingISBN setting. Combined into one lookup
// because both gate the same per-candidate edition prefetch in
// fetchAuthorBooks. Defaults to "no filter" (0, false) on any lookup
// failure, so an unresolvable profile never causes unexpected catalogue
// loss.
func (h *AuthorHandler) resolveEditionFilters(ctx context.Context, author *models.Author) (minPages int, skipMissingISBN bool) {
	res, err := db.ResolveMetadataProfile(ctx, h.profiles, author)
	if err != nil {
		return 0, false
	}
	p := res.Profile
	return p.MinPages, p.SkipMissingISBN
}

// resolveMinEditionCount returns the author's effective metadata profile's
// MinEditionCount floor (#2235). Defaults to 0 (filter disabled) on any
// lookup failure, so an unresolvable profile never turns into unexpected
// catalogue loss.
func (h *AuthorHandler) resolveMinEditionCount(ctx context.Context, author *models.Author) int {
	res, err := db.ResolveMetadataProfile(ctx, h.profiles, author)
	if err != nil {
		return 0
	}
	p := res.Profile
	return p.MinEditionCount
}

// anyEditionHasISBN reports whether any edition carries an ISBN-13 or
// ISBN-10. Returns false for a nil or empty slice — a work with no editions
// to check has no ISBN to confirm.
func anyEditionHasISBN(editions []models.Edition) bool {
	for _, e := range editions {
		if e.ISBN13 != nil && strings.TrimSpace(*e.ISBN13) != "" {
			return true
		}
		if e.ISBN10 != nil && strings.TrimSpace(*e.ISBN10) != "" {
			return true
		}
	}
	return false
}

// passesMinPagesFilter reports whether a work satisfies the profile's
// MinPages floor. Mirrors Chaptarr's semantics: an edition meeting the
// floor passes the work, but if no edition reports a page count at all,
// that's treated as unknown rather than zero and passes through
// unfiltered — a work with no page data is not the same as a work with no
// pages. (Named to read as "did this pass the filter", not "does the work
// have enough pages" — the unknown-data case makes those two questions
// have different answers.)
func passesMinPagesFilter(editions []models.Edition, minPages int) bool {
	anyReported := false
	for _, e := range editions {
		if e.NumPages == nil || *e.NumPages <= 0 {
			continue
		}
		anyReported = true
		if *e.NumPages >= minPages {
			return true
		}
	}
	return !anyReported
}

// resolveSkipPartBooks returns the author's effective metadata profile's
// SkipPartBooks setting. Defaults to false (matches the setting's prior
// no-op behavior) on any lookup failure, so an unresolvable profile never
// turns into unexpected catalogue loss.
func (h *AuthorHandler) resolveSkipPartBooks(ctx context.Context, author *models.Author) bool {
	res, err := db.ResolveMetadataProfile(ctx, h.profiles, author)
	if err != nil {
		return false
	}
	p := res.Profile
	return p.SkipPartBooks
}
