package nb

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// groupWorks folds edition records into one book per (author, work title), in
// first-appearance order so the API's relevance ordering survives. When
// authorID is set, only records crediting that authority ID as author are
// kept: the name filter that fetched them also matches homonyms, and records
// where the person is only translator or narrator.
//
// Translations join the original's work through the title the cataloguer
// recorded for it (see workTitle). A translation with no such title stays its
// own book; the metadata profile's language filter then decides whether it is
// wanted, the same as for any other provider.
func groupWorks(items []item, authorID string) []models.Book {
	var order []string
	groups := make(map[string][]item)
	for _, it := range items {
		m := it.Metadata
		if m.Identifiers.SesamID == "" {
			m.Identifiers.SesamID = it.ID
		}
		if !sesamIDRe.MatchString(m.Identifiers.SesamID) || recordTitle(m) == "" {
			continue
		}
		author := primaryAuthor(m, authorID)
		if authorID != "" && author == nil {
			continue
		}
		authorKey := ""
		if author != nil {
			authorKey = author.Identifier + author.Name
		}
		// Spaces are ignored so a catalogue slip that splits a word
		// ("Fjel lbyen") stays with its work; the key is per author, so two
		// titles differing only in spacing are taken as one work.
		key := authorKey + "|" + strings.ReplaceAll(indexer.CanonicalDedupKey(workTitle(m)), " ", "")
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], item{ID: it.ID, Metadata: m})
	}
	books := make([]models.Book, 0, len(order))
	for _, key := range order {
		books = append(books, buildWork(groups[key], authorID))
	}
	return books
}

// buildWork turns one group of editions into a book. The representative
// record, which supplies the ID and title, is the best Norwegian edition: this
// provider exists for original-language Norwegian titles, and for a foreign
// author it yields the Norwegian translation's title, which is what a
// Norwegian library's files are named after.
func buildWork(group []item, authorID string) models.Book {
	rep := group[0]
	for _, it := range group[1:] {
		if repScore(it.Metadata) > repScore(rep.Metadata) {
			rep = it
		}
	}
	m := rep.Metadata
	title := commonTitle(group, recordTitle(m))
	b := models.Book{
		ForeignID:        idPrefix + m.Identifiers.SesamID,
		Title:            title,
		SortTitle:        title,
		Description:      m.Summary,
		Language:         language(m),
		MetadataProvider: "nb",
		Monitored:        true,
		Status:           models.BookStatusWanted,
	}
	if p := primaryAuthor(m, authorID); p != nil && p.authorityID() != "" {
		a := personToAuthor(*p)
		b.Author = &a
	}
	for _, p := range m.People {
		if p.isAuthor() && p.authorityID() != "" {
			b.CreditedAuthorForeignIDs = append(b.CreditedAuthorForeignIDs, authorPrefix+p.authorityID())
		}
	}
	b.Genres = workGenres(group, rep)
	for _, it := range group {
		em := it.Metadata
		if b.Narrator == "" && isAudio(em) {
			b.Narrator = narrators(em)
		}
		if b.Description == "" {
			b.Description = em.Summary
		}
		date := parseYear(em.OriginInfo.Issued)
		if date != nil && (b.ReleaseDate == nil || date.Before(*b.ReleaseDate)) {
			b.ReleaseDate = date
		}
		ed := toEdition(em, date)
		if b.DurationSeconds == 0 {
			b.DurationSeconds = ed.DurationSeconds
		}
		b.Editions = append(b.Editions, ed)
		b.ProviderISBNs = append(b.ProviderISBNs, em.Identifiers.ISBN13...)
		b.ProviderISBNs = append(b.ProviderISBNs, em.Identifiers.ISBN10...)
	}
	return b
}

// commonTitle is the title most of a work's editions carry, so one record's
// cataloguing slip cannot name the book. Ties keep fallback, the
// representative's own title; among other equal counts the first in sort
// order wins, so the result does not depend on map order.
func commonTitle(group []item, fallback string) string {
	counts := make(map[string]int, len(group))
	for _, it := range group {
		counts[recordTitle(it.Metadata)]++
	}
	titles := make([]string, 0, len(counts))
	for t := range counts {
		titles = append(titles, t)
	}
	sort.Strings(titles) // a deterministic winner among equal counts
	best := fallback
	for _, t := range titles {
		if counts[t] > counts[best] {
			best = t
		}
	}
	return best
}

// narrators lists a record's narrator credits (relator "nrt") in display
// form, comma separated, as the Audible provider writes them.
func narrators(m itemMetadata) string {
	var names []string
	for _, p := range m.People {
		if p.hasRole("nrt") {
			names = append(names, invertName(p.Name))
		}
	}
	return strings.Join(names, ", ")
}

// genreFormatTerms are entries in NB's genre list that name the format, not
// a genre.
var genreFormatTerms = map[string]bool{"lydbøker": true, "lydbok": true, "e-bøker": true, "e-bok": true}

// workGenres returns the work's genres from the representative record's
// subject genres, or the first edition that has any. NB lists each term in
// both written standards ("Romaner", "Romanar"), so a Nynorsk "-ar" form is
// dropped when its Bokmål "-er" form is present.
// ponytail: plural-ending rule, not a Bokmål/Nynorsk vocabulary; a twin
// differing in more than the ending stays as a second genre.
func workGenres(group []item, rep item) []string {
	terms := rep.Metadata.Subject.Genres
	for _, it := range group {
		if len(terms) > 0 {
			break
		}
		terms = it.Metadata.Subject.Genres
	}
	present := make(map[string]bool, len(terms))
	for _, t := range terms {
		present[strings.ToLower(strings.TrimSpace(t))] = true
	}
	genres := []string{}
	seen := make(map[string]bool, len(terms))
	for _, t := range terms {
		t = strings.TrimSpace(t)
		key := strings.ToLower(t)
		if t == "" || seen[key] || genreFormatTerms[key] {
			continue
		}
		if stem, ok := strings.CutSuffix(key, "ar"); ok && present[stem+"er"] {
			continue
		}
		seen[key] = true
		genres = append(genres, t)
	}
	return genres
}

var (
	extentClockRe = regexp.MustCompile(`^\s*(\d+):(\d{2}):(\d{2})\s*$`)
	extentHoursRe = regexp.MustCompile(`(\d+)\s*t\b`)
	extentMinsRe  = regexp.MustCompile(`(\d+)\s*min\b`)
)

// extentDuration reads an audiobook's running time, in seconds, from NB's
// extent text: "1 lydfil (11 t, 16 min)", "21:34:00", or a CD set's
// "3 plater (CD)(3 t, 7 min) …". 0 when it states none.
func extentDuration(extent string) int {
	if m := extentClockRe.FindStringSubmatch(extent); m != nil {
		h, _ := strconv.Atoi(m[1])
		mins, _ := strconv.Atoi(m[2])
		secs, _ := strconv.Atoi(m[3])
		return h*3600 + mins*60 + secs
	}
	total := 0
	if m := extentHoursRe.FindStringSubmatch(extent); m != nil {
		h, _ := strconv.Atoi(m[1])
		total += h * 3600
	}
	if m := extentMinsRe.FindStringSubmatch(extent); m != nil {
		mins, _ := strconv.Atoi(m[1])
		total += mins * 60
	}
	return total
}

func toEdition(m itemMetadata, date *time.Time) models.Edition {
	ed := models.Edition{
		ForeignID:   idPrefix + m.Identifiers.SesamID,
		Title:       recordTitle(m),
		Publisher:   m.OriginInfo.Publisher,
		PublishDate: date,
		Language:    language(m),
	}
	if len(m.Identifiers.ISBN13) > 0 {
		ed.ISBN13 = &m.Identifiers.ISBN13[0]
	}
	if len(m.Identifiers.ISBN10) > 0 {
		ed.ISBN10 = &m.Identifiers.ISBN10[0]
	}
	if isAudio(m) {
		ed.Format = models.MediaTypeAudiobook
		ed.DurationSeconds = extentDuration(m.PhysicalDescription.Extent)
	} else if m.PageCount > 0 {
		pages := m.PageCount
		ed.NumPages = &pages
	}
	return ed
}

// repScore ranks a record as the representative of its work: Norwegian beats
// other languages, print beats audio.
func repScore(m itemMetadata) int {
	score := 0
	switch language(m) {
	case "nob", "nno", "nor":
		score += 2
	}
	if !isAudio(m) {
		score++
	}
	return score
}

// primaryAuthor picks the credited author: the one with authorID when given,
// otherwise the first author credit.
func primaryAuthor(m itemMetadata, authorID string) *person {
	for i := range m.People {
		p := &m.People[i]
		if !p.isAuthor() {
			continue
		}
		if authorID == "" || p.authorityID() == authorID {
			return p
		}
	}
	return nil
}

// workTitle is the title editions of one work share. A translation records
// its original title either as an "Originaltittel" alternative or as a
// uniform title of the form "<original> <Language>" ("<original> Fransk").
// Anything else groups by its own title: on originals the uniform title is
// cataloguing noise ("<title> Norsk", "<title> 2023", a collection heading)
// that would split one work's printings apart.
func workTitle(m itemMetadata) string {
	for _, ti := range m.TitleInfos {
		if ti.Type == "alternative" && strings.HasPrefix(ti.DisplayLabel, "Originaltittel") {
			return ti.Title
		}
	}
	if translated(m) {
		for _, ti := range m.TitleInfos {
			if ti.Type == "uniform" {
				return stripLanguageQualifier(ti.Title)
			}
		}
	}
	return recordTitle(m)
}

// languageQualifiers are the Norwegian language names the catalogue appends
// to a uniform or series title ("<title> Fransk", "<series> Norsk"). A list,
// not a pattern: a pattern also matched names ("Inspektør Brask"). A language
// missing here leaves that translation as its own book and its series as its
// own row, which the language filter still handles.
var languageQualifiers = map[string]bool{
	"norsk": true, "nynorsk": true, "engelsk": true, "fransk": true, "tysk": true,
	"svensk": true, "dansk": true, "islandsk": true, "finsk": true, "spansk": true,
	"italiensk": true, "portugisisk": true, "nederlandsk": true, "russisk": true,
	"ukrainsk": true, "polsk": true, "tsjekkisk": true, "slovakisk": true,
	"slovensk": true, "kroatisk": true, "serbisk": true, "bulgarsk": true,
	"rumensk": true, "ungarsk": true, "gresk": true, "tyrkisk": true,
	"estisk": true, "latvisk": true, "litauisk": true, "hebraisk": true,
	"arabisk": true, "persisk": true, "kinesisk": true, "japansk": true,
	"koreansk": true, "vietnamesisk": true,
}

// stripLanguageQualifier drops a trailing language name from a catalogue
// title. See languageQualifiers.
func stripLanguageQualifier(title string) string {
	words := strings.Fields(title)
	if len(words) < 2 || !languageQualifiers[strings.ToLower(words[len(words)-1])] {
		return title
	}
	return strings.Join(words[:len(words)-1], " ")
}

func translated(m itemMetadata) bool {
	for _, p := range m.People {
		if p.hasRole("trl") {
			return true
		}
	}
	return false
}

// recordTitle is the record's own title. A volume catalogued as a part of a
// larger work ("<series> : <volume>", "<series>. [5] : <volume>") has the
// series name as its title proper and its own title only as the part name,
// so the part name is used; otherwise the title proper. Taking the title
// proper there turned every such volume into a book named after its series.
func recordTitle(m itemMetadata) string {
	for _, ti := range m.TitleInfos {
		if ti.Type == "" && strings.TrimSpace(ti.PartName) != "" {
			return strings.Join(strings.Fields(ti.PartName), " ")
		}
	}
	return mainTitle(m.Title)
}

// mainTitle is the title proper without its subtitle ("<title> : roman"), with
// the double space NB leaves after a non-sorting article collapsed.
func mainTitle(title string) string {
	title, _, _ = strings.Cut(title, " : ")
	return strings.Join(strings.Fields(title), " ")
}

func language(m itemMetadata) string {
	if len(m.Languages) == 0 {
		return ""
	}
	return m.Languages[0].Code
}

func isAudio(m itemMetadata) bool {
	for _, t := range m.MediaTypes {
		if t == "lydopptak" {
			return true
		}
	}
	return false
}

// parseYear reads the first four-digit run of a publication date such as
// "2019", "[2019]" or "cop. 2019".
func parseYear(s string) *time.Time {
	run := 0
	for i, r := range s {
		if r < '0' || r > '9' {
			run = 0
			continue
		}
		run++
		if run == 4 {
			year, _ := strconv.Atoi(s[i-3 : i+1])
			if year < 1400 || year > 2100 {
				return nil
			}
			t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			return &t
		}
	}
	return nil
}
