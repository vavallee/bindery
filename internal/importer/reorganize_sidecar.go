package importer

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/vavallee/bindery/internal/models"
)

// folderIndex maps each folder to the books that will hold files in it once
// the run is done. A sidecar describes one book, so a folder with two books
// in it gets none (#4008). It is built once per preview or apply from a single
// query, never per file: a per-file LIKE scan was quadratic on a large
// library.
type folderIndex struct {
	byFolder map[string]map[int64]int
}

// folderOf is the folder a tracked path occupies: the path itself for an
// audiobook folder, its parent for a single file. It decides from the format
// and the file extension alone and never touches disk, so a planned
// destination that does not exist yet is classified the same way.
func folderOf(path, format string) string {
	if format == models.MediaTypeAudiobook && !IsAudioFile(path) {
		return path
	}
	return filepath.Dir(path)
}

func (x *folderIndex) add(s *Scanner, folder string, bookID int64, delta int) {
	k := s.pathKey(folder)
	m := x.byFolder[k]
	if m == nil {
		m = map[int64]int{}
		x.byFolder[k] = m
	}
	m[bookID] += delta
}

// sharedBy reports whether a book other than bookID holds files in dir.
func (x *folderIndex) sharedBy(s *Scanner, dir string, bookID int64) bool {
	for id, n := range x.byFolder[s.pathKey(dir)] {
		if id != bookID && n > 0 {
			return true
		}
	}
	return false
}

// buildFolderIndex indexes every tracked file of a live book, then applies the
// planned moves so a folder two books are about to share counts as shared in
// both the preview and the apply. nil when the rows cannot be read.
func (s *Scanner) buildFolderIndex(ctx context.Context, planned []ReorganizeMove) *folderIndex {
	owners, err := s.books.ListLiveFileOwners(ctx)
	if err != nil {
		slog.Warn("reorganize: could not index tracked folders, skipping sidecars", "error", err)
		return nil
	}
	x := &folderIndex{byFolder: make(map[string]map[int64]int, len(owners))}
	for _, o := range owners {
		x.add(s, folderOf(o.Path, o.Format), o.BookID, 1)
	}
	for _, m := range planned {
		if m.Status != ReorgStatusMove {
			continue
		}
		x.add(s, folderOf(m.Current, m.Format), m.BookID, -1)
		x.add(s, folderOf(m.Proposed, m.Format), m.BookID, 1)
	}
	return x
}

// annotateSidecars fills in the preview's metadata.opf action for every row,
// after the whole plan is known. Already-correct rows get write, unchanged,
// foreign or shared; rows that will move only get shared when their final
// folder is shared, which is what apply will report.
func (s *Scanner) annotateSidecars(ctx context.Context, moves []ReorganizeMove) {
	if len(moves) == 0 || !s.opfSidecarEnabled(ctx) {
		return
	}
	idx := s.buildFolderIndex(ctx, moves)
	type ctxBook struct {
		book   *models.Book
		author *models.Author
		sTitle string
		sNum   string
	}
	cache := map[int64]*ctxBook{}
	load := func(id int64) *ctxBook {
		if c, ok := cache[id]; ok {
			return c
		}
		var c *ctxBook
		if book, err := s.books.GetByID(ctx, id); err == nil && book != nil {
			c = &ctxBook{book: book}
			if a, aerr := s.authors.GetByID(ctx, book.AuthorID); aerr == nil {
				c.author = a
			}
			c.sTitle, c.sNum = s.primarySeriesFor(ctx, book)
		}
		cache[id] = c
		return c
	}
	for i := range moves {
		m := &moves[i]
		if s.configuredImportModeFor(ctx, m.Format) == "external" {
			continue
		}
		switch m.Status {
		case ReorgStatusNoop:
			c := load(m.BookID)
			if c == nil {
				continue
			}
			m.Sidecar, m.SidecarMessage = s.sidecarForNoop(ctx, true, idx, c.book, c.author, c.sTitle, c.sNum,
				models.BookFile{ID: m.FileID, Format: m.Format, Path: m.Current})
		case ReorgStatusMove:
			if idx != nil && idx.sharedBy(s, folderOf(m.Proposed, m.Format), m.BookID) {
				m.Sidecar = ReorgSidecarShared
				m.SidecarMessage = "folder holds files of more than one book, no metadata.opf written"
			}
		}
	}
	dedupeSidecarPreview(moves)
}

// sidecarIndexForApply plans the run's own moves, so the first of two books
// moving into one folder does not write a sidecar the second then makes stale.
func (s *Scanner) sidecarIndexForApply(ctx context.Context, fileIDs []int64) *folderIndex {
	if !s.opfSidecarEnabled(ctx) {
		return nil
	}
	var planned []ReorganizeMove
	for _, id := range fileIDs {
		book, file, err := s.bookFileByID(ctx, id)
		if err != nil {
			continue
		}
		var author *models.Author
		if a, aerr := s.authors.GetByID(ctx, book.AuthorID); aerr == nil {
			author = a
		}
		seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
		proposed, status, _ := s.reorganizePathFor(ctx, book, author, seriesTitle, seriesNum, file)
		planned = append(planned, ReorganizeMove{BookID: book.ID, FileID: id, Format: file.Format, Current: file.Path, Proposed: proposed, Status: status})
	}
	return s.buildFolderIndex(ctx, planned)
}
