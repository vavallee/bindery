package calibre

import "strings"

// Mode selects which Calibre-integration flow the importer runs after a
// successful Bindery import.
type Mode string

const (
	// ModeOff disables Calibre integration. Imports behave as if no Calibre
	// library were configured: no external call, no mutation on the book row.
	ModeOff Mode = "off"

	// ModeCalibredb shells out to `calibredb add --with-library <path>`.
	// Requires calibredb to be accessible from the Bindery process (same
	// container or a shared volume mount).
	ModeCalibredb Mode = "calibredb"
)

// ParseMode coerces the raw settings-table string into a known Mode. Unknown
// or empty values fall through to ModeOff — treating a fresh install or a
// typoed value as "leave it alone" is strictly safer than "try calibredb and
// log warnings".
//
// The string is case-insensitive so the UI can round-trip "Off" / "calibredb"
// labels without the backend caring about capitalisation. Legacy "true"/"false"
// values (from the v0.8.0 `calibre.enabled` boolean) are deliberately NOT
// handled here: migration 011 rewrites them into proper mode values at upgrade
// time. Legacy "drop_folder" values are treated as ModeOff (the mode was
// removed in v0.17.0 — see docs/ROADMAP.md).
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(ModeCalibredb):
		return ModeCalibredb
	default:
		return ModeOff
	}
}

// Valid reports whether m is one of the canonical modes. The settings
// endpoint uses this to reject PUTs that would leave the integration in an
// uninterpretable state.
func (m Mode) Valid() bool {
	switch m {
	case ModeOff, ModeCalibredb:
		return true
	}
	return false
}

// String implements fmt.Stringer so slog output reads "mode=calibredb"
// rather than "mode=calibre.Mode("calibredb")".
func (m Mode) String() string { return string(m) }
