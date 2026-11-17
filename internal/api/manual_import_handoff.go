package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// handedOffIndex holds the sources that downloads have handed off to an
// external import tool and that are still waiting on it, so the manual import
// folder scan does not offer them again as fresh matches.
//
// In External mode with a drop folder, Copy and Hardlink placement keep the
// source by design (a torrent keeps seeding, a manual import leaves the
// original where it was). Nothing is written to book_files until the external
// tool places the file in the library, so neither of the scan's "already
// imported" filters can see the source, and a rescan listed it under Matched,
// ticked, one click away from dropping a second copy.
//
// Matching is exact. A hand-off records the files it handed off
// (models.EncodeHandedOffSources), and only a unit that is one of them, or a
// folder unit whose every importable file is one of them, counts as handed
// off. A recorded folder never covers what is inside it: a Transmission
// download's path is the client's shared category folder, so treating it as
// covering its contents hid every unrelated book in that folder until each
// handed off row was removed. A held download records its download path, so
// it hides a unit only when that unit is the same path.
//
// The rule is the download's state. A source is handed off while a download
// that recorded it (import_path) is in StateImportExternal or StateImportHeld:
// the hand-off is outstanding, or the file is held for its pair and will be
// dropped when the sibling arrives. Once that download leaves those states the
// source is offered again: a failed or blocked hand-off (importFailed,
// importBlocked), a row removed from the queue, or a retry that has not handed
// off yet. Once the external tool places the file in the library, the
// book_files filters take over as they do in Auto mode.
//
// The manual import routes are admin only and the scan reads the filesystem,
// not one user's library, so the index covers every user's downloads, as the
// book_files index does: a file another user's download handed off is still
// handed off, and offering it would still drop it twice.
type handedOffIndex struct {
	paths map[string]struct{}
}

// handedOffSources builds the index from the downloads currently handed off.
// A recorded path that is, or contains, a configured library root, an import
// source folder or a download folder is left out: no single hand-off owns a
// whole intake folder, and a row held before pair gating recorded only its own
// files can carry one (it recorded the download path, which for Transmission
// is the shared category folder).
func (h *ManualImportHandler) handedOffSources(ctx context.Context) (*handedOffIndex, error) {
	rows, err := h.downloads.ListByStatuses(ctx, models.StateImportExternal, models.StateImportHeld)
	if err != nil {
		return nil, err
	}
	idx := &handedOffIndex{paths: make(map[string]struct{}, len(rows))}
	if len(rows) == 0 {
		return idx, nil
	}
	broad := append(h.roots.Configured(ctx), h.excluded...)
	dirs := newDirResolver()
	for i := range rows {
		for _, p := range rows[i].HandedOffSources() {
			p = strings.TrimSpace(p)
			if p == "" || !filepath.IsAbs(p) {
				continue
			}
			cleaned := filepath.Clean(p)
			// Scan walks the symlink resolved folder, and the importer records
			// the path it was handed, so both forms are indexed (as in
			// trackedFileIndex).
			for _, form := range []string{cleaned, dirs.resolvePath(cleaned)} {
				if containsAny(form, broad) {
					continue
				}
				idx.paths[form] = struct{}{}
			}
		}
	}
	return idx, nil
}

// containsAny reports whether dir is one of paths or an ancestor of one.
func containsAny(dir string, paths []string) bool {
	for _, p := range paths {
		rel, err := filepath.Rel(dir, filepath.Clean(p))
		if err == nil && (rel == "." || filepath.IsLocal(rel)) {
			return true
		}
	}
	return false
}

// covers reports whether p is exactly a recorded source.
func (x *handedOffIndex) covers(p string) bool {
	if x == nil || len(x.paths) == 0 {
		return false
	}
	_, ok := x.paths[filepath.Clean(p)]
	return ok
}

// coversUnit reports whether a scan unit was handed off. A folder unit (an
// audiobook) counts when it is covered itself, or when every importable file
// below it is, mirroring directoryMatchesTracked: one file that was not handed
// off keeps the folder offered.
func (x *handedOffIndex) coversUnit(p string, isDir bool) bool {
	if x.covers(p) {
		return true
	}
	if !isDir || !x.hasBelow(p) {
		return false
	}
	found, all := false, true
	_ = filepath.WalkDir(p, func(f string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			all = false
			return nil
		}
		if entry.IsDir() || (!importer.IsEbookFile(f) && !importer.IsAudioFile(f)) {
			return nil
		}
		found = true
		if !x.covers(f) {
			all = false
		}
		return nil
	})
	return found && all
}

// hasBelow reports whether any recorded source sits inside dir, so coversUnit
// only walks a folder that could be handed off.
func (x *handedOffIndex) hasBelow(dir string) bool {
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	for q := range x.paths {
		if strings.HasPrefix(q, prefix) {
			return true
		}
	}
	return false
}
