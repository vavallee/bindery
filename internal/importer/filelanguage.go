package importer

import (
	"context"
	"log/slog"
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
// (#1160). The library scan calls it for each ebook it attaches, and
// BackfillFileLanguagesOnce for ebooks tracked before it did. It reports
// whether it filled the language, and whether the file could be read at all.
//
// It fills a blank only. A provider's language and a user's locked edit are
// never replaced. That is stricter than an import, where the file outranks the
// provider (#1933): a scan matches files already on disk by name, often many
// at once, and rewriting languages the catalogue already had on the strength
// of those matches would change books nobody asked about.
//
// book may be stale (the scan lists its rows when it starts) or partial (the
// series tier's row carries no field locks), so the in-memory checks only save
// reading the file. The write itself, BookRepo.FillLanguage, is what refuses a
// book that has gained a language or a lock since.
//
// Best effort: an unreadable file, or one that declares no specific language,
// leaves the book as it was.
func (s *Scanner) fillLanguageFromFile(ctx context.Context, book *models.Book, author *models.Author, filePath string) (filled, read bool) {
	if book == nil || strings.TrimSpace(book.Language) != "" ||
		book.IsFieldLocked(models.BookFieldLanguage) || !HasEmbeddedMetadata(filePath) {
		return false, false
	}
	meta, err := ReadEbookMetadata(filePath)
	if err != nil {
		slog.Debug("book language not read from file", "bookID", book.ID, "path", filePath, "error", err)
		return false, false
	}
	lang := s.fileLanguageFor(ctx, book, author, meta)
	if lang == "" {
		return false, true
	}
	ok, err := s.books.FillLanguage(ctx, book.ID, lang)
	if err != nil {
		slog.Warn("failed to persist language read from file", "bookID", book.ID, "language", lang, "error", err)
		return false, true
	}
	if !ok {
		// A language or a lock arrived after the book was read; it wins.
		return false, true
	}
	book.Language = lang
	slog.Info("filled book language from embedded file metadata", "bookID", book.ID, "language", lang)
	return true, true
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
// The row is not written when the pass did not finish (shutdown, or the book
// list could not be read), or when there were books to fill and not one of
// their files could be opened, which is what a library volume that is not
// mounted yet at startup looks like. The next start tries again. A file that
// cannot be read among others that can is skipped and does not stop the pass.
func (s *Scanner) BackfillFileLanguagesOnce(ctx context.Context) {
	if s.settings == nil || s.books == nil {
		return
	}
	want := strconv.Itoa(fileLanguageBackfillRev)
	if st, err := s.settings.Get(ctx, fileLanguageBackfillKey); err == nil && st != nil && strings.TrimSpace(st.Value) == want {
		return
	}
	res, done := s.backfillFileLanguages(ctx)
	if !done {
		return
	}
	if res.candidates > 0 && res.readable == 0 {
		slog.Info("file language backfill: no ebook file could be opened, will try again on the next start",
			"books", res.candidates)
		return
	}
	slog.Info("filled book languages from tracked ebook files", "books", res.filled, "candidates", res.candidates)
	if err := s.settings.Set(ctx, fileLanguageBackfillKey, want); err != nil {
		slog.Warn("could not record the file language backfill", "error", err)
	}
}

// fileLanguageBackfill counts what one backfill pass saw: books with no
// language, no lock and an ebook file to read (candidates), those with at
// least one file that could be read (readable), and those filled.
type fileLanguageBackfill struct {
	candidates, readable, filled int
}

// backfillFileLanguages fills the language of every book that has none, is
// not locked, and has a tracked ebook file that declares one. It reports what
// it saw and whether it got through the whole list.
func (s *Scanner) backfillFileLanguages(ctx context.Context) (fileLanguageBackfill, bool) {
	var res fileLanguageBackfill
	books, err := s.books.List(ctx)
	if err != nil {
		slog.Warn("file language backfill: list books failed", "error", err)
		return res, false
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
	for i := range books {
		if ctx.Err() != nil {
			return res, false
		}
		b := &books[i]
		if strings.TrimSpace(b.Language) != "" || b.IsFieldLocked(models.BookFieldLanguage) {
			continue
		}
		files, err := s.books.ListFiles(ctx, b.ID)
		if err != nil {
			continue
		}
		candidate, readable := false, false
		for _, f := range files {
			if f.Format != models.MediaTypeEbook || !HasEmbeddedMetadata(f.Path) {
				continue
			}
			candidate = true
			filled, read := s.fillLanguageFromFile(ctx, b, authorFor(b.AuthorID), f.Path)
			readable = readable || read
			if filled {
				res.filled++
				break
			}
		}
		if candidate {
			res.candidates++
		}
		if readable {
			res.readable++
		}
	}
	return res, true
}
