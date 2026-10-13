package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/vavallee/bindery/internal/models"
)

// DestinationPreview is where a file WOULD land if it were imported against a
// given book, computed without touching disk (#2055).
//
// Fix Match runs the full import pipeline, so choosing a different book moves
// and renames the file into that book's templated location. The modal needs to
// show that path before the user commits, because Reassign returns 202 and the
// move happens in a background goroutine with nothing to undo against.
//
// Status and Message reuse the ReorgStatus* vocabulary the reorganize preview
// already speaks, so the UI has one set of outcomes to render.
type DestinationPreview struct {
	// Source is the file's current on-disk path.
	Source string `json:"source"`
	// Destination is the templated path the import would place it at.
	Destination string `json:"destination"`
	// Format is the media type the import would treat this file as
	// ("ebook" or "audiobook").
	Format string `json:"format"`
	// Status is one of the ReorgStatus* values: move, noop, collision,
	// missing or error.
	Status string `json:"status"`
	// Message explains a non-move status. Empty for move and noop.
	Message string `json:"message,omitempty"`
}

// ErrPreviewBookNotFound is returned by PreviewImportDestination when the
// target book id names no book.
var ErrPreviewBookNotFound = errors.New("book not found")

// PreviewImportDestination reports where importing srcPath against bookID would
// place the file, without modifying anything. formatHint ("ebook"/"audiobook")
// overrides extension-based detection, exactly as it does in ImportFromPath.
//
// It routes through the same proposedPathFor the reorganize preview uses, which
// in turn calls the same renamer entrypoints the import path calls (DestPath for
// ebooks, AudiobookDestDir for audiobook folders). That is deliberate: a preview
// computed any other way would eventually drift from where the import actually
// writes, and a warning dialog that names the wrong path is worse than none.
func (s *Scanner) PreviewImportDestination(ctx context.Context, bookID int64, srcPath, formatHint string) (DestinationPreview, error) {
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return DestinationPreview{}, err
	}
	if book == nil {
		return DestinationPreview{}, fmt.Errorf("%w: %d", ErrPreviewBookNotFound, bookID)
	}

	var author *models.Author
	if a, err := s.authors.GetByID(ctx, book.AuthorID); err != nil {
		slog.Warn("destination preview: failed to load author", "authorID", book.AuthorID, "error", err)
	} else {
		author = a
	}
	seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)

	format := previewFormat(ctx, srcPath, formatHint)
	proposed, status, msg := s.proposedPathFor(ctx, book, author, seriesTitle, seriesNum, models.BookFile{
		Format: format,
		Path:   srcPath,
	})
	return DestinationPreview{
		Source:      srcPath,
		Destination: proposed,
		Format:      format,
		Status:      status,
		Message:     msg,
	}, nil
}

// previewFormat resolves the media type the import would use for srcPath.
// An explicit hint wins (the import path treats a caller-declared format as
// authoritative); otherwise the extensions decide, via the same
// detectDownloadFormat the import runs over its discovered files.
func previewFormat(ctx context.Context, srcPath, formatHint string) string {
	if formatHint == models.MediaTypeEbook || formatHint == models.MediaTypeAudiobook {
		return formatHint
	}
	if info, err := os.Stat(srcPath); err == nil && info.IsDir() {
		return detectDownloadFormat(discoverBookFiles(ctx, srcPath, nil))
	}
	return detectDownloadFormat([]string{filepath.Clean(srcPath)})
}
