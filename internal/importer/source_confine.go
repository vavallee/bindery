package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// relInside reports whether rel, the result of filepath.Rel(base, target),
// names base itself or something below it.
func relInside(rel string) bool {
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// sourceWithinRoot reports whether the real location of file lies inside the
// real location of root. A download is untrusted input: a release (or an
// archive extracted from one) can plant a directory symlink, and the client's
// file list then names "<download>/link/book.epub", which passes a check on the
// last path component alone while its bytes live outside the download.
//
// Only a file that sits lexically under root is judged. The client's file list
// is joined onto its save path, so a name always lies under the content folder
// for a sane client; anything else has no root to be held to and is left to the
// other guards. Symlinks that stay inside root are allowed, and root itself may
// be reached through a link of the operator's own (a common deployment shape),
// because both sides are resolved before they are compared.
func sourceWithinRoot(file, root string) bool {
	if strings.TrimSpace(root) == "" {
		return true
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(file))
	if err != nil || !relInside(rel) {
		return true
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	realFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		return false
	}
	rel, err = filepath.Rel(realRoot, realFile)
	return err == nil && relInside(rel)
}

// escapesDownloadRoot reports whether any listed file is present on this host
// as a plain entry yet resolves outside root. A missing entry does not count:
// that is the phantom-path case with its own handling.
func escapesDownloadRoot(root string, files []string) bool {
	for _, f := range files {
		if _, ok := importableSourceInfo(f); ok && !sourceWithinRoot(f, root) {
			return true
		}
	}
	return false
}

// requireRegularFile fails unless the open handle f is a regular file, so a
// link swapped in after the caller's own checks, or a device or directory, is
// refused rather than copied.
func requireRegularFile(f *os.File, src string) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", src)
	}
	return nil
}

// resolvedForCompare returns p with symlinks resolved, or p cleaned when it
// cannot be resolved (a path that does not exist yet).
func resolvedForCompare(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// swallowedStorage returns the first of roots that src is, or sits above. A
// folder placement moves, copies or hardlinks the whole source folder, so a
// source that is a library root, or an ancestor of one, would pull the library
// itself into a book's folder (and in move mode delete it from where it was).
// The path comes from a download client's own report, which is not trusted to
// be a job folder. Empty roots are ignored.
func swallowedStorage(src string, roots []string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	s := resolvedForCompare(src)
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		rel, err := filepath.Rel(s, resolvedForCompare(root))
		if err == nil && relInside(rel) {
			return root
		}
	}
	return ""
}

// isDownloadRoot reports whether src is exactly one of the configured download
// folders. A client that reports the shared download folder as a job's folder
// would otherwise get every other job's files imported (and, in move mode,
// removed) as part of this book.
func isDownloadRoot(src string, roots []string) bool {
	if strings.TrimSpace(src) == "" {
		return false
	}
	s := resolvedForCompare(src)
	for _, root := range roots {
		if strings.TrimSpace(root) != "" && resolvedForCompare(root) == s {
			return true
		}
	}
	return false
}

// storageRoots lists the library folders an audiobook source must not contain
// or equal: the configured ebook and audiobook directories and every root
// folder.
func (s *Scanner) storageRoots(ctx context.Context) []string {
	roots := []string{s.libraryDir, s.audiobookDir}
	if s.rootFolders != nil {
		if list, err := s.rootFolders.List(ctx); err == nil {
			for _, rf := range list {
				roots = append(roots, rf.Path)
			}
		}
	}
	return roots
}
