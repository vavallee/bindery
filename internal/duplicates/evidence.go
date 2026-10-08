package duplicates

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/seriesmatch"
)

// This file is the evidence layer of the duplicate review (#2999). Detection
// (Scan, Detect) decides which rows group; nothing here changes that. Evidence
// is what a person needs to decide which row of a group to keep: which row
// has files, and whether the rows agree on the facts that identify a book
// (ISBN, ASIN, series position) or disagree on them (series position, year,
// language). Everything is built from data Bindery already stores; there are
// no provider calls, and nothing here writes.

// maxShownISBNs caps the ISBNs a member carries in its JSON. A work row can
// hold many editions; the comparison uses all of them, the row only needs to
// show a few.
const maxShownISBNs = 5

// maxSignalValues caps the shared values one agreement signal lists.
const maxSignalValues = 3

// FileRef is one file Bindery records for a book: its kind ("ebook" or
// "audiobook", the book_files.format column) and its path.
type FileRef struct {
	Kind string
	Path string
}

// FileEvidence is one file a member has, as the review shows it: the kind and
// the file format taken from the extension ("epub", "m4b"). Format is empty
// when the path has no usable extension, which is normal for an audiobook
// stored as a folder.
type FileEvidence struct {
	Kind   string `json:"kind"`
	Format string `json:"format"`
}

// SeriesEvidence is one series membership of a member. An empty Position is
// unknown, never "different".
type SeriesEvidence struct {
	SeriesID int64  `json:"seriesId"`
	Title    string `json:"title"`
	Position string `json:"position"`
}

// Evidence is the per-member fact sheet. Build it with NewEvidence, which
// normalizes identifiers so equal books compare equal.
type Evidence struct {
	Files []FileEvidence `json:"files"`
	// ISBNs are the member's edition ISBNs as ISBN-13 where the check digit
	// allows, sorted, at most maxShownISBNs of them; ISBNCount is the total.
	ISBNs     []string         `json:"isbns"`
	ISBNCount int              `json:"isbnCount"`
	ASINs     []string         `json:"asins"`
	Series    []SeriesEvidence `json:"series"`
	// Year is the release year, 0 when unknown.
	Year int `json:"year,omitempty"`

	// allISBNs is the full sorted set the comparison uses.
	allISBNs []string
}

// NewEvidence builds a member's evidence from what the caller loaded: the
// book row itself, its book_files rows, the ISBNs and ASINs of its editions,
// and its series memberships. The book's own ebook and audiobook path columns
// stand in for a missing book_files row (the shape migration 028 left behind),
// and the book's own ASIN joins the edition ASINs.
func NewEvidence(b models.Book, files []FileRef, isbns, asins []string, series []SeriesEvidence) Evidence {
	ev := Evidence{
		Files:  fileEvidence(b, files),
		ASINs:  normalizeASINs(append(append([]string{}, asins...), b.ASIN)),
		Series: append([]SeriesEvidence{}, series...),
	}
	ev.allISBNs = normalizeISBNs(isbns)
	ev.ISBNCount = len(ev.allISBNs)
	ev.ISBNs = ev.allISBNs
	if len(ev.ISBNs) > maxShownISBNs {
		ev.ISBNs = ev.ISBNs[:maxShownISBNs]
	}
	sort.SliceStable(ev.Series, func(i, j int) bool {
		if ev.Series[i].Title != ev.Series[j].Title {
			return ev.Series[i].Title < ev.Series[j].Title
		}
		return ev.Series[i].SeriesID < ev.Series[j].SeriesID
	})
	if b.ReleaseDate != nil && !b.ReleaseDate.IsZero() {
		ev.Year = b.ReleaseDate.Year()
	}
	return ev
}

func fileEvidence(b models.Book, files []FileRef) []FileEvidence {
	refs := append([]FileRef{}, files...)
	hasKind := func(kind string) bool {
		for _, f := range refs {
			if f.Kind == kind {
				return true
			}
		}
		return false
	}
	if b.EbookFilePath != "" && !hasKind(models.MediaTypeEbook) {
		refs = append(refs, FileRef{Kind: models.MediaTypeEbook, Path: b.EbookFilePath})
	}
	if b.AudiobookFilePath != "" && !hasKind(models.MediaTypeAudiobook) {
		refs = append(refs, FileRef{Kind: models.MediaTypeAudiobook, Path: b.AudiobookFilePath})
	}
	out := []FileEvidence{}
	seen := map[FileEvidence]bool{}
	for _, f := range refs {
		if strings.TrimSpace(f.Path) == "" {
			continue
		}
		fe := FileEvidence{Kind: f.Kind, Format: fileFormat(f.Path)}
		if seen[fe] {
			continue
		}
		seen[fe] = true
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Format < out[j].Format
	})
	return out
}

// fileFormat returns the lowercase extension of path without the dot, or ""
// when the extension is not a plausible file format: a folder name such as
// "Vol. 2" or "J. R. R. Tolkien" yields " 2" or similar, which is not one.
func fileFormat(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(strings.ReplaceAll(path, `\`, "/")), ".")
	if ext == "" || len(ext) > 5 {
		return ""
	}
	for _, r := range ext {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return strings.ToLower(ext)
}

// normalizeISBNs converts each ISBN to ISBN-13 when its check digit is valid,
// so an edition recorded as ISBN-10 matches the same edition recorded as
// ISBN-13. One with a bad check digit is kept in its normalized raw form: two
// rows carrying the same wrong ISBN still share it.
func normalizeISBNs(raw []string) []string {
	set := map[string]struct{}{}
	for _, r := range raw {
		v := isbnutil.ToISBN13(r)
		if v == "" {
			v = isbnutil.Normalize(r)
		}
		if v != "" {
			set[v] = struct{}{}
		}
	}
	return sortedHolderKeys(set)
}

func normalizeASINs(raw []string) []string {
	set := map[string]struct{}{}
	for _, r := range raw {
		if v := isbnutil.NormalizeASIN(r); v != "" {
			set[v] = struct{}{}
		}
	}
	return sortedHolderKeys(set)
}

// SignalKind names one agreement or conflict. Like RuleID, the values are
// stable identifiers the UI translates.
type SignalKind string

const (
	// SignalSharedISBN: two or more rows have an edition with the same ISBN.
	// The strongest evidence that they are one book.
	SignalSharedISBN SignalKind = "shared-isbn"
	// SignalSharedASIN: two or more rows carry the same ASIN.
	SignalSharedASIN SignalKind = "shared-asin"
	// SignalSameSeriesPosition: two or more rows hold the same known position
	// in the same series.
	SignalSameSeriesPosition SignalKind = "same-series-position"
	// SignalSeriesPositionConflict: rows hold different known positions in
	// the same series, so they are different books of that series.
	SignalSeriesPositionConflict SignalKind = "series-position-conflict"
	// SignalYearConflict: the rows' release years are more than a year apart.
	SignalYearConflict SignalKind = "year-conflict"
	// SignalLanguageConflict: the rows are in different languages.
	SignalLanguageConflict SignalKind = "language-conflict"
)

// Signal is one agreement or conflict between members of a group. BookIDs are
// the members it concerns, sorted. Values are the facts behind it, which the
// UI shows as data: the shared ISBNs or ASINs; the series title then the
// position for a shared position; the series title then each distinct
// position for a series conflict; the earliest and latest year; or each
// distinct language as recorded.
type Signal struct {
	Kind     SignalKind `json:"kind"`
	Conflict bool       `json:"conflict"`
	BookIDs  []int64    `json:"bookIds"`
	Values   []string   `json:"values"`
}

// Annotate attaches evidence to every member of g and works out the group's
// signals, conflict flag, keeper and suggested exclusions. evidence is keyed
// by book ID; a member with no entry gets evidence built from its book row
// alone. Signals and the keeper consider non-excluded members only: an
// excluded row has already been decided and should not hold back the rest.
//
// The keeper rule is deliberately narrow. Exactly one non-excluded member
// with files makes it the keeper; with none or several there is no keeper,
// because then the files themselves do not say which row is the book. The
// empty rows are suggested for exclusion only when there is a keeper, no
// conflict, and positive evidence tying every empty row to the keeper (see
// linkedToKeeper). The suggestion can never include a row that has files and
// is never made for a group whose evidence says the rows may be different
// books, or where nothing says they are the same one. SuggestionWithheld says
// why when there is no suggestion.
func Annotate(g *Group, evidence map[int64]Evidence) {
	for i := range g.Members {
		m := &g.Members[i]
		ev, ok := evidence[m.ID]
		if !ok {
			ev = NewEvidence(m.Book, nil, nil, nil, nil)
		}
		m.Evidence = ev
		m.HasFiles = len(ev.Files) > 0
	}

	active := make([]*Member, 0, len(g.Members))
	for i := range g.Members {
		if !g.Members[i].Excluded {
			active = append(active, &g.Members[i])
		}
	}

	g.Signals = signalsFor(active)
	g.Conflict = false
	for _, s := range g.Signals {
		if s.Conflict {
			g.Conflict = true
			break
		}
	}

	g.KeeperID = 0
	g.SuggestedExcludeIDs = []int64{}
	g.SuggestionWithheld = ""
	var withFiles []*Member
	for _, m := range active {
		if m.HasFiles {
			withFiles = append(withFiles, m)
		}
	}
	switch len(withFiles) {
	case 0:
		g.SuggestionWithheld = WithheldNoFiles
		return
	case 1:
	default:
		g.SuggestionWithheld = WithheldSeveralWithFiles
		return
	}
	keeper := withFiles[0]
	g.KeeperID = keeper.ID
	if g.Conflict {
		g.SuggestionWithheld = WithheldConflict
		return
	}
	var empty []int64
	for _, m := range active {
		if m.HasFiles {
			continue
		}
		// Every empty row needs its own positive link to the keeper. No
		// conflict is not the same as agreement: "Mistborn" (files) and
		// "Mistborn: The Hero of Ages" (no year, no series, no ISBN) disagree
		// on nothing only because the empty row knows nothing.
		if !linkedToKeeper(keeper, m) {
			g.SuggestionWithheld = WithheldNoEvidence
			return
		}
		empty = append(empty, m.ID)
	}
	g.SuggestedExcludeIDs = empty
}

// Reasons Annotate gives for not suggesting an exclusion. Stable identifiers
// the UI translates.
const (
	WithheldNoFiles          = "no-files"
	WithheldSeveralWithFiles = "several-with-files"
	WithheldConflict         = "conflict"
	WithheldNoEvidence       = "no-evidence"
)

// linkedToKeeper reports whether there is positive evidence that row is the
// same book as keeper: a shared ISBN or ASIN, the same position in the same
// series, or titles that match by a rule stronger than substring (identical
// once folded, or identical after dropping a leading article or an edition
// marker). A substring match alone is the weakest rule, the one that pairs a
// series opener with its sequels, so it never counts as evidence here.
func linkedToKeeper(keeper, row *Member) bool {
	if intersects(keeper.Evidence.allISBNs, row.Evidence.allISBNs) ||
		intersects(keeper.Evidence.ASINs, row.Evidence.ASINs) {
		return true
	}
	for _, a := range keeper.Evidence.Series {
		for _, b := range row.Evidence.Series {
			if a.SeriesID == b.SeriesID && seriesmatch.SamePosition(a.Position, b.Position) {
				return true
			}
		}
	}
	if AggressiveTitleKey(keeper.Title) == "" || AggressiveTitleKey(row.Title) == "" {
		return false
	}
	return len(pairRules(bookKeyFor(keeper.Title), bookKeyFor(row.Title), false)) > 0
}

// intersects reports whether two sorted string slices share a value.
func intersects(a, b []string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			return true
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return false
}

// signalsFor computes every agreement and conflict among members. Each kind
// is aggregated over the whole group rather than emitted per pair, so a large
// transitive group yields a handful of signals, not one per pair.
func signalsFor(members []*Member) []Signal {
	signals := []Signal{}
	signals = append(signals, sharedValueSignals(members, SignalSharedISBN, func(m *Member) []string { return m.Evidence.allISBNs })...)
	signals = append(signals, sharedValueSignals(members, SignalSharedASIN, func(m *Member) []string { return m.Evidence.ASINs })...)
	signals = append(signals, seriesSignals(members)...)
	if s, ok := yearConflict(members); ok {
		signals = append(signals, s)
	}
	if s, ok := languageConflict(members); ok {
		signals = append(signals, s)
	}
	return signals
}

// sharedValueSignals finds values (ISBNs, ASINs) held by two or more members
// and emits one signal per distinct set of holders, listing the shared values.
// Two rows that share thirty edition ISBNs yield one signal, not thirty.
func sharedValueSignals(members []*Member, kind SignalKind, values func(*Member) []string) []Signal {
	holders := map[string][]int64{}
	for _, m := range members {
		for _, v := range values(m) {
			holders[v] = append(holders[v], m.ID)
		}
	}
	type bucket struct {
		ids    []int64
		values []string
	}
	byHolders := map[string]*bucket{}
	var order []string
	for _, v := range sortedHolderKeys(holders) {
		ids := holders[v]
		if len(ids) < 2 {
			continue
		}
		ids = sortedIDs(ids)
		key := idsKey(ids)
		b, ok := byHolders[key]
		if !ok {
			b = &bucket{ids: ids}
			byHolders[key] = b
			order = append(order, key)
		}
		b.values = append(b.values, v)
	}
	out := make([]Signal, 0, len(order))
	for _, key := range order {
		b := byHolders[key]
		vals := b.values
		if len(vals) > maxSignalValues {
			vals = vals[:maxSignalValues]
		}
		out = append(out, Signal{Kind: kind, BookIDs: b.ids, Values: vals})
	}
	return out
}

// seriesSignals reports, per series, a shared position (two or more members
// at the same known position) and a conflict (two or more distinct known
// positions). Both can hold for one series: two rows at #1 agree, a third at
// #2 is a different book of the series. Positions are compared with
// seriesmatch.SamePosition, so "1" and "1.0" are one position; each position
// is shown as the first spelling seen.
func seriesSignals(members []*Member) []Signal {
	type cluster struct {
		pos string
		ids []int64
	}
	type slot struct {
		title    string
		clusters []*cluster
	}
	bySeries := map[int64]*slot{}
	var seriesOrder []int64
	for _, m := range members {
		for _, s := range m.Evidence.Series {
			if strings.TrimSpace(s.Position) == "" {
				continue
			}
			sl, ok := bySeries[s.SeriesID]
			if !ok {
				sl = &slot{title: s.Title}
				bySeries[s.SeriesID] = sl
				seriesOrder = append(seriesOrder, s.SeriesID)
			}
			var c *cluster
			for _, existing := range sl.clusters {
				if seriesmatch.SamePosition(existing.pos, s.Position) {
					c = existing
					break
				}
			}
			if c == nil {
				c = &cluster{pos: strings.TrimSpace(s.Position)}
				sl.clusters = append(sl.clusters, c)
			}
			c.ids = append(c.ids, m.ID)
		}
	}
	sort.Slice(seriesOrder, func(i, j int) bool { return seriesOrder[i] < seriesOrder[j] })

	var out []Signal
	for _, id := range seriesOrder {
		sl := bySeries[id]
		sort.Slice(sl.clusters, func(i, j int) bool { return sl.clusters[i].pos < sl.clusters[j].pos })
		for _, c := range sl.clusters {
			if ids := sortedIDs(c.ids); len(ids) >= 2 {
				out = append(out, Signal{
					Kind:    SignalSameSeriesPosition,
					BookIDs: ids,
					Values:  []string{sl.title, c.pos},
				})
			}
		}
		if len(sl.clusters) >= 2 {
			var ids []int64
			positions := make([]string, 0, len(sl.clusters))
			for _, c := range sl.clusters {
				ids = append(ids, c.ids...)
				positions = append(positions, c.pos)
			}
			out = append(out, Signal{
				Kind:     SignalSeriesPositionConflict,
				Conflict: true,
				BookIDs:  sortedIDs(ids),
				Values:   append([]string{sl.title}, positions...),
			})
		}
	}
	return out
}

// yearConflict reports when the known release years span more than one year.
// One year apart is not a conflict: an ebook and its audiobook routinely
// release a few months apart across a new year. BookIDs are the members at
// the two extremes.
func yearConflict(members []*Member) (Signal, bool) {
	minYear, maxYear := 0, 0
	for _, m := range members {
		y := m.Evidence.Year
		if y == 0 {
			continue
		}
		if minYear == 0 || y < minYear {
			minYear = y
		}
		if y > maxYear {
			maxYear = y
		}
	}
	if minYear == 0 || maxYear-minYear <= 1 {
		return Signal{}, false
	}
	var ids []int64
	for _, m := range members {
		if y := m.Evidence.Year; y == minYear || y == maxYear {
			ids = append(ids, m.ID)
		}
	}
	return Signal{
		Kind:     SignalYearConflict,
		Conflict: true,
		BookIDs:  sortedIDs(ids),
		Values:   []string{strconv.Itoa(minYear), strconv.Itoa(maxYear)},
	}, true
}

// languageConflict reports when members with a known language disagree on
// it. Codes are compared after models.NormalizeLanguageCode, so "en", "eng"
// and "English" agree; Values carry the language as each row records it.
func languageConflict(members []*Member) (Signal, bool) {
	shown := map[string]string{}
	var ids []int64
	for _, m := range members {
		norm := models.NormalizeLanguageCode(m.Language)
		if _, unknown := unknownLanguages[norm]; unknown || norm == "" {
			continue
		}
		ids = append(ids, m.ID)
		if _, ok := shown[norm]; !ok {
			shown[norm] = strings.TrimSpace(m.Language)
		}
	}
	if len(shown) < 2 {
		return Signal{}, false
	}
	values := make([]string, 0, len(shown))
	for _, norm := range sortedHolderKeys(shown) {
		values = append(values, shown[norm])
	}
	return Signal{
		Kind:     SignalLanguageConflict,
		Conflict: true,
		BookIDs:  sortedIDs(ids),
		Values:   values,
	}, true
}

// unknownLanguages are the ISO 639-2 codes that say nothing about which
// language a book is in: undetermined, multiple, uncoded and no linguistic
// content. A row tagged with one of them is treated as having no language.
var unknownLanguages = map[string]struct{}{"und": {}, "mul": {}, "mis": {}, "zxx": {}}

func sortedHolderKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func idsKey(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}
