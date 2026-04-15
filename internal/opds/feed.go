// Package opds builds OPDS 1.2 / Atom 1.0 catalogue feeds from Bindery's
// authors / books / series tables. The catalogue is read-only and purely
// derived — nothing here writes to the database.
//
// Wire format reference:
//
//	https://specs.opds.io/opds-1.2
//	https://datatracker.ietf.org/doc/html/rfc4287  (Atom)
//	https://github.com/dewitt/opensearch/blob/master/opensearch-1-1-draft-6.md
//
// We emit namespace prefixes literally in the element/attribute names rather
// than using encoding/xml's namespace machinery, because Go's default
// "ns1:" auto-prefixing confuses some strict OPDS parsers (notably
// KOReader's, which wants the conventional `opds:` / `dc:` prefixes).
package opds

import (
	"encoding/xml"
)

// XML namespaces declared on the root <feed> element.
const (
	NSAtom       = "http://www.w3.org/2005/Atom"
	NSOPDS       = "http://opds-spec.org/2010/catalog"
	NSDC         = "http://purl.org/dc/terms/"
	NSOpenSearch = "http://a9.com/-/spec/opensearch/1.1/"
)

// Link relations used throughout the catalogue.
const (
	RelSelf        = "self"
	RelStart       = "start"
	RelUp          = "up"
	RelNext        = "next"
	RelPrevious    = "previous"
	RelSubsection  = "subsection"
	RelAcquisition = "http://opds-spec.org/acquisition"
	RelOpenAccess  = "http://opds-spec.org/acquisition/open-access"
	RelImage       = "http://opds-spec.org/image"
	RelThumbnail   = "http://opds-spec.org/image/thumbnail"
	RelSortNew     = "http://opds-spec.org/sort/new"
)

// MIME types. The `profile=opds-catalog` bit is what OPDS clients sniff
// to distinguish a catalogue feed from a plain Atom feed; `kind=` tells
// them whether the entries are navigation subsections or downloadable
// publications.
const (
	TypeNavigation  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	TypeAcquisition = "application/atom+xml;profile=opds-catalog;kind=acquisition"
)

// ContentTypeFeed is the Content-Type header value used on every OPDS
// response regardless of feed kind. Clients key the specific kind off the
// <link rel="self" type="..."/> element, not the header.
const ContentTypeFeed = "application/atom+xml;profile=opds-catalog;charset=utf-8"

// Feed is an OPDS/Atom root element.
type Feed struct {
	XMLName xml.Name `xml:"feed"`

	Xmlns           string `xml:"xmlns,attr"`
	XmlnsDC         string `xml:"xmlns:dc,attr,omitempty"`
	XmlnsOPDS       string `xml:"xmlns:opds,attr,omitempty"`
	XmlnsOpenSearch string `xml:"xmlns:opensearch,attr,omitempty"`

	ID       string  `xml:"id"`
	Title    string  `xml:"title"`
	Updated  string  `xml:"updated"`
	Subtitle string  `xml:"subtitle,omitempty"`
	Author   *Person `xml:"author,omitempty"`

	Links   []Link  `xml:"link"`
	Entries []Entry `xml:"entry"`

	// OpenSearch paging (optional — only emitted when the caller sets
	// the values; xml:omitempty on int treats 0 as empty).
	TotalResults int `xml:"opensearch:totalResults,omitempty"`
	ItemsPerPage int `xml:"opensearch:itemsPerPage,omitempty"`
	StartIndex   int `xml:"opensearch:startIndex,omitempty"`
}

// Entry is a single feed item — either a navigation subsection or an
// acquisition (downloadable publication).
type Entry struct {
	XMLName xml.Name `xml:"entry"`

	ID       string   `xml:"id"`
	Title    string   `xml:"title"`
	Updated  string   `xml:"updated"`
	Authors  []Person `xml:"author,omitempty"`
	Summary  string   `xml:"summary,omitempty"`
	Content  *Content `xml:"content,omitempty"`
	Language string   `xml:"dc:language,omitempty"`
	Issued   string   `xml:"dc:issued,omitempty"`
	Links    []Link   `xml:"link,omitempty"`
}

// Person is an Atom person construct (used for both feed and entry authors).
type Person struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

// Content is an Atom content element. OPDS uses text/html or text for
// publication descriptions.
type Content struct {
	Type string `xml:"type,attr,omitempty"`
	Body string `xml:",chardata"`
}

// Link is an Atom link with the OPDS `count` extension attribute.
type Link struct {
	Rel   string `xml:"rel,attr,omitempty"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr,omitempty"`
	Title string `xml:"title,attr,omitempty"`
	// opds:count decorates a subsection link with the number of entries
	// behind it — lets clients render "Authors (152)" without a probe.
	Count string `xml:"opds:count,attr,omitempty"`
}
