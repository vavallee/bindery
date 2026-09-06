package models

import (
	"reflect"
	"testing"
)

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

// Every int counter on AuthorSyncSummary except Total has to be part of
// AccountedFor, or Total stops reconciling and the difference reads as lost
// books. That is exactly what happened in #2449: Matched and Failed had no
// field at all, and a reader doing the arithmetic on a real author concluded
// the sync was silently dropping 13 works.
//
// Reflection rather than a hand-written sum, because a hand-written sum is the
// thing that drifted. Set every counter to 1 and AccountedFor must equal the
// number of counters; a new Skipped* field added without wiring fails here
// rather than on someone's author page.
func TestAuthorSyncSummaryAccountedForCoversEveryCounter(t *testing.T) {
	var summary AuthorSyncSummary
	v := reflect.ValueOf(&summary).Elem()
	typ := v.Type()

	counters := 0
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.Int || field.Name == "Total" {
			continue
		}
		v.Field(i).SetInt(1)
		counters++
	}
	if counters < 13 {
		t.Fatalf("found %d counter fields, expected at least 13: the reflection walk is not seeing the struct", counters)
	}
	if got := summary.AccountedFor(); got != counters {
		t.Errorf("AccountedFor() = %d with every one of %d counters set to 1.\n"+
			"A counter is missing from the sum, so Total will not reconcile and the shortfall will look like data loss (#2449).", got, counters)
	}

	summary.Total = counters
	if got := summary.Unaccounted(); got != 0 {
		t.Errorf("Unaccounted() = %d, want 0 when Total equals the sum of every counter", got)
	}
}

// SkippedExcluded is the deliberate asymmetry: out of SkippedTotal because the
// notice does not render a book the user excluded on purpose, in AccountedFor
// because Total still has to add up. Asserted so a later tidy-up cannot quietly
// collapse the two.
func TestAuthorSyncSummarySkippedExcludedIsAccountedButNotNoticed(t *testing.T) {
	summary := AuthorSyncSummary{Total: 3, Added: 1, Matched: 1, SkippedExcluded: 1}
	if got := summary.SkippedTotal(); got != 0 {
		t.Errorf("SkippedTotal() = %d, want 0: the notice must not count a hand-excluded book as something to explain", got)
	}
	if got := summary.Unaccounted(); got != 0 {
		t.Errorf("Unaccounted() = %d, want 0: an excluded work still has to be part of the arithmetic", got)
	}
}

// A nil summary is the "this process has not synced that author" case the
// detail endpoint hands straight to the template.
func TestAuthorSyncSummaryNilIsSafe(t *testing.T) {
	var summary *AuthorSyncSummary
	if got := summary.AccountedFor(); got != 0 {
		t.Errorf("nil AccountedFor() = %d, want 0", got)
	}
	if got := summary.Unaccounted(); got != 0 {
		t.Errorf("nil Unaccounted() = %d, want 0", got)
	}
}
