// Package dirperm resolves the directory mode used for the library content
// directories created by the importer: author, book and audiobook folders,
// the metadata-opf sidecars and the drop-folder handoff.
//
// The default remains the historical 0750. Operators whose storage rewrites
// client IDs server-side (NFS exports with all_squash/mapall, root-squash
// setups) or who need other readers to traverse these directories can relax
// the restriction with BINDERY_DIR_MODE.
package dirperm

import (
	"os"
	"strings"
)

// DefaultLibraryMode is the historical mode for importer-created library
// directories.
const DefaultLibraryMode os.FileMode = 0o750

// allowedModes is the closed set of modes BINDERY_DIR_MODE accepts, keyed
// without the leading zero; both spellings are accepted at lookup. The set
// only holds modes whose bits survive the usual 022 umask, so the value an
// operator sets is the mode the directories get. A closed whitelist (rather
// than free-form octal parsing) keeps operator mistakes, such as the Go
// spelling "0o755" or a stray space, from locking the importer out of its
// own directories, and makes the accepted values easy to document.
var allowedModes = map[string]os.FileMode{
	"700": 0o700,
	"750": 0o750,
	"755": 0o755,
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
