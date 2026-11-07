package importer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer/formatsniff"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// Ebook upgrades on import (#2420, #1357).
//
// An upgrade is a download for a book that already has an ebook, where the
// quality profile has upgrades on and the new files beat the owned ones. It is
// recognised here rather than marked on the download row, so a grab made by an
// older build or from the queue is judged by the same rule, and so no column
// has to say it.
//
// What an upgrade changes about an ordinary import:
//
//   - The content check is stricter. Any finding, in warn mode and for an
//     uncertain finding too, refuses the download exactly as block mode does.
//     Nothing has been placed at that point, so the old file is untouched, and
//     the release is blocklisted for the book so the next search tries
//     another. A wrong book must never take the place of a good one.
//   - The new file is placed next to the old one by the ordinary path. A
//     different format has a different extension and so a different
//     destination; the old file is never overwritten.
//   - The old file is removed only afterwards, and only when the profile's
//     RemoveReplaced is on, the import was clean, the book is not a Calibre
//     book, and the new file's own title and author positively matched the
//     book (see removeReplaced). A profile
//     that keeps replaced files, the default, leaves every old file where it
//     is. The book then holds a file at the cutoff, so it is not searched
//     again.
//
// A second file of the same format is not an upgrade and is unchanged: an
// upgrade needs a format that ranks strictly higher, so the destination never
// collides with the owned file, and a same-format download still hits the
// per-file idempotency guard or the collision suffix as it always did.

// ebookUpgrade describes an upgrade import in progress.
type ebookUpgrade struct {
	profile *models.QualityProfile
	// existing are the book's recorded ebook files that are on disk now.
	existing []string
}

// detectEbookUpgrade reports whether importing files for book is an upgrade
// of its ebook, or nil. It never returns an upgrade for a manual import, a
// book matched from the file itself, or anything but an ebook-only book.
func (s *Scanner) detectEbookUpgrade(ctx context.Context, dl *models.Download, book *models.Book, author *models.Author, files []string, grabbedForBook bool) *ebookUpgrade {
	if book == nil || author == nil || !grabbedForBook || isManualImport(ctx) ||
		book.MediaType != models.MediaTypeEbook || s.qualityProfiles == nil {
		return nil
	}
	profile := db.ResolveAuthorQualityProfile(ctx, s.qualityProfiles, author)
	if profile == nil || !profile.UpgradeAllowed {
		return nil
	}
	rows, err := s.books.ListFiles(ctx, book.ID)
	if err != nil {
		slog.Warn("upgrade check: failed to list book files; importing as an ordinary download", "bookID", book.ID, "error", err)
		return nil
	}
	var existing []string
	for _, f := range rows {
		if f.Format != models.MediaTypeEbook {
			continue
		}
		if _, statErr := os.Stat(f.Path); statErr == nil {
			existing = append(existing, f.Path)
		}
	}
	if len(existing) == 0 {
		return nil
	}
	if !indexer.EbookUpgradeEligible(profile, existing, fileFormats(files)) {
		return nil
	}
	slog.Info("import is an ebook upgrade", "title", dl.Title, "bookID", book.ID, "existing", len(existing))
	return &ebookUpgrade{profile: profile, existing: existing}
}

// fileFormats is the format token of each file: its content type, else its
// extension.
func fileFormats(files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if format := formatsniff.Detect(f); format != "" {
			out = append(out, format)
		}
	}
	return out
}

// ReplacedFileRemover deletes one file an upgrade replaced. It is the
// containment and ownership gated delete the book handlers use (library roots
// only, never a file another book tracks), supplied by the caller because the
// importer cannot import the api package. skipped is true when it declined to
// touch the disk. With no remover wired nothing is ever deleted.
type ReplacedFileRemover func(ctx context.Context, bookID int64, path string) (skipped bool, err error)

// WithReplacedFileRemover wires the guarded delete an upgrade uses for the
// file it replaced (#2420).
func (s *Scanner) WithReplacedFileRemover(r ReplacedFileRemover) *Scanner {
	s.replacedRemover = r
	return s
}

// removeReplaced deletes the files an upgrade replaced and drops their
// book_files rows, and returns the paths it removed. Every condition below
// must hold or the old file stays, and the log says which one failed.
//
//   - The profile asks for removal (RemoveReplaced).
//   - The book has no Calibre id: a file inside Calibre's library is Calibre's
//     to manage, and deleting it leaves metadata.db pointing at nothing.
//   - A guarded remover is wired.
//   - Positive evidence: verifiedFormats are the formats of the files whose
//     own title matched and whose creator was named and matched. Only those
//     can vouch, so a verified low ranked file cannot vouch for an unread
//     higher ranked one. The old files go only if those formats, among the
//     ones that actually landed on disk, are themselves an upgrade of what is
//     being removed.
//
// The delete goes through the remover per file, which refuses a path outside
// the library roots and one another book tracks; a refusal keeps the file and
// its row.
func (s *Scanner) removeReplaced(ctx context.Context, book *models.Book, up *ebookUpgrade, newPaths []string, verifiedFormats []string) []string {
	if up == nil || !up.profile.RemoveReplaced {
		return nil
	}
	if book.CalibreID != nil {
		slog.Info("upgrade imported; keeping the replaced file because the book lives in Calibre", "bookID", book.ID)
		return nil
	}
	if s.replacedRemover == nil {
		slog.Info("upgrade imported; keeping the replaced file because no guarded delete is wired", "bookID", book.ID)
		return nil
	}
	var landedVerified []string
	for _, p := range newPaths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		format := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if slices.Contains(verifiedFormats, format) {
			landedVerified = append(landedVerified, format)
		}
	}
	if !indexer.EbookUpgradeEligible(up.profile, up.existing, landedVerified) {
		slog.Info("upgrade imported; keeping the replaced file because no new file that is an upgrade of it was confirmed as this book by its title and author",
			"bookID", book.ID)
		return nil
	}
	keep := make(map[string]bool, len(newPaths))
	for _, p := range newPaths {
		keep[s.pathKey(p)] = true
	}
	var removed []string
	for _, old := range up.existing {
		if keep[s.pathKey(old)] {
			continue
		}
		skipped, err := s.replacedRemover(ctx, book.ID, old)
		if err != nil {
			slog.Warn("could not remove the file an upgrade replaced; leaving it tracked",
				"bookID", book.ID, "path", old, "error", err)
			continue
		}
		if skipped {
			slog.Info("keeping the file an upgrade replaced: it is outside the library roots or another book tracks it",
				"bookID", book.ID, "path", old)
			continue
		}
		if _, err := s.books.RemoveBookFile(ctx, old); err != nil {
			slog.Warn("removed the file an upgrade replaced but could not drop its book_files row; the next scan reconciles it",
				"bookID", book.ID, "path", old, "error", err)
		}
		removed = append(removed, old)
	}
	return removed
}

// upgradeFormat names the best format among paths, for the notification.
func upgradeFormat(paths []string) string {
	for _, p := range paths {
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), ".")); ext != "" {
			return ext
		}
	}
	return models.MediaTypeEbook
}
