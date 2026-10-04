package importer

import (
	"context"
	"fmt"
	"log/slog"
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

// declaredDownloadLanguage returns the language the download's ebook declares:
// the dc:language of the first EPUB that has one, normalised. It considers the
// same files, in the same order, as the import loop's relabelling, so the
// language checked here is the language the book would have been relabelled
// to. Files the quality profile skips are skipped here too.
func (s *Scanner) declaredDownloadLanguage(ctx context.Context, author *models.Author, files []string, slotMediaType string) string {
	for _, f := range files {
		if !IsEpubFile(f) {
			continue
		}
		if ok, _ := s.allowedFormat(ctx, author, formatsniff.Detect(f), slotMediaType); !ok {
			continue
		}
		meta, err := ReadEpubMetadata(f)
		if err != nil || meta.Language == "" {
			continue
		}
		return meta.Language
	}
	return ""
}

// definiteLanguage reports whether a normalised code names one specific
// language. A file that says "und" (undetermined), "mul" (several), "zxx" (no
// linguistic content), "mis" (uncoded), a private use code, or something that
// is not an ISO 639-2 code at all ("unknown", an unmapped two letter code) has
// not told us what it is in, and is passed for the same reason an untagged
// release name is: rejecting on ignorance would block legitimate books.
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

// languageCheck is the outcome of comparing a download's declared language
// with the languages allowed for its book. A zero value means nothing to act
// on.
type languageCheck struct {
	disallowed bool
	declared   string
	reason     string
}

// checkDownloadLanguage compares the language the download's EPUB declares
// with the languages its book may be in.
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
	allowed, source := s.importAllowedLanguages(ctx, author)
	if len(allowed) == 0 {
		return languageCheck{}
	}
	declared := s.declaredDownloadLanguage(ctx, author, files, slotMediaType)
	if !definiteLanguage(declared) {
		return languageCheck{}
	}
	if models.IsLanguageAllowed(declared, allowed, false) {
		return languageCheck{}
	}
	if book.IsFieldLocked(models.BookFieldLanguage) && book.Language != "" &&
		models.NormalizeLanguageCode(book.Language) == declared {
		return languageCheck{}
	}
	names := make([]string, 0, len(allowed))
	for _, code := range allowed {
		n := models.NormalizeLanguageCode(code)
		names = append(names, fmt.Sprintf("%s (%s)", models.LanguageName(n), n))
	}
	return languageCheck{
		disallowed: true,
		declared:   declared,
		reason: fmt.Sprintf("file declares %s (%s), but %s allows only %s. "+
			"Not imported, and the release was blocklisted so the next search picks another. "+
			"To keep this file anyway, use Match to book in the Queue or manual import",
			models.LanguageName(declared), declared, source, strings.Join(names, ", ")),
	}
}
