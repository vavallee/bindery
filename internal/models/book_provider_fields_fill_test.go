package models

import (
	"slices"
	"testing"
)

func TestAdoptRefreshedProviderFields_LanguageAndGenres(t *testing.T) {
	cases := []struct {
		name       string
		book, src  Book
		want       []string
		wantLang   string
		wantGenres []string
	}{
		{"fills both when empty", Book{MetadataProvider: "openlibrary"},
			Book{MetadataProvider: "openlibrary", Language: "eng", Genres: []string{"Fantasy"}},
			[]string{BookFieldLanguage, BookFieldGenres}, "eng", []string{"Fantasy"}},
		{"blank language filled", Book{MetadataProvider: "openlibrary", Language: " "},
			Book{MetadataProvider: "openlibrary", Language: "eng"},
			[]string{BookFieldLanguage}, "eng", nil},
		{"language kept", Book{MetadataProvider: "openlibrary", Language: "fre"},
			Book{MetadataProvider: "openlibrary", Language: "eng"}, nil, "fre", nil},
		{"genres kept", Book{MetadataProvider: "openlibrary", Genres: []string{"Mine"}},
			Book{MetadataProvider: "openlibrary", Genres: []string{"Fantasy"}}, nil, "", []string{"Mine"}},
		{"language left for the file", Book{MetadataProvider: "openlibrary", EbookFilePath: "/b/a.epub"},
			Book{MetadataProvider: "openlibrary", Language: "eng"}, nil, "", nil},
		{"language left for a legacy file path", Book{MetadataProvider: "openlibrary", FilePath: "/b/a.epub"},
			Book{MetadataProvider: "openlibrary", Language: "eng"}, nil, "", nil},
		{"language left for an audiobook file", Book{MetadataProvider: "openlibrary", AudiobookFilePath: "/b/a"},
			Book{MetadataProvider: "openlibrary", Language: "eng"}, nil, "", nil},
		{"language left for file rows", Book{MetadataProvider: "openlibrary", BookFiles: []BookFile{{}}},
			Book{MetadataProvider: "openlibrary", Language: "eng"}, nil, "", nil},
		{"genres still filled with a file", Book{MetadataProvider: "openlibrary", EbookFilePath: "/b/a.epub"},
			Book{MetadataProvider: "openlibrary", Genres: []string{"Fantasy"}}, []string{BookFieldGenres}, "", []string{"Fantasy"}},
		{"locked both", Book{MetadataProvider: "openlibrary", LockedFields: []string{BookFieldLanguage, BookFieldGenres}},
			Book{MetadataProvider: "openlibrary", Language: "eng", Genres: []string{"Fantasy"}}, nil, "", nil},
		{"unknown both", Book{MetadataProvider: "openlibrary"},
			Book{MetadataProvider: "openlibrary", Language: "eng", Genres: []string{"Fantasy"},
				UnknownFields: []string{BookFieldLanguage, BookFieldGenres}}, nil, "", nil},
		{"other provider", Book{MetadataProvider: "audiobookshelf"},
			Book{MetadataProvider: "openlibrary", Language: "eng", Genres: []string{"Fantasy"}}, nil, "", nil},
		{"empty record never clears", Book{MetadataProvider: "openlibrary", Language: "fre", Genres: []string{"Mine"}},
			Book{MetadataProvider: "openlibrary"}, nil, "fre", []string{"Mine"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, src := c.book, c.src
			if got := b.AdoptRefreshedProviderFields(&src); !slices.Equal(got, c.want) {
				t.Errorf("changed = %v, want %v", got, c.want)
			}
			if b.Language != c.wantLang {
				t.Errorf("language = %q, want %q", b.Language, c.wantLang)
			}
			if !slices.Equal(b.Genres, c.wantGenres) {
				t.Errorf("genres = %v, want %v", b.Genres, c.wantGenres)
			}
		})
	}
}

func TestAdoptRefreshedProviderFieldsDoesNotAliasGenres(t *testing.T) {
	b := Book{MetadataProvider: "openlibrary"}
	src := Book{MetadataProvider: "openlibrary", Genres: []string{"Fantasy"}}
	b.AdoptRefreshedProviderFields(&src)
	src.Genres[0] = "Changed"
	if b.Genres[0] != "Fantasy" {
		t.Errorf("genres alias the record's slice: %v", b.Genres)
	}
}
