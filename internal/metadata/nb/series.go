package nb

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"unicode"

	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

const (
	seriesIDPrefix = "nb-series:"
	// seriesConcurrency bounds MODS requests in flight for one catalogue.
	seriesConcurrency = 4
	// maxSeriesProbes caps MODS requests per work. Not every edition records
	// the author's series (a reprint may list only the publisher's imprint),
	// so a few are tried before giving up.
	maxSeriesProbes = 3
)

// modsRecord is the part of a MODS record that carries series membership.
type modsRecord struct {
	RelatedItems []struct {
		Type  string `xml:"type,attr"`
		Href  string `xml:"http://www.w3.org/1999/xlink href,attr"`
		Title string `xml:"titleInfo>title"`
		Part  string `xml:"titleInfo>partNumber"`
	} `xml:"relatedItem"`
}

// seriesRecords returns the IDs of the records whose search hit names any
// series. Only those are worth a MODS request; the search JSON has the name
// but not the number.
func seriesRecords(items []item) map[string]bool {
	ids := make(map[string]bool)
	for _, it := range items {
		if len(it.Metadata.Series) > 0 {
			ids[firstNonEmpty(it.Metadata.Identifiers.SesamID, it.ID)] = true
		}
	}
	return ids
}

// fillSeries sets each book's series and position from the MODS record of
// one of its editions. Best-effort: series is enrichment, so a failed lookup
// leaves the book without one rather than failing the catalogue.
//
// Two passes. The author's own series are the entries linked to their
// authority record. A cataloguer occasionally records one without the link;
// such an entry is accepted only when its series is linked elsewhere in the
// same books, because an unlinked series nobody links is a publisher imprint.
//
// The second pass also takes the number a volume carries in its own title
// fields (see partSeries), on the same condition.
//
// Both passes read only the book's Norwegian and other Scandinavian records
// (see seriesLanguage). A series linked on a translation's record alone is
// the last resort, for books both passes leave without one. Finally, names
// one work's records give the same number are merged into one series (see
// mergeSeriesNames).
func (c *Client) fillSeries(ctx context.Context, books []models.Book, items []item, authorID string, memo *seriesMemo) {
	if memo == nil {
		memo = &seriesMemo{}
	}
	withSeries := seriesRecords(items)
	parts := partSeries(items, authorID)
	var todo []int
	for i := range books {
		if p, f := seriesCandidates(books[i], withSeries); len(p)+len(f) > 0 {
			todo = append(todo, i)
		}
	}
	unlinked := make([][]models.SeriesRef, len(books))
	same := make([][]string, len(books)) // series IDs naming the book's series
	// Each goroutine writes only its own books[i], unlinked[i] and same[i].
	concurrency.RunBounded(ctx, todo, seriesConcurrency, func(ctx context.Context, i int) {
		var linked []models.SeriesRef
		preferred, _ := seriesCandidates(books[i], withSeries)
		for _, id := range preferred {
			l, other, err := memo.recordSeries(ctx, c, id, authorID)
			if err != nil {
				slog.Debug("nb: series lookup failed", "record", id, "error", err)
				break
			}
			linked = append(linked, l...)
			unlinked[i] = append(unlinked[i], other...)
		}
		if len(linked) == 0 {
			return
		}
		// Not every record numbers the volume; take a numbered entry when
		// one exists.
		for j, ref := range linked {
			if ref.Position != "" {
				linked[0], linked[j] = linked[j], linked[0]
				break
			}
		}
		books[i].SeriesRefs = []models.SeriesRef{linked[0]}
		// NB names one series differently across records ("<A> og <B>",
		// "<full name A> og <full name B>"). Names linked to the author that
		// the work's records give the same number are one series. Unlinked
		// names are not evidence: a publisher's imprint can share the number.
		for _, ref := range linked {
			if ref.Position == linked[0].Position {
				same[i] = append(same[i], ref.ForeignID)
			}
		}
	})

	// Last resort: a series linked only on a translation's record. A foreign
	// name beats none, but never one the Norwegian records supply, so it is
	// applied after the second pass. It still counts as known there: another
	// volume's Norwegian record may name the same series unlinked.
	fallback := make([]*models.SeriesRef, len(books))
	var rest []int
	for i := range books {
		if _, f := seriesCandidates(books[i], withSeries); len(books[i].SeriesRefs) == 0 && len(f) > 0 {
			rest = append(rest, i)
		}
	}
	concurrency.RunBounded(ctx, rest, seriesConcurrency, func(ctx context.Context, i int) {
		_, ids := seriesCandidates(books[i], withSeries)
		for _, id := range ids {
			linked, _, err := memo.recordSeries(ctx, c, id, authorID)
			if err != nil {
				slog.Debug("nb: series lookup failed", "record", id, "error", err)
				return
			}
			if len(linked) > 0 {
				fallback[i] = &linked[0]
				return
			}
		}
	})

	known := make(map[string]bool)
	for i, b := range books {
		if fallback[i] != nil {
			known[fallback[i].ForeignID] = true
		}
		for _, ref := range b.SeriesRefs {
			known[ref.ForeignID] = true
		}
		for _, id := range same[i] {
			known[id] = true
		}
	}
	for i := range books {
		if len(books[i].SeriesRefs) > 0 {
			continue
		}
		candidates := unlinked[i]
		for _, ed := range books[i].Editions {
			if !seriesLanguage(books[i].Language, ed.Language) {
				continue
			}
			if ref, ok := parts[strings.TrimPrefix(ed.ForeignID, idPrefix)]; ok {
				candidates = append(candidates, ref)
			}
		}
		for _, ref := range candidates {
			if known[ref.ForeignID] {
				books[i].SeriesRefs = []models.SeriesRef{ref}
				break
			}
		}
	}

	for i := range books {
		if len(books[i].SeriesRefs) == 0 && fallback[i] != nil {
			books[i].SeriesRefs = []models.SeriesRef{*fallback[i]}
		}
	}
	mergeSeriesNames(books, same)
}

// mergeSeriesNames gives every name of one series the same ID and title: the
// name most of the books carry, the longer on a tie. same[i] lists the series
// IDs book i's records give its number; names linked through any book are
// one series.
func mergeSeriesNames(books []models.Book, same [][]string) {
	parent := make(map[string]string)
	var find func(string) string
	find = func(id string) string {
		if p, ok := parent[id]; ok && p != id {
			parent[id] = find(p)
			return parent[id]
		}
		parent[id] = id
		return id
	}
	for _, ids := range same {
		for _, id := range ids[min(1, len(ids)):] {
			parent[find(id)] = find(ids[0])
		}
	}
	if len(parent) == 0 {
		return
	}
	count := make(map[string]int)
	title := make(map[string]string)
	for _, b := range books {
		for _, ref := range b.SeriesRefs {
			count[ref.ForeignID]++
			title[ref.ForeignID] = ref.Title
		}
	}
	best := make(map[string]string) // group root -> chosen series ID
	for id := range count {
		root := find(id)
		cur, ok := best[root]
		if !ok || count[id] > count[cur] || (count[id] == count[cur] && (len(title[id]) > len(title[cur]) || len(title[id]) == len(title[cur]) && id < cur)) {
			best[root] = id
		}
	}
	for i := range books {
		for j, ref := range books[i].SeriesRefs {
			if id := best[find(ref.ForeignID)]; id != "" && id != ref.ForeignID {
				books[i].SeriesRefs[j].ForeignID, books[i].SeriesRefs[j].Title = id, title[id]
			}
		}
	}
}

// partSeries returns, per record ID, the series a record names in its own
// title fields: a record catalogued as part n of a larger work carries that
// work's name and the number in a titleInfo (the uniform title preferred,
// since the title proper may have its leading article split off). It is a
// candidate only: the work may be an omnibus rather than a series, so
// fillSeries takes it only when the author's catalogue links that series.
func partSeries(items []item, authorID string) map[string]models.SeriesRef {
	refs := make(map[string]models.SeriesRef)
	for _, it := range items {
		var found *titleInfo
		for i, ti := range it.Metadata.TitleInfos {
			if strings.TrimSpace(ti.PartName) == "" || partPosition(ti.PartNumber) == "" {
				continue
			}
			if found == nil || ti.Type == "uniform" {
				found = &it.Metadata.TitleInfos[i]
			}
		}
		if found == nil {
			continue
		}
		title := stripLanguageQualifier(strings.Join(strings.Fields(found.Title), " "))
		slug := seriesSlug(title)
		if slug == "" {
			continue
		}
		refs[firstNonEmpty(it.Metadata.Identifiers.SesamID, it.ID)] = models.SeriesRef{
			ForeignID: seriesIDPrefix + authorID + ":" + slug,
			Title:     title,
			Position:  partPosition(found.PartNumber),
			Primary:   true,
		}
	}
	return refs
}

// partPosition cleans a catalogue part number: "[5]" (supplied by the
// cataloguer) and "3." both mean the plain number.
func partPosition(s string) string {
	return strings.Trim(strings.TrimSpace(s), "[]. ")
}

// seriesCandidates lists the book's record IDs that name a series: preferred
// are the representative, then editions in the book's own language, then
// other Scandinavian ones (see seriesLanguage), at most maxSeriesProbes.
// fallback are the other translations, at most maxSeriesProbes, tried only
// when no preferred record links a series: some series are linked on a
// translation's record alone, and a foreign name beats none.
func seriesCandidates(b models.Book, withSeries map[string]bool) (preferred, fallback []string) {
	seen := make(map[string]bool)
	add := func(ids *[]string, foreignID string) {
		id := strings.TrimPrefix(foreignID, idPrefix)
		if withSeries[id] && !seen[id] && len(*ids) < maxSeriesProbes {
			seen[id] = true
			*ids = append(*ids, id)
		}
	}
	add(&preferred, b.ForeignID)
	for _, own := range []bool{true, false} {
		for _, ed := range b.Editions {
			if (ed.Language == b.Language) == own && seriesLanguage(b.Language, ed.Language) {
				add(&preferred, ed.ForeignID)
			}
		}
	}
	for _, ed := range b.Editions {
		if !seriesLanguage(b.Language, ed.Language) {
			add(&fallback, ed.ForeignID)
		}
	}
	return preferred, fallback
}

// scandinavian are the languages whose editions name an author's series as
// the Norwegian ones do ("Min kamp", "Barrøy"), often when the Norwegian
// records name none.
var scandinavian = map[string]bool{"nob": true, "nno": true, "nor": true, "dan": true, "swe": true}

// seriesLanguage reports whether an edition in language ed may supply the
// series of a book in language book: its own language, or between
// Scandinavian languages. Other translations name the series in their own
// language ("<series>-romaani"), which is not the series the book belongs to.
func seriesLanguage(book, ed string) bool {
	return ed == book || scandinavian[book] && scandinavian[ed]
}

// seriesMemo caches recordSeries results for one catalogue fetch, which may
// fill series twice (see recallSeriesVolumes). The zero value is ready.
type seriesMemo struct {
	mu   sync.Mutex
	seen map[string]modsSeries
}

type modsSeries struct {
	linked   []models.SeriesRef
	unlinked []models.SeriesRef
	err      error
}

func (m *seriesMemo) recordSeries(ctx context.Context, c *Client, sesamID, authorID string) ([]models.SeriesRef, []models.SeriesRef, error) {
	m.mu.Lock()
	r, ok := m.seen[sesamID]
	m.mu.Unlock()
	if !ok {
		r.linked, r.unlinked, r.err = c.recordSeries(ctx, sesamID, authorID)
		m.mu.Lock()
		if m.seen == nil {
			m.seen = make(map[string]modsSeries)
		}
		m.seen[sesamID] = r
		m.mu.Unlock()
	}
	return r.linked, r.unlinked, r.err
}

// recordSeries reads a record's MODS. linked is the author's own series: the
// entries linked to their authority record, usually one, more when the record
// names the series twice. Publisher imprint series ("<publisher>
// krim") are recorded as series too but carry no such link; those, and any
// author series recorded without the link, come back in unlinked when they
// have a number, for fillSeries to decide on.
func (c *Client) recordSeries(ctx context.Context, sesamID, authorID string) (linked, unlinked []models.SeriesRef, err error) {
	var rec modsRecord
	found, err := c.getXML(ctx, metadataBase+sesamID+"/mods", &rec)
	if err != nil || !found {
		return nil, nil, err
	}
	for _, ri := range rec.RelatedItems {
		if ri.Type != "series" {
			continue
		}
		title := stripLanguageQualifier(strings.Join(strings.Fields(ri.Title), " "))
		slug := seriesSlug(title)
		if slug == "" {
			continue
		}
		ref := models.SeriesRef{
			// Scoped to the author: the series is the author's authority-
			// linked one, and two authors can name a series alike.
			ForeignID: seriesIDPrefix + authorID + ":" + slug,
			Title:     title,
			Position:  strings.TrimRight(strings.TrimSpace(ri.Part), "."),
			Primary:   true,
		}
		if strings.TrimSpace(ri.Href) == "(NO-TrBIB)"+authorID {
			linked = append(linked, ref)
		} else if ref.Position != "" {
			unlinked = append(unlinked, ref)
		}
	}
	return linked, unlinked, nil
}

// seriesSlug is the stable part of a series ID: folded, script-preserving
// (textutil.FoldForSlug), with each run of other characters collapsed to "-".
func seriesSlug(title string) string {
	words := strings.FieldsFunc(textutil.FoldForSlug(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Mc, r)
	})
	return strings.Join(words, "-")
}
