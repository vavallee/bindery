package api

import (
	"strings"

	"github.com/vavallee/bindery/internal/notifier"
)

// Length caps, in runes, for text a request carries. Titles and names come
// from a metadata provider, and OpenLibrary is publicly editable, so they are
// capped and cleaned like any outside input.
const (
	requestTitleMaxRunes    = 300
	requestAuthorMaxRunes   = 200
	requestUsernameMaxRunes = 64
	requestReasonMaxRunes   = 500
	requestForeignIDMaxLen  = 128
)

// cleanRequestText is notifier.CleanText: control and invisible characters
// stripped, whitespace collapsed, capped. Stored text goes through it; text
// sent to a webhook goes through notifier.SafeText as well.
func cleanRequestText(s string, maxRunes int) string {
	return notifier.CleanText(s, maxRunes)
}

// validForeignID reports whether id is a plausible provider id: non empty,
// bounded, drawn from letters, digits and ":_.-/", and without "..". Every
// provider id Bindery issues fits: OL123W, hc:slug, dnb:123, gb:abc_d,
// audnex:B0..., ranobedb:12, nb:author:..., and the BnF ARK form
// bnf:ark:/12148/cb12345 that needs the slash. The id is later spliced into
// provider URLs and query strings, so "?", "#", "%", quotes and spaces are
// refused here and the clients escape what they splice.
func validForeignID(id string) bool {
	if id == "" || len(id) > requestForeignIDMaxLen || strings.Contains(id, "..") {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == ':', c == '_', c == '.', c == '-', c == '/':
		default:
			return false
		}
	}
	return true
}
