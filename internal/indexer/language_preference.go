package indexer

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vavallee/bindery/internal/models"
)

// languageMatchBonus is what a release earns in scoreResult when its name is
// tagged with a language the author's metadata profile allows (#4130).
//
// The filter (SplitByAllowedLanguages) can only say no to a release that is
// tagged with a language outside the profile. An untagged release always
// passes, and untagged is what most English releases are, so under a German
// only profile the English release kept outranking the tagged German ones on
// format, retail and year. The term is the positive half of the same setting.
//
// 450 is deliberately larger than one format step (100) and the RETAIL bonus
// (50) together, so a tagged release beats an untagged one of better format.
// On the built in QualityRank path the widest gap inside one media type is mp3
// (7) to flac (10), 300, and with the capped grabs, seeders, size and year
// terms (about 65) 450 clears the worst case of 415. A quality profile with an
// order of its own scores a format n..1 times 100 for n entries, so there the
// gap can be larger and a tagged release beats a better format up to four
// steps higher (450 > 400); that is the intent, a wrong language file is
// worth less than any format of the right one. The bonus stays under the 500
// media type mismatch penalty so a wrong kind of release does not win on
// language alone.
//
// This applies to every install whose profile lists a language, which includes
// the default profile (an empty list is stored as "eng"): a release tagged
// English now beats an untagged one of better format there too.
const languageMatchBonus = 450.0

// allowedLanguageSet reduces a profile's allowed language list to the set the
// filter and the ranker both read. restricted is false for an empty list or
// one that contains "any", the two spellings of "no restriction".
func allowedLanguageSet(allowed []string) (set map[string]bool, restricted bool) {
	if len(allowed) == 0 {
		return nil, false
	}
	set = make(map[string]bool, len(allowed))
	for _, code := range allowed {
		if strings.ToLower(strings.TrimSpace(code)) == "any" {
			return nil, false
		}
		code = models.NormalizeLanguageCode(code)
		if code == "" {
			continue
		}
		set[code] = true
	}
	return set, true
}

// releaseDetectedLanguages is every language the release name is tagged with,
// by the same three detectors the filter runs. The wanted book's own title and
// author are masked out of the text the word and glued detectors read, so a
// German book called "English Lessons", or a book by an author named German,
// is not read as tagged with that language just for being itself. The scene
// suffix detector only reads the metadata after the format marker, where the
// title cannot be, so it reads the raw name.
func releaseDetectedLanguages(title string, c MatchCriteria) []string {
	norm := NormalizeRelease(title)
	raw := title
	for _, own := range append([]string{c.Title, c.Author}, c.AuthorAliases...) {
		if strings.TrimSpace(own) == "" {
			continue
		}
		norm = maskOwnName(norm, NormalizeRelease(own))
		raw = maskOwnNameRaw(raw, own)
	}
	return append(append(releaseLanguageCodes(norm), sceneLanguageCodes(title)...), gluedLanguageCodes(raw)...)
}

// maskOwnName blanks whole word occurrences of an already normalized name in
// an already normalized release name.
func maskOwnName(norm, own string) string {
	if own == "" {
		return norm
	}
	re := regexp.MustCompile(`(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(own) + `($|[^\p{L}\p{N}])`)
	for {
		next := re.ReplaceAllString(norm, " ")
		if next == norm {
			return norm
		}
		norm = next
	}
}

// maskOwnNameRaw blanks case insensitive occurrences of name in the raw
// release name, for the glued detector, which needs the original casing of
// everything else.
func maskOwnNameRaw(raw, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return raw
	}
	// A short name ("Man", "IT") is a common substring of other words and of
	// scene tags, so it only matches as a whole word; blanking it inside
	// "GERMAN" or "DeutschUngekürzt" would lose a real tag.
	if utf8.RuneCountInString(name) < 4 {
		re := regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(name) + `($|[^\p{L}\p{N}])`)
		for {
			next := re.ReplaceAllString(raw, "$1 $2")
			if next == raw {
				return raw
			}
			raw = next
		}
	}
	return regexp.MustCompile(`(?i)`+regexp.QuoteMeta(name)).ReplaceAllString(raw, " ")
}

// languageScore is the language term of scoreResult. It is zero unless the
// profile restricts languages, zero for a release with no detectable language,
// and zero for one tagged with anything outside the list (the filter drops
// those; a caller that kept one, such as the interactive search's dropped
// list, ranks it without a bonus). Every detected language has to be allowed,
// the same rule the filter applies, so a release tagged for two languages
// earns the bonus only when the profile allows both.
//
// An exact ASIN or ISBN match also earns the bonus, provided nothing in the
// name is tagged outside the list. The identifier pins the wanted edition,
// whose language is the one the profile asks for, so an untagged release that
// carries the book's own ASIN must not lose to a tagged sibling: the bonus is
// larger than the ASIN (+250) and ISBN (+200) terms, and without this a
// language only match would outrank an identifier match.
func languageScore(title, releaseISBN string, c MatchCriteria) float64 {
	set, restricted := allowedLanguageSet(c.AllowedLanguages)
	if !restricted {
		return 0
	}
	identified := (c.ISBN != "" && releaseISBN != "" && strings.EqualFold(releaseISBN, c.ISBN)) ||
		(c.ASIN != "" && strings.Contains(strings.ToUpper(title), strings.ToUpper(c.ASIN)))
	codes := releaseDetectedLanguages(title, c)
	if len(codes) == 0 && !identified {
		return 0
	}
	for _, code := range codes {
		if !set[code] {
			return 0
		}
	}
	return languageMatchBonus
}
