package calibre

import "strings"

// Mode selects which Calibre-integration flow the importer runs after a
// successful Bindery import. v0.8.0 shipped only the calibredb path; v0.8.1
// adds drop-folder ingest and a per-library toggle so operators can pick
// the one that fits their deployment.
type Mode string

const (
	// ModeOff disables Calibre integration. Imports behave as if no Calibre
	// library were configured: no external call, no mutation on the book row.
	ModeOff Mode = "off"

	// ModeCalibredb shells out to `calibredb add --with-library <path>`.
	// This is the v0.8.0 path and is preserved unchanged for installs that
	// have calibredb on PATH and a writable library directory.
	ModeCalibredb Mode = "calibredb"

	// ModeDropFolder writes the imported file into a Calibre-watched
	// directory and then polls Calibre's metadata.db for the resulting
	// book id. This path is for deployments where Calibre runs in a
	// separate container / host and calibredb is not reachable from Bindery.
	ModeDropFolder Mode = "drop_folder"
)

// ParseMode coerces the raw settings-table string into a known Mode. Unknown
// or empty values fall through to ModeOff — treating a fresh install or a
// typoed value as "leave it alone" is strictly safer than "try calibredb and
// log warnings".
//
// The string is case-insensitive so the UI can round-trip "Off" / "Drop
// folder" labels without the backend caring about capitalisation. Legacy
// "true"/"false" values (from the v0.8.0 `calibre.enabled` boolean) are
// deliberately NOT handled here: migration 011 rewrites them into proper
// mode values at upgrade time, so by the time this function runs the
// settings table always holds one of the three canonical strings (or empty
// on a brand-new install before the migration seeds defaults).
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(ModeCalibredb):
		return ModeCalibredb
	case string(ModeDropFolder):
		return ModeDropFolder
	default:
		return ModeOff
	}
}

// Valid reports whether m is one of the three canonical modes. The settings
// endpoint uses this to reject PUTs that would leave the integration in an
// uninterpretable state.
func (m Mode) Valid() bool {
	switch m {
	case ModeOff, ModeCalibredb, ModeDropFolder:
		return true
	}
	return false
}

// String implements fmt.Stringer so slog output reads "mode=drop_folder"
// rather than "mode=calibre.Mode("drop_folder")".
func (m Mode) String() string { return string(m) }
