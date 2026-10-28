package models

import (
	"slices"
	"testing"
	"time"
)

func dayOf(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestAdoptRefreshedProviderFields(t *testing.T) {
	cases := []struct {
		name     string
		book     Book
		src      Book
		want     []string
		wantDate *time.Time
		wantDesc string
	}{
		{"date differs", Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(1999, 1, 1)},
			Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1)},
			[]string{BookFieldReleaseDate}, dayOf(2002, 1, 1), ""},
		{"date missing on book", Book{MetadataProvider: "openlibrary"},
			Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1)},
			[]string{BookFieldReleaseDate}, dayOf(2002, 1, 1), ""},
		{"same date", Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1)},
			Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1)}, nil, dayOf(2002, 1, 1), ""},
		{"locked date", Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(1999, 1, 1), LockedFields: []string{BookFieldReleaseDate}},
			Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1)}, nil, dayOf(1999, 1, 1), ""},
		{"unknown date", Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(1999, 1, 1)},
			Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1), UnknownFields: []string{BookFieldReleaseDate}},
			nil, dayOf(1999, 1, 1), ""},
		{"empty record never clears", Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(1999, 1, 1), Description: "d"},
			Book{MetadataProvider: "openlibrary"}, nil, dayOf(1999, 1, 1), "d"},
		{"other provider", Book{MetadataProvider: "audiobookshelf", ReleaseDate: dayOf(1999, 1, 1)},
			Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1)}, nil, dayOf(1999, 1, 1), ""},
		{"no provider on book", Book{ReleaseDate: dayOf(1999, 1, 1)},
			Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1)}, nil, dayOf(1999, 1, 1), ""},
		{"provider case", Book{MetadataProvider: "OpenLibrary"},
			Book{MetadataProvider: "openlibrary", ReleaseDate: dayOf(2002, 1, 1)},
			[]string{BookFieldReleaseDate}, dayOf(2002, 1, 1), ""},
		{"description filled", Book{MetadataProvider: "openlibrary"},
			Book{MetadataProvider: "openlibrary", Description: "new"}, []string{BookFieldDescription}, nil, "new"},
		{"blank description filled", Book{MetadataProvider: "openlibrary", Description: "  "},
			Book{MetadataProvider: "openlibrary", Description: "new"}, []string{BookFieldDescription}, nil, "new"},
		{"description kept", Book{MetadataProvider: "openlibrary", Description: "old"},
			Book{MetadataProvider: "openlibrary", Description: "new"}, nil, nil, "old"},
		{"locked description", Book{MetadataProvider: "openlibrary", LockedFields: []string{BookFieldDescription}},
			Book{MetadataProvider: "openlibrary", Description: "new"}, nil, nil, ""},
		{"unknown description", Book{MetadataProvider: "openlibrary"},
			Book{MetadataProvider: "openlibrary", Description: "new", UnknownFields: []string{BookFieldDescription}}, nil, nil, ""},
		{"language never adopted", Book{MetadataProvider: "openlibrary", Language: "fre"},
			Book{MetadataProvider: "openlibrary", Language: "eng"}, nil, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, src := c.book, c.src
			got := b.AdoptRefreshedProviderFields(&src)
			if !slices.Equal(got, c.want) {
				t.Errorf("changed = %v, want %v", got, c.want)
			}
			if (b.ReleaseDate == nil) != (c.wantDate == nil) || (b.ReleaseDate != nil && !b.ReleaseDate.Equal(*c.wantDate)) {
				t.Errorf("date = %v, want %v", b.ReleaseDate, c.wantDate)
			}
			if b.Description != c.wantDesc {
				t.Errorf("description = %q, want %q", b.Description, c.wantDesc)
			}
		})
	}
	var nilBook *Book
	if nilBook.AdoptRefreshedProviderFields(&Book{}) != nil || (&Book{}).AdoptRefreshedProviderFields(nil) != nil {
		t.Error("nil receiver or source must change nothing")
	}
}

func TestAdoptRefreshedProviderFieldsDoesNotAliasDate(t *testing.T) {
	d := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)
	src := Book{MetadataProvider: "openlibrary", ReleaseDate: &d}
	b := Book{MetadataProvider: "openlibrary"}
	b.AdoptRefreshedProviderFields(&src)
	*b.ReleaseDate = b.ReleaseDate.AddDate(5, 0, 0)
	if src.ReleaseDate.Year() != 2002 {
		t.Error("the book shares the record's date pointer")
	}
}

func TestSameReleaseDate(t *testing.T) {
	cases := []struct {
		name      string
		have, inc *time.Time
		wantSame  bool
	}{
		{"no date yet", nil, dayOf(2002, 1, 1), false},
		{"equal", dayOf(2002, 3, 4), dayOf(2002, 3, 4), true},
		{"year only same year", dayOf(2002, 3, 4), dayOf(2002, 1, 1), true},
		{"year only other year", dayOf(2001, 3, 4), dayOf(2002, 1, 1), false},
		{"precise incoming other date same year", dayOf(2002, 1, 1), dayOf(2002, 6, 1), false},
		{"precise incoming differs", dayOf(2002, 3, 4), dayOf(2002, 5, 6), false},
	}
	for _, c := range cases {
		if got := sameReleaseDate(c.have, c.inc); got != c.wantSame {
			t.Errorf("%s: got %v, want %v", c.name, got, c.wantSame)
		}
	}
}
