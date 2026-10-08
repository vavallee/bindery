package nb

import (
	"strings"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// searchResponse is the HAL page returned by GET /catalog/v1/items.
type searchResponse struct {
	Embedded struct {
		Items []item `json:"items"`
	} `json:"_embedded"`
	Page struct {
		Number        int `json:"number"`
		TotalElements int `json:"totalElements"`
		TotalPages    int `json:"totalPages"`
	} `json:"page"`
}

// item is one catalogue record: a single edition (print, e-book, audiobook or
// translation), never a work.
type item struct {
	ID       string       `json:"id"`
	Metadata itemMetadata `json:"metadata"`
}

type itemMetadata struct {
	Title       string      `json:"title"`
	TitleInfos  []titleInfo `json:"titleInfos"`
	People      []person    `json:"people"`
	Identifiers struct {
		ISBN13  []string `json:"isbn13"`
		ISBN10  []string `json:"isbn10"`
		SesamID string   `json:"sesamId"`
	} `json:"identifiers"`
	Languages []struct {
		Code string `json:"code"`
	} `json:"languages"`
	MediaTypes []string `json:"mediaTypes"`
	OriginInfo struct {
		Publisher string `json:"publisher"`
		Issued    string `json:"issued"`
	} `json:"originInfo"`
	Summary   string `json:"summary"`
	PageCount int    `json:"pageCount"`
	// Series names the record's series, author's and publisher's alike,
	// without the number. See authorSeries.
	Series []string `json:"series"`
	// Subject.Genres is the cataloguer's genre list, Bokmål and Nynorsk
	// forms side by side. See workGenres.
	Subject struct {
		Genres []string `json:"genres"`
	} `json:"subject"`
	// PhysicalDescription.Extent is free text: page count for print, the
	// running time for an audiobook. See extentDuration.
	PhysicalDescription struct {
		Extent string `json:"extent"`
	} `json:"physicalDescription"`
}

// titleInfo is one title of a record. Type is "" for the title proper,
// "uniform" for the cataloguer's work title, and "alternative" for others,
// including the original title of a translation.
type titleInfo struct {
	Title        string `json:"title"`
	Type         string `json:"type"`
	DisplayLabel string `json:"displayLabel"`
	// PartName and PartNumber are set when the record is catalogued as one
	// numbered part of a larger work: Title is then that work's name (often
	// the series), PartName the volume's own title. See recordTitle.
	PartName   string `json:"partName"`
	PartNumber string `json:"partNumber"`
}

// person is a credited name. Identifier is "bibsys.no:authority:<id>" when the
// name is linked to the Norwegian authority file. Roles are MARC relator codes.
type person struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Roles      []struct {
		Name string `json:"name"`
	} `json:"roles"`
}

func (p person) hasRole(code string) bool {
	for _, r := range p.Roles {
		if r.Name == code {
			return true
		}
	}
	return false
}

// isAuthor reports an author credit. NB catalogues one as "aut" or, about
// as often, as the generic "cre" (creator), occasionally spelled out.
func (p person) isAuthor() bool {
	return p.hasRole("aut") || p.hasRole("cre") || p.hasRole("creator")
}

func (p person) authorityID() string {
	id, ok := strings.CutPrefix(p.Identifier, authorityIDPrefix)
	if !ok || !authorityIDRe.MatchString(id) {
		return ""
	}
	return id
}

// authorityRecord is the subset of an authority.bibsys.no record used here.
// The authorised name heading is MARC field 100 $a.
type authorityRecord struct {
	Deleted  bool `json:"deleted"`
	MarcData []struct {
		Tag       string `json:"tag"`
		Subfields []struct {
			Code  string `json:"subcode"`
			Value string `json:"value"`
		} `json:"subfields"`
	} `json:"marcdata"`
}

// variants returns the record's other name forms (MARC 400 $a) in display
// form: spellings without diacritics, transliterations, earlier names.
func (r authorityRecord) variants() []string {
	var out []string
	seen := map[string]bool{invertName(r.heading()): true}
	for _, f := range r.MarcData {
		if f.Tag != "400" {
			continue
		}
		for _, sf := range f.Subfields {
			if sf.Code != "a" {
				continue
			}
			if name := invertName(strings.TrimSpace(sf.Value)); name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func (r authorityRecord) heading() string {
	for _, f := range r.MarcData {
		if f.Tag != "100" {
			continue
		}
		for _, sf := range f.Subfields {
			if sf.Code == "a" {
				return strings.TrimSpace(sf.Value)
			}
		}
	}
	return ""
}

func personToAuthor(p person) models.Author {
	return models.Author{
		ForeignID:        authorPrefix + p.authorityID(),
		Name:             invertName(p.Name),
		SortName:         p.Name,
		MetadataProvider: "nb",
	}
}

// invertName turns the catalogue form "Last, First" into "First Last".
func invertName(name string) string {
	last, first, ok := strings.Cut(name, ", ")
	if !ok || strings.TrimSpace(first) == "" {
		return name
	}
	return strings.TrimSpace(first) + " " + last
}

// nameMatches reports whether every query word appears in the name, ignoring
// case and diacritics. The search filter already matched the record; this
// drops the record's other credited authors.
func nameMatches(name string, words []string) bool {
	folded := textutil.FoldForSearch(name)
	for _, w := range words {
		if !strings.Contains(folded, textutil.FoldForSearch(w)) {
			return false
		}
	}
	return true
}
