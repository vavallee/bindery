package newznab

import (
	"encoding/xml"
	"fmt"
)

// IndexerError is a structured error returned by a Newznab/Torznab indexer via
// its <error code="N" description="..."/> response element. It carries the raw
// numeric code so callers can classify the failure:
//
//   - 1xx codes (100–199) indicate authentication / authorization problems.
//   - 5xx codes (500–599) indicate rate-limiting by the indexer.
//   - Other codes (200–499, 9xx, …) are indexer-defined operational errors.
//
// Callers that only need the human-readable message can treat IndexerError like
// any other error via its Error() method.
type IndexerError struct {
	Code        int
	Description string
}

func (e *IndexerError) Error() string {
	switch {
	case e.Code == 0 && e.Description == "":
		return "indexer error"
	case e.Code == 0:
		return fmt.Sprintf("indexer error: %s", e.Description)
	case e.Description == "":
		return fmt.Sprintf("indexer error %d", e.Code)
	default:
		return fmt.Sprintf("indexer error %d: %s", e.Code, e.Description)
	}
}

// IsAuthError reports whether the error is an authentication / authorization
// failure (Newznab 1xx code range: 100 = bad credentials, 101 = account
// suspended, 102 = VPN forbidden, etc.).
func IsAuthError(err error) bool {
	var ie *IndexerError
	if err == nil {
		return false
	}
	// Unwrap manually so we work with both direct and wrapped IndexerErrors.
	for {
		if e, ok := err.(*IndexerError); ok {
			ie = e
			break
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return ie.Code >= 100 && ie.Code <= 199
}

// IsRateLimitError reports whether the error is a rate-limit rejection
// (Newznab 5xx code range: 500 = request limit reached, 520 = maximum
// grabs reached, etc.).
func IsRateLimitError(err error) bool {
	var ie *IndexerError
	if err == nil {
		return false
	}
	for {
		if e, ok := err.(*IndexerError); ok {
			ie = e
			break
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return ie.Code >= 500 && ie.Code <= 599
}

// IsHardIndexerError reports whether err is an error that the indexer itself
// deliberately returned (auth failure or rate limit), as opposed to a transient
// network or decoding problem. Callers can use this to abort tier fall-through:
// if an indexer has explicitly rejected the session, retrying lower tiers
// against the same indexer is wasteful and will produce the same result.
func IsHardIndexerError(err error) bool {
	return IsAuthError(err) || IsRateLimitError(err)
}

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
	GUID            string `json:"guid"`
	IndexerID       int64  `json:"indexerId"`
	IndexerName     string `json:"indexerName"`
	Title           string `json:"title"`
	Size            int64  `json:"size"`
	PubDate         string `json:"pubDate"`
	NZBURL          string `json:"nzbUrl"`
	Category        string `json:"category"`
	Grabs           int    `json:"grabs"`
	Author          string `json:"author"`
	BookTitle       string `json:"bookTitle"`
	Protocol        string `json:"protocol"`            // "usenet" or "torrent"
	Language        string `json:"language,omitempty"`  // ISO 639-1 from newznab:attr language (when present)
	MediaType       string `json:"mediaType,omitempty"` // "ebook" or "audiobook"; set for dual-format book searches
	IndexerPriority int    `json:"indexerPriority"`     // copied from models.Indexer.Priority; higher wins
}
