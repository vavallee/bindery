package importer

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dhowden/tag"
)

// audioTagExtensions lists audio formats whose embedded tags we try to read
// during library scans. Extensions outside this set are skipped — the scan
// falls back to filename parsing without paying the open/seek cost.
var audioTagExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".m4b": true,
	".flac": true, ".ogg": true, ".opus": true,
}

// AudioTags is the subset of embedded audio metadata the library scanner
// uses to match audiobook files to book records.
type AudioTags struct {
	Title  string
	Author string
	ASIN   string
}

// IsAudioTagFile reports whether path has an extension we attempt to read
// embedded tags from.
func IsAudioTagFile(path string) bool {
	return audioTagExtensions[strings.ToLower(filepath.Ext(path))]
}

// ReadAudioTags extracts title, author, and (if present) Amazon ASIN from
// an audio file's embedded ID3 / iTunes atoms. Callers should fall back to
// filename-based parsing on error.
func ReadAudioTags(path string) (AudioTags, error) {
	f, err := os.Open(path)
	if err != nil {
		return AudioTags{}, err
	}
	defer f.Close()
	return readAudioTagsFrom(f)
}

func readAudioTagsFrom(r io.ReadSeeker) (AudioTags, error) {
	m, err := tag.ReadFrom(r)
	if err != nil {
		return AudioTags{}, err
	}
	return AudioTags{
		Title:  strings.TrimSpace(m.Title()),
		Author: pickAudioAuthor(m),
		ASIN:   pickAudioASIN(m.Raw()),
	}, nil
}

// narratorCreditRe matches the leading "Read by" / "Narrated by" credit that
// chapter-split audiobook releases frequently store in the Artist tag instead
// of the author. Such a value is a narrator, not a book author, so treating it
// as the author poisons library-scan matching (#1239).
var narratorCreditRe = regexp.MustCompile(`(?i)^(read|narrated|performed|presented|told)\s+by\b`)

// isNarratorCredit reports whether s looks like a narrator credit ("Read by
// Nigel Planer") rather than an author name.
func isNarratorCredit(s string) bool {
	return narratorCreditRe.MatchString(strings.TrimSpace(s))
}

// pickAudioAuthor prefers the Artist tag (which audiobook tooling
// conventionally uses for the book's author) and falls back to AlbumArtist
// or Composer for files that leave Artist empty. Narrator-credit values
// ("Read by …") are skipped rather than returned as the author (#1239); when
// every candidate is empty or a narrator credit, the caller keeps whatever the
// folder hierarchy resolved instead.
func pickAudioAuthor(m tag.Metadata) string {
	for _, candidate := range []string{m.Artist(), m.AlbumArtist(), m.Composer()} {
		s := strings.TrimSpace(candidate)
		if s == "" || isNarratorCredit(s) {
			continue
		}
		return s
	}
	return ""
}

// pickAudioASIN searches the raw tag map for an Amazon ASIN. MP4 freeform
// atoms from com.apple.iTunes surface under the sub-atom name directly (e.g.
// "ASIN"); ID3v2 encoders use a TXXX user-defined text frame with
// Description="ASIN". dhowden/tag may suffix duplicate frame names with
// "_0"/"_1"/... when more than one is present, so we match by prefix.
func pickAudioASIN(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	for _, k := range []string{"ASIN", "asin", "audible_asin", "AUDIBLE_ASIN"} {
		if s, ok := raw[k].(string); ok && isASIN(s) {
			return s
		}
	}
	for k, v := range raw {
		if !strings.HasPrefix(k, "TXXX") && !strings.HasPrefix(k, "TXX") {
			continue
		}
		c, ok := v.(*tag.Comm)
		if !ok {
			continue
		}
		desc := strings.ToUpper(strings.TrimSpace(c.Description))
		if desc != "ASIN" && desc != "AUDIBLE_ASIN" {
			continue
		}
		if s := strings.TrimSpace(c.Text); isASIN(s) {
			return s
		}
	}
	return ""
}

// isASIN matches Amazon's 10-char ASIN format: a leading 'B' followed by
// nine uppercase alphanumerics. Narrower than parser.go's asinRe because
// tag values sometimes contain surrounding whitespace or junk and we only
// want exact matches here.
func isASIN(s string) bool {
	if len(s) != 10 || s[0] != 'B' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
