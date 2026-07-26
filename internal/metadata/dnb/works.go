package dnb

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/models"
)

// collapseAuthorWorks merges the several catalogue records DNB returns for the
// same physical book into one, while keeping genuinely distinct volumes apart.
// Unlike OpenLibrary, DNB has no "work" abstraction: every edition, printing,
// and volume is its own bibliographic record, each with a slightly different 245
// title. A German multi-volume translation is the tricky case — "Die Furcht des
// Weisen" ships as two Bände, and DNB holds several records for each:
//
//	Die Furcht des Weisen                                     (Teil 1 via 245 $n)
//	Die Furcht des Weisen (1): … Band 1                       (Band 1)
//	Die Furcht des Weisen/Band 1: …                           (Band 1)
//	Die Furcht des Weisen (2): … Band 2                       (Band 2)
//	Die Furcht des Weisen/Band 2: …                           (Band 2)
//
// Grouping by (work title, volume number) collapses the printings of each Band
// to one row but preserves Band 1 and Band 2 as separate books — so the two-part
// work surfaces as two entries, not five and not one (#1574). Each vols[i] is the
// volume number for books[i] (0 = none), derived by the caller from the title
// marker or MARC 245 $n.
//
// When a work has numbered volumes, its combined/undesignated edition (vol 0,
// e.g. the one-volume "Die Furcht des Weisen : Roman" published alongside the two
// Bände) is dropped: it is the same content as the volumes together and would
// otherwise show up as a redundant third row. A work with no numbered volumes
// keeps its single record untouched, so a standalone title carrying a real
// subtitle is never rewritten. Order follows first appearance so the catalogue's
// relevance ordering survives.
func collapseAuthorWorks(books []models.Book, vols []int) []models.Book {
	// A work title that has at least one numbered volume; its vol-0 combined
	// edition is redundant and dropped below.
	numbered := make(map[string]bool, len(books))
	for i, b := range books {
		if vols[i] > 0 {
			numbered[workDedupKey(b.Title)] = true
		}
	}
	order := make([]string, 0, len(books))
	groups := make(map[string][]int, len(books))
	for i, b := range books {
		work := workDedupKey(b.Title)
		if vols[i] == 0 && numbered[work] {
			continue
		}
		key := work + "#" + strconv.Itoa(vols[i])
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}
	result := make([]models.Book, 0, len(order))
	for _, key := range order {
		result = append(result, collapseGroup(books, vols, groups[key]))
	}
	return result
}

// collapseGroup reduces the records sharing one (work, volume) key to a single
// book. When the group is a real collapse (more than one record) or the record
// carries a volume number, the title is rebuilt as the clean work title plus the
// volume ("Die Furcht des Weisen 1"), matching the form the issue asked for. A
// lone record with no volume is returned untouched so its subtitle survives. The
// representative prefers a record that was already a plain work title so a later
// single-record GetBook refresh returns a matching title; the remaining records
// fill gaps (date, description, cover, language, ISBNs).
func collapseGroup(books []models.Book, vols []int, idxs []int) models.Book {
	baseIdx := idxs[0]
	for _, i := range idxs {
		if isPlainWorkRecord(books[i].Title) {
			baseIdx = i
			break
		}
	}
	rep := books[baseIdx]
	vol := vols[baseIdx]
	if len(idxs) == 1 && vol == 0 {
		return rep
	}
	title := workBaseTitle(rep.Title)
	if vol > 0 {
		title += " " + strconv.Itoa(vol)
	}
	rep.Title = title
	rep.SortTitle = title
	for _, i := range idxs {
		if i == baseIdx {
			continue
		}
		rep = fillWorkFields(rep, books[i])
	}
	return rep
}

// fillWorkFields fills gaps in dst from src without overwriting existing values,
// keeps the earlier release date, and unions the edition/ISBN lists.
func fillWorkFields(dst, src models.Book) models.Book {
	if dst.Description == "" {
		dst.Description = src.Description
	}
	if dst.ImageURL == "" {
		dst.ImageURL = src.ImageURL
	}
	if dst.Language == "" {
		dst.Language = src.Language
	}
	if dst.Author == nil {
		dst.Author = src.Author
	}
	if src.ReleaseDate != nil && (dst.ReleaseDate == nil || src.ReleaseDate.Before(*dst.ReleaseDate)) {
		dst.ReleaseDate = src.ReleaseDate
	}
	dst.Editions = append(dst.Editions, src.Editions...)
	return dst
}

// collectSeriesTitles returns the set of work keys for every series title the
// records declare in MARC 490 $a (series statement) and 800 $t (series added
// entry). A record whose own title matches one of these is a whole-series /
// omnibus product (e.g. the complete-series audiobook "Die Königsmörder-Chronik
// : vollständige Lesung"), not one of the author's individual books.
func collectSeriesTitles(records []marcRecord) map[string]bool {
	titles := make(map[string]bool)
	for _, rec := range records {
		for _, s := range rec.subfieldAll("490", "a") {
			if k := workDedupKey(marcClean(s)); k != "" {
				titles[k] = true
			}
		}
		for _, s := range rec.subfieldAll("800", "t") {
			if k := workDedupKey(marcClean(s)); k != "" {
				titles[k] = true
			}
		}
	}
	return titles
}

// seriesPartTitle extracts the individual work title from MARC 245 $p and the
// volume number from the $n that follows it. Returns ok=false when the record
// has no $p (the common single-work case), so the caller keeps the assembled
// title. The pre-$p $n (the record's position within the collective series, e.g.
// "Tag 2") is deliberately ignored — it is not a volume of the individual work.
func seriesPartTitle(rec marcRecord) (string, int, bool) {
	for _, df := range rec.DataFields {
		if df.Tag != "245" {
			continue
		}
		title, vol, seenP := "", 0, false
		for _, sf := range df.Subfields {
			switch sf.Code {
			case "p":
				if !seenP {
					if t := cleanPartTitle(sf.Value); t != "" {
						title, seenP = t, true
					}
				}
			case "n":
				if seenP && vol == 0 {
					vol = parseVolumeToken(sf.Value)
				}
			}
		}
		return title, vol, seenP
	}
	return "", 0, false
}

// cleanPartTitle normalizes a MARC 245 $p value to the bare work title, dropping
// the statement-of-responsibility ("/ …"), a bracketed edition note (": [Mit
// Bonuskapitel …]"), and promotional chains ("| …").
func cleanPartTitle(s string) string {
	s = marcClean(s)
	for _, sep := range []string{" / ", " : ", " | "} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(strings.Trim(s, " /:.,;"))
}

// volumeMarkerRe matches a trailing volume designator on a DNB main title:
// "(1)", "/Band 1", " Band 1", " Bd. 1", "/Teil 2", " Teil 2". The number may be
// arabic or a short roman numeral; the whole marker sits at the end of the title.
// The two capture groups hold the number from the parenthetical and worded forms.
var volumeMarkerRe = regexp.MustCompile(`(?i)\s*[/,]?\s*(?:\((\d+|[ivxlcdm]+)\)|(?:band|bd\.?|teil|vol\.?|volume)\s*\.?\s*(\d+|[ivxlcdm]+))\s*$`)

// dnbVolumeNumber returns the volume/part number for a DNB record, or 0 when the
// record is not part of a numbered multi-volume set. The title marker wins (it
// is the most specific), falling back to MARC 245 $n ("Teil 1.", "Band 2") — the
// subfield DNB uses for the plain-titled first volume that carries no marker in
// its 245 $a.
func dnbVolumeNumber(rec marcRecord, title string) int {
	if v := titleVolumeNumber(title); v > 0 {
		return v
	}
	return parseVolumeToken(rec.subfield("245", "n"))
}

// titleVolumeNumber extracts the trailing volume number from a record title,
// looking only at the main-title head (before the subtitle colon).
func titleVolumeNumber(title string) int {
	head := title
	if i := strings.Index(head, ":"); i > 0 {
		head = head[:i]
	}
	m := volumeMarkerRe.FindStringSubmatch(strings.TrimSpace(head))
	if m == nil {
		return 0
	}
	if m[1] != "" {
		return parseVolumeToken(m[1])
	}
	return parseVolumeToken(m[2])
}

// parseVolumeToken parses a volume token that may be a bare arabic number, a
// roman numeral, or a worded value like "Teil 1." Returns 0 when no number is
// present.
func parseVolumeToken(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, ok := firstArabicNumber(s); ok {
		return n
	}
	return romanToInt(s)
}

func firstArabicNumber(s string) (int, bool) {
	start := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if start < 0 {
		return 0, false
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[start:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

func romanToInt(s string) int {
	vals := map[rune]int{'i': 1, 'v': 5, 'x': 10, 'l': 50, 'c': 100, 'd': 500, 'm': 1000}
	total, prev := 0, 0
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		v, ok := vals[r]
		if !ok {
			return 0
		}
		if v > prev && prev != 0 {
			total += v - 2*prev
		} else {
			total += v
		}
		prev = v
	}
	return total
}

// workBaseTitle reduces a DNB record title to the underlying work title: it drops
// the subtitle (everything after the first colon, which cleanDNBTitle assembles
// from MARC 245 $b) and strips a trailing volume marker. Applied repeatedly so
// "Die Furcht des Weisen (1)" and "Die Furcht des Weisen/Band 1" both reduce to
// "Die Furcht des Weisen".
func workBaseTitle(title string) string {
	head := title
	if i := strings.Index(head, ":"); i > 0 {
		head = head[:i]
	}
	head = strings.TrimSpace(head)
	for {
		stripped := strings.TrimRight(strings.TrimSpace(volumeMarkerRe.ReplaceAllString(head, "")), " /,-–—")
		if stripped == head || stripped == "" {
			break
		}
		head = stripped
	}
	if head == "" {
		return strings.TrimSpace(title)
	}
	return head
}

// workDedupKey is the normalized grouping key for workBaseTitle: lowercased,
// NFC-folded, punctuation removed, and whitespace collapsed, so titles that
// differ only in case, spacing, or diacritic encoding group together.
func workDedupKey(title string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFC.String(strings.ToLower(workBaseTitle(title))) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

// isPlainWorkRecord reports whether a title is already a bare work title — no
// subtitle and no trailing volume marker — i.e. workBaseTitle leaves it
// unchanged. Such records are preferred as the surviving representative.
func isPlainWorkRecord(title string) bool {
	return strings.EqualFold(strings.TrimSpace(title), workBaseTitle(title))
}
