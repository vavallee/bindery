package openlibrary

import "encoding/json"

// OpenLibrary API response types

// flexStringSlice unmarshals a JSON array whose elements may be plain strings
// or objects (e.g. {"key": "...", "title": "..."}). Strings are kept as-is;
// objects are decoded by extracting the "title" field when present, otherwise
// they are skipped. This handles schema variance in the OpenLibrary works API.
type flexStringSlice []string

func (f *flexStringSlice) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, elem := range raw {
		var s string
		if err := json.Unmarshal(elem, &s); err == nil {
			*f = append(*f, s)
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(elem, &obj); err == nil {
			if titleRaw, ok := obj["title"]; ok {
				var title string
				if err := json.Unmarshal(titleRaw, &title); err == nil && title != "" {
					*f = append(*f, title)
				}
			}
		}
	}
	return nil
}

type searchResponse struct {
	NumFound int         `json:"numFound"`
	Docs     []searchDoc `json:"docs"`
}

type searchDoc struct {
	Key              string   `json:"key"` // e.g. "/works/OL123W"
	Title            string   `json:"title"`
	AuthorName       []string `json:"author_name"`
	AuthorKey        []string `json:"author_key"` // e.g. ["OL123A"]
	AuthorAltName    []string `json:"author_alternative_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	EditionCount     int      `json:"edition_count"`
	RatingsAverage   float64  `json:"ratings_average"`
	RatingsCount     int      `json:"ratings_count"`
	CoverI           *int     `json:"cover_i"` // cover ID
	ISBN             []string `json:"isbn"`
	Language         []string `json:"language"`
	NumberOfPages    *int     `json:"number_of_pages_median"`
	Publisher        []string `json:"publisher"`
	Subject          []string `json:"subject"`
	Editions         struct {
		Docs []searchEditionDoc `json:"docs"`
	} `json:"editions"`
}

type searchEditionDoc struct {
	Key      string   `json:"key"` // e.g. "/books/OL123M"
	Title    string   `json:"title"`
	Language []string `json:"language"`
}

type authorSearchResponse struct {
	NumFound int               `json:"numFound"`
	Docs     []authorSearchDoc `json:"docs"`
}

type authorSearchDoc struct {
	Key          string   `json:"key"` // e.g. "OL123A"
	Name         string   `json:"name"`
	TopWork      string   `json:"top_work"`
	WorkCount    int      `json:"work_count"`
	BirthDate    string   `json:"birth_date"`
	TopSubjects  []string `json:"top_subjects"`
	RatingsAvg   float64  `json:"ratings_average"`
	RatingsCount int      `json:"ratings_count"`
}

type authorWorksResponse struct {
	Size    int               `json:"size"`
	Entries []authorWorkEntry `json:"entries"`
}

type authorWorkEntry struct {
	Key         string          `json:"key"` // "/works/OL123W"
	Title       string          `json:"title"`
	Description interface{}     `json:"description"`
	Covers      []int           `json:"covers"`
	Subjects    []string        `json:"subjects"`
	Series      flexStringSlice `json:"series"`
}

type workResponse struct {
	Key         string          `json:"key"` // "/works/OL123W"
	Title       string          `json:"title"`
	Description interface{}     `json:"description"` // can be string or {type, value}
	Covers      []int           `json:"covers"`
	Subjects    []string        `json:"subjects"`
	Authors     []workAuthor    `json:"authors"`
	Series      flexStringSlice `json:"series"`
}

type workAuthor struct {
	Author struct {
		Key string `json:"key"` // "/authors/OL123A"
	} `json:"author"`
}

type authorResponse struct {
	Key            string      `json:"key"`
	Name           string      `json:"name"`
	PersonalName   string      `json:"personal_name"`
	Bio            interface{} `json:"bio"` // can be string or {type, value}
	BirthDate      string      `json:"birth_date"`
	Photos         []int       `json:"photos"`
	AlternateNames []string    `json:"alternate_names"`
}

type editionsResponse struct {
	Entries []editionEntry `json:"entries"`
}

type editionEntry struct {
	Key            string   `json:"key"` // "/books/OL123M"
	Title          string   `json:"title"`
	ISBN13         []string `json:"isbn_13"`
	ISBN10         []string `json:"isbn_10"`
	Publishers     []string `json:"publishers"`
	PublishDate    string   `json:"publish_date"`
	PhysicalFormat string   `json:"physical_format"`
	NumberOfPages  int      `json:"number_of_pages"`
	Languages      []struct {
		Key string `json:"key"` // "/languages/eng"
	} `json:"languages"`
	Covers      []int       `json:"covers"`
	Description interface{} `json:"description"`
}

type isbnResponse struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	ISBN13      []string `json:"isbn_13"`
	ISBN10      []string `json:"isbn_10"`
	Publishers  []string `json:"publishers"`
	PublishDate string   `json:"publish_date"`
	Works       []struct {
		Key string `json:"key"` // "/works/OL123W"
	} `json:"works"`
	Authors []struct {
		Key string `json:"key"` // "/authors/OL123A"
	} `json:"authors"`
	Covers []int `json:"covers"`
}

// subjectBooksResponse is the OpenLibrary /subjects/{subject}.json response.
type subjectBooksResponse struct {
	Name      string        `json:"name"`
	WorkCount int           `json:"work_count"`
	Works     []subjectWork `json:"works"`
}

// subjectWork is a single work entry inside subjectBooksResponse.
// Note: the subjects API uses "cover_id" (not "cover_i" like the search API).
type subjectWork struct {
	Key     string `json:"key"` // "/works/OL123W"
	Title   string `json:"title"`
	Authors []struct {
		Key  string `json:"key"` // "/authors/OL123A"
		Name string `json:"name"`
	} `json:"authors"`
	CoverID          *int     `json:"cover_id"`
	FirstPublishYear int      `json:"first_publish_year"`
	Subject          []string `json:"subject"`
	EditionCount     int      `json:"edition_count"`
}
