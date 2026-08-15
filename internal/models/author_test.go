package models

import "testing"

// TestAuthorSyncSummary_SkippedTotal verifies SkippedTotal sums every skip
// reason, including the five metadata-profile-filter fields (#1968, #2005,
// #2006, #2007, #2008) landed as plumbing ahead of those PRs. Guards against
// the exact gap that motivated adding them: a filter that increments its own
// count but isn't included here renders no notice at all on the author page,
// since AuthorSyncNotice gates on SkippedTotal() > 0.
func TestAuthorSyncSummary_SkippedTotal(t *testing.T) {
	t.Parallel()

	if got := (&AuthorSyncSummary{}).SkippedTotal(); got != 0 {
		t.Errorf("zero-value summary: SkippedTotal() = %d, want 0", got)
	}

	s := &AuthorSyncSummary{
		SkippedLanguage:      1,
		SkippedJunk:          2,
		SkippedMediaType:     3,
		SkippedNotAccepted:   4,
		SkippedPartBooks:     5,
		SkippedMissingDate:   6,
		SkippedMinPopularity: 7,
		SkippedMinPages:      8,
		SkippedMissingISBN:   9,
	}
	if want, got := 1+2+3+4+5+6+7+8+9, s.SkippedTotal(); got != want {
		t.Errorf("SkippedTotal() = %d, want %d", got, want)
	}

	if got := (*AuthorSyncSummary)(nil).SkippedTotal(); got != 0 {
		t.Errorf("nil receiver: SkippedTotal() = %d, want 0", got)
	}
}

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
