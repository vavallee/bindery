package models

import "testing"

func TestAuthorProviderFromForeignID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		id   string
		want string
	}{
		{id: "OL13200512A", want: "openlibrary"},
		{id: "hc:emilia-jae", want: "hardcover"},
		{id: "dnb:123456789", want: "dnb"},
		{id: "gb:volume", want: "googlebooks"},
		{id: "calibre:author:1", want: "calibre"},
		{id: "abs:author:lib:author", want: "audiobookshelf"},
	}
	for _, tc := range cases {
		if got := AuthorProviderFromForeignID(tc.id); got != tc.want {
			t.Fatalf("AuthorProviderFromForeignID(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestCanReplaceAuthorIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		author *Author
		want   bool
	}{
		{name: "nil", author: nil, want: false},
		{name: "empty foreign id", author: &Author{}, want: true},
		{name: "abs id", author: &Author{ForeignID: "abs:author:lib:author"}, want: true},
		{name: "calibre id", author: &Author{ForeignID: "calibre:author:1"}, want: true},
		{name: "audiobookshelf provider", author: &Author{ForeignID: "legacy", MetadataProvider: "audiobookshelf"}, want: true},
		{name: "calibre provider", author: &Author{ForeignID: "legacy", MetadataProvider: "calibre"}, want: true},
		{name: "openlibrary", author: &Author{ForeignID: "OL13200512A", MetadataProvider: "openlibrary"}, want: false},
		{name: "hardcover", author: &Author{ForeignID: "hc:emilia-jae", MetadataProvider: "hardcover"}, want: false},
		{name: "dnb", author: &Author{ForeignID: "dnb:123456789", MetadataProvider: "dnb"}, want: false},
	}
	for _, tc := range cases {
		if got := CanReplaceAuthorIdentity(tc.author); got != tc.want {
			t.Fatalf("%s: CanReplaceAuthorIdentity(%+v) = %v, want %v", tc.name, tc.author, got, tc.want)
		}
	}
}
