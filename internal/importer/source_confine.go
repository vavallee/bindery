package importer

import (
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

// openRegularSource opens src for reading without following a symlink at the
// last component and confirms the open handle is a regular file, so a link
// swapped in after the caller's own checks is refused rather than followed.
func openRegularSource(src string) (*os.File, error) {
	f, err := openNoFollow(src)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("source is not a regular file: %s", src)
	}
	return f, nil
}
