package importer

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/vavallee/bindery/internal/importer/formatsniff"
	"github.com/vavallee/bindery/internal/models"
)

// Post-download language enforcement (#2998).
//
// The language filter runs at grab time against the release *name*, and it can
// only act on a name that says its language. A Swedish EPUB in a release named
// "John.Grisham.The.Racketeer.EPUB" passes it. #1933 then read the file's
// dc:language at import and relabelled the book to match, which is right for a
// library that keeps every language and wrong for one restricted to a few: an
// English only library quietly filled with Swedish and Dutch books that showed
// as imported and were never searched again.
//
// So when the book's allowed languages are an explicit list and the file
// declares a language outside it, the release is treated as the wrong release,
// the same way the format check treats a disallowed format: blocked before
// anything is placed, blocklisted so the next search picks another, and the
// book left Wanted. Everything else keeps #1933's relabelling unchanged.

// manualImportKey marks a context as an import a person asked for (manual
// import, Reassign, the queue's Match to book), as opposed to one the download
// client poll started.
type manualImportKey struct{}

// withManualImport marks ctx as a manual import. ImportFromPath and
// ImportFilesFromPath are the only entry points that do this, and every caller
// of those is a user action in internal/api.
func withManualImport(ctx context.Context) context.Context {
	return context.WithValue(ctx, manualImportKey{}, true)
}

// isManualImport reports whether withManualImport marked ctx.
func isManualImport(ctx context.Context) bool {
	v, _ := ctx.Value(manualImportKey{}).(bool)
	return v
}

// importAllowedLanguages returns the languages a downloaded file for this
// author may be in, and a phrase naming where that list came from. Nil means
// any language.
//
// It mirrors the grab time filter exactly (Scheduler.searchAndGrabFormat and
// IndexerHandler.SearchBook): the author's metadata profile wins, and only
// when that profile allows any language does search.preferredLanguage apply,
// and then only its "en" value, the one indexer.FilterByLanguage acts on. The
// file is held to the same rule its release name was, no stricter.
//
// Every failure reads as "no restriction": refusing a file because a profile
// could not be loaded would block books for a reason the user cannot see.
func (s *Scanner) importAllowedLanguages(ctx context.Context, author *models.Author) ([]string, string) {
	if author != nil {
		id := models.DefaultMetadataProfileID
		if author.MetadataProfileID != nil {
			id = *author.MetadataProfileID
		}
		p, err := s.metadataProfiles.GetByID(ctx, id)
		if err != nil {
			slog.Warn("import language check: could not load metadata profile, not enforcing",
				"profileID", id, "error", err)
		} else if p != nil {
			if langs := models.ParseAllowedLanguages(p.AllowedLanguages); len(langs) > 0 {
				return langs, fmt.Sprintf("the metadata profile %q", p.Name)
			}
		}
	}
	if s.settings == nil {
		return nil, ""
	}
	setting, err := s.settings.Get(ctx, "search.preferredLanguage")
	if err != nil || setting == nil || setting.Value != "en" {
		return nil, ""
	}
	return []string{"eng"}, "the preferred search language"
}

// definiteLanguages returns the codes in langs (already normalised) that name
// one specific language, in order.
func definiteLanguages(langs []string) []string {
	var out []string
	for _, l := range langs {
		if definiteLanguage(l) {
			out = append(out, l)
		}
	}
	return out
}

// definiteLanguage reports whether a normalised code names one specific
// language. A file that says "und" (undetermined), "mul" (several), "zxx" (no
// linguistic content), "mis" (uncoded), a private use code, or something that
// is not an ISO 639-2 code at all ("unknown", "x-default") has not told us
// what it is in, and is passed for the same reason an untagged release name
// is: rejecting on ignorance would block legitimate books.
func definiteLanguage(code string) bool {
	if len(code) != 3 {
		return false
	}
	for _, r := range code {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	switch code {
	case "und", "mul", "zxx", "mis":
		return false
	}
	// qaa to qtz is reserved for local use.
	return code < "qaa" || code > "qtz"
}

// languageCheck is the outcome of comparing the languages a download's EPUBs
// declare with the languages allowed for its book. The zero value means
// nothing is enforced.
type languageCheck struct {
	// allowed is the effective allowed set (the profile's list plus a locked
	// book language). Nil when any language is allowed.
	allowed []string
	// reject means no EPUB in the download is in an allowed language and at
	// least one declares a language that is not: the wrong release.
	reject bool
	// declared lists the disallowed languages found, for the log.
	declared []string
	reason   string
	// skip holds EPUBs in a disallowed language inside a release that also
	// carries one in an allowed language. The import loop leaves them out and
	// imports the rest, the way it leaves out a disallowed format.
	skip map[string]bool
}

// allows reports whether code is in the effective allowed set.
func (lc languageCheck) allows(code string) bool {
	return models.IsLanguageAllowed(code, lc.allowed, false)
}

// relabelLanguage picks the language to record for the book from an imported
// EPUB's declared languages. With a restricted profile it is the first
// declared language the profile allows, so a bilingual "fr, en" edition kept
// under an English only profile does not relabel the book French. Otherwise,
// or when none is allowed (a manual import), it is the first declared one, as
// before #2998.
func (lc languageCheck) relabelLanguage(meta EpubMetadata) string {
	if len(lc.allowed) > 0 {
		for _, l := range definiteLanguages(meta.Languages) {
			if lc.allows(l) {
				return l
			}
		}
	}
	return meta.Language
}

// checkDownloadLanguage compares every language every candidate EPUB in the
// download declares with the languages its book may be in.
//
// The candidates are the EPUBs the import loop would place: files the quality
// profile skips are skipped here too. An EPUB counts as allowed when ANY of its
// declared languages is allowed (a bilingual edition, or a stray tag ahead of
// the real one), as disallowed when it declares at least one specific language
// and none is allowed, and as unknown when it declares no specific language.
// The release is rejected only when some EPUB is disallowed and none is
// allowed, the same shape as the format gate, which rejects only when every
// file is disallowed. A release mixing allowed and disallowed EPUBs imports
// the allowed ones and skips the rest.
//
// The allowed set is the profile's list (see importAllowedLanguages) plus,
// when the user locked the book's language, that language. A lock is the user
// saying "this book is in X", which outranks the author wide profile for that
// one book, so a Swedish file for a book locked to Swedish is wanted even under
// an English only profile. The lock adds its own language and nothing more.
//
// Unwired (no metadata profile repo) or with no book, it never objects.
func (s *Scanner) checkDownloadLanguage(ctx context.Context, book *models.Book, author *models.Author, files []string, slotMediaType string) languageCheck {
	if s.metadataProfiles == nil || book == nil {
		return languageCheck{}
	}
	profileAllowed, source := s.importAllowedLanguages(ctx, author)
	if len(profileAllowed) == 0 {
		return languageCheck{}
	}
	lc := languageCheck{allowed: slices.Clone(profileAllowed)}
	if book.IsFieldLocked(models.BookFieldLanguage) && book.Language != "" {
		lc.allowed = append(lc.allowed, models.NormalizeLanguageCode(book.Language))
	}

	anyAllowed := false
	for _, f := range files {
		if !IsEpubFile(f) {
			continue
		}
		if ok, _ := s.allowedFormat(ctx, author, formatsniff.Detect(f), slotMediaType); !ok {
			continue
		}
		meta, err := ReadEpubMetadata(f)
		if err != nil {
			continue
		}
		langs := definiteLanguages(meta.Languages)
		if len(langs) == 0 {
			continue
		}
		if slices.ContainsFunc(langs, lc.allows) {
			anyAllowed = true
			continue
		}
		if lc.skip == nil {
			lc.skip = make(map[string]bool)
		}
		lc.skip[f] = true
		for _, l := range langs {
			if !slices.Contains(lc.declared, l) {
				lc.declared = append(lc.declared, l)
			}
		}
	}
	if len(lc.skip) == 0 || anyAllowed {
		return lc
	}

	lc.reject = true
	lc.reason = fmt.Sprintf("file declares %s, but %s allows only %s. "+
		"Not imported, and the release was blocklisted so the next search picks another. "+
		"To keep this file anyway, use Match to book in the Queue or manual import",
		languageList(lc.declared), source, languageList(profileAllowed))
	return lc
}

// languageList renders codes as "Swedish (swe), German (ger)".
func languageList(codes []string) string {
	names := make([]string, 0, len(codes))
	for _, code := range codes {
		n := models.NormalizeLanguageCode(code)
		names = append(names, fmt.Sprintf("%s (%s)", models.LanguageName(n), n))
	}
	return strings.Join(names, ", ")
}
