package bookorbit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The decoders below read a response as a bag of raw fields and pull out the
// ones the importer uses. Fields it does not know are ignored, so BookOrbit
// adding one changes nothing. A field the import cannot work without has to be
// present and of the expected type, and its absence is an error rather than a
// zero value: a book whose "files" key vanished must not be read as a book
// with no files.

var errNull = errors.New("null")

func field(obj map[string]json.RawMessage, key string) (json.RawMessage, error) {
	raw, ok := obj[key]
	if !ok {
		return nil, fmt.Errorf("field %q is missing", key)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errNull
	}
	return raw, nil
}

// requireInt reads an integer field that must be present and not null.
func requireInt(obj map[string]json.RawMessage, key string, out *int64) error {
	raw, err := field(obj, key)
	if errors.Is(err, errNull) {
		return fmt.Errorf("field %q is null", key)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("field %q is not an integer", key)
	}
	return nil
}

// requireString reads a string field that must be present and not null.
func requireString(obj map[string]json.RawMessage, key string, out *string) error {
	raw, err := field(obj, key)
	if errors.Is(err, errNull) {
		return fmt.Errorf("field %q is null", key)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("field %q is not a string", key)
	}
	return nil
}

// nullableString reads a string field that must be present but may be null.
func nullableString(obj map[string]json.RawMessage, key string, out *string) error {
	raw, err := field(obj, key)
	if errors.Is(err, errNull) {
		*out = ""
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("field %q is not a string", key)
	}
	return nil
}

// optionalString reads a string field when it is there and is a string.
func optionalString(obj map[string]json.RawMessage, key string) string {
	var out string
	if raw, ok := obj[key]; ok {
		_ = json.Unmarshal(raw, &out)
	}
	return strings.TrimSpace(out)
}

func optionalNumber(obj map[string]json.RawMessage, key string) float64 {
	var out float64
	if raw, ok := obj[key]; ok {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func requireObjects(obj map[string]json.RawMessage, key string) ([]map[string]json.RawMessage, error) {
	raw, err := field(obj, key)
	if errors.Is(err, errNull) {
		return nil, fmt.Errorf("field %q is null", key)
	}
	if err != nil {
		return nil, err
	}
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("field %q is not an array of objects", key)
	}
	return out, nil
}

// scalarText reads a value BookOrbit writes as either a string or a number
// and returns its text exactly as written. A series index of 4.5 arrives as
// "4.5" on some versions and 4.5 on others, and going through a float would
// turn 1 into 1.0 or lose a trailing digit.
func scalarText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func decodeBook(body []byte) (*Book, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, errors.New("not a JSON object")
	}
	book := &Book{}
	if err := requireInt(obj, "id", &book.ID); err != nil {
		return nil, err
	}
	if err := requireInt(obj, "libraryId", &book.LibraryID); err != nil {
		return nil, err
	}
	// A book can have no title in BookOrbit, so null is data. A missing key
	// is not.
	if err := nullableString(obj, "title", &book.Title); err != nil {
		return nil, err
	}

	authors, err := requireObjects(obj, "authors")
	if err != nil {
		return nil, err
	}
	for _, a := range authors {
		var p Person
		if err := requireString(a, "name", &p.Name); err != nil {
			return nil, fmt.Errorf("authors[]: %w", err)
		}
		// The id only sharpens matching on a later run, so its absence is
		// tolerated.
		var id int64
		if requireInt(a, "id", &id) == nil {
			p.ID = id
		}
		book.Authors = append(book.Authors, p)
	}

	files, err := requireObjects(obj, "files")
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		var file File
		if err := requireString(f, "absolutePath", &file.Path); err != nil {
			return nil, fmt.Errorf("files[]: %w", err)
		}
		if err := requireString(f, "role", &file.Role); err != nil {
			return nil, fmt.Errorf("files[]: %w", err)
		}
		var id int64
		if requireInt(f, "id", &id) == nil {
			file.ID = id
		}
		file.Format = strings.ToLower(optionalString(f, "format"))
		file.SizeBytes = int64(optionalNumber(f, "sizeBytes"))
		book.Files = append(book.Files, file)
	}

	book.Subtitle = optionalString(obj, "subtitle")
	book.Description = optionalString(obj, "description")
	book.Publisher = optionalString(obj, "publisher")
	book.Language = optionalString(obj, "language")
	book.PublishedDate = optionalString(obj, "publishedDate")
	book.PublishedYear = int(optionalNumber(obj, "publishedYear"))
	book.ISBN10 = optionalString(obj, "isbn10")
	book.ISBN13 = optionalString(obj, "isbn13")
	if raw, ok := obj["genres"]; ok {
		_ = json.Unmarshal(raw, &book.Genres)
	}

	if raw, ok := obj["providerIds"]; ok {
		var ids map[string]json.RawMessage
		if json.Unmarshal(raw, &ids) == nil {
			// Bindery's ASIN is the audiobook's Audible id. BookOrbit's amazon
			// id can be a Kindle one, which would be the wrong book there.
			book.ASIN = scalarText(ids["audible"])
		}
	}

	if raw, ok := obj["audioMetadata"]; ok {
		var audio struct {
			DurationSeconds float64 `json:"durationSeconds"`
			Narrators       []struct {
				Name string `json:"name"`
			} `json:"narrators"`
		}
		if json.Unmarshal(raw, &audio) == nil {
			book.DurationSeconds = audio.DurationSeconds
			for _, n := range audio.Narrators {
				if name := strings.TrimSpace(n.Name); name != "" {
					book.Narrators = append(book.Narrators, name)
				}
			}
		}
	}

	book.Series = decodeSeries(obj)
	return book, nil
}

// decodeSeries reads the book's series. Newer servers list every membership;
// older ones only carry the primary series as three flat fields.
func decodeSeries(obj map[string]json.RawMessage) []SeriesRef {
	var out []SeriesRef
	if raw, ok := obj["seriesMemberships"]; ok {
		var memberships []map[string]json.RawMessage
		if json.Unmarshal(raw, &memberships) == nil {
			for _, m := range memberships {
				ref := SeriesRef{Name: optionalString(m, "seriesName"), Index: scalarText(m["seriesIndex"])}
				ref.ID = int64(optionalNumber(m, "seriesId"))
				if ref.Name != "" {
					out = append(out, ref)
				}
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	if name := optionalString(obj, "seriesName"); name != "" {
		return []SeriesRef{{
			ID:    int64(optionalNumber(obj, "seriesId")),
			Name:  name,
			Index: scalarText(obj["seriesIndex"]),
		}}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
