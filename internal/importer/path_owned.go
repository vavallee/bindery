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
