package metadata

import (
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/models"
)

// nameSuffixSegments are comma separated parts of an author name that belong
// to the person rather than naming another one: "King, Martin Luther, Jr.".
var nameSuffixSegments = map[string]bool{
	"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "v": true,
	"phd": true, "md": true, "esq": true,
}

// compositeAuthorName reports whether an author record's name is really a list
// of people, the cast of an audiobook that OpenLibrary keeps as one author
// ("Tavia Gilbert, William Dufris, John Scalzi").
//
// It takes three or more comma separated parts that each name someone. One
// comma is never enough, since "Scalzi, John" and "Martin Luther King, Jr."
// are single people. Parts that are a generational or degree suffix ("Jr.",
// "III") or carry no letters (a "1835-1910" lifespan) do not count, so
// "King, Martin Luther, Jr." stays a single name. OpenLibrary's repeated name
// noise ("Black, Chuck, Black, Chuck") is left to the dedupe that already
// folds it into the clean record.
func compositeAuthorName(name string) bool {
	parts := strings.Split(name, ",")
	if len(parts) < 3 || duplicatedAuthorName(name) {
		return false
	}
	people := 0
	for _, p := range parts {
		key := strings.ToLower(strings.NewReplacer(".", "", " ", "").Replace(p))
		if nameSuffixSegments[key] || !strings.ContainsFunc(key, unicode.IsLetter) {
			continue
		}
		people++
	}
	return people >= 3
}

// demoteCompositeAuthors moves cast list records (compositeAuthorName) below
// every single name record, keeping each group's existing order. When a single
// name record is the query's exact name match, the cast lists are dropped
// outright: the person searched for is already there, and a cast list record
// is never the author to add or bind.
//
// A query that is itself a list of names is left alone: whoever typed or
// imported it wants that record, and the bulk importers take the first result
// with an id (migrate.firstLinkableAuthor), so demoting it would bind the
// first person on the list instead.
func demoteCompositeAuthors(authors []models.Author, query string) []models.Author {
	if compositeAuthorName(query) {
		return authors
	}
	composite := make([]bool, len(authors))
	anyComposite, exact := false, false
	want := canonicalAuthorKey(query)
	for i := range authors {
		composite[i] = compositeAuthorName(authors[i].Name)
		switch {
		case composite[i]:
			anyComposite = true
		case want != "" && canonicalAuthorKey(authors[i].Name) == want:
			exact = true
		}
	}
	if !anyComposite {
		return authors
	}
	out := make([]models.Author, 0, len(authors))
	for i := range authors {
		if !composite[i] {
			out = append(out, authors[i])
		}
	}
	if exact {
		return out
	}
	for i := range authors {
		if composite[i] {
			out = append(out, authors[i])
		}
	}
	return out
}
