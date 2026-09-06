package decision

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// --- QualityAllowed ---

// QualityAllowed rejects releases whose format is not in the quality profile's
// allowed format list. An empty allowed list means "allow all".
//
// A release whose format could not be determined from its title always passes.
// ParseRelease only sets Format when it finds a known token in the release
// name, and plenty of legitimate releases carry none — Usenet titles in
// particular are often just "Author - Title (Year)". Rejecting those would turn
// this filter into a near-total grab blackout the moment a user ticked any box,
// which is the opposite of what the UI promises. The filter can only speak to
// formats it can actually see.
//
// A release is judged only against the profile items of its own media type, and
// passes when the profile lists none of them (#2307). models.QualityProfile has
// no media-type column and quality_profile_id lives on authors, so an author who
// tracks both formats gets exactly one profile: without this narrowing, a
// profile listing m4b/mp3/flac would reject every epub with "format \"epub\" not
// in quality profile", which is the spec asserting something it was never asked.
// The consequences differed by path and the quiet one was the worse of the two —
// interactive search only annotates, so Grab stayed enabled, but the scheduler
// uses the same spec as a hard filter, so an ebook under an audiobook-only
// profile could never be auto-grabbed at all. Narrowing here rather than at the
// call sites is deliberate: the scheduler, interactive search and the importer's
// format check all share this one spec and must not drift apart.
//
// A profile that deliberately mixes both media types has items in both buckets,
// so both narrow to a non-empty set and behaviour is exactly what it was.
type QualityAllowed struct {
	Profile *models.QualityProfile
}

func (s QualityAllowed) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	if s.Profile == nil || len(s.Profile.Items) == 0 {
		return true, ""
	}
	if r.Format == "" {
		return true, ""
	}

	// indexer.MediaTypeForFormat is the single source of truth for the token →
	// media-type mapping; a second copy of the token lists here is exactly the
	// drift that function's doc comment warns about. It returns "" for a token
	// Bindery does not recognise, on either side, in which case there is nothing
	// to narrow by and every item is considered, as before.
	mediaType := indexer.MediaTypeForFormat(r.Format)

	sawSameMediaType := false
	for _, item := range s.Profile.Items {
		if mediaType != "" && indexer.MediaTypeForFormat(item.Quality) != mediaType {
			continue
		}
		// Listed but unticked still counts as an opinion: a profile with epub
		// explicitly turned off has been asked about ebooks and said no.
		sawSameMediaType = true
		if item.Allowed && strings.EqualFold(item.Quality, r.Format) {
			return true, ""
		}
	}
	if !sawSameMediaType {
		return true, ""
	}
	return false, fmt.Sprintf("format %q not in quality profile %q", r.Format, s.Profile.Name)
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

// --- FreeleechOnly ---

// RejectionFreeleechHold prefixes every rejection produced by
// FreeleechOnlySpec. The scheduler matches on this prefix to decide that a
// release should be parked in pending_releases for manual approval rather than
// discarded, so keep the two in step (the delay profile uses the same
// substring-matching convention).
const RejectionFreeleechHold = "freeleech hold"

// FreeleechOnlySpec restricts AUTOMATIC grabs from selected indexers to
// releases that cost no download ratio. It exists for private trackers where a
// user near a ratio floor would otherwise have to restrict the whole indexer to
// Freeleech/VIP upstream — which also hides normal releases from interactive
// search, where they would happily pay the cost on a book they actually want.
//
// Gating here instead means the scheduler (and bulk multi-book search, which
// has no picker and is pure fire-and-forget) stays ratio-safe, while rejected
// releases are held for manual approval rather than hidden. Interactive search
// builds its own specification set and must NOT include this spec.
//
// It must be part of the same DecisionMaker the scheduler uses to re-evaluate
// pending releases: that path re-grabs anything that starts passing, so a
// release held here has to keep failing until the user approves it by hand.
type FreeleechOnlySpec struct {
	// IndexerIDs is the set of indexer ids with the policy enabled. Releases
	// from any other indexer pass untouched.
	IndexerIDs map[int64]bool
}

func (s FreeleechOnlySpec) IsSatisfiedBy(r Release, _ models.Book) (bool, string) {
	if len(s.IndexerIDs) == 0 || !s.IndexerIDs[r.IndexerID] {
		return true, ""
	}
	// Usenet has no ratio economy — downloadvolumefactor is a torznab concept
	// and is never reported. Holding usenet releases would be pure noise if the
	// policy were switched on for a newznab indexer by mistake.
	if r.Protocol != "torrent" {
		return true, ""
	}
	if r.DownloadVolumeFactor == nil {
		// Deliberately fail closed. This is a ratio-protection policy: assuming
		// an unreported release is free would silently spend the ratio the user
		// asked us to protect. Holding it is visible and recoverable — the item
		// shows up in Pending with this reason and can be approved or the
		// policy turned off.
		return false, RejectionFreeleechHold + ": indexer did not report downloadvolumefactor"
	}
	if *r.DownloadVolumeFactor == 0 {
		return true, ""
	}
	// Anything above zero costs ratio, including half-leech (0.5).
	return false, fmt.Sprintf("%s: costs %g× ratio (not freeleech)", RejectionFreeleechHold, *r.DownloadVolumeFactor)
}

// --- MultiBookPack ---

// RejectionMultiBookPack prefixes every rejection produced by
// MultiBookPackSpec, so the scheduler can recognise one without a typed flag
// (the same convention RejectionFreeleechHold uses).
const RejectionMultiBookPack = "multi-book pack"

// MultiBookPackSpec rejects releases that name themselves as several books —
// "Books 1-4", a box set, an omnibus — when the search was for one book.
//
// This is not a quality preference. A download row carries one BookID and the
// importer computes one destination from it, so there is no outcome in which
// grabbing a four-book pack for one book record is right: the files either all
// land in one book's folder or the import fails. Bindery has no pack splitter,
// so the only correct move is not to select it.
//
// Constructed only on the automatic path in the scheduler. Interactive search
// deliberately does not build it, exactly as FreeleechOnlySpec is left out
// there: a user who can see the release name and wants the pack anyway should
// still be able to see it and grab it. The importer carries its own guard for
// that case (#2276).
type MultiBookPackSpec struct{}

func (MultiBookPackSpec) IsSatisfiedBy(r Release, book models.Book) (bool, string) {
	marker := indexer.MultiBookPackMarker(r.Title)
	if marker == "" {
		return true, ""
	}
	// The wanted book is itself a bundle. Someone tracking "The Lord of the
	// Rings Omnibus" or a box set edition as a single book record wants
	// exactly the release this spec would otherwise refuse, and for them the
	// one-destination problem does not arise: the pack IS the book.
	if indexer.MultiBookPackMarker(book.Title) != "" {
		return true, ""
	}
	return false, fmt.Sprintf("%s: release is titled %q, and a download is linked to one book — Bindery cannot split a pack across book records", RejectionMultiBookPack, marker)
}
