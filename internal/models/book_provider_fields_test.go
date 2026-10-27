package models

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// bookFieldDecisions forces a decision for every Book field. A new field makes
// TestProviderOwnedBookFields_EveryBookFieldDecided fail until it is listed
// here: either "clear" (and added to ProviderOwnedBookFields) or "keep" with a
// reason, so nobody adds a provider supplied field that a rebind silently
// keeps from the old record (#3104).
var bookFieldDecisions = map[string]string{
	// Provider owned and cleared when the new record lacks them.
	"ReleaseDate": "clear",
	"Language":    "clear",
	"Description": "clear",
	"Genres":      "clear",
	// Kept: OpenLibrary's single-book fetch never returns a rating, so
	// clearing would erase one on every rebind to OpenLibrary.
	"AverageRating": "keep: no rating from the OpenLibrary fetch",
	"RatingsCount":  "keep: no rating from the OpenLibrary fetch",
	// Kept: neither provider's single-book fetch supplies it.
	"OriginalTitle": "keep: no provider fetch supplies it",

	// Identity and bookkeeping, written by the caller itself.
	"ID": "keep: identity", "ForeignID": "keep: identity", "AuthorID": "keep: identity",
	"MetadataProvider": "keep: identity", "HardcoverForeignID": "keep: identity",
	"DedupKey": "keep: derived", "CreatedAt": "keep: bookkeeping",
	"UpdatedAt": "keep: bookkeeping", "UpdatedAtRaw": "keep: bookkeeping",
	"LastMetadataRefreshAt": "keep: bookkeeping", "OwnerUserID": "keep: ownership",
	"LockedFields": "keep: the lock set itself",

	// A record without a title is broken, not a signal to blank the book.
	"Title": "keep: never blank", "SortTitle": "keep: never blank",
	// A cover may come from the user's file, an import or an edition.
	"ImageURL": "keep: cover may be the user's own",
	// Not a Book column; the editions are replaced by the rebind itself.
	"EditionCount": "keep: derived", "Editions": "keep: joined",

	// User or import pipeline owned.
	"Monitored": "keep: user", "Status": "keep: user", "AnyEditionOK": "keep: user",
	"SelectedEditionID": "keep: user", "FilePath": "keep: files", "MediaType": "keep: user",
	"Narrator": "keep: recording", "DurationSeconds": "keep: recording", "ASIN": "keep: recording",
	"CalibreID": "keep: import", "Excluded": "keep: user",
	"EbookFilePath": "keep: files", "AudiobookFilePath": "keep: files", "BookFiles": "keep: files",

	// Transport only or joined, never persisted on the row.
	"IsCompilation": "keep: transport", "AuthorUnmonitored": "keep: transport",
	"Author": "keep: joined", "ImportInFlight": "keep: transport", "Identifiers": "keep: joined",
	"SeriesRefs": "keep: relinked by the caller", "ProviderISBNs": "keep: transport",
	"CreditedAuthorForeignIDs": "keep: transport", "UnknownFields": "keep: transport", "Cleared": "keep: transport",
}

func TestProviderOwnedBookFields_EveryBookFieldDecided(t *testing.T) {
	typ := reflect.TypeOf(Book{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if _, ok := bookFieldDecisions[name]; !ok {
			t.Errorf("Book.%s has no decision: add it to bookFieldDecisions, and to ProviderOwnedBookFields if a rebind should clear it when the new record lacks it", name)
		}
	}
	for name := range bookFieldDecisions {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("bookFieldDecisions names %s, which is not a Book field", name)
		}
	}
}

func TestProviderOwnedBookFields_ListMatchesDecisions(t *testing.T) {
	want := map[string]bool{"releaseDate": true, "language": true, "description": true,
		"genres": true}
	got := map[string]bool{}
	for _, f := range ProviderOwnedBookFields {
		got[f.Name] = true
		if f.Lock != "" && !slices.Contains(LockableBookFields, f.Lock) {
			t.Errorf("%s names lock %q, which is not lockable", f.Name, f.Lock)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProviderOwnedBookFields = %v, want %v", got, want)
	}
	clearN := 0
	for _, d := range bookFieldDecisions {
		if d == "clear" {
			clearN++
		}
	}
	if clearN != len(ProviderOwnedBookFields) {
		t.Errorf("%d fields decided clear but %d listed", clearN, len(ProviderOwnedBookFields))
	}
}

func TestClearProviderFieldsMissingFrom(t *testing.T) {
	d := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	full := func() *Book {
		return &Book{ReleaseDate: &d, Language: "eng", Description: "d", Genres: []string{"g"},
			OriginalTitle: "o", AverageRating: 4, RatingsCount: 9, Title: "T", ImageURL: "u", Narrator: "n"}
	}
	b := full()
	cleared := b.ClearProviderFieldsMissingFrom(&Book{}, nil)
	if len(cleared) != len(ProviderOwnedBookFields) {
		t.Fatalf("cleared %v", cleared)
	}
	if b.ReleaseDate != nil || b.Language != "" || b.Description != "" || len(b.Genres) != 0 {
		t.Errorf("not cleared: %+v", b)
	}
	if b.Title != "T" || b.ImageURL != "u" || b.Narrator != "n" || b.AverageRating != 4 || b.OriginalTitle != "o" {
		t.Errorf("a non-listed field was cleared: %+v", b)
	}

	b = full()
	b.ClearProviderFieldsMissingFrom(&Book{}, []string{BookFieldReleaseDate, BookFieldDescription})
	if b.ReleaseDate == nil || b.Description != "d" || b.Language != "" {
		t.Errorf("lock not honoured: %+v", b)
	}

	b = full()
	b.ClearProviderFieldsMissingFrom(&Book{UnknownFields: []string{BookFieldReleaseDate, BookFieldLanguage}}, nil)
	if b.ReleaseDate == nil || b.Language != "eng" || b.Description != "" {
		t.Errorf("unknown fields must be kept, the rest cleared: %+v", b)
	}

	b = full()
	if got := b.ClearProviderFieldsMissingFrom(full(), nil); len(got) != 0 {
		t.Errorf("cleared fields the source has: %v", got)
	}
	var nilBook *Book
	if nilBook.ClearProviderFieldsMissingFrom(&Book{}, nil) != nil {
		t.Error("nil receiver must be a no-op")
	}
}
