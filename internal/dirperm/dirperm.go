// Package dirperm resolves the directory mode used for the library content
// directories created by the importer: author, book and audiobook folders,
// the metadata-opf sidecars and the drop-folder handoff.
//
// The default remains the historical 0750. Operators whose storage rewrites
// client IDs server-side (NFS exports with all_squash/mapall, root-squash
// setups) or who need other readers to traverse these directories can relax
// the restriction with BINDERY_DIR_MODE.
//
// mkdir(2) applies the process umask, so a plain os.MkdirAll with 0770 under
// the usual 022 umask yields 0750. The group-writable modes therefore only
// work through MkdirAll and Mkdir below, which chmod the directories they
// create when the umask changed the result.
package dirperm

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// DefaultLibraryMode is the historical mode for importer-created library
// directories.
const DefaultLibraryMode os.FileMode = 0o750

// allowedModes is the closed set of modes BINDERY_DIR_MODE accepts, keyed
// without the leading zero; both spellings are accepted at lookup. The value
// an operator sets is the mode the directories get: MkdirAll and Mkdir chmod
// explicitly when the umask would have stripped bits. No world-writable mode
// is accepted. A closed whitelist (rather
// than free-form octal parsing) keeps operator mistakes, such as the Go
// spelling "0o755" or a stray space, from locking the importer out of its
// own directories, and makes the accepted values easy to document.
var allowedModes = map[string]os.FileMode{
	"700": 0o700,
	"750": 0o750,
	"755": 0o755,
	// Group-writable, for libraries shared with another service through a
	// common group (#4175). The operator opts in to wider write access.
	"770": 0o770,
	"775": 0o775,
}

// Library returns the mode to use when creating library content directories.
//
// It reads BINDERY_DIR_MODE on every call. The value must be one of the
// whitelisted modes, written with or without a leading zero (e.g. "0755" or
// "755"). Any unset, empty or unrecognised value falls back to 0o750, the
// historical default; config.Validate warns at startup when the value is set
// but unrecognised.
func Library() os.FileMode {
	return parse(os.Getenv("BINDERY_DIR_MODE"))
}

// Valid reports whether raw is a BINDERY_DIR_MODE value the package accepts.
// A whitespace-only value is valid: it means "use the default". A literal "0"
// is not: the operator typed something, and normalize would otherwise swallow
// it into the unset case and hide it from the startup warning.
func Valid(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	_, ok := allowedModes[normalize(raw)]
	return ok
}

func normalize(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "0")
}

func parse(raw string) os.FileMode {
	if m, ok := allowedModes[normalize(raw)]; ok {
		return m
	}
	return DefaultLibraryMode
}

// MkdirAll creates path and any missing parents like os.MkdirAll, then makes
// every directory it created carry exactly Library(), whatever the umask.
// Directories that already existed are left alone. A failed chmod does not
// fail the import (some network and FUSE mounts refuse it); it is logged.
func MkdirAll(path string) error {
	want := Library()
	var created []string
	for p := filepath.Clean(path); ; {
		if _, err := os.Lstat(p); err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
		created = append(created, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	if err := os.MkdirAll(path, want); err != nil {
		return err
	}
	for _, p := range created {
		fixMode(p, want, func(m os.FileMode) error { return os.Chmod(p, m) }, func() (os.FileMode, error) {
			fi, err := os.Lstat(p)
			if err != nil {
				return 0, err
			}
			return fi.Mode(), nil
		})
	}
	return nil
}

// Mkdir creates one directory inside root like root.Mkdir, then applies
// Library() exactly. An already existing directory is returned as an error
// satisfying os.IsExist and is not modified.
func Mkdir(root *os.Root, name string) error {
	want := Library()
	if err := root.Mkdir(name, want); err != nil {
		return err
	}
	fixMode(name, want, func(m os.FileMode) error { return root.Chmod(name, m) }, func() (os.FileMode, error) {
		fi, err := root.Lstat(name)
		if err != nil {
			return 0, err
		}
		return fi.Mode(), nil
	})
	return nil
}

func fixMode(name string, want os.FileMode, chmod func(os.FileMode) error, stat func() (os.FileMode, error)) {
	got, err := stat()
	if err != nil || !got.IsDir() || got.Perm() == want {
		return
	}
	if err := chmod(want); err != nil {
		slog.Warn("dirperm: could not set the configured directory mode",
			"path", name, "mode", want.String(), "error", err)
	}
}
