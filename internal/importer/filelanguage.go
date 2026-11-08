package importer

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// fileLanguageBackfillKey is the settings row that records the one-off
// backfill of languages from files tracked before the library scan read them
// (#1160). fileLanguageBackfillRev is the value it holds once that pass has
// run; bump it to run the pass again on the next start.
const (
	fileLanguageBackfillKey = "backfill.file_language_rev"
	fileLanguageBackfillRev = 1
)

// fillLanguageFromFile gives book the language its ebook file declares (an
// EPUB's dc:language, or a Kindle file's EXTH language) when the book has none
// (#1160), and reports whether it did. The library scan calls it for each
// ebook it attaches, and BackfillFileLanguagesOnce for ebooks tracked before
// it did.
//
// It fills a blank only. A provider's language and a user's locked edit are
// never replaced. That is stricter than an import, where the file outranks the
// provider (#1933): a scan matches files already on disk by name, often many
// at once, and rewriting languages the catalogue already had on the strength
// of those matches would change books nobody asked about.
//
// Best effort: an unreadable file, or one that declares no specific language,
// leaves the book as it was.
func (s *Scanner) fillLanguageFromFile(ctx context.Context, book *models.Book, author *models.Author, filePath string) bool {
	if book == nil || strings.TrimSpace(book.Language) != "" ||
		book.IsFieldLocked(models.BookFieldLanguage) || !HasEmbeddedMetadata(filePath) {
		return false
	}
	meta, err := ReadEbookMetadata(filePath)
	if err != nil {
		slog.Debug("book language not read from file", "bookID", book.ID, "path", filePath, "error", err)
		return false
	}
	lang := s.fileLanguageFor(ctx, book, author, meta)
	if lang == "" {
		return false
	}
	// The book has no language, so this only fills it; it never reaches the
	// "language corrected" branch.
	s.applyEmbeddedLanguage(ctx, book, lang, filepath.Base(filePath))
	return book.Language == lang
}

// fileLanguageFor picks the language to record from a file's declared ones,
// the way an import does (relabelLanguage): under a restricted profile the
// first declared language the profile allows, otherwise the first declared
// one. "und", "mul" and other codes that name no specific language are never
// recorded.
func (s *Scanner) fileLanguageFor(ctx context.Context, book *models.Book, author *models.Author, meta EpubMetadata) string {
	langs := definiteLanguages(meta.Languages)
	if len(langs) == 0 {
		return ""
	}
	if s.metadataProfiles != nil {
		lc, _, _ := s.languageAllowance(ctx, book, author)
		if len(lc.allowed) > 0 {
			for _, l := range langs {
				if lc.allows(l) {
					return l
				}
			}
		}
	}
	return langs[0]
}

// BackfillFileLanguagesOnce runs backfillFileLanguages on the first start
// after this feature shipped and never again: the library scan only reads a
// file's language when it attaches the file, so books whose ebook was already
// tracked would otherwise never get one. A settings row records that it ran.
//
// An interrupted pass (shutdown) or one that could not list the books does not
// write the row, so the next start tries again. A file that cannot be read is
// skipped and does not stop the pass.
func (s *Scanner) BackfillFileLanguagesOnce(ctx context.Context) {
	if s.settings == nil || s.books == nil {
		return
	}
	want := strconv.Itoa(fileLanguageBackfillRev)
	if st, err := s.settings.Get(ctx, fileLanguageBackfillKey); err == nil && st != nil && strings.TrimSpace(st.Value) == want {
		return
	}
	filled, done := s.backfillFileLanguages(ctx)
	if !done {
		return
	}
	slog.Info("filled book languages from tracked ebook files", "books", filled)
	if err := s.settings.Set(ctx, fileLanguageBackfillKey, want); err != nil {
		slog.Warn("could not record the file language backfill", "error", err)
	}
}

// backfillFileLanguages fills the language of every book that has none, is
// not locked, and has a tracked ebook file that declares one. It returns how
// many books it filled and whether it got through the whole list.
func (s *Scanner) backfillFileLanguages(ctx context.Context) (int, bool) {
	books, err := s.books.List(ctx)
	if err != nil {
		slog.Warn("file language backfill: list books failed", "error", err)
		return 0, false
	}
	authors := make(map[int64]*models.Author)
	authorFor := func(id int64) *models.Author {
		if a, ok := authors[id]; ok {
			return a
		}
		var a *models.Author
		if s.authors != nil {
			if got, err := s.authors.GetByID(ctx, id); err == nil {
				a = got
			}
		}
		authors[id] = a
		return a
	}
	filled := 0
	for i := range books {
		if ctx.Err() != nil {
			return filled, false
		}
		b := &books[i]
		if strings.TrimSpace(b.Language) != "" || b.IsFieldLocked(models.BookFieldLanguage) {
			continue
		}
		files, err := s.books.ListFiles(ctx, b.ID)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Format != models.MediaTypeEbook || !HasEmbeddedMetadata(f.Path) {
				continue
			}
			// The list above can be minutes old by the time a large library
			// gets here; re-read the row so an edit made meanwhile is seen.
			fresh, err := s.books.GetByID(ctx, b.ID)
			if err != nil || fresh == nil {
				break
			}
			if s.fillLanguageFromFile(ctx, fresh, authorFor(fresh.AuthorID), f.Path) {
				filled++
				break
			}
		}
	}
	return filled, true
}
