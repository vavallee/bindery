package decision

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// --- QualityAllowed ---

// QualityAllowed rejects releases whose format is not in the quality profile's
// allowed format list. An empty allowed list means "allow all".
type QualityAllowed struct {
	Profile *models.QualityProfile
}

func (s QualityAllowed) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	if s.Profile == nil || len(s.Profile.Items) == 0 {
		return true, ""
	}
	for _, item := range s.Profile.Items {
		if item.Allowed && strings.EqualFold(item.Quality, r.Format) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("format %q not in quality profile %q", r.Format, s.Profile.Name)
}

// --- QualityCutoff ---

// QualityCutoff rejects releases for books that already have a file at or
// above the quality profile's cutoff format. This prevents grabbing a worse
// copy of a book the user already owns. CurrentQuality is the existing file's
// format (empty string when unknown or no file exists).
type QualityCutoff struct {
	Profile        *models.QualityProfile
	CurrentQuality string
}

func (s QualityCutoff) IsSatisfiedBy(r Release, book models.Book) (bool, string) {
	if s.Profile == nil || s.Profile.Cutoff == "" {
		return true, ""
	}
	if book.FilePath == "" || s.CurrentQuality == "" {
		return true, "" // no file yet — grab allowed
	}
	itemQuality := func(name string) int {
		for i, item := range s.Profile.Items {
			if strings.EqualFold(item.Quality, name) {
				return i
			}
		}
		return -1
	}
	cutoffIdx := itemQuality(s.Profile.Cutoff)
	currentIdx := itemQuality(s.CurrentQuality)
	if cutoffIdx < 0 || currentIdx < 0 {
		return true, "" // unknown format — let it through
	}
	// Higher index = better quality in the profile's ordered list.
	if currentIdx >= cutoffIdx {
		return false, fmt.Sprintf("book already has %q which meets quality cutoff %q", s.CurrentQuality, s.Profile.Cutoff)
	}
	return true, ""
}

// --- DelayProfile ---

// DelayProfileSpec rejects releases that haven't aged past the configured
// delay for their protocol. A nil profile skips the check.
type DelayProfileSpec struct {
	Profile *models.DelayProfile
}

func (s DelayProfileSpec) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	if s.Profile == nil {
		return true, ""
	}
	switch r.Protocol {
	case "usenet":
		if !s.Profile.EnableUsenet {
			return false, "usenet disabled in delay profile"
		}
		if s.Profile.UsenetDelay > 0 && r.AgeMinutes < s.Profile.UsenetDelay {
			remaining := s.Profile.UsenetDelay - r.AgeMinutes
			return false, fmt.Sprintf("usenet delay not met — %d min remaining", remaining)
		}
	case "torrent":
		if !s.Profile.EnableTorrent {
			return false, "torrent disabled in delay profile"
		}
		if s.Profile.TorrentDelay > 0 && r.AgeMinutes < s.Profile.TorrentDelay {
			remaining := s.Profile.TorrentDelay - r.AgeMinutes
			return false, fmt.Sprintf("torrent delay not met — %d min remaining", remaining)
		}
	}
	return true, ""
}

// --- Blocklisted ---

// BlocklistedSpec rejects releases whose GUID appears in the blocklist.
type BlocklistedSpec struct {
	GUIDs map[string]struct{}
}

// NewBlocklistedSpec builds a BlocklistedSpec from a slice of blocked GUIDs.
func NewBlocklistedSpec(entries []models.BlocklistEntry) *BlocklistedSpec {
	m := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		m[e.GUID] = struct{}{}
	}
	return &BlocklistedSpec{GUIDs: m}
}

func (s *BlocklistedSpec) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	if _, blocked := s.GUIDs[r.GUID]; blocked {
		return false, "release is blocklisted"
	}
	return true, ""
}

// --- AlreadyImported ---

// AlreadyImportedSpec rejects releases for books whose corresponding format is
// already on disk. The check is per-format: for a dual-format book
// (media_type=both) that has only the audiobook imported, ebook releases must
// stay grabbable and vice versa. The release's MediaType ("ebook"/"audiobook",
// populated for dual-format searches) selects which path column to consult.
// When MediaType is empty — single-format book searches don't tag results — it
// falls back to the legacy whole-book FilePath check.
type AlreadyImportedSpec struct{}

func (AlreadyImportedSpec) IsSatisfiedBy(r Release, book models.Book) (bool, string) {
	switch r.MediaType {
	case models.MediaTypeEbook:
		if book.EbookFilePath != "" {
			return false, "ebook already imported"
		}
		return true, ""
	case models.MediaTypeAudiobook:
		if book.AudiobookFilePath != "" {
			return false, "audiobook already imported"
		}
		return true, ""
	default:
		if book.FilePath != "" {
			return false, "book already imported"
		}
		return true, ""
	}
}

// --- SizeLimit ---

// SizeLimitSpec rejects releases outside a configured byte range.
// Zero values for Min/Max mean "no limit".
type SizeLimitSpec struct {
	MinBytes int64
	MaxBytes int64
}

func (s SizeLimitSpec) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	if s.MinBytes > 0 && r.Size < s.MinBytes {
		return false, fmt.Sprintf("release size %d B below minimum %d B", r.Size, s.MinBytes)
	}
	if s.MaxBytes > 0 && r.Size > s.MaxBytes {
		return false, fmt.Sprintf("release size %d B above maximum %d B", r.Size, s.MaxBytes)
	}
	return true, ""
}

// --- LanguageFilter ---

// LanguageFilterSpec rejects releases whose language tag is explicitly set
// and does not appear in the allowed list. Empty language on the release
// always passes (data not available). Empty allowedLangs means no filter.
type LanguageFilterSpec struct {
	AllowedLangs []string // ISO 639-1 or ISO 639-3, e.g. ["en", "eng"]
}

func (s LanguageFilterSpec) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	if len(s.AllowedLangs) == 0 || r.Language == "" {
		return true, ""
	}
	lang := strings.ToLower(r.Language)
	for _, a := range s.AllowedLangs {
		if strings.ToLower(a) == lang {
			return true, ""
		}
	}
	return false, fmt.Sprintf("release language %q not in allowed list", r.Language)
}

// --- CustomFormatScore ---

// CustomFormatScoreSpec does not reject releases — it annotates the Release
// with a cumulative score from all matching custom formats. Since it never
// returns false, it must run after blocking specs.
type CustomFormatScoreSpec struct {
	Formats []models.CustomFormat
}

func (s *CustomFormatScoreSpec) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	return true, "" // scoring only; never rejects
}

// Score computes the total custom-format score for a release.
// A format matches when all of its conditions are satisfied.
func (s *CustomFormatScoreSpec) Score(r Release) int {
	var total int
	for i, cf := range s.Formats {
		if s.matchesAll(r, s.Formats[i].Conditions) {
			total += i + 1 // simple weight: earlier formats score higher
			_ = cf
		}
	}
	return total
}

func (s *CustomFormatScoreSpec) matchesAll(r Release, conditions []models.CustomCondition) bool {
	for _, c := range conditions {
		matched := s.matchCondition(r, c)
		if c.Negate {
			matched = !matched
		}
		if !matched {
			return false
		}
	}
	return true
}

func (s *CustomFormatScoreSpec) matchCondition(r Release, c models.CustomCondition) bool {
	switch c.Type {
	case "releaseTitle":
		re, err := regexp.Compile("(?i)" + c.Pattern)
		if err != nil {
			return false
		}
		return re.MatchString(r.Title)
	case "releaseGroup":
		re, err := regexp.Compile("(?i)" + c.Pattern)
		if err != nil {
			return false
		}
		return re.MatchString(r.Title)
	case "format":
		return strings.EqualFold(r.Format, c.Pattern)
	case "protocol":
		return strings.EqualFold(r.Protocol, c.Pattern)
	default:
		return false
	}
}
