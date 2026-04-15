package newznab

import "encoding/xml"

// Newznab RSS response
type rssResponse struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title    string      `xml:"title"`
	Response nzbResponse `xml:"response"`
	Items    []rssItem   `xml:"item"`
}

type nzbResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type rssItem struct {
	Title     string       `xml:"title"`
	GUID      rssGUID      `xml:"guid"`
	Link      string       `xml:"link"`
	PubDate   string       `xml:"pubDate"`
	Category  string       `xml:"category"`
	Enclosure rssEnclosure `xml:"enclosure"`
	Attrs     []nzbAttr    `xml:"attr"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type nzbAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// Caps response
type capsResponse struct {
	XMLName    xml.Name       `xml:"caps"`
	Searching  capsSearching  `xml:"searching"`
	Categories capsCategories `xml:"categories"`
}

type capsSearching struct {
	Search     capsSearch `xml:"search"`
	BookSearch capsSearch `xml:"book-search"`
}

type capsSearch struct {
	Available string `xml:"available,attr"`
}

type capsCategories struct {
	Categories []capsCategory `xml:"category"`
}

type capsCategory struct {
	ID      string         `xml:"id,attr"`
	Name    string         `xml:"name,attr"`
	SubCats []capsCategory `xml:"subcat"`
}

// SearchResult is the domain type for an indexer search result.
type SearchResult struct {
	GUID        string `json:"guid"`
	IndexerID   int64  `json:"indexerId"`
	IndexerName string `json:"indexerName"`
	Title       string `json:"title"`
	Size        int64  `json:"size"`
	PubDate     string `json:"pubDate"`
	NZBURL      string `json:"nzbUrl"`
	Category    string `json:"category"`
	Grabs       int    `json:"grabs"`
	Author      string `json:"author"`
	BookTitle   string `json:"bookTitle"`
	Protocol    string `json:"protocol"`           // "usenet" or "torrent"
	Language    string `json:"language,omitempty"` // ISO 639-1 from newznab:attr language (when present)
}
