package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// FixMatch describes a Fix match (Reassign, #1238): the user said a file
// tracked on one book really belongs to another. The handler detaches the
// file from FromBookID before the import runs; the importer uses this to
// record the move in history and, should the source path still be held by
// another book when the import records it, to move that row deliberately
// instead of refusing (#2937).
type FixMatch struct {
	// FromBookID and FromTitle name the book the file was detached from. Zero
	// when the handler found no row to detach.
	FromBookID int64
	FromTitle  string
	// SourcePaths are the spellings of the file being reassigned (the path
	// the client sent and its symlink-resolved form, #1368). Only these paths
	// may be moved off another book; any other owned path still fails.
	SourcePaths []string
}

type fixMatchKey struct{}

// WithFixMatch marks ctx as a Fix match import. ImportFromPath keeps the
// value, so the Reassign handler sets it on the context it hands the import.
func WithFixMatch(ctx context.Context, fm FixMatch) context.Context {
	return context.WithValue(ctx, fixMatchKey{}, &fm)
}

func fixMatchFrom(ctx context.Context) *FixMatch {
	fm, _ := ctx.Value(fixMatchKey{}).(*FixMatch)
	return fm
}

// covers reports whether path is one of the files the user reassigned.
func (fm *FixMatch) covers(path string) bool {
	clean := filepath.Clean(path)
	for _, p := range fm.SourcePaths {
		if p != "" && filepath.Clean(p) == clean {
			return true
		}
	}
	return false
}

// recordImportedFile writes the book_files row for a file this import placed.
//
// book_files.path is unique and used to be inserted OR IGNORE, so a path
// another book already tracked recorded nothing while the import carried on
// to report success (#2937). AddBookFile now refuses with *db.PathOwnedError,
// and this turns that into a reason the user can act on, naming the book that
// holds the file. The one exception is a Fix match of that very file, where
// moving the row is what the user asked for: the row moves and the move is
// recorded in history once the import completes.
func (s *Scanner) recordImportedFile(ctx context.Context, book *models.Book, format, path string) error {
	err := s.books.AddBookFile(ctx, book.ID, format, path)
	var owned *db.PathOwnedError
	if !errors.As(err, &owned) {
		return err
	}
	fm := fixMatchFrom(ctx)
	if fm == nil || !fm.covers(path) {
		return fmt.Errorf("%w; if the file is really %q, use Fix match on that book to move it here", owned, book.Title)
	}
	prev, err := s.books.MoveBookFile(ctx, path, book.ID, format)
	if err != nil {
		return fmt.Errorf("fix match: move book file: %w", err)
	}
	slog.Info("fix match: moved a tracked file to the chosen book",
		"path", path, "fromBookID", prev, "toBookID", book.ID)
	if fm.FromBookID == 0 {
		fm.FromBookID, fm.FromTitle = owned.OwnerBookID, owned.OwnerTitle
	}
	return nil
}

// recordFixMatchMove writes the bookFileMoved history row for a Fix match
// whose import completed, naming the book the file came from. dest is where
// the file now lives. A plain import, or a Fix match that found nothing to
// detach, writes nothing.
func (s *Scanner) recordFixMatchMove(ctx context.Context, book *models.Book, dest string) {
	fm := fixMatchFrom(ctx)
	if fm == nil || book == nil || fm.FromBookID == 0 || fm.FromBookID == book.ID {
		return
	}
	source := ""
	if len(fm.SourcePaths) > 0 {
		source = fm.SourcePaths[0]
	}
	s.createHistoryEvent(ctx, models.HistoryEventBookFileMoved, book.Title, &book.ID, map[string]string{
		"path":       dest,
		"sourcePath": source,
		"fromBookId": strconv.FormatInt(fm.FromBookID, 10),
		"fromTitle":  fm.FromTitle,
		"message":    fmt.Sprintf("Fix match moved this file from %q (id %d)", fm.FromTitle, fm.FromBookID),
	})
}

// RelinkResult describes what RelinkFile did.
type RelinkResult struct {
	// Path is the book_files path that now belongs to the target book. The
	// file itself is wherever it was.
	Path   string
	Format string
	// FromBookID is the book the row was taken from, 0 when the path was not
	// tracked before.
	FromBookID int64
	// Unchanged is true when the path already belonged to the target book.
	Unchanged bool
}

// RelinkFile is Fix match's "correct the match only" mode (#2055): it makes
// bookID the owner of the tracked file without touching the file on disk. No
// import runs, so nothing is moved, renamed or deleted, which is what the *arr
// apps' fix match does and what a library imported in place expects.
//
// paths are the spellings of the file the user picked (the path the client
// sent and its symlink-resolved form, #1368); the first one book_files tracks
// is the row relinked, keeping the stored spelling. An untracked file is
// recorded under the first spelling. formatHint wins over the stored format,
// which wins over detection from the path.
func (s *Scanner) RelinkFile(ctx context.Context, bookID int64, paths []string, formatHint string) (RelinkResult, error) {
	if len(paths) == 0 {
		return RelinkResult{}, fmt.Errorf("relink: no path")
	}
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return RelinkResult{}, fmt.Errorf("relink: load book %d: %w", bookID, err)
	}
	if book == nil {
		return RelinkResult{}, fmt.Errorf("relink: book %d not found", bookID)
	}
	var stored *models.BookFile
	for _, p := range paths {
		if p == "" {
			continue
		}
		f, err := s.books.FileByPath(ctx, filepath.Clean(p))
		if err != nil {
			return RelinkResult{}, fmt.Errorf("relink: %w", err)
		}
		if f != nil {
			stored = f
			break
		}
	}
	res := RelinkResult{Path: filepath.Clean(paths[0]), Format: formatHint}
	if stored != nil {
		res.Path = stored.Path
		if res.Format == "" {
			res.Format = stored.Format
		}
	}
	if res.Format == "" {
		res.Format = lookupDetectFormat(filepath.Clean(paths[len(paths)-1]))
	}
	if stored != nil && stored.BookID == bookID {
		res.Unchanged = true
		return res, nil
	}
	prev, err := s.books.MoveBookFile(ctx, res.Path, bookID, res.Format)
	if err != nil {
		return RelinkResult{}, fmt.Errorf("relink: move book file: %w", err)
	}
	res.FromBookID = prev
	slog.Info("fix match: linked a file to the chosen book without moving it",
		"path", res.Path, "fromBookID", prev, "toBookID", bookID)
	if prev != 0 && prev != bookID {
		fromTitle := ""
		if from, err := s.books.GetByID(ctx, prev); err == nil && from != nil {
			fromTitle = from.Title
		}
		s.createHistoryEvent(ctx, models.HistoryEventBookFileMoved, book.Title, &book.ID, map[string]string{
			"path":       res.Path,
			"sourcePath": res.Path,
			"fromBookId": strconv.FormatInt(prev, 10),
			"fromTitle":  fromTitle,
			"message":    fmt.Sprintf("Fix match linked this file from %q (id %d) and left it where it was", fromTitle, prev),
		})
	}
	return res, nil
}
