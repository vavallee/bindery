package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/models"
)

// releaseExtraExts are the file types a usenet release ships beside the book
// that Bindery has no use for once the book is in the library (#3647): scene
// notes, checksums, link files, cover scans, the NZB itself and its par2
// recovery set. The list is deliberately closed. A type not on it, a book
// format above all, keeps the job folder.
var releaseExtraExts = map[string]bool{
	".nfo": true, ".diz": true, ".sfv": true, ".srr": true, ".srs": true,
	".url": true, ".txt": true, ".jpg": true, ".jpeg": true, ".png": true,
	".gif": true, ".nzb": true, ".par2": true, ".opf": true, ".md5": true,
	".sha1": true, ".log": true,
}

// releaseExtraNames are operating system litter matched by whole name.
var releaseExtraNames = map[string]bool{
	"thumbs.db": true, ".ds_store": true, "desktop.ini": true,
}

// isReleaseExtra reports whether a file name is a release extra that the
// usenet job folder sweep may delete.
func isReleaseExtra(name string) bool {
	lower := strings.ToLower(name)
	if releaseExtraNames[lower] {
		return true
	}
	return releaseExtraExts[filepath.Ext(lower)]
}

// downloadRootsFunc returns the Bindery side folders a download client
// finishes jobs into (its completed folder and every category folder Bindery
// grabs under). An error means they could not be read.
type downloadRootsFunc func(context.Context) ([]string, error)

type clientDownloadRootsKey struct{}

// withClientDownloadRoots attaches the client's completed folders to an
// import, so the usenet job folder sweep can refuse to touch them.
func withClientDownloadRoots(ctx context.Context, fn downloadRootsFunc) context.Context {
	return context.WithValue(ctx, clientDownloadRootsKey{}, fn)
}

func clientDownloadRootsFrom(ctx context.Context) downloadRootsFunc {
	fn, _ := ctx.Value(clientDownloadRootsKey{}).(downloadRootsFunc)
	return fn
}

// usenetClientRoots asks a SABnzbd or NZBGet client where it finishes jobs:
// with no category, with each category Bindery sends, and with the category
// the client itself reports for this job (reportedCategory), which differs
// from the configured ones when the job was recategorised in the client or
// the setting changed after the grab. Every answer goes through the client's
// PathRemap. Any lookup that fails, or a SABnzbd job whose category is not
// known (reportedCategory empty), is an error, so the sweep refuses: the
// folder it did not learn about may be a shared category folder. It is only
// called when the sweep is about to delete something, so a clean import with
// no extras costs no API call.
func (s *Scanner) usenetClientRoots(client *models.DownloadClient, reportedCategory string) downloadRootsFunc {
	return func(ctx context.Context) ([]string, error) {
		if client == nil {
			return nil, errors.New("no download client")
		}
		if client.Type == "sabnzbd" && strings.TrimSpace(reportedCategory) == "" {
			// SABnzbd reports "*" for a job with no category, so an empty
			// value means the job's category is unknown.
			return nil, errors.New("SABnzbd reported no category for the job")
		}
		seen := map[string]bool{}
		var out []string
		for _, cat := range []string{"", client.Category, client.CategoryAudiobook, reportedCategory} {
			if seen[cat] {
				continue
			}
			seen[cat] = true
			var dir string
			var err error
			switch client.Type {
			case "sabnzbd":
				// Strict: CompleteDir's quiet fallback to complete_dir is
				// right for Diagnose and wrong here.
				dir, err = downloader.SabnzbdFor(client).CompleteDirStrict(ctx, cat)
			case "nzbget":
				dir, _, err = downloader.NzbgetFor(client).GrabDestDir(ctx, cat)
			default:
				return nil, fmt.Errorf("client type %q is not a usenet client", client.Type)
			}
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(dir) == "" {
				return nil, fmt.Errorf("the client reported no usable completed folder for category %q", cat)
			}
			out = append(out, s.remapDownloadClientPath(client, dir))
		}
		return out, nil
	}
}

// sweepUsenetJobExtras finishes the cleanup of a usenet job folder after a
// clean move mode import (#3647). cleanupMovedSources removes only the moved
// book files and prunes folders with os.Remove, so a job that also shipped an
// .nfo or a cover scan was left behind for good, and SABnzbd's history
// removal does not delete the job's files either.
//
// The caller runs this only for a SABnzbd or NZBGet download whose every book
// file imported, in move mode. Torrents never come here: their folder may
// still be seeding. Within that, the sweep is all or nothing:
//
//   - it works only inside the job's own folder (movePruneRoot), and refuses a
//     folder that is or contains Bindery's download folders, a library root,
//     a root folder, or the client's completed or category folder;
//   - every file in the folder tree must be a regular file on the release
//     extras list and neither one of the download's book files nor a file
//     big enough to be a book in its own right (a .txt edition beside a
//     single file job, which the import never walked). One other file
//     (an unimported book, an unknown type, a symlink) and nothing is
//     deleted; that file is logged once at INFO;
//   - files go with os.Remove and folders with os.Remove, deepest first,
//     never os.RemoveAll, and symlinks are never followed.
func (s *Scanner) sweepUsenetJobExtras(ctx context.Context, downloadPath string, importedSrcFiles, bookFiles []string) {
	// A book file the import discovered but did not place (a format the
	// profile disallows, say) keeps the folder even when its extension is on
	// the extras list: .txt is both a release note and an ebook format.
	unplaced := make(map[string]bool, len(bookFiles))
	for _, f := range bookFiles {
		unplaced[filepath.Clean(f)] = true
	}
	pruneRoot, reason := s.movePruneRoot(downloadPath, importedSrcFiles)
	if reason != "" {
		return // cleanupMovedSources already logged why
	}
	info, err := os.Lstat(pruneRoot)
	if err != nil || !info.IsDir() {
		// Already pruned (no extras), or not a real folder.
		return
	}

	var extras, dirs []string
	blocker := ""
	walkErr := filepath.WalkDir(pruneRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, path)
		case d.Type().IsRegular() && isReleaseExtra(d.Name()) && !unplaced[filepath.Clean(path)] && !plausibleBookFile(path, d):
			extras = append(extras, path)
		default:
			blocker = path
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		slog.Info("usenet job folder kept: could not read it", "folder", pruneRoot, "error", walkErr)
		return
	}
	if blocker != "" {
		slog.Info("usenet job folder kept: it holds a file that is not a release extra", "folder", pruneRoot, "file", blocker)
		return
	}
	if err := s.jobFolderGuard(ctx, pruneRoot); err != nil {
		slog.Info("usenet job folder kept", "folder", pruneRoot, "reason", err)
		return
	}

	for _, f := range extras {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			slog.Warn("usenet job folder: could not remove a release extra", "file", f, "error", err)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			slog.Debug("usenet job folder: directory not pruned (kept)", "dir", dir, "reason", err)
		}
	}
	slog.Info("removed usenet job folder left after import", "folder", pruneRoot, "extras", len(extras))
}

// jobFolderGuard refuses a job folder that is, or contains, a folder the
// sweep must never empty: Bindery's own download folders, the library roots
// and root folders, and the download client's completed and category
// folders. An unknown client folder is a refusal too, since a SABnzbd
// category with a trailing asterisk puts jobs straight into the category
// folder and a single file job's parent is then that shared folder.
func (s *Scanner) jobFolderGuard(ctx context.Context, pruneRoot string) error {
	protected := []string{s.downloadDir, s.audiobookDownloadDir, s.libraryDir, s.audiobookDir}
	if s.rootFolders != nil {
		rfs, err := s.rootFolders.List(ctx)
		if err != nil {
			return fmt.Errorf("could not list root folders: %w", err)
		}
		for _, rf := range rfs {
			protected = append(protected, rf.Path)
		}
	}
	fn := clientDownloadRootsFrom(ctx)
	if fn == nil {
		return errors.New("the download client's completed folder is unknown")
	}
	roots, err := fn(ctx)
	if err != nil {
		return fmt.Errorf("could not read the download client's completed folder: %w", err)
	}
	protected = append(protected, roots...)
	for _, p := range protected {
		if strings.TrimSpace(p) == "" {
			continue
		}
		c := filepath.Clean(p)
		if pathUnderDir(c, pruneRoot) {
			return fmt.Errorf("it is or contains %s", c)
		}
	}
	return nil
}

// plausibleBookFile reports whether a file the sweep found could be a book:
// a book extension and not too small to be one. bookFiles alone does not
// cover this, because a single file job's import sees only that one file
// while the sweep walks its whole folder.
func plausibleBookFile(path string, d fs.DirEntry) bool {
	if !IsBookFile(path) {
		return false
	}
	info, err := d.Info()
	if err != nil {
		return true // cannot tell, so keep it
	}
	return !TooSmallToBeABook(path, info.Size())
}
