package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// Reorganize move statuses (#1181). Every previewed file carries one so the UI
// can show why a file will or will not move before anything touches disk.
const (
	ReorgStatusMove      = "move"      // proposed differs from current; ready to move
	ReorgStatusNoop      = "noop"      // already at the templated location
	ReorgStatusCollision = "collision" // a different file already occupies the proposed path
	ReorgStatusMissing   = "missing"   // the tracked file is not on disk
	ReorgStatusError     = "error"     // the target path could not be computed
	ReorgStatusMoved     = "moved"     // apply: the move completed
	ReorgStatusFailed    = "failed"    // apply: the move failed
)

// ReorganizeMove is a single tracked file's current location and where the
// current naming template says it should live. It is the unit of both the
// preview (proposed moves) and the apply result (with Status updated to
// moved/failed).
type ReorganizeMove struct {
	BookID    int64  `json:"bookId"`
	FileID    int64  `json:"fileId"`
	Format    string `json:"format"`
	BookTitle string `json:"bookTitle"`
	Author    string `json:"author"`
	Current   string `json:"current"`
	Proposed  string `json:"proposed"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

// PreviewReorganizeBook returns the proposed moves for one book's tracked
// files. It never touches disk.
func (s *Scanner) PreviewReorganizeBook(ctx context.Context, bookID int64) ([]ReorganizeMove, error) {
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, fmt.Errorf("book %d not found", bookID)
	}
	return s.previewBook(ctx, book)
}

// PreviewReorganizeAuthor returns the proposed moves for every book by an
// author. It never touches disk.
func (s *Scanner) PreviewReorganizeAuthor(ctx context.Context, authorID int64) ([]ReorganizeMove, error) {
	books, err := s.books.ListByAuthor(ctx, authorID)
	if err != nil {
		return nil, err
	}
	return s.previewBooks(ctx, books)
}

// PreviewReorganizeLibrary returns the proposed moves for the whole library. It
// never touches disk. On a large library this walks every book once; callers
// should treat it as an admin-triggered, potentially slow read.
func (s *Scanner) PreviewReorganizeLibrary(ctx context.Context) ([]ReorganizeMove, error) {
	books, err := s.books.List(ctx)
	if err != nil {
		return nil, err
	}
	return s.previewBooks(ctx, books)
}

func (s *Scanner) previewBooks(ctx context.Context, books []models.Book) ([]ReorganizeMove, error) {
	var moves []ReorganizeMove
	for i := range books {
		if err := ctx.Err(); err != nil {
			return moves, err
		}
		bm, err := s.previewBook(ctx, &books[i])
		if err != nil {
			slog.Warn("reorganize: failed to preview book", "bookID", books[i].ID, "error", err)
			continue
		}
		moves = append(moves, bm...)
	}
	return moves, nil
}

// previewBook computes, for each of a book's tracked files, where the current
// naming template would place it and whether that differs from where it sits.
func (s *Scanner) previewBook(ctx context.Context, book *models.Book) ([]ReorganizeMove, error) {
	files, err := s.books.ListBookFiles(ctx, book.ID)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	var author *models.Author
	if a, err := s.authors.GetByID(ctx, book.AuthorID); err != nil {
		slog.Warn("reorganize: failed to load author", "authorID", book.AuthorID, "error", err)
	} else {
		author = a
	}
	authorName := ""
	if author != nil {
		authorName = author.Name
	}
	seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)

	moves := make([]ReorganizeMove, 0, len(files))
	for _, f := range files {
		m := ReorganizeMove{
			BookID:    book.ID,
			FileID:    f.ID,
			Format:    f.Format,
			BookTitle: book.Title,
			Author:    authorName,
			Current:   f.Path,
		}
		proposed, status, msg := s.proposedPathFor(ctx, book, author, seriesTitle, seriesNum, f)
		m.Proposed = proposed
		m.Status = status
		m.Message = msg
		moves = append(moves, m)
	}
	return moves, nil
}

// proposedPathFor computes the templated destination for a single tracked file
// and classifies the move. It reuses the exact renamer entrypoints the import
// path uses (DestPath for ebooks, AudiobookDestDir for audiobook folders), so a
// reorganized library matches a freshly-imported one byte-for-byte.
func (s *Scanner) proposedPathFor(ctx context.Context, book *models.Book, author *models.Author, seriesTitle, seriesNum string, f models.BookFile) (proposed, status, msg string) {
	var dest string
	var err error
	audiobook := f.Format == models.MediaTypeAudiobook
	if audiobook {
		root := s.effectiveAudiobookDir(ctx, author)
		dest, err = s.renamer.AudiobookDestDir(root, author, book, seriesTitle, seriesNum)
	} else {
		root := s.effectiveLibraryDir(ctx, author)
		dest, err = s.renamer.DestPath(root, author, book, seriesTitle, seriesNum, f.Path)
	}
	if err != nil {
		return "", ReorgStatusError, err.Error()
	}

	// Mirror the import path's uniqueness handling for audiobook folders. Import
	// runs the templated dir through UniqueDir (scanner.go), so a dual-format
	// book whose audiobook template resolves to the same "Title (Year)" folder
	// its ebook already occupies lands at "Title (Year) (2)". Reorganize must
	// resolve to the same variant — treating this file's OWN current location as
	// available, so an already-correctly-placed audiobook reads as a noop rather
	// than a false collision (or an endless (N)→(N+1) churn). Ebooks are single
	// files and are not uniquified at import, so they skip this.
	if audiobook {
		dest = uniqueDirExcluding(dest, f.Path)
	}

	if filepath.Clean(dest) == filepath.Clean(f.Path) {
		return dest, ReorgStatusNoop, ""
	}
	// The source must still be on disk to move it.
	if _, err := os.Stat(f.Path); err != nil {
		if os.IsNotExist(err) {
			return dest, ReorgStatusMissing, "tracked file is not on disk"
		}
		return dest, ReorgStatusError, err.Error()
	}
	// Never overwrite: if something already occupies the proposed path it is
	// either an untracked file or another book's file. Surface it and skip.
	if _, err := os.Stat(dest); err == nil {
		return dest, ReorgStatusCollision, "destination already exists"
	} else if !os.IsNotExist(err) {
		return dest, ReorgStatusError, err.Error()
	}
	// The destination can be free on disk yet already claimed in the index by a
	// dangling book_files row (a file deleted off disk without clearing its
	// row). book_files.path is UNIQUE, so moving here would land the file but
	// then fail the index update, leaving the row pointing at a now-missing
	// path. Treat an index-level owner as a collision too.
	if owned, err := s.books.PathOwnedByOtherBook(ctx, dest, book.ID); err != nil {
		return dest, ReorgStatusError, err.Error()
	} else if owned {
		return dest, ReorgStatusCollision, "another book already tracks the destination path"
	}
	return dest, ReorgStatusMove, ""
}

// uniqueDirExcluding mirrors UniqueDir but treats keep as if it were absent, so
// resolving the destination for a file already parked at a uniquified location
// returns that same location (a noop) instead of bumping to the next suffix.
func uniqueDirExcluding(base, keep string) string {
	keep = filepath.Clean(keep)
	if filepath.Clean(base) == keep {
		return base
	}
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)", base, i)
		if filepath.Clean(candidate) == keep {
			return candidate
		}
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return base
}

// ApplyReorganize moves each of the given tracked files to its templated
// location and updates the book_files index. It recomputes the destination
// itself (it does not trust a client-supplied target), skips anything that is
// no longer a clean move, and returns a result per file. Files not classified
// as a move (noop/collision/missing/error) are reported unchanged, never moved.
//
// Reorganize is always a move within the library — never a copy — so a
// hard-linked file keeps its link and a file is never duplicated.
func (s *Scanner) ApplyReorganize(ctx context.Context, fileIDs []int64) []ReorganizeMove {
	results := make([]ReorganizeMove, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		if err := ctx.Err(); err != nil {
			break
		}
		results = append(results, s.applyOne(ctx, fileID))
	}
	return results
}

func (s *Scanner) applyOne(ctx context.Context, fileID int64) ReorganizeMove {
	book, file, err := s.bookFileByID(ctx, fileID)
	if err != nil {
		return ReorganizeMove{FileID: fileID, Status: ReorgStatusFailed, Message: err.Error()}
	}

	var author *models.Author
	if a, aerr := s.authors.GetByID(ctx, book.AuthorID); aerr == nil {
		author = a
	}
	authorName := ""
	if author != nil {
		authorName = author.Name
	}
	seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)

	proposed, status, msg := s.proposedPathFor(ctx, book, author, seriesTitle, seriesNum, file)
	m := ReorganizeMove{
		BookID: book.ID, FileID: fileID, Format: file.Format,
		BookTitle: book.Title, Author: authorName,
		Current: file.Path, Proposed: proposed, Status: status, Message: msg,
	}
	// Only a clean, still-valid move is carried out. Anything the recompute now
	// classifies otherwise (a racing collision, a vanished source, a template
	// that no longer resolves) is reported as-is and left on disk.
	if status != ReorgStatusMove {
		return m
	}

	if err := s.moveTrackedFile(ctx, file, proposed); err != nil {
		m.Status = ReorgStatusFailed
		m.Message = err.Error()
		return m
	}

	if err := s.books.UpdateBookFilePath(ctx, file.ID, book.ID, proposed); err != nil {
		// The file is already at the new path on disk; failing to record it
		// leaves the index pointing at a now-missing location. This is the one
		// genuinely bad outcome, so it is surfaced as a failure with a specific
		// message rather than swallowed.
		slog.Error("reorganize: moved file but failed to update index", "fileID", file.ID, "from", file.Path, "to", proposed, "error", err)
		m.Status = ReorgStatusFailed
		m.Message = "file moved on disk but the library index could not be updated: " + err.Error()
		return m
	}

	pruneEmptyParents(filepath.Dir(file.Path), s.rootsForFormat(ctx, author, file.Format))
	bookID := book.ID
	s.createHistoryEvent(ctx, models.HistoryEventBookRenamed, book.Title, &bookID, map[string]string{
		"from":   file.Path,
		"to":     proposed,
		"format": file.Format,
	})

	m.Status = ReorgStatusMoved
	return m
}

// moveTrackedFile moves an already-imported file (ebook) or folder (audiobook)
// from its current path to dest, creating the parent directory. It reuses the
// import machinery's move primitives: StagedImport for a single ebook file
// (atomic commit + rollback across a same-fs rename or cross-fs copy), and
// MoveDirCtx for an audiobook folder.
func (s *Scanner) moveTrackedFile(ctx context.Context, file models.BookFile, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	info, err := os.Stat(file.Path)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if info.IsDir() {
		// Audiobook folder.
		return MoveDirCtx(ctx, file.Path, dest)
	}

	// Single ebook file: stage → commit for an atomic, rollback-safe move.
	_, commit, rollback, err := StagedImport(ctx, "move", file.Path, dest)
	if err != nil {
		return err
	}
	if err := commit(); err != nil {
		rollback()
		return err
	}
	return nil
}

// bookFileByID resolves a book_files row and its owning book. book_files has no
// single-row getter, so this scans the owner's files for the id.
func (s *Scanner) bookFileByID(ctx context.Context, fileID int64) (*models.Book, models.BookFile, error) {
	bookID, err := s.books.BookIDForFile(ctx, fileID)
	if err != nil {
		return nil, models.BookFile{}, err
	}
	if bookID == 0 {
		return nil, models.BookFile{}, fmt.Errorf("book file %d not found", fileID)
	}
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, models.BookFile{}, err
	}
	if book == nil {
		return nil, models.BookFile{}, fmt.Errorf("book %d not found", bookID)
	}
	files, err := s.books.ListBookFiles(ctx, bookID)
	if err != nil {
		return nil, models.BookFile{}, err
	}
	for _, f := range files {
		if f.ID == fileID {
			return book, f, nil
		}
	}
	return nil, models.BookFile{}, fmt.Errorf("book file %d not found", fileID)
}

// rootsForFormat returns the library root(s) a file of the given format may
// live under, used to bound empty-directory pruning so it can never walk above
// the library.
func (s *Scanner) rootsForFormat(ctx context.Context, author *models.Author, format string) []string {
	var roots []string
	if format == models.MediaTypeAudiobook {
		roots = append(roots, s.effectiveAudiobookDir(ctx, author), s.audiobookDir)
	} else {
		roots = append(roots, s.effectiveLibraryDir(ctx, author), s.libraryDir)
	}
	out := roots[:0]
	for _, r := range roots {
		if r != "" {
			out = append(out, filepath.Clean(r))
		}
	}
	return out
}

// pruneEmptyParents removes now-empty directories starting at dir and walking
// upward, stopping at (and never removing) any of the given library roots.
// Best-effort: a non-empty directory (ENOTEMPTY) ends the walk, and any other
// error is ignored. Mirrors the import move-cleanup contract.
func pruneEmptyParents(dir string, roots []string) {
	isRoot := func(p string) bool {
		for _, r := range roots {
			if filepath.Clean(p) == r {
				return true
			}
		}
		return false
	}
	underAnyRoot := func(p string) bool {
		for _, r := range roots {
			if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	cur := filepath.Clean(dir)
	// Only prune inside the library; never touch a root or anything above it.
	for underAnyRoot(cur) && !isRoot(cur) {
		if err := os.Remove(cur); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				// ENOTEMPTY (siblings remain) or any other error: stop walking up.
				return
			}
		}
		cur = filepath.Dir(cur)
	}
}
